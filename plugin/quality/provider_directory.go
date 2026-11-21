package quality

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

var errProviderPrecondition = errors.New("conflict: CPA provider configuration changed")
var strongConfigTag = regexp.MustCompile(`^"[0-9a-fA-F]{64}"$`)

const providerConfigPath = "/v8/management/config/api-keys"

type providerObject = map[string]any

type providerInventoryStatus struct {
	Verified bool
	Reason   string
}
type providerInventoryStatusKey struct{}

// Only fixed diagnostic categories and a validated HTTP status may cross the
// inventory boundary. Never retain configuration bodies, keys or raw errors.
func providerDirectoryFailureReason(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if strings.HasPrefix(message, "management_session_required:") {
		return "本轮尚未建立 CPA 管理会话，未能读取提供商配置目录。请查看后台连接状态。"
	}
	var status int
	if _, scanErr := fmt.Sscanf(message, "management_http_%d:", &status); scanErr == nil && status >= 100 && status <= 599 {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return fmt.Sprintf("本轮 CPA 管理接口鉴权未通过（HTTP %d），未能核实提供商配置目录。", status)
		}
		return fmt.Sprintf("本轮 CPA 管理接口未成功返回提供商配置目录（HTTP %d）。", status)
	}
	if strings.HasPrefix(message, "management_uncertain:") {
		return "本轮连接或读取 CPA 管理接口失败，未能核实提供商配置目录。"
	}
	if strings.HasPrefix(message, "management_unavailable:") {
		return "本轮 CPA 提供商配置响应无法核实，未建立可信的路由目录。"
	}
	return "本轮 CPA 提供商配置目录未能核实，未进行路由匹配。"
}

func markProviderDirectoryUnavailable(t *Target, reason string) {
	if !t.IsProvider {
		return
	}
	if reason == "" {
		reason = "本轮 CPA 提供商配置目录未能核实，未进行路由匹配。"
	}
	for _, code := range []string{"provider_route_unverified", "provider_directory_unavailable"} {
		if !contains(t.Reasons, code) {
			t.Reasons = append(t.Reasons, code)
		}
	}
	t.ProviderProbeReason = reason
}

// Objects containing keys, endpoints and custom headers exist only for the
// duration of an authenticated config read, probe or conditional write.
type providerRoute struct {
	family, name, executor, groupID, unit               string
	id, index, etag, base, proxy, prefix                string
	group, key                                          providerObject
	keys                                                []providerObject
	familyList                                          []providerObject
	members                                             []string
	mapping                                             map[string]string
	configModelsKnown, disabled, conflict, missingIndex bool
	forcePrefix                                         bool
	probeProblem                                        string
	globalProxy                                         string
	groupPosition                                       int
}

func familyOfProviderID(id string) string {
	for _, family := range []string{"gemini", "gemini-interactions", "vertex", "claude", "codex", "meta", "xai"} {
		if result := providerFamily(id, family); result != "" {
			return result
		}
	}
	if first, ok := strings.CutPrefix(id, "openai-compatibility:"); ok {
		if i := strings.LastIndexByte(first, ':'); i > 0 {
			return providerFamily(id, compatibilityExecutor(first[:i]))
		}
	}
	return ""
}

func configText(field any) string {
	v, _ := field.(string)
	return strings.TrimSpace(v)
}

func configList(field any) []providerObject {
	items, _ := providerObjects(field)
	return items
}

func providerObjects(field any) ([]providerObject, bool) {
	list, ok := field.([]any)
	if !ok {
		return nil, false
	}
	items := make([]providerObject, 0, len(list))
	for _, raw := range list {
		item, ok := raw.(map[string]any)
		if !ok || item == nil {
			return nil, false
		}
		items = append(items, item)
	}
	return items, true
}

