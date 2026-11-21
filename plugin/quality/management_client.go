package quality

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// ManagementClient owns one loopback transport and one lifecycle-bound verifier.
type ManagementClient struct {
	mu   sync.RWMutex
	edit sync.Mutex
	connectionState
	base   string
	key    string
	client *http.Client
}

func NewManagementClient(base string) (*ManagementClient, error) {
	if base == "" {
		base = "http://127.0.0.1:8317"
	}
	u, err := url.Parse(base)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid local CPA management URL")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("CPA management URL must use a loopback IP")
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 8, MaxConnsPerHost: 24}
	return &ManagementClient{connectionState: connectionState{status: ConnectionStatus{State: "unconfigured", CanMigrate: true}}, base: strings.TrimRight(base, "/"), client: &http.Client{
		Transport: transport, Timeout: 70 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func managementKey(headers http.Header) string {
	// Match the credential selected by the authenticated CPA middleware.
	key := headers.Get("Authorization")
	if scheme, value, found := strings.Cut(key, " "); found && strings.EqualFold(scheme, "Bearer") {
		key = value
	}
	if key == "" {
		key = headers.Get("X-Management-Key")
	}
	if !validManagementKey(key) {
		return ""
	}
	return key
}

func validManagementKey(key string) bool {
	return key != "" && len(key) <= 8192 && utf8.ValidString(key) && strings.IndexFunc(key, func(r rune) bool { return r < 32 || r == 127 }) < 0 && strings.TrimSpace(key) == key
}

func (m *ManagementClient) ready() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status.State == "ready" && m.key != ""
}

// Auth model discovery returns IDs only. Join the display page to the
// corresponding CPA channel catalog without adding models to any credential.
func (m *ManagementClient) addModelReasoning(ctx context.Context, targets []Target) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	byChannel := make(map[string]map[string]*thinkingOptions)
	for i := range targets {
		target := &targets[i]
		reasoning, _ := target.Reasoning.(map[string]*thinkingOptions)
		modelIDs := make(map[string]string)
		for _, id := range target.Models {
			if _, configured := reasoning[id]; configured {
				continue
			}
			catalogID := id
			if target.reasoningModelIDs != nil {
				catalogID = target.reasoningModelIDs[id]
			}
			if catalogID != "" {
				modelIDs[id] = catalogID
			}
		}
		channel := strings.ToLower(strings.TrimSpace(target.Provider))
		if len(modelIDs) == 0 || channel == "" || channel == "." || channel == ".." || strings.ContainsAny(channel, "/\\") {
			continue
		}
		// These catalogs aggregate public aliases from multiple routes. Only
		// authenticated route-local configuration can establish their support.
		if channel == "openai-compatibility" || strings.HasPrefix(channel, "openai-compatible-") {
			continue
		}
		catalog, loaded := byChannel[channel]
		if !loaded {
			var reply struct {
				Channel string `json:"channel"`
				Models  []struct {
					ID       string           `json:"id"`
					Thinking *thinkingOptions `json:"thinking"`
				} `json:"models"`
			}
			byChannel[channel] = nil
			if err := m.request(ctx, http.MethodGet, "/v8/management/routing/model-definitions/"+url.PathEscape(channel), nil, &reply, 4<<20); err != nil || reply.Channel != channel {
				continue
			}
			catalog = make(map[string]*thinkingOptions)
			for _, model := range reply.Models {
				catalog[model.ID] = normalizedThinkingOptions(model.Thinking)
			}
			byChannel[channel] = catalog
		}
		if reasoning == nil {
			reasoning = make(map[string]*thinkingOptions)
		}
		for id, catalogID := range modelIDs {
			if support, known := catalog[catalogID]; known {
				reasoning[id] = support
			}
		}
		target.Reasoning = reasoning
	}
}

func (m *ManagementClient) response(ctx context.Context, method, path string, body any) (*http.Response, error) {
	return m.conditionalResponse(ctx, method, path, body, "")
}

