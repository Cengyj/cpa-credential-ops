package quality

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type providerDiagnosticReply struct {
	status int
	body   string
}

func providerDiagnosticFixture(t *testing.T) (*Engine, *Rule, *NativeHost, func(int, string), string, string) {
	t.Helper()
	key := providerObject{"api-key": "offline-diagnostic-private-key"}
	route := providerRoute{family: "openai-compatibility", name: "diagnostic", executor: "openai-compatible-diagnostic",
		base: "https://offline.invalid/v1", key: key, keys: []providerObject{key}}
	id, index := route.identity()
	key["auth_index"] = index
	config := map[string][]providerObject{"openai-compatibility": {{"name": route.name, "base-url": route.base, "keys": []providerObject{key}}}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var reply atomic.Value
	reply.Store(providerDiagnosticReply{http.StatusOK, string(raw)})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != providerConfigPath {
			t.Errorf("diagnostic admission made an unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		current := reply.Load().(providerDiagnosticReply)
		w.WriteHeader(current.status)
		_, _ = w.Write([]byte(current.body))
	}))
	t.Cleanup(server.Close)
	management, err := NewManagementClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host := &NativeHost{Management: management, Call: func(method string, request, response any) error {
		switch method {
		case "host.auth.list":
			return decode([]byte(`{"files":[]}`), response)
		case "host.auth.models":
			response.(*pluginapi.HostAuthModelsResponse).Credentials = []pluginapi.HostAuthModels{{ID: id, AuthIndex: index, Provider: route.executor, Models: []string{"a", "b"}}}
			return nil
		case "host.auth.get_runtime":
			return errors.New("configured route has no file runtime")
		default:
			t.Errorf("diagnostic admission called a model or mutated a credential: %s", method)
			return errors.New("unexpected callback")
		}
	}}
	en, _, rule := scopeFixture(t)
	rule.ObjectTypes = []string{objectProvider}
	rule, err = en.SaveRule(context.Background(), *rule, true)
	if err != nil {
		t.Fatal(err)
	}
	en.host = host
	return en, rule, host, func(status int, body string) { reply.Store(providerDiagnosticReply{status, body}) }, string(raw), id
}

func TestProviderScopeDirectoryDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, code, reason, status string
		httpStatus                 int
		body                       string
		authorized, duplicate      bool
	}{
		{name: "session absent", code: "provider_directory_unavailable", reason: "管理会话", status: "skipped"},
		{name: "management unauthorized", code: "provider_directory_unavailable", reason: "HTTP 401", status: "skipped", httpStatus: 401, authorized: true},
		{name: "management forbidden", code: "provider_directory_unavailable", reason: "HTTP 403", status: "skipped", httpStatus: 403, authorized: true},
		{name: "management unavailable", code: "provider_directory_unavailable", reason: "HTTP 503", status: "skipped", httpStatus: 503, authorized: true},
		{name: "invalid configuration", code: "provider_directory_unavailable", reason: "配置响应", status: "skipped", httpStatus: 200, body: "private-invalid-response", authorized: true},
		{name: "trailing configuration data", code: "provider_directory_unavailable", reason: "配置响应", status: "skipped", httpStatus: 200, body: "{} private-invalid-response", authorized: true},
		{name: "multiple configuration values", code: "provider_directory_unavailable", reason: "配置响应", status: "skipped", httpStatus: 200, body: "{} {}", authorized: true},
		{name: "null directory", code: "provider_directory_unavailable", reason: "配置响应", status: "skipped", httpStatus: 200, body: "null", authorized: true},
		{name: "route not matched", code: "provider_route_unverified", reason: "未能匹配", status: "skipped", httpStatus: 200, body: "{}", authorized: true},
		{name: "official absent directory", code: "provider_route_unverified", reason: "未能匹配", status: "skipped", httpStatus: 404, body: `{"error":"not_found"}`, authorized: true},
		{name: "duplicate index", code: "identity_conflict", reason: "标识互相冲突", status: "blocked", httpStatus: 200, authorized: true, duplicate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			en, rule, host, setReply, good, id := providerDiagnosticFixture(t)
			if tc.authorized {
				host.Management.authorize(http.Header{"Authorization": {"Bearer offline-panel"}})
			}
			body := tc.body
			if tc.duplicate {
				var config map[string][]providerObject
				if err := json.Unmarshal([]byte(good), &config); err != nil {
					t.Fatal(err)
				}
				group := config["openai-compatibility"][0]
				keys := configList(group["keys"])
				group["keys"] = []providerObject{keys[0], keys[0]}
				raw, _ := json.Marshal(config)
				body = string(raw)
			}
			if tc.httpStatus != 0 {
				setReply(tc.httpStatus, body)
			}
			receipt, err := en.dispatchRule(context.Background(), rule.ID, rule.Revision, false)
			if err != nil || receipt.Count != len(receipt.PreflightDetails) || len(en.queue) != 0 {
				t.Fatal("unverified route was admitted", receipt, err)
			}
			var run *Run
			for _, candidate := range en.runs {
				if candidate.Config.ScopeBatchID == receipt.BatchID {
					if run != nil {
						t.Fatal("duplicate diagnostic runs for one target")
					}
					run = candidate
				}
			}
			if run == nil || run.Config.AuthID != id || run.Status != tc.status || len(run.Samples) != 0 || run.ActionStatus != "observe_only" {
				t.Fatal("diagnostic changed execution or handling", run)
			}
			detail := run.SkipDetail
			if detail == nil {
				detail = run.PreflightDetail
			}
			if detail == nil || detail.Code != tc.code || !strings.Contains(detail.Reason, tc.reason) {
				t.Fatalf("lost the actual admission cause: want %s containing %q, got %+v", tc.code, tc.reason, detail)
			}
			raw, _ := json.Marshal(run)
			if strings.Contains(string(raw), "offline-diagnostic-private-key") || strings.Contains(string(raw), "private-invalid-response") {
				t.Fatal("diagnostic persisted private upstream configuration or response")
			}
		})
	}
}

