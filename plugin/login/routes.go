package login

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const RoutePrefix = "/v0/management/plugins/model-quality/login"

// These routes are plugin-owned and inherit the official host's management
// authentication. Public plugin resources contain only static UI assets.
func ManagementRoutes() []map[string]string {
	routes := []map[string]string{}
	for _, p := range []string{"/capabilities", "/targets", "/jobs", "/logs", "/detail", "/candidate", "/vault/status", "/accounts", "/accounts/detail", "/accounts/settings"} {
		routes = append(routes, map[string]string{"method": "GET", "path": RoutePrefix + p})
	}
	for _, p := range []string{"/start", "/callback", "/cancel", "/discard", "/commit", "/accounts/import", "/accounts/start", "/accounts/inspect", "/accounts/security", "/accounts/security/complete", "/conversion/preview", "/conversion/upload"} {
		routes = append(routes, map[string]string{"method": "POST", "path": RoutePrefix + p})
	}
	for _, p := range []string{"/vault/entry", "/accounts/entry"} {
		for _, method := range []string{"PUT", "DELETE"} {
			routes = append(routes, map[string]string{"method": method, "path": RoutePrefix + p})
		}
	}
	routes = append(routes, map[string]string{"method": "DELETE", "path": RoutePrefix + "/jobs"})
	routes = append(routes, map[string]string{"method": "PUT", "path": RoutePrefix + "/accounts/settings"})
	return routes
}
func reply(status int, value any) pluginapi.ManagementResponse {
	b, e := Encode(value)
	if e != nil {
		status = 500
		b = []byte(`{"error":"storage_unavailable"}`)
	}
	return pluginapi.ManagementResponse{StatusCode: status, Headers: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"}, "Pragma": {"no-cache"}, "X-Content-Type-Options": {"nosniff"}}, Body: b}
}
func errorReply(err error) pluginapi.ManagementResponse {
	code := SafeCode(err)
	status := 409
	switch code {
	case "not_found":
		status = 404
	case "invalid_request", "invalid_credentials", "invalid_proxy", "invalid_callback":
		status = 400
	case "runtime_unavailable", "storage_unavailable", "plugin_unavailable", "cpa_credential_unavailable", "quality_authority_unavailable":
		status = 503
	case "rate_limited", "queue_full":
		status = 429
	}
	source := "business"
	switch code {
	case "runtime_unavailable", "runtime_failed", "runtime_timeout", "runtime_protocol_error", "runtime_invalid":
		source = "login_runtime"
	case "plugin_unavailable", "storage_unavailable":
		source = "plugin"
	case "cpa_credential_unavailable", "quality_authority_unavailable":
		source = "sdk"
	}
	return reply(status, map[string]any{"error": code, "code": code, "source": source, "outcome_unknown": code == "idempotency_unknown" || code == "commit_unknown" || code == "security_uncertain" || code == "saved_not_adopted"})
}
func (s *Service) Handle(ctx context.Context, req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	if s == nil {
		return errorReply(errors.New("plugin_unavailable"))
	}
	if !strings.HasPrefix(req.Path, RoutePrefix+"/") {
		return reply(404, map[string]string{"error": "not_found"})
	}
	path := strings.TrimPrefix(req.Path, RoutePrefix)
	if req.Method == http.MethodGet {
		var value any
		var err error
		limit := 200
		accountID := ""
		var cursor uint64
		for k, v := range req.Query {
			if len(v) != 1 {
				return errorReply(errors.New("invalid_request"))
			}
			switch k {
			case "account_id":
				if path != "/jobs" || v[0] == "" || len(v[0]) > 512 {
					return errorReply(errors.New("invalid_request"))
				}
				accountID = v[0]
			case "id", "auth_id":
				if len(v[0]) > 8192 {
					return errorReply(errors.New("invalid_request"))
				}
			case "limit":
				if path != "/jobs" && path != "/logs" {
					return errorReply(errors.New("invalid_request"))
				}
				limit, err = strconv.Atoi(v[0])
				if err != nil || limit < 1 || limit > 200 {
					return errorReply(errors.New("invalid_request"))
				}
			case "cursor":
				if path != "/logs" {
					return errorReply(errors.New("invalid_request"))
				}
				cursor, err = strconv.ParseUint(v[0], 10, 64)
				if err != nil {
					return errorReply(errors.New("invalid_request"))
				}
			default:
				return errorReply(errors.New("invalid_request"))
			}
		}
		switch path {
		case "/capabilities":
			value = s.Capabilities()
		case "/targets":
			value = map[string]any{"items": s.Targets()}
		case "/jobs":
			var items []Job
			var more bool
			items, more, err = s.JobsPage(accountID, limit)
			value = map[string]any{"items": items, "has_more": more}
		case "/logs":
			var logs JobLogs
			logs, err = s.Logs(req.Query.Get("id"))
			filtered := []LogEntry{}
			logs.NextCursor = cursor
			for _, entry := range logs.Entries {
				if entry.ID <= cursor {
					continue
				}
				if len(filtered) == limit {
					logs.HasMore = true
					break
				}
				filtered = append(filtered, entry)
				logs.NextCursor = entry.ID
			}
			logs.Entries = filtered
			value = logs
		case "/detail":
			value, err = s.Detail(req.Query.Get("id"))
		case "/candidate":
			value, err = s.Candidate(req.Query.Get("id"))
		case "/vault/status":
			value = s.VaultStatus(req.Query.Get("auth_id"))
		case "/accounts":
			var items []Account
			items, err = s.Accounts()
			value = map[string]any{"items": items}
		case "/accounts/detail":
			value, err = s.AccountDetail(req.Query.Get("id"))
		case "/accounts/settings":
			value, err = s.AccountSettings()
		default:
			return reply(404, map[string]string{"error": "not_found"})
		}
		if err != nil {
			return errorReply(err)
		}
		return reply(200, value)
	}
	validMethod := false
	for _, r := range ManagementRoutes() {
		if r["path"] == req.Path && r["method"] == req.Method {
			validMethod = true
			break
		}
	}
	if !validMethod {
		return reply(405, map[string]string{"error": "invalid_request"})
	}
	limit := 16 << 10
	if path == "/accounts/import" {
		limit = 512 << 10
	}
	if isConversionPath(path) {
		limit = 4 << 20
	}
	if len(req.Query) != 0 || len(req.Body) > limit {
		return errorReply(errors.New("invalid_request"))
	}
	// A preview is a pure transformation; never cache its token-bearing response.
	if path == "/conversion/preview" {
		value, err := s.conversionPreview(req.Body)
		if err != nil {
			return errorReply(err)
		}
		return reply(200, value)
	}
	body, err := s.Idempotent(req.Headers.Get("Idempotency-Key"), req.Method+path, req.Body, func() (any, error) { return s.write(ctx, req.Method, path, req.Body) })
	if err != nil {
		return errorReply(err)
	}
	r := reply(200, nil)
	r.Body = body
	return r
}
func (s *Service) write(ctx context.Context, method, path string, raw []byte) (any, error) {
	if path == "/conversion/upload" && method == http.MethodPost {
		return s.uploadConversion(ctx, raw)
	}
	if strings.HasPrefix(path, "/accounts/") {
		return credentialAccountWrite(s, method, path, raw)
	}
	if path == "/start" {
		r, e := DecodeStart(raw)
		if e != nil {
			return nil, e
		}
		return s.Start(r)
	}
	if path == "/vault/entry" {
		r, e := Object(raw, "auth_id", "revision", "credentials")
		if e != nil {
			return nil, e
		}
		if e = validateLoginFields(r); e != nil {
			return nil, e
		}
		id := r.Get("auth_id").String()
		if id == "" {
			return nil, errors.New("invalid_request")
		}
		if method == "DELETE" {
			if r.Get("credentials").Exists() {
				return nil, errors.New("invalid_request")
			}
			e = s.DeleteVault(id, r.Get("revision").Uint())
		} else {
			c, ce := DecodeCredentials(r.Get("credentials"))
			if ce != nil {
				return nil, ce
			}
			e = s.PutVault(id, r.Get("revision").Uint(), c)
		}
		return s.VaultStatus(id), e
	}
	allowed := []string{"job_id", "revision"}
	if path == "/callback" {
		allowed = append(allowed, "callback_url")
	}
	if path == "/commit" {
		allowed = append(allowed, "candidate_id")
	}
	r, e := Object(raw, allowed...)
	if e != nil {
		return nil, e
	}
	if e = validateLoginFields(r); e != nil {
		return nil, e
	}
	id := r.Get("job_id").String()
	if id == "" {
		return nil, errors.New("invalid_request")
	}
	switch path {
	case "/jobs":
		if !r.Get("revision").Exists() {
			return nil, errors.New("invalid_request")
		}
		if err := s.DeleteJob(id, r.Get("revision").Uint()); err != nil {
			return nil, err
		}
		return map[string]bool{"deleted": true}, nil
	case "/callback":
		return s.Callback(id, r.Get("callback_url").String())
	case "/cancel":
		return s.Cancel(id, false)
	case "/discard":
		return s.Cancel(id, true)
	case "/commit":
		return s.Commit(ctx, id, r.Get("candidate_id").String(), r.Get("revision").Uint())
	}
	return nil, errors.New("not_found")
}
func validateLoginFields(r ojson.Result) error {
	valid := true
	r.ForEach(func(k, v ojson.Result) bool {
		switch k.String() {
		case "revision":
			_, e := strconv.ParseUint(v.Raw, 10, 64)
			valid = v.Type == ojson.Number && e == nil
		case "credentials":
			valid = v.IsObject()
		default:
			valid = v.Type == ojson.String && len(v.String()) <= 8192
		}
		return valid
	})
	if !valid {
		return errors.New("invalid_request")
	}
	return nil
}