func (m *ManagementClient) conditionalResponse(ctx context.Context, method, path string, body any, ifMatch string) (*http.Response, error) {
	if m == nil {
		return nil, fmt.Errorf("unavailable: CPA management connection is not configured")
	}
	m.mu.RLock()
	key, status := m.key, m.status
	m.mu.RUnlock()
	if status.State != "ready" || key == "" {
		return nil, connectionError(status)
	}
	resp, err := m.send(ctx, key, method, path, body, ifMatch)
	// CPA reports upstream transport failures as an outer 502 on api-call.
	upstreamTransport := resp != nil && path == "/v8/management/requests/api-call" && resp.StatusCode == http.StatusBadGateway
	if ctx.Err() == nil && !upstreamTransport && (err != nil || resp.StatusCode >= 400) {
		m.observeFailure(status.Revision, resp)
	}
	if err != nil && method != http.MethodGet {
		return nil, fmt.Errorf("management_uncertain: CPA did not confirm the operation")
	}
	return resp, err
}

func (m *ManagementClient) send(ctx context.Context, key, method, path string, body any, ifMatch string) (*http.Response, error) {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid management request")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("management_network: CPA management endpoint did not respond")
	}
	return resp, nil
}

func (m *ManagementClient) request(ctx context.Context, method, path string, body any, out any, limit int64) error {
	resp, err := m.response(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("management_http_%d: CPA management request failed", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return fmt.Errorf("management_uncertain: invalid or oversized CPA response")
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return fmt.Errorf("management_uncertain: invalid CPA response")
	}
	return nil
}

// CPA wraps an upstream HTTP response inside its own successful management
// response. Only one explicit status and body can authorize an error policy.
type managementProbeResponse struct {
	StatusCode int
	Body       string
	Header     http.Header
}

// CPA expands its authentication placeholder in the serialized request body as
// well as in headers. Escape it in JSON string values so the upstream receives
// the original text and the authentication header can still use the placeholder.
type managementProbeText string

func (s managementProbeText) MarshalJSON() ([]byte, error) {
	quoted, err := json.Marshal(string(s))
	if err != nil || !strings.Contains(string(s), "$TOKEN$") {
		return quoted, err
	}
	return bytes.ReplaceAll(quoted, []byte("$TOKEN$"), []byte(`\u0024TOKEN\u0024`)), nil
}

func (r *managementProbeResponse) UnmarshalJSON(raw []byte) error {
	root := ojson.ParseBytes(raw)
	invalid := errors.New("invalid CPA probe response")
	if !root.IsObject() || !ojson.ValidBytes(raw) {
		return invalid
	}
	var status int
	var body, header ojson.Result
	valid := true
	root.ForEach(func(key, value ojson.Result) bool {
		name := key.String()
		switch {
		case strings.EqualFold(name, "status_code"):
			n, err := strconv.Atoi(value.Raw)
			valid = name == "status_code" && status == 0 && err == nil && n >= 200 && n <= 599
			status = n
		case strings.EqualFold(name, "body"):
			valid = name == "body" && !body.Exists() && value.Type == ojson.String
			body = value
		case strings.EqualFold(name, "header"):
			valid = name == "header" && !header.Exists() && (value.IsObject() || value.Raw == "null")
			header = value
		}
		return valid
	})
	if !valid || status == 0 || !body.Exists() {
		return invalid
	}
	var upstreamHeader http.Header
	if status == http.StatusTooManyRequests && header.IsObject() {
		rawHeader := []byte(header.Raw)
		if uniqueJSON(rawHeader) != nil || json.Unmarshal(rawHeader, &upstreamHeader) != nil {
			return invalid
		}
	}
	r.StatusCode, r.Body, r.Header = status, body.String(), upstreamHeader
	return nil
}

// Snapshots contain hashes and public routing identity, never token material.
// UpdatedAt is an additional interference check, not an atomic ownership token.
type CredentialSnapshot struct {
	ProviderGroupID   string   `json:"provider_group_id,omitempty"`
	ProviderMechanism string   `json:"provider_mechanism,omitempty"`
	ProviderMembers   []string `json:"provider_members,omitempty"`
	ProviderETag      string   `json:"provider_etag,omitempty"`
	// The restore operation can put an inherited field back in its original form.
	ProviderExclusionSource string    `json:"provider_exclusion_source,omitempty"`
	AccountFingerprint      string    `json:"account_fingerprint,omitempty"`
	AuthID                  string    `json:"auth_id"`
	AuthIndex               string    `json:"auth_index"`
	Name                    string    `json:"name"`
	Provider                string    `json:"provider"`
	Fingerprint             string    `json:"fingerprint"`
	UpdatedAt               time.Time `json:"updated_at"`
	FileModifiedAt          time.Time `json:"file_modified_at"`
	Disabled                bool      `json:"disabled"`
}

type managedHost interface {
	CredentialSnapshot(context.Context, string, string) (CredentialSnapshot, error)
	SetCredentialDisabled(context.Context, CredentialSnapshot, bool) (CredentialSnapshot, error)
	DeleteCredential(context.Context, CredentialSnapshot) error
}

// Only local checks before a mutation request may return this error. HTTP
// failures after sending a request still leave the result uncertain.
var errCredentialPrecondition = errors.New("conflict: credential changed before write")

func sameCredential(a, b CredentialSnapshot) bool {
	return a.AuthID == b.AuthID && a.AuthIndex == b.AuthIndex && a.Name == b.Name && a.Provider == b.Provider && a.Fingerprint != "" && a.Fingerprint == b.Fingerprint
}

// Entry continuity is distinct from the content that a particular round used.
// CPA file indices follow the file locator; provider indices may change with a
// key edit. Never infer that such a new provider locator inherits ownership.
func sameCredentialEntry(a, b CredentialSnapshot) bool {
	return a.AuthID != "" && a.AuthID == b.AuthID && a.AuthIndex != "" && a.AuthIndex == b.AuthIndex &&
		a.Name != "" && a.Name == b.Name && a.Provider == b.Provider && a.ProviderMechanism == b.ProviderMechanism &&
		a.ProviderGroupID == b.ProviderGroupID
}
func sameSnapshot(a, b CredentialSnapshot) bool {
	if a.ProviderMechanism != "" || b.ProviderMechanism != "" {
		return sameCredential(a, b) && a.Disabled == b.Disabled && a.ProviderMechanism == b.ProviderMechanism &&
			a.ProviderGroupID != "" && a.ProviderGroupID == b.ProviderGroupID && slices.Equal(a.ProviderMembers, b.ProviderMembers)
	}
	return sameCredential(a, b) && a.Disabled == b.Disabled && !a.UpdatedAt.IsZero() && a.UpdatedAt.Equal(b.UpdatedAt) && a.FileModifiedAt.Equal(b.FileModifiedAt)
}

func (h *NativeHost) credentialProfile(ctx context.Context, id, index string) (CredentialSnapshot, pluginapi.HostAuthFileEntry, map[string]any, error) {
	var runtime pluginapi.HostAuthGetRuntimeResponse
	var raw pluginapi.HostAuthGetResponse
	var empty CredentialSnapshot
	if id == "" || index == "" {
		return empty, runtime.Auth, nil, rejectScope("blocked: credential identity is missing", "identity_missing", "凭证标识或索引缺失，无法读取快照。")
	}
	if err := h.call(ctx, "host.auth.get_runtime", pluginapi.HostAuthGetRequest{AuthIndex: index}, &runtime); err != nil {
		return empty, runtime.Auth, nil, rejectScope("blocked: credential no longer exists", "snapshot_unavailable", "CPA 未能按索引返回该凭证的运行信息。")
	}
	a := runtime.Auth
	provider := a.Provider
	if provider == "" {
		provider = a.Type
	}
	const unsupportedFile = "unsupported: automatic handling requires one identifiable file credential"
	if a.ID != id || a.AuthIndex != index {
		return empty, a, nil, rejectScope(unsupportedFile, "identity_conflict", "CPA 返回的凭证标识或索引与本轮目标不一致。")
	}
	if providerFamily(a.ID, provider) != "" {
		return empty, a, nil, rejectScope(unsupportedFile, "provider_route", "供应商配置通过 CPA 原生路由检测，不能作为文件凭证处置。")
	}
	if a.RuntimeOnly || a.Source != "file" || a.Name == "" || filepath.Base(a.Name) != a.Name || strings.ContainsAny(a.Name, "/\\") || !strings.HasSuffix(strings.ToLower(a.Name), ".json") {
		return empty, a, nil, rejectScope(unsupportedFile, "unsupported_file", "管理探测需要可单独标识的 JSON 文件凭证。")
	}
	if a.UpdatedAt.IsZero() {
		return empty, a, nil, rejectScope(unsupportedFile, "snapshot_unavailable", "CPA 缺少凭证的运行更新时间，无法核对快照。")
	}
	if provider != "codex" && provider != "openai" && provider != "openai-compatibility" {
		return empty, a, nil, rejectScope("unsupported: management probes currently support OpenAI and Codex file credentials", "unsupported_provider", "当前提供方不支持凭证管理探测。")
	}
	if err := h.call(ctx, "host.auth.get", pluginapi.HostAuthGetRequest{AuthIndex: index}, &raw); err != nil {
		return empty, a, nil, rejectScope("blocked: credential snapshot is unavailable", "snapshot_unavailable", "CPA 未能返回该凭证的文件快照。")
	}
	var data map[string]any
	if raw.AuthIndex != index || raw.Name != a.Name {
		return empty, a, nil, rejectScope("blocked: credential snapshot identity changed", "identity_conflict", "CPA 返回的文件名或索引与运行信息不一致。")
	}
	if json.Unmarshal(raw.JSON, &data) != nil || data == nil {
		return empty, a, nil, rejectScope("blocked: credential snapshot identity changed", "snapshot_unavailable", "无法解析凭证文件快照。")
	}
	fileType, ok := data["type"].(string)
	if !ok || strings.TrimSpace(fileType) != provider {
		return empty, a, nil, rejectScope("blocked: credential file type differs from runtime", "identity_conflict", "CPA 凭证文件类型与当前运行信息不一致。")
	}
	if disabled, exists := data["disabled"]; exists {
		value, valid := disabled.(bool)
		if !valid || value != (a.Disabled || a.Status == "disabled") {
			return empty, a, nil, rejectScope("blocked: credential file and runtime disabled states disagree", "snapshot_unavailable", "CPA 凭证文件与运行时的启停状态尚未一致，或文件启停字段无效。")
		}
	}
	// Expanded sources, SIWC, and ambiguous routing need their native executor.
	for _, field := range []string{"use_siwc", "virtual", "projects", "project_ids"} {
		if value := data[field]; value != nil && value != false && value != "false" && value != "" {
			return empty, a, nil, rejectScope("unsupported: credential requires native routing", "native_routing_required", "该凭证需要使用宿主原生路由。")
		}
	}
	fileDisabled, hasDisabled := data["disabled"]
	delete(data, "disabled")
	stamp := fingerprint(struct {
		ID, Index, Name, Provider, Base, Created string
		Data                                     map[string]any
	}{id, index, a.Name, provider, a.BaseURL, a.CreatedAt.Format(time.RFC3339Nano), data})
	if hasDisabled {
		data["disabled"] = fileDisabled
	}
	snapshot := CredentialSnapshot{AuthID: id, AuthIndex: index, Name: a.Name, Provider: provider, Fingerprint: stamp, UpdatedAt: a.UpdatedAt, FileModifiedAt: a.ModTime, Disabled: a.Disabled || a.Status == "disabled"}
	if account, _ := data["account_id"].(string); strings.TrimSpace(account) != "" {
		snapshot.AccountFingerprint = fingerprint([2]string{provider, account})
	}
	return snapshot, a, data, nil
}

// Re-read the pinned runtime and physical file through public SDK callbacks.
// Token refresh and host restart may both change the full snapshot fingerprint.
func (h *NativeHost) verifyEnabledManualFile(ctx context.Context, t Target, _ *CredentialDisposition) (string, error) {
	current, runtime, data, err := h.credentialProfile(ctx, t.AuthID, t.AuthIndex)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("blocked: 无法核实 CPA 当前凭证文件，请刷新后重试。")
	}
	if current.Disabled || runtime.Status != "active" || runtime.Unavailable || runtime.NextRetryAfter.After(time.Now()) {
		return "", fmt.Errorf("blocked: CPA 当前凭证已停用或暂不可用。")
	}
	if disabled, exists := data["disabled"]; exists && disabled != false {
		return "", fmt.Errorf("blocked: CPA 文件尚未确认启用此凭证，请核对文件状态。")
	}
	fileType, _ := data["type"].(string)
	if current.AuthID != t.AuthID || current.AuthIndex != t.AuthIndex || current.Provider != t.Provider || fileType != current.Provider {
		return "", fmt.Errorf("blocked: CPA 当前凭证文件与所选条目的身份不一致，请刷新目录。")
	}
	// Pin the current account, independently of historical suspension content.
	if current.AccountFingerprint != "" {
		return current.AccountFingerprint, nil
	}
	return current.Fingerprint, nil
}