func TestProviderScopeSessionRestoresSameTargetOnce(t *testing.T) {
	en, rule, host, _, _, id := providerDiagnosticFixture(t)
	ctx := context.Background()
	armed, err := en.setRuleTimer(ctx, rule.ID, rule.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	en.Tick(armed.NextRun)
	waiting := en.ruleForDisplayLocked(en.rules[rule.ID])
	if host.Management.ready() || waiting.LastScopeDispatch != nil || !strings.Contains(waiting.timerWaitReason, "管理会话") ||
		!en.rules[rule.ID].NextRun.Equal(armed.NextRun) || len(en.runs) != 0 || len(en.queue) != 0 {
		t.Fatal("timer lost its missing-session cause or created work", waiting.timerWaitReason)
	}
	response := en.Handle(ctx, pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/model-quality/settings",
		Headers: http.Header{"Authorization": {"Bearer offline-panel"}}})
	if response.StatusCode != http.StatusOK || host.Management.ready() {
		t.Fatal("a panel read configured background authorization")
	}
	host.Management.authorize(http.Header{"Authorization": {"Bearer offline-panel"}})
	en.Tick(armed.NextRun)
	en.Tick(armed.NextRun)
	current := en.rules[rule.ID]
	if !current.NextRun.After(armed.NextRun) || len(en.queue) != 1 {
		t.Fatal("same current route did not return after backend recovery", current.NextRun)
	}
	queued := 0
	for _, run := range en.runs {
		if run.Config.AuthID != id {
			t.Fatal("session recovery substituted another route", run)
		}
		if run.Status == "queued" {
			queued++
		}
	}
	if queued != 1 {
		t.Fatal("recovery created repeated queued work", queued)
	}
	if _, err := en.dispatchRule(ctx, current.ID, current.Revision, false); err == nil || !strings.Contains(err.Error(), "rule already running") || len(en.queue) != 1 {
		t.Fatal("overlapping repeat admission escaped the existing rule lock", err)
	}
}

func TestProviderScopeEmptyDirectoryRetainsSharedTimerCause(t *testing.T) {
	en, rule, host, _, _, _ := providerDiagnosticFixture(t)
	ctx := context.Background()
	host.Management.authorize(http.Header{"Authorization": {"Bearer offline-panel"}})
	targets, err := host.Targets(ctx)
	if err != nil || len(targets) != 1 || targets[0].ProviderGroupID == "" {
		t.Fatal(targets, err)
	}
	bound := *rule
	bound.ID, bound.Revision, bound.Name = "", 0, "empty bound group"
	bound.TargetScope, bound.TargetGroup = "single", targets[0].ProviderGroupID
	groupRule, err := en.SaveRule(ctx, bound, false)
	if err != nil {
		t.Fatal(err)
	}
	armed, err := en.setRuleTimer(ctx, rule.ID, rule.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	groupRule, err = en.setRuleTimer(ctx, groupRule.ID, groupRule.Revision, true)
	if err != nil || !armed.NextRun.Equal(groupRule.NextRun) {
		t.Fatal("fixture rules did not share one timer occurrence", groupRule, err)
	}
	host.Management.mu.Lock()
	host.Management.key = ""
	host.Management.mu.Unlock()
	listCalls, catalogCalls := 0, 0
	host.Call = func(method string, request, response any) error {
		switch method {
		case "host.auth.list":
			listCalls++
			return decode([]byte(`{"files":[]}`), response)
		case "host.auth.models":
			catalogCalls++
			response.(*pluginapi.HostAuthModelsResponse).Credentials = []pluginapi.HostAuthModels{}
			return nil
		default:
			t.Errorf("empty directory invoked an unexpected callback: %s", method)
			return errors.New("unexpected callback")
		}
	}
	en.Tick(armed.NextRun)
	for _, id := range []string{armed.ID, groupRule.ID} {
		waiting := en.ruleForDisplayLocked(en.rules[id])
		if !strings.Contains(waiting.timerWaitReason, "管理会话") {
			t.Fatal("empty directory lost the current failure from the shared inventory", waiting.timerWaitReason)
		}
		if waiting.LastScopeDispatch != nil || !en.rules[id].NextRun.Equal(armed.NextRun) {
			t.Fatal("authorization outage was recorded as a completed group occurrence", waiting.LastScopeDispatch)
		}
	}
	if listCalls != 0 || catalogCalls != 0 || len(en.queue) != 0 || host.Management.ready() {
		t.Fatal("known missing backend authorization triggered directory requests", listCalls, catalogCalls)
	}
}