// Validate the fields used to identify, contact or modify a route. Preserve
// additional fields verbatim when writing the original family document back.
func validProviderFields(item providerObject) bool {
	if item == nil {
		return false
	}
	for _, key := range []string{"name", "api-key", "base-url", "proxy-url", "prefix", "auth_index"} {
		if _, ok := item[key].(string); !ok && item[key] != nil {
			return false
		}
	}
	if _, ok := item["disabled"].(bool); !ok && item["disabled"] != nil {
		return false
	}
	if raw := item["headers"]; raw != nil {
		headers, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		for _, value := range headers {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	if raw := item["excluded-models"]; raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return false
		}
		for _, value := range list {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	if raw := item["models"]; raw != nil {
		models, ok := providerObjects(raw)
		if !ok {
			return false
		}
		for _, model := range models {
			for _, key := range []string{"name", "alias"} {
				if _, ok := model[key].(string); !ok && model[key] != nil {
					return false
				}
			}
		}
	}
	return true
}

func configStrings(field any) []string {
	result := []string{}
	if list, ok := field.([]any); ok {
		for _, raw := range list {
			if s, ok := raw.(string); ok {
				result = append(result, s)
			}
		}
	}
	return result
}

func (r providerRoute) effective(field string) any {
	if r.family == "openai-compatibility" {
		switch field {
		case "proxy-url", "api-key":
			return r.key[field]
		default:
			return r.group[field]
		}
	}
	if value := r.key[field]; value != nil {
		return value
	}
	return r.group[field]
}

func poolID(group, key providerObject) string {
	outer, _ := group["connection-pool"].(map[string]any)
	inner, _ := key["connection-pool"].(map[string]any)
	if outer == nil && inner == nil {
		return ""
	}
	field := func(name string) string {
		v := inner[name]
		if v == nil {
			v = outer[name]
		}
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return "pool:" + field("enabled") + "\x00" + field("idle-conn-timeout") + "\x00" + field("max-idle-conns-per-host")
}

func sortedConfigHeaders(field any) string {
	headers, _ := field.(map[string]any)
	clean := make(map[string]string, len(headers))
	for key, raw := range headers {
		key, val := strings.TrimSpace(key), configText(raw)
		if key != "" && val != "" {
			clean[key] = val
		}
	}
	var b strings.Builder
	for _, key := range slices.Sorted(maps.Keys(clean)) {
		b.WriteString(key + "\x00" + clean[key] + "\x00")
	}
	return b.String()
}

// These two hash projections mirror CPA 8.0.76's StableIDGenerator and
// Auth.EnsureIndex. Duplicate identities never get a guessed collision suffix.
// They also identify an OpenAI-compatible group before its first registration.
func (r providerRoute) identity() (string, string) {
	kind := r.executor + ":apikey"
	key := configText(r.key["api-key"])
	effectiveBase := r.base
	if effectiveBase == "" && r.family == "meta" {
		effectiveBase = "https://api.meta.ai/v1"
	}
	parts := []string{key, effectiveBase, r.proxy, r.prefix, sortedConfigHeaders(r.effective("headers"))}
	if r.family == "openai-compatibility" {
		kind = "openai-compatibility:" + strings.ToLower(r.name)
		parts = parts[:3]
		if len(r.keys) == 0 {
			parts = []string{r.base}
		}
	} else if r.family == "vertex" {
		parts = parts[:3]
	}
	pool := poolID(r.group, r.key)
	if pool != "" {
		parts = append(parts, pool)
	}
	hash := sha256.New()
	hash.Write([]byte(kind))
	for _, p := range parts {
		hash.Write([]byte{0})
		hash.Write([]byte(strings.TrimSpace(p)))
	}
	id := kind + ":" + hex.EncodeToString(hash.Sum(nil))[:12]
	seed := "id:" + id
	if key != "" && r.family != "vertex" {
		family := r.family + "-api-key"
		if r.family == "openai-compatibility" {
			family = r.family
		}
		seed = family + ":" + effectiveBase + "+" + key
		if pool != "" {
			seed += "\x00" + pool
		}
	}
	index := sha256.Sum256([]byte(seed))
	return id, hex.EncodeToString(index[:8])
}

func (m *ManagementClient) readProviderValue(ctx context.Context, path string, out any) (tag string, present bool, err error) {
	resp, err := m.response(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		var missing map[string]string
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
		if readErr != nil || len(raw) > 4096 || decodeProviderValue(raw, &missing) != nil || len(missing) != 1 || missing["error"] != "not_found" {
			return "", false, fmt.Errorf("management_unavailable: CPA could not verify the configuration path")
		}
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("management_http_%d: CPA configuration unavailable", resp.StatusCode)
	}
	const maxConfig = 8 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxConfig+1))
	if err != nil || len(raw) > maxConfig {
		return "", false, fmt.Errorf("management_unavailable: invalid CPA configuration")
	}
	if err := decodeProviderValue(raw, out); err != nil {
		return "", false, fmt.Errorf("management_unavailable: invalid CPA configuration")
	}
	return resp.Header.Get("ETag"), true, nil
}

func decodeProviderValue(raw []byte, out any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("null CPA configuration")
	}
	if err := uniqueJSON(raw); err != nil {
		return err
	}
	decoder := stdjson.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("invalid trailing CPA configuration data")
	}
	return nil
}

