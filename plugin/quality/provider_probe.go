package quality

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	"golang.org/x/net/http/httpguts"
)

const providerConfigProbe = "provider_config_probe"

func (r providerRoute) safeHeaders() bool {
	headers, _ := r.effective("headers").(map[string]any)
	seen := make(map[string]bool, len(headers))
	for name, raw := range headers {
		value, ok := raw.(string)
		lower := strings.ToLower(name)
		if !ok || seen[lower] || !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) || strings.Contains(value, "$") {
			return false
		}
		switch lower {
		case "host", "forwarded", "proxy-authorization", "proxy-connection", "content-length", "transfer-encoding", "connection", "trailer", "upgrade", "te", "expect":
			return false
		}
		seen[lower] = true
	}
	return true
}

func (r providerRoute) sensitiveText(text string) bool {
	if text == "" {
		return false
	}
	secrets := []string{configText(r.key["api-key"])}
	headers, _ := r.effective("headers").(map[string]any)
	for name, value := range headers {
		raw := configText(value)
		secrets = append(secrets, raw)
		if scheme, v, ok := strings.Cut(raw, " "); ok && (strings.EqualFold(scheme, "Bearer") || strings.EqualFold(scheme, "Basic")) {
			secrets = append(secrets, strings.TrimSpace(v))
		}
		if strings.EqualFold(name, "Cookie") {
			for _, part := range strings.Split(raw, ";") {
				if _, v, ok := strings.Cut(part, "="); ok {
					secrets = append(secrets, strings.TrimSpace(v))
				}
			}
		}
	}
	if u, err := url.Parse(r.proxy); err == nil && u.User != nil {
		password, _ := u.User.Password()
		secrets = append(secrets, r.proxy, u.User.String(), u.User.Username(), password)
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(text, s) {
			return true
		}
	}
	return false
}

func (h *NativeHost) executeProvider(ctx context.Context, e Execution) (s Sample) {
	start := time.Now()
	s = Sample{Model: e.Model, ExecutionStatus: "failed", QualityVerdict: "unknown", Evidence: Evidence{Path: providerConfigProbe, Reason: "configuration_pinned_probe"}}
	defer func() {
		s.TotalMS = time.Since(start).Milliseconds()
		if ctx.Err() != nil {
			s.ExecutionStatus = "cancelled"
			s.UpstreamCondition = ""
			s.Error = "cancelled"
		}
	}()
	expected := *e.ManagedProbe
	r, err := h.pinnedConfiguredRoute(ctx, e.AuthID, expected.AuthIndex)
	if err != nil || !sameSnapshot(r.snapshot(), expected) || e.Provider != expected.Provider || e.AuthID != expected.AuthID {
		s.ExecutionStatus = "skipped"
		s.Error = "供应商路由或配置快照已变化。"
		return
	}
	wire := r.wireModel(e.Model)
	if reason := r.probeReason(); reason != "" {
		s.ExecutionStatus = "skipped"
		s.Error = reason
		return
	}
	if wire == "" {
		s.ExecutionStatus = "skipped"
		s.Error = "无法唯一核实当前模型的上游映射。"
		return
	}
	if r.forcePrefix && r.prefix != "" && !strings.HasPrefix(e.Model, r.prefix+"/") && e.Model != r.prefix {
		s.ExecutionStatus = "skipped"
		s.Error = "CPA 模型前缀配置未允许所选模型。"
		return
	}
	transport, _, err := proxyutil.BuildHTTPTransport(r.proxy)
	if err != nil {
		s.ExecutionStatus = "skipped"
		s.Error = "无法核实此提供商使用的代理。"
		return
	}
	if transport == nil {
		transport = proxyutil.NewDirectTransport()
	}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, fields, err := r.probeRequest(ctx, e, wire)
	if err != nil {
		s.ExecutionStatus = "skipped"
		s.Error = err.Error()
		return
	}
	response, err := client.Do(request)
	if err != nil {
		s.Error = "供应商定向探测连接失败。"
		return
	}
	defer response.Body.Close()
	limit := e.ResponseLimit
	if limit < 1024 {
		limit = h.MaxResponseBytes
	}
	if limit < 1024 {
		limit = 262144
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil || len(raw) > limit {
		s.Error = "未收到完整且在大小限额内的供应商响应。"
		return
	}
	after, err := h.pinnedConfiguredRoute(ctx, e.AuthID, expected.AuthIndex)
	if err != nil || !sameSnapshot(after.snapshot(), expected) || after.etag != r.etag {
		s.Evidence.ConditionsChanged = true
		s.Error = "定向探测期间 CPA 配置或身份已变化。"
		return
	}
	s.Evidence.AuthID = e.AuthID
	s.Evidence.RequestedModel = e.Model
	s.Evidence.PayloadFingerprint = fingerprint(fields)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.Error = fmt.Sprintf("upstream HTTP %d", response.StatusCode)
		s.UpstreamCondition = managedUpstreamCondition(response.StatusCode, string(raw), wire, response.Header)
		return
	}
	output, model, usage, err := parseProviderAnswer(r.family, raw, limit)
	if err != nil || model == "" || strings.TrimSpace(output) == "" {
		s.ExecutionStatus = "incomplete"
		s.Error = "未收到可核实的完整最终回答。"
		return
	}
	if r.sensitiveText(output) || r.sensitiveText(model) {
		s.ExecutionStatus = "incomplete"
		s.Error = "响应包含敏感配置内容，已隐藏。"
		return
	}
	if model != wire {
		s.ExecutionStatus = "incomplete"
		s.Error = "上游返回的模型与定向配置映射不一致。"
		return
	}
	s.Output, s.ResponseModel, s.Usage = output, model, usage
	s.Evidence.ResolvedModel = model
	s.ExecutionStatus = "completed"
	return
}