func (h *NativeHost) CredentialSnapshot(ctx context.Context, id, index string) (CredentialSnapshot, error) {
	if familyOfProviderID(id) != "" {
		route, err := h.pinnedConfiguredRoute(ctx, id, index)
		if err != nil {
			return CredentialSnapshot{}, err
		}
		return route.snapshot(), nil
	}
	s, _, _, err := h.credentialProfile(ctx, id, index)
	return s, err
}

func (h *NativeHost) SetCredentialDisabled(ctx context.Context, expected CredentialSnapshot, disabled bool) (CredentialSnapshot, error) {
	if expected.ProviderMechanism != "" {
		return h.setProviderDisabled(ctx, expected, disabled)
	}
	current, err := h.CredentialSnapshot(ctx, expected.AuthID, expected.AuthIndex)
	if err != nil || !sameSnapshot(current, expected) || current.Disabled == disabled {
		return CredentialSnapshot{}, errCredentialPrecondition
	}
	var response struct {
		Status   string `json:"status"`
		Disabled *bool  `json:"disabled"`
	}
	err = h.Management.request(ctx, http.MethodPatch, "/v8/management/credentials/status", map[string]any{"name": expected.Name, "auth_index": expected.AuthIndex, "disabled": disabled}, &response, 4096)
	if err != nil {
		return CredentialSnapshot{}, err
	}
	if response.Status != "ok" || response.Disabled == nil || *response.Disabled != disabled {
		return CredentialSnapshot{}, fmt.Errorf("management_uncertain: status was not confirmed")
	}
	current, err = h.CredentialSnapshot(ctx, expected.AuthID, expected.AuthIndex)
	if err != nil || !sameCredential(current, expected) || current.Disabled != disabled {
		return CredentialSnapshot{}, fmt.Errorf("management_uncertain: updated credential could not be verified")
	}
	// CPA publishes the persisted file through its watcher after status returns.
	// Wait briefly for this runtime timestamp to settle, while refusing any new
	// file write or content change. Never relax later recovery comparisons.
	fileTime := current.FileModifiedAt
	stable := 0
	for range 8 {
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return CredentialSnapshot{}, ctx.Err()
		case <-timer.C:
		}
		next, readErr := h.CredentialSnapshot(ctx, expected.AuthID, expected.AuthIndex)
		if readErr != nil || !sameCredential(next, expected) || next.Disabled != disabled || !next.FileModifiedAt.Equal(fileTime) {
			return CredentialSnapshot{}, fmt.Errorf("management_uncertain: credential changed during CPA synchronization")
		}
		if sameSnapshot(next, current) {
			stable++
		} else {
			stable = 0
		}
		current = next
		if stable >= 3 {
			return current, nil
		}
	}
	return CredentialSnapshot{}, fmt.Errorf("management_uncertain: CPA credential synchronization did not settle")
}