func (h *NativeHost) providerDirectory(ctx context.Context) ([]providerRoute, error) {
	if h.Management == nil || !h.Management.ready() {
		return nil, fmt.Errorf("management_session_required: CPA provider directory unavailable")
	}
	var all map[string][]providerObject
	tag, present, err := h.Management.readProviderValue(ctx, providerConfigPath, &all)
	if err != nil || !present {
		return nil, err
	}
	if all == nil {
		return nil, fmt.Errorf("management_unavailable: invalid CPA provider directory")
	}
	var out []providerRoute
	groupCounts, indexCounts, idCounts := map[string]int{}, map[string]int{}, map[string]int{}
	for _, family := range slices.Sorted(maps.Keys(all)) {
		groups := all[family]
		switch family {
		case "gemini", "interactions", "claude", "codex", "xai", "meta", "vertex", "openai-compatibility":
		default:
			continue
		}
		if groups == nil {
			return nil, fmt.Errorf("management_unavailable: invalid CPA provider groups")
		}
		for position, group := range groups {
			if !validProviderFields(group) {
				return nil, fmt.Errorf("management_unavailable: invalid CPA provider group")
			}
			keys, complete := providerObjects(group["keys"])
			_, hasKeys := group["keys"]
			// Older keyless compatible groups omit the array entirely.
			if !complete && (family != "openai-compatibility" || hasKeys) {
				return nil, fmt.Errorf("management_unavailable: invalid CPA provider members")
			}
			name := configText(group["name"])
			if name == "" {
				name = family
			}
			executor, unit := family, "route"
			if family == "openai-compatibility" {
				executor, unit = compatibilityExecutor(name), "group"
			}
			if family == "interactions" {
				executor = "gemini-interactions"
			}
			base := configText(group["base-url"])
			gid := "pg-" + fingerprint([3]string{family, strings.ToLower(name), base})[:32]
			groupCounts[gid]++
			entries := keys
			if len(entries) == 0 && family == "openai-compatibility" {
				entries = []providerObject{nil}
			}
			for _, key := range entries {
				if key != nil && !validProviderFields(key) {
					return nil, fmt.Errorf("management_unavailable: invalid CPA provider member")
				}
				r := providerRoute{family: family, name: name, executor: executor, unit: unit, groupID: gid, etag: tag, base: base, group: group, key: key, keys: keys, familyList: groups, groupPosition: position}
				r.proxy = configText(r.effective("proxy-url"))
				r.prefix = strings.Trim(configText(r.effective("prefix")), "/")
				if strings.Contains(r.prefix, "/") {
					r.prefix = ""
				}
				r.id, r.index = r.identity()
				source := key
				if key == nil {
					source = group
				}
				published := configText(source["auth_index"])
				r.missingIndex = published == ""
				if published != "" {
					r.index = published
				}
				r.disabled, _ = group["disabled"].(bool)
				if unit == "route" {
					r.disabled = r.disabled || hasProviderWildcard(configStrings(r.effective("excluded-models")))
				}
				r.computeModels()
				out = append(out, r)
				idCounts[r.id]++
				indexCounts[r.index]++
			}
		}
	}
	type location struct {
		family   string
		position int
	}
	routeGroups := map[location][]string{}
	for _, r := range out {
		if groupCounts[r.groupID] > 1 {
			loc := location{r.family, r.groupPosition}
			routeGroups[loc] = append(routeGroups[loc], r.id)
		}
	}
	groupSuffixes := make(map[location]string, len(routeGroups))
	for location, members := range routeGroups {
		slices.Sort(members)
		groupSuffixes[location] = "-" + fingerprint(members)[:16]
	}
	for i := range out {
		r := &out[i]
		r.conflict = idCounts[r.id] != 1 || indexCounts[r.index] != 1
		if groupCounts[r.groupID] > 1 {
			r.groupID += groupSuffixes[location{r.family, r.groupPosition}]
		}
	}
	var globalProxy string
	var forcePrefix bool
	needProxy, needPrefix := false, false
	for _, r := range out {
		if strongConfigTag.MatchString(tag) && r.base != "" {
			needProxy = needProxy || r.proxy == ""
			needPrefix = needPrefix || r.prefix != ""
		}
	}
	proxyKnown, prefixKnown := true, true
	if needProxy {
		v, exists, err := h.Management.readProviderValue(ctx, "/v8/management/config/requests/proxy-url", &globalProxy)
		proxyKnown = err == nil && (!exists || v == tag)
	}
	if needPrefix {
		v, exists, err := h.Management.readProviderValue(ctx, "/v8/management/config/routing/force-model-prefix", &forcePrefix)
		prefixKnown = err == nil && (!exists || v == tag)
	}
	groupMembers := map[string][]string{}
	groupConflicts := map[string]bool{}
	for _, r := range out {
		if r.unit == "group" {
			groupMembers[r.groupID] = append(groupMembers[r.groupID], r.id)
			groupConflicts[r.groupID] = groupConflicts[r.groupID] || r.conflict
		}
	}
	for _, members := range groupMembers {
		slices.Sort(members)
	}
	for i := range out {
		r := &out[i]
		r.globalProxy = globalProxy
		if r.proxy == "" {
			r.proxy = globalProxy
			if !proxyKnown {
				r.probeProblem = "CPA 未能核实提供商使用的代理配置。"
			}
		}
		r.forcePrefix = forcePrefix
		if r.prefix != "" && !prefixKnown {
			r.probeProblem = "CPA 未能核实此路由使用的模型前缀。"
		}
		if r.unit == "group" {
			// The in-memory directory shares an immutable list per group.
			r.members = groupMembers[r.groupID]
			r.conflict = groupConflicts[r.groupID]
		} else {
			r.members = []string{r.id}
		}
	}
	return out, nil
}