var googleVersionSuffix = regexp.MustCompile(`/v[0-9]+(?:beta[0-9]*)?$`)
var googleModelSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (r providerRoute) probeRequest(ctx context.Context, e Execution, wire string) (*http.Request, map[string]any, error) {
	base := strings.TrimRight(r.base, "/")
	endpoint := base
	key := configText(r.key["api-key"])
	headers := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}}
	if key != "" {
		headers.Set("Authorization", "Bearer "+key)
	}
	fields := map[string]any{}
	switch r.family {
	case "openai-compatibility":
		endpoint += "/chat/completions"
		fields = map[string]any{"model": wire, "messages": []map[string]string{{"role": "user", "content": e.Prompt}}, "stream": false}
		maxTokens := "max_tokens"
		for _, m := range configList(r.effective("models")) {
			if configText(m["name"]) == wire && m["use-max-completion-tokens"] == true {
				maxTokens = "max_completion_tokens"
				break
			}
		}
		fields[maxTokens] = e.Parameters.MaxTokens
		if e.Parameters.Temperature != nil {
			fields["temperature"] = *e.Parameters.Temperature
		}
		if e.Parameters.ReasoningEffort != "" {
			fields["reasoning_effort"] = e.Parameters.ReasoningEffort
		}
	case "codex", "meta", "xai":
		endpoint += "/responses"
		headers.Set("Accept", "text/event-stream")
		fields = map[string]any{"model": wire, "input": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": e.Prompt}}}}, "stream": true, "store": false, "max_output_tokens": e.Parameters.MaxTokens}
		if e.Parameters.ReasoningEffort != "" {
			fields["reasoning"] = map[string]string{"effort": e.Parameters.ReasoningEffort}
		}
		if e.Parameters.Temperature != nil {
			fields["temperature"] = *e.Parameters.Temperature
		}
	case "claude":
		endpoint += "/v1/messages?beta=true"
		u, _ := url.Parse(base)
		if u.Scheme == "https" && strings.EqualFold(u.Hostname(), "api.anthropic.com") && (u.Port() == "" || u.Port() == "443") {
			headers.Del("Authorization")
			if key != "" {
				headers.Set("x-api-key", key)
			}
		}
		headers.Set("anthropic-version", "2023-06-01")
		fields = map[string]any{"model": wire, "max_tokens": e.Parameters.MaxTokens, "messages": []map[string]string{{"role": "user", "content": e.Prompt}}}
		if e.Parameters.Temperature != nil {
			fields["temperature"] = *e.Parameters.Temperature
		}
		if e.Parameters.ReasoningEffort != "" {
			return nil, nil, fmt.Errorf("此供应商所选思考参数需要 CPA 原生转换。")
		}
	case "gemini", "vertex", "interactions":
		headers.Del("Authorization")
		if key != "" {
			headers.Set("x-goog-api-key", key)
		}
		if e.Parameters.ReasoningEffort != "" {
			return nil, nil, fmt.Errorf("此供应商所选思考参数需要 CPA 原生转换。")
		}
		if r.family == "interactions" {
			endpoint += "/v1beta/interactions"
			headers.Set("Api-Revision", "2026-05-20")
			cfg := map[string]any{"max_output_tokens": e.Parameters.MaxTokens}
			if e.Parameters.Temperature != nil {
				cfg["temperature"] = *e.Parameters.Temperature
			}
			fields = map[string]any{"model": wire, "input": e.Prompt, "store": false, "generation_config": cfg}
		} else {
			if !googleModelSegment.MatchString(wire) {
				return nil, nil, fmt.Errorf("无法安全定位当前模型的上游路径。")
			}
			if r.family == "gemini" {
				endpoint += "/v1beta/models/"
			} else if strings.Contains(base, "/publishers/") {
				endpoint += "/"
			} else {
				if !googleVersionSuffix.MatchString(base) {
					endpoint += "/v1"
				}
				endpoint += "/publishers/google/models/"
			}
			endpoint += wire + ":generateContent"
			cfg := map[string]any{"maxOutputTokens": e.Parameters.MaxTokens}
			if e.Parameters.Temperature != nil {
				cfg["temperature"] = *e.Parameters.Temperature
			}
			fields = map[string]any{"generationConfig": cfg, "contents": []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": e.Prompt}}}}}
		}
	default:
		return nil, nil, fmt.Errorf("无法核实供应商探测协议。")
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("无法构造定向探测请求。")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("提供商地址无效。")
	}
	req.Header = headers
	custom, _ := r.effective("headers").(map[string]any)
	for name, v := range custom {
		if val := configText(v); val != "" {
			req.Header.Set(name, val)
		}
	}
	return req, fields, nil
}

func parseProviderAnswer(family string, raw []byte, limit int) (string, string, *Usage, error) {
	incomplete := fmt.Errorf("incomplete provider response")
	switch family {
	case "openai-compatibility":
		c := newCollector(limit)
		if err := c.Completion(raw); err != nil || c.refusal || c.finish != "stop" {
			return "", "", nil, incomplete
		}
		return c.text.String(), c.model, c.usage, nil
	case "codex", "meta", "xai":
		return parseManagedResponses(string(raw))
	}
	if !ojson.ValidBytes(raw) || uniqueJSON(raw) != nil {
		return "", "", nil, incomplete
	}
	root := ojson.ParseBytes(raw)
	if e := root.Get("error"); e.Exists() && e.Raw != "null" {
		return "", "", nil, incomplete
	}
	var text strings.Builder
	model := root.Get("model").String()
	parts := root.Get("content")
	switch family {
	case "claude":
		if root.Get("type").String() != "message" || root.Get("role").String() != "assistant" || root.Get("stop_reason").String() != "end_turn" {
			return "", "", nil, incomplete
		}
	case "gemini", "vertex":
		candidates := root.Get("candidates").Array()
		if len(candidates) != 1 || candidates[0].Get("finishReason").String() != "STOP" || candidates[0].Get("content.role").String() != "model" {
			return "", "", nil, incomplete
		}
		parts = candidates[0].Get("content.parts")
		model = root.Get("modelVersion").String()
	case "interactions":
		if nested := root.Get("interaction"); nested.Exists() {
			root = nested
		}
		if root.Get("status").String() != "completed" {
			return "", "", nil, incomplete
		}
		model = root.Get("model").String()
		seen := false
		for _, step := range root.Get("steps").Array() {
			switch step.Get("type").String() {
			case "thought", "user_input":
				if seen {
					return "", "", nil, incomplete
				}
			case "model_output":
				if seen {
					return "", "", nil, incomplete
				}
				seen = true
				parts = step.Get("content")
			default:
				return "", "", nil, incomplete
			}
		}
		if !seen {
			return "", "", nil, incomplete
		}
	default:
		return "", "", nil, incomplete
	}
	if !parts.IsArray() {
		return "", "", nil, incomplete
	}
	for _, part := range parts.Array() {
		t := part.Get("type").String()
		if (family == "gemini" || family == "vertex") && part.Get("thought").Bool() || family == "claude" && t == "thinking" {
			continue
		}
		v := part.Get("text")
		if v.Type != ojson.String || (family == "claude" || family == "interactions") && t != "text" || part.Get("functionCall").Exists() || part.Get("inlineData").Exists() {
			return "", "", nil, incomplete
		}
		text.WriteString(v.String())
	}
	return text.String(), model, nil, nil
}
