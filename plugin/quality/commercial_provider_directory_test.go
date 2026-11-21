package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestMalformedProviderDirectoryCannotEstablishRoute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		change func(string) string
	}{
		{"duplicate family", 200, func(s string) string { return strings.Replace(s, "{", `{"openai-compatibility":[],`, 1) }},
		{"duplicate key list", 200, func(s string) string { return strings.Replace(s, `"keys":`, `"keys":null,"keys":`, 1) }},
		{"non-object key before valid key", 200, func(s string) string { return strings.Replace(s, `"keys":[`, `"keys":[null,`, 1) }},
		{"malformed model before valid mapping", 200, func(s string) string {
			return strings.Replace(s, `"name":"diagnostic"`, `"name":"diagnostic","models":[0,{"name":"a"}]`, 1)
		}},
		{"null family", 200, func(string) string { return `{"openai-compatibility":null}` }},
		{"null group", 200, func(string) string { return `{"openai-compatibility":[null]}` }},
		{"non-list keys", 200, func(string) string { return `{"openai-compatibility":[{"name":"diagnostic","keys":{}}]}` }},
		{"ambiguous missing response", 404, func(string) string { return `{"error":"read_failed","error":"not_found"}` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			en, rule, h, set, good, _ := providerDiagnosticFixture(t)
			h.Management.authorize(http.Header{"Authorization": {"Bearer offline-panel"}})
			set(tc.status, tc.change(good))
			got, err := en.dispatchRule(context.Background(), rule.ID, rule.Revision, false)
			if err != nil || got.Skipped != 1 || got.Count != 0 || len(got.SkipDetails) != 1 || got.SkipDetails[0].Code != "provider_directory_unavailable" || len(en.queue) != 0 {
				t.Fatalf("ambiguous directory was used for admission: count=%d skipped=%d details=%v err=%v", got.Count, got.Skipped, got.SkipDetails, err)
			}
		})
	}
}

func TestProviderWriteRequiresCompleteCurrentMembershipAndRuntime(t *testing.T) {
	for _, scenario := range []string{"unidentified group member", "missing files array", "missing credentials array"} {
		t.Run(scenario, func(t *testing.T) {
			f := newConfigAdapterFixture(t)
			group := f.groups["openai-compatibility"][0]
			group["disabled"] = true
			selected := f.routes()[0]
			for _, r := range f.routes() {
				if r.name == "Example" && r.unit == "group" {
					selected = r
					break
				}
			}
			ctx := context.Background()
			before, err := f.host.CredentialSnapshot(ctx, selected.id, selected.index)
			if err != nil || !before.Disabled || len(before.ProviderMembers) != 2 {
				t.Fatal("fixture snapshot unavailable", err)
			}
			if scenario == "unidentified group member" {
				group["keys"] = append(group["keys"].([]any), nil)
			} else {
				call := f.host.Call
				f.host.Call = func(method string, in, out any) error {
					if method == "host.auth.list" && scenario == "missing files array" || method == "host.auth.models" && scenario == "missing credentials array" {
						return nil
					}
					return call(method, in, out)
				}
			}
			_, err = f.host.SetCredentialDisabled(ctx, before, false)
			if err == nil || f.attempts != 0 || f.writes != 0 || f.calls != 0 {
				t.Fatalf("incomplete group evidence permitted restore: err=%v attempts=%d writes=%d calls=%d", err, f.attempts, f.writes, f.calls)
			}
		})
	}
}

func TestProviderDirectoryRejectsCatalogExecutorMismatch(t *testing.T) {
	f := newConfigAdapterFixture(t)
	var selected providerRoute
	for _, r := range f.routes() {
		if r.name == "Example" && r.unit == "group" {
			selected = r
			break
		}
	}
	call := f.host.Call
	f.host.Call = func(method string, in, out any) error {
		if method == "host.auth.list" {
			b, _ := json.Marshal(map[string]any{"files": []pluginapi.HostAuthFileEntry{{ID: selected.id, AuthIndex: selected.index, Provider: selected.executor, Source: "memory", Status: "active"}}})
			return decode(b, out)
		}
		if err := call(method, in, out); err != nil {
			return err
		}
		if method == "host.auth.models" {
			rows := out.(*pluginapi.HostAuthModelsResponse)
			for i := range rows.Credentials {
				if rows.Credentials[i].ID == selected.id {
					rows.Credentials[i].Provider = "other-executor"
				}
			}
		}
		return nil
	}
	targets, err := f.host.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.AuthID != selected.id {
			continue
		}
		if !target.identityConflict || target.identityVerified || target.catalogVerified || target.EffectiveAvailable || len(target.Models) != 0 {
			t.Fatal("route borrowed another executor's catalog", target)
		}
		return
	}
	t.Fatal("missing diagnostic for the conflicting route")
}