func (h *NativeHost) configuredTargetForEntry(ctx context.Context, entry pluginapi.HostAuthModels) Target {
	routes, err := h.providerDirectory(ctx)
	target := configuredTarget(entry, routesByIndex(routes)[entry.AuthIndex])
	if err != nil {
		markProviderDirectoryUnavailable(&target, providerDirectoryFailureReason(err))
	}
	return target
}

func routesByIndex(routes []providerRoute) map[string]providerRoute {
	byIndex := make(map[string]providerRoute, len(routes))
	for _, r := range routes {
		byIndex[r.index] = r
	}
	return byIndex
}

func hasProviderWildcard(patterns []string) bool {
	for _, p := range patterns {
		if strings.TrimSpace(p) == "*" {
			return true
		}
	}
	return false
}

// A model excluded independently of a whole-route stop remains excluded.
// CPA matches '*' anywhere, including across '/'.
func matchesProviderPattern(model, pattern string) bool {
	chunks := strings.Split(strings.ToLower(strings.TrimSpace(pattern)), "*")
	model = strings.ToLower(model)
	if len(chunks) == 1 {
		return chunks[0] == model
	}
	if !strings.HasPrefix(model, chunks[0]) {
		return false
	}
	rest := strings.TrimPrefix(model, chunks[0])
	for _, c := range chunks[1 : len(chunks)-1] {
		_, suffix, found := strings.Cut(rest, c)
		if !found {
			return false
		}
		rest = suffix
	}
	return strings.HasSuffix(rest, chunks[len(chunks)-1])
}