func (h *NativeHost) DeleteCredential(ctx context.Context, expected CredentialSnapshot) error {
	if expected.ProviderMechanism != "" || familyOfProviderID(expected.AuthID) != "" {
		return fmt.Errorf("unsupported: provider configuration cannot be removed")
	}
	current, err := h.CredentialSnapshot(ctx, expected.AuthID, expected.AuthIndex)
	if err != nil || !sameSnapshot(current, expected) {
		return errCredentialPrecondition
	}
	// Never use the bulk/all endpoint. CPA's delete API is filename based.
	var response struct {
		Status string `json:"status"`
	}
	err = h.Management.request(ctx, http.MethodDelete, "/v8/management/credentials?name="+url.QueryEscape(expected.Name), nil, &response, 4096)
	if err != nil {
		return err
	}
	if response.Status != "ok" {
		return fmt.Errorf("management_uncertain: removal was not confirmed")
	}
	dir, err := h.credentialDirectory(ctx)
	if err != nil || !dir.absent(expected) || !h.missingCredentialFile(ctx, expected.Name) {
		return fmt.Errorf("management_uncertain: removal could not be verified")
	}
	return nil
}

func (h *NativeHost) executeManaged(ctx context.Context, e Execution) (s Sample) {
	start := time.Now()
	s = Sample{Model: e.Model, ExecutionStatus: "failed", QualityVerdict: "unknown", Evidence: Evidence{Path: "cpa_management_probe", Reason: "management_probe_not_business_payload"}}
	defer func() {
		s.TotalMS = time.Since(start).Milliseconds()
		if ctx.Err() != nil {
			s.ExecutionStatus = "cancelled"
			s.UpstreamCondition = ""
			s.Error = "cancelled"
		}
	}()
	expected := *e.ManagedProbe
	current, runtime, data, err := h.credentialProfile(ctx, expected.AuthID, expected.AuthIndex)
	if err != nil || !sameSnapshot(current, expected) || e.AuthID != expected.AuthID || e.Provider != expected.Provider {
		s.Error = "credential changed before management probe"
		return
	}
	base := runtime.BaseURL
	if base == "" {
		base, _ = data["base_url"].(string)
	}
	codex := expected.Provider == "codex"
	if base == "" {
		if codex {
			base = "https://chatgpt.com/backend-api/codex"
		} else {
			base = "https://api.openai.com/v1"
		}
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		s.Error = "invalid credential upstream URL"
		return
	}
	doc := ojson.NewDocString(`{}`)
	fields := map[string]any{"model": managementProbeText(e.Model), "stream": false, "max_tokens": e.Parameters.MaxTokens, "messages": []map[string]any{{"role": "user", "content": managementProbeText(e.Prompt)}}}
	headers := map[string]string{"Authorization": "Bearer $TOKEN$", "Content-Type": "application/json", "Accept": "application/json"}
	path := "/chat/completions"
	if codex {
		path = "/responses"
		fields = map[string]any{"model": managementProbeText(e.Model), "stream": true, "store": false, "instructions": "Answer the question directly.", "input": []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": managementProbeText(e.Prompt)}}}}, "max_output_tokens": e.Parameters.MaxTokens}
		headers["Accept"] = "text/event-stream"
		headers["Originator"] = "codex-tui"
		headers["User-Agent"] = "codex-tui/0.154.0"
		if account, _ := data["account_id"].(string); account != "" {
			headers["Chatgpt-Account-Id"] = account
		}
		if e.Parameters.ReasoningEffort != "" {
			fields["reasoning"] = map[string]string{"effort": e.Parameters.ReasoningEffort}
		}
	} else {
		if e.Parameters.ReasoningEffort != "" {
			fields["reasoning_effort"] = e.Parameters.ReasoningEffort
		}
		if e.Parameters.Temperature != nil {
			fields["temperature"] = *e.Parameters.Temperature
		}
	}
	for k, v := range fields {
		if err := doc.Set(k, v); err != nil {
			s.Error = "request construction failed"
			return
		}
	}
	limit := e.ResponseLimit
	if limit < 1024 {
		limit = h.MaxResponseBytes
	}
	if limit < 1024 {
		limit = 262144
	}
	var response managementProbeResponse
	err = h.Management.request(ctx, http.MethodPost, "/v8/management/requests/api-call", map[string]any{"auth_index": expected.AuthIndex, "method": "POST", "url": strings.TrimRight(base, "/") + path, "header": headers, "data": doc.String()}, &response, int64(limit)*6+16384)
	if err != nil {
		s.Error = scrubError(err)
		return
	}
	confirmProbe := func() bool {
		after, readErr := h.CredentialSnapshot(ctx, expected.AuthID, expected.AuthIndex)
		if readErr != nil || !sameSnapshot(after, expected) {
			s.Error = "credential changed during management probe"
			s.Evidence.ConditionsChanged = true
			return false
		}
		s.Evidence.AuthID = e.AuthID
		s.Evidence.RequestedModel = e.Model
		s.Evidence.PayloadFingerprint = fingerprint(fields)
		return true
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.Error = fmt.Sprintf("upstream HTTP %d", response.StatusCode)
		if len(response.Body) <= limit {
			if condition := managedUpstreamCondition(response.StatusCode, response.Body, e.Model, response.Header); condition != "" && confirmProbe() {
				s.UpstreamCondition = condition
			}
		}
		return
	}
	if len(response.Body) > limit {
		s.Error = "response limit exceeded"
		return
	}
	if codex {
		s.Output, s.ResponseModel, s.Usage, err = parseManagedResponses(response.Body)
	} else {
		c := newCollector(limit)
		err = c.Completion([]byte(response.Body))
		if err == nil && (c.refusal || c.finish != "stop") {
			err = fmt.Errorf("incomplete or refused answer")
		}
		s.Output, s.ResponseModel, s.Usage = c.text.String(), c.model, c.usage
	}
	if err != nil {
		s.Error = scrubError(err)
		return
	}
	if strings.TrimSpace(s.Output) == "" || s.ResponseModel == "" {
		s.ExecutionStatus = "incomplete"
		return
	}
	if !confirmProbe() {
		return
	}
	s.ExecutionStatus = "completed"
	s.Evidence.ResolvedModel = s.ResponseModel
	return
}

// Only the complete terminal Responses object is evidence; deltas, truncation,
// refusals, tool calls and successful HTTP statuses alone cannot pass a round.
func parseManagedResponses(body string) (string, string, *Usage, error) {
	var terminal ojson.Result
	var responseID, responseModel string
	done := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			return "", "", nil, fmt.Errorf("invalid responses stream")
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "[DONE]" {
			done = true
			continue
		}
		if done {
			return "", "", nil, fmt.Errorf("event after responses stream end")
		}
		if !ojson.Valid(raw) || uniqueJSON([]byte(raw)) != nil {
			return "", "", nil, fmt.Errorf("invalid responses event")
		}
		event := ojson.Parse(raw)
		if !observeResponseIdentity(event.Get("response.id"), &responseID) || !observeResponseIdentity(event.Get("response_id"), &responseID) ||
			!observeResponseIdentity(event.Get("response.model"), &responseModel) {
			return "", "", nil, fmt.Errorf("conflicting responses identity")
		}
		switch event.Get("type").String() {
		case "error", "response.failed", "response.incomplete":
			return "", "", nil, fmt.Errorf("incomplete responses result")
		case "response.completed":
			if terminal.Exists() {
				return "", "", nil, fmt.Errorf("duplicate responses completion")
			}
			terminal = event.Get("response")
		default:
			if terminal.Exists() {
				return "", "", nil, fmt.Errorf("event after responses completion")
			}
		}
	}
	if terminal.Get("status").String() != "completed" || terminal.Get("error").Raw != "" && terminal.Get("error").Raw != "null" {
		return "", "", nil, fmt.Errorf("missing completed responses result")
	}
	if details := terminal.Get("incomplete_details"); details.Exists() && details.Raw != "null" {
		return "", "", nil, fmt.Errorf("responses result contains incomplete details")
	}
	output := terminal.Get("output")
	if !output.IsArray() {
		return "", "", nil, fmt.Errorf("invalid responses output")
	}
	var text strings.Builder
	answerSeen := false
	var completionErr error
	for _, item := range output.Array() {
		if item.Get("type").String() == "reasoning" {
			continue
		}
		if item.Get("type").String() != "message" {
			return "", "", nil, fmt.Errorf("non-answer responses output")
		}
		if item.Get("status").String() != "completed" {
			completionErr = fmt.Errorf("unconfirmed responses message status")
		}
		if role := item.Get("role"); role.Exists() && (role.Type != ojson.String || role.String() != "assistant") {
			return "", "", nil, fmt.Errorf("non-assistant responses message")
		}
		// Older endpoints omit phase. A single unphased message can be the answer.
		phase := item.Get("phase")
		commentary := phase.Type == ojson.String && phase.String() == "commentary"
		if phase.Exists() && phase.Raw != "null" && !commentary && (phase.Type != ojson.String || phase.String() != "final_answer") {
			return "", "", nil, fmt.Errorf("unknown responses message phase")
		}
		if answerSeen {
			return "", "", nil, fmt.Errorf("unexpected message after final answer")
		}
		content := item.Get("content")
		if !content.IsArray() {
			return "", "", nil, fmt.Errorf("invalid responses message content")
		}
		for _, part := range content.Array() {
			value := part.Get("text")
			if part.Get("type").String() != "output_text" || value.Type != ojson.String {
				return "", "", nil, fmt.Errorf("refused or non-text answer")
			}
			if !commentary {
				text.WriteString(value.String())
			}
		}
		answerSeen = !commentary
	}
	if !answerSeen {
		return "", "", nil, fmt.Errorf("missing final responses answer")
	}
	model := terminal.Get("model")
	if model.Exists() && model.Raw != "null" && model.Type != ojson.String {
		return text.String(), "", nil, fmt.Errorf("invalid response model")
	}
	u := terminal.Get("usage")
	doc := ojson.NewDocString(`{}`)
	_ = doc.Set("prompt_tokens", u.Get("input_tokens").Value())
	_ = doc.Set("completion_tokens", u.Get("output_tokens").Value())
	_ = doc.Set("total_tokens", u.Get("total_tokens").Value())
	return text.String(), strings.TrimSpace(model.String()), parseUsage(ojson.ParseBytes(doc.Bytes())), completionErr
}