func BenchmarkProviderDirectoryLargeGroup(b *testing.B) {
	for _, n := range []int{256, 1024} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			keys := make([]providerObject, n)
			for i := range keys {
				keys[i] = providerObject{"api-key": fmt.Sprintf("synthetic-key-%d", i)}
			}
			raw, err := json.Marshal(map[string][]providerObject{"openai-compatibility": {{"name": "large", "keys": keys}}})
			if err != nil {
				b.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
			b.Cleanup(srv.Close)
			m, err := NewManagementClient(srv.URL)
			if err != nil {
				b.Fatal(err)
			}
			m.authorize(http.Header{"Authorization": {"Bearer fake"}})
			h := NativeHost{Management: m}
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				routes, err := h.providerDirectory(ctx)
				if err != nil || len(routes) != n || len(routes[0].members) != n {
					b.Fatal("incomplete directory", err)
				}
			}
		})
	}
}

func TestIncompleteModelIDsCannotEstablishMissingModel(t *testing.T) {
	f := newConfigAdapterFixture(t)
	selected := f.routes()[0]
	for _, r := range f.routes() {
		if r.unit == "group" && r.name == "Example" {
			selected = r
			break
		}
	}
	rule := &Rule{RunConfig: RunConfig{Models: []string{"missing"}}, UpstreamErrorActions: map[string]string{"model_unsupported": disableCredential}}
	snap, err := f.host.CredentialSnapshot(context.Background(), selected.id, selected.index)
	if err != nil {
		t.Fatal(err)
	}
	detail := ScopeSkipDetail{Code: "missing_model", Model: "missing"}
	for _, tc := range []struct {
		name   string
		models []string
		want   bool
	}{
		{"verified IDs", []string{"other"}, true}, {"empty ID", []string{""}, false}, {"incomplete IDs", []string{"other", "  "}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := pluginapi.HostAuthModels{ID: selected.id, AuthIndex: selected.index, Provider: selected.executor, Models: tc.models}
			routes, err := f.host.providerDirectory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			provider := configuredTarget(entry, routesByIndex(routes)[selected.index])
			file := runtimeTarget(pluginapi.HostAuthFileEntry{ID: entry.ID, AuthIndex: entry.AuthIndex, Provider: entry.Provider, Source: "file", Status: "active"}, entry, true)
			for _, target := range []Target{provider, file} {
				if got := catalogMissingModelAction(rule, target, snap, detail); got != tc.want {
					t.Fatalf("catalog with incomplete identifiers supported a missing-model disposition: got %v want %v", got, tc.want)
				}
			}
		})
	}
}

func TestMismatchedRouteCannotSupplyFrozenProviderName(t *testing.T) {
	f := newConfigAdapterFixture(t)
	row := f.routes()[0]
	atIndex := row
	atIndex.name = "different-config-name"
	atIndex.id = "unrelated-config-id"
	got := configuredTarget(pluginapi.HostAuthModels{ID: row.id, AuthIndex: row.index, Provider: row.executor, Models: []string{"alias"}}, atIndex)
	if !got.identityConflict || got.ProviderName != "" || got.ProviderGroupID != "" {
		t.Fatal("a conflict inherited another configured route's name or group", got.ProviderName, got.ProviderGroupID)
	}
}

func TestOccupiedProviderIndexRetainsConfiguredRouteAndOwnership(t *testing.T) {
	f := newConfigAdapterFixture(t)
	f.groups["openai-compatibility"][1]["disabled"] = true
	ctx := context.Background()
	var selected providerRoute
	for _, r := range f.routes() {
		if r.name == "Other" {
			selected = r
			break
		}
	}
	snap, err := f.host.CredentialSnapshot(ctx, selected.id, selected.index)
	if err != nil || !snap.Disabled {
		t.Fatal("fixture group missing", err)
	}
	en, _, _ := scopeFixture(t)
	en.host = f.host
	record := &CredentialDisposition{AuthID: selected.id, RuleID: "owner", ActionID: "action", State: "disabled", Operation: disableCredential, Snapshot: snap, UpstreamCondition: "unauthorized"}
	en.managed = map[string]*CredentialDisposition{selected.id: record}
	call := f.host.Call
	f.host.Call = func(method string, in, out any) error {
		if method == "host.auth.list" {
			b, _ := json.Marshal(map[string]any{"files": []pluginapi.HostAuthFileEntry{{ID: "other-file.json", AuthIndex: selected.index, Provider: "codex", Source: "file", Status: "active"}}})
			return decode(b, out)
		}
		return call(method, in, out)
	}
	rows, err := en.visibleTargets(ctx)
	if err != nil || en.managed[selected.id].State != "disabled" || f.calls != 0 || f.writes != 0 {
		t.Fatal("conflicting runtime index retired the original owner's verified disposition", en.managed[selected.id].State, err)
	}
	for _, row := range rows {
		if row.AuthID == selected.id && row.identityConflict && !row.identityVerified && !row.EffectiveAvailable {
			return
		}
	}
	t.Fatal("the configured route was hidden by another target's index")
}