func (r *providerRoute) computeModels() {
	list := configList(r.effective("models"))
	r.configModelsKnown = len(list) > 0
	r.mapping = make(map[string]string)
	for _, m := range list {
		wire := configText(m["name"])
		if wire == "" {
			r.configModelsKnown = false
			continue
		}
		alias := configText(m["alias"])
		if alias == "" {
			alias = wire
		}
		if r.family != "openai-compatibility" {
			excluded := false
			for _, p := range configStrings(r.effective("excluded-models")) {
				if strings.TrimSpace(p) != "*" && matchesProviderPattern(alias, p) {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}
		}
		if previous, exists := r.mapping[alias]; exists && previous != wire {
			r.mapping[alias] = ""
			continue
		}
		r.mapping[alias] = wire
	}
}

func (r providerRoute) models() []string {
	list := []string{}
	for alias := range r.mapping {
		if r.prefix != "" {
			list = append(list, r.prefix+"/"+alias)
		}
		if r.prefix == "" || !r.forcePrefix || alias == r.prefix {
			list = append(list, alias)
		}
	}
	slices.Sort(list)
	return list
}

func (r providerRoute) wireModel(model string) string {
	if r.prefix != "" {
		model = strings.TrimPrefix(model, r.prefix+"/")
	}
	return r.mapping[model]
}

func (r providerRoute) probeReason() string {
	if r.probeProblem != "" {
		return r.probeProblem
	}
	if _, err := proxyutil.Parse(r.proxy); err != nil {
		return "无法核实此提供商配置的代理地址。"
	}
	if r.conflict {
		return "提供商配置组或路由索引重复，无法唯一核实。"
	}
	u, err := url.Parse(r.base)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" {
		return "缺少可核实的提供商上游地址。"
	}
	if !r.configModelsKnown {
		return "此提供商没有可核实的配置模型映射。"
	}
	switch r.family {
	case "openai-compatibility", "codex", "claude", "gemini", "vertex", "xai", "meta", "interactions":
	default:
		return "此提供商的定向探测协议暂不可核实。"
	}
	if strings.HasPrefix(configText(r.key["api-key"]), "dca:") {
		return "此提供商密钥需要 CPA 原生认证。"
	}
	if !r.safeHeaders() {
		return "提供商请求头需要宿主动态解析或无法安全发送。"
	}
	return ""
}

// The config API injects indexes on GET. They are runtime projections, not
// persisted group settings or part of a member's configuration fingerprint.
func withoutProviderIndex(m providerObject) providerObject {
	out := maps.Clone(m)
	delete(out, "auth_index")
	delete(out, "auth-index")
	return out
}

func withoutWildcard(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if strings.TrimSpace(p) != "*" {
			out = append(out, p)
		}
	}
	return out
}

func (r providerRoute) snapshot() CredentialSnapshot {
	group := withoutProviderIndex(r.group)
	delete(group, "keys")
	mechanism := "group_disabled"
	var content any
	source := ""
	if r.unit == "group" {
		delete(group, "disabled")
		keys := make([]providerObject, 0, len(r.keys))
		for _, k := range r.keys {
			keys = append(keys, withoutProviderIndex(k))
		}
		// Every member must carry the same group signature, even when two
		// keys use different proxy overrides.
		proxy := ""
		if len(r.keys) == 0 || slices.ContainsFunc(r.keys, func(k providerObject) bool { return configText(k["proxy-url"]) == "" }) {
			proxy = r.globalProxy
		}
		content = []any{group, keys, proxy}
	} else {
		mechanism = "excluded_models"
		key := withoutProviderIndex(r.key)
		// Preserve all effective exclusions other than the route-wide stop.
		delete(key, "excluded-models")
		content = []any{group, key, withoutWildcard(configStrings(r.effective("excluded-models"))), r.proxy}
		switch value, ok := r.key["excluded-models"]; {
		case !ok:
			source = "inherited"
		case value == nil:
			source = "null"
		default:
			source = "explicit"
		}
	}
	return CredentialSnapshot{AuthID: r.id, AuthIndex: r.index, Provider: r.executor, Name: r.name,
		ProviderMechanism: mechanism, ProviderGroupID: r.groupID, ProviderMembers: slices.Clone(r.members), ProviderETag: r.etag,
		ProviderExclusionSource: source, Disabled: r.disabled, Fingerprint: fingerprint([]any{content, r.forcePrefix && r.prefix != ""})}
}

func (h *NativeHost) pinnedConfiguredRoute(ctx context.Context, id, index string) (providerRoute, error) {
	routes, err := h.providerDirectory(ctx)
	if err != nil {
		return providerRoute{}, err
	}
	for _, r := range routes {
		if r.id != id || r.index != index {
			continue
		}
		if r.conflict {
			return providerRoute{}, rejectScope("blocked: ambiguous provider identity", "identity_conflict", "CPA 配置目录内的供应商路由或索引冲突。")
		}
		if !strongConfigTag.MatchString(r.etag) {
			return providerRoute{}, rejectScope("blocked: unknown provider revision", "provider_revision_unavailable", "CPA 未提供有效的配置 ETag。")
		}
		if err := h.confirmProviderRuntime(ctx, r); err != nil {
			return providerRoute{}, err
		}
		return r, nil
	}
	return providerRoute{}, rejectScope("blocked: provider route unavailable", "provider_route_unverified", "CPA 配置目录中找不到身份和索引一致的目标路由。")
}

func (h *NativeHost) confirmProviderRuntime(ctx context.Context, r providerRoute) error {
	var all pluginapi.HostAuthModelsResponse
	var files struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if err := h.call(ctx, "host.auth.list", struct{}{}, &files); err != nil || files.Files == nil {
		return rejectScope("unavailable: CPA provider origin unavailable", "provider_runtime_unavailable", "无法从 CPA 文件目录确认提供商身份。")
	}
	for _, file := range files.Files {
		if file.Source == "file" && (file.ID == r.id || file.AuthIndex == r.index) {
			return rejectScope("blocked: provider identity conflict", "identity_conflict", "供应商路由与 CPA 文件凭证的标识发生冲突。")
		}
	}
	// An empty successful full catalog establishes absence. Failure to read
	// a particular index does not distinguish removal from a callback error.
	if err := h.call(ctx, "host.auth.models", pluginapi.HostAuthModelsRequest{}, &all); err != nil || all.Credentials == nil {
		return rejectScope("unavailable: CPA provider runtime unavailable", "provider_runtime_unavailable", "CPA 未能返回运行时路由目录。")
	}
	count := 0
	for _, entry := range all.Credentials {
		if entry.ID != r.id && entry.AuthIndex != r.index {
			continue
		}
		if entry.ID != r.id || entry.AuthIndex != r.index || entry.Provider != r.executor || count > 0 {
			return rejectScope("blocked: provider identity conflict", "identity_conflict", "提供商配置与运行时的目标身份或索引冲突。")
		}
		count++
		if !validCatalogModels(entry.Models) || r.disabled && len(entry.Models) > 0 || !r.disabled && len(entry.Models) == 0 {
			return rejectScope("blocked: CPA provider runtime pending", "provider_runtime_pending", "CPA 配置与运行时模型目录尚未同步。")
		}
	}
	if !r.disabled && count != 1 || r.unit == "route" && count != 1 {
		return rejectScope("blocked: CPA provider runtime pending", "provider_runtime_pending", "CPA 未注册所选路由，无法确认启停状态。")
	}
	return nil
}
