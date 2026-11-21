package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// The fake config view exposes auth_index only while the compatible group
// is registered, exactly as the official v8 GET does.
type configAdapterFixture struct {
	t                                  *testing.T
	mu                                 sync.Mutex
	groups                             map[string][]providerObject
	writes, attempts, calls, redirects int
	reject, redirect, echo             bool
	host                               *NativeHost
	base                               string
}

func newConfigAdapterFixture(t *testing.T) *configAdapterFixture {
	t.Helper()
	f := &configAdapterFixture{t: t}
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.mu.Lock(); defer f.mu.Unlock(); f.redirects++ }))
	t.Cleanup(trap.Close)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer example-provider-key-two" || r.Header.Get("X-Route") != "private-marker" || r.Header.Get("X-Management-Key") != "" {
			t.Error("probe did not use the selected upstream configuration")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "wire" {
			t.Error("model alias was not resolved")
		}
		if f.groups["openai-compatibility"][0]["disabled"] != true {
			t.Error("provider group was temporarily enabled")
		}
		f.calls++
		if f.redirect {
			http.Redirect(w, r, trap.URL, http.StatusTemporaryRedirect)
			return
		}
		answer := "4"
		if f.echo {
			answer = "private-marker"
		}
		fmt.Fprintf(w, `{"model":"wire","choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, answer)
	}))
	t.Cleanup(up.Close)
	f.base = up.URL + "/v1"
	f.groups = map[string][]providerObject{
		"openai-compatibility": {
			{"name": "Example", "base-url": f.base, "keys": []any{providerObject{"api-key": "example-provider-key-one", "proxy-url": "direct"}, providerObject{"api-key": "example-provider-key-two"}}, "models": []any{providerObject{"name": "wire", "alias": "alias"}}, "headers": providerObject{"X-Route": "private-marker"}, "priority": json.Number("9007199254740993")},
			{"name": "Other", "base-url": "https://offline.invalid/v1", "keys": []any{providerObject{"api-key": "other"}}},
		},
		"codex": {{"name": "Example", "base-url": f.base, "models": []any{providerObject{"name": "wire", "alias": "alias"}}, "excluded-models": []any{"legacy-*"}, "keys": []any{providerObject{"api-key": "single-key-one"}, providerObject{"api-key": "single-key-two", "excluded-models": []any{"other-*"}}}}},
	}
	mgmt := httptest.NewServer(http.HandlerFunc(f.serveConfig))
	t.Cleanup(mgmt.Close)
	client, err := NewManagementClient(mgmt.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.authorize(http.Header{"Authorization": {"Bearer offline-panel"}})
	f.host = &NativeHost{Management: client, Call: func(method string, request, response any) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch method {
		case "host.auth.models":
			req := request.(pluginapi.HostAuthModelsRequest)
			out := response.(*pluginapi.HostAuthModelsResponse)
			out.Credentials = []pluginapi.HostAuthModels{}
			for _, r := range f.routes() {
				if req.AuthIndex != "" && r.index != req.AuthIndex || r.unit == "group" && r.disabled {
					continue
				}
				models := []string{"alias"}
				if r.disabled {
					models = []string{}
				}
				out.Credentials = append(out.Credentials, pluginapi.HostAuthModels{ID: r.id, AuthIndex: r.index, Provider: r.executor, Models: models})
			}
		case "host.auth.list":
			return decode([]byte(`{"files":[]}`), response)
		case "host.auth.get_runtime":
			return errors.New("not a file")
		default:
			t.Fatal("unexpected model or credential callback", method)
		}
		return nil
	}}
	return f
}

func (f *configAdapterFixture) routes() []providerRoute {
	var all []providerRoute
	for family, groups := range f.groups {
		for _, group := range groups {
			keys := configList(group["keys"])
			for _, k := range keys {
				r := providerRoute{family: family, name: configText(group["name"]), executor: family, base: configText(group["base-url"]), unit: "route", key: k, keys: keys, group: group}
				r.proxy = configText(r.effective("proxy-url"))
				r.disabled = hasProviderWildcard(configStrings(r.effective("excluded-models")))
				if family == "openai-compatibility" {
					r.executor = compatibilityExecutor(r.name)
					r.unit = "group"
					r.disabled, _ = group["disabled"].(bool)
				}
				r.id, r.index = r.identity()
				all = append(all, r)
			}
		}
	}
	return all
}

func (f *configAdapterFixture) serveConfig(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Header.Get("Authorization") != "Bearer offline-panel" {
		w.WriteHeader(401)
		return
	}
	tag := func() string { return `"` + fingerprint(f.groups) + `"` }
	if req.Method == "GET" {
		if req.URL.Path != providerConfigPath {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not_found"}`)
			return
		}
		w.Header().Set("ETag", tag())
		// v8 decorates a view, leaving its underlying file unchanged.
		b, _ := json.Marshal(f.groups)
		var copy map[string][]providerObject
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.UseNumber()
		_ = dec.Decode(&copy)
		for _, group := range f.routes() {
			if group.unit == "group" && group.disabled {
				continue
			}
			for _, g := range copy[group.family] {
				for _, k := range configList(g["keys"]) {
					if k["api-key"] == group.key["api-key"] {
						k["auth_index"] = group.index
					}
				}
			}
		}
		_ = json.NewEncoder(w).Encode(copy)
		return
	}
	family := strings.TrimPrefix(req.URL.Path, providerConfigPath+"/")
	if req.Method != "PUT" || f.groups[family] == nil {
		f.t.Error("unexpected config write", req.Method, req.URL.Path)
		w.WriteHeader(400)
		return
	}
	f.attempts++
	if f.reject || req.Header.Get("If-Match") != tag() {
		w.WriteHeader(412)
		return
	}
	var list []providerObject
	dec := json.NewDecoder(req.Body)
	dec.UseNumber()
	if err := dec.Decode(&list); err != nil {
		f.t.Fatal(err)
	}
	for _, g := range list {
		delete(g, "auth_index")
		for _, k := range configList(g["keys"]) {
			delete(k, "auth_index")
		}
	}
	f.groups[family] = list
	f.writes++
	w.Header().Set("ETag", tag())
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

func TestManualRegisteredProviderWithPersistedErrorSuspension(t *testing.T) {
	ctx := context.Background()
	f := newConfigAdapterFixture(t)
	en, _, _ := semanticsEngine(t)
	en.host = f.host
	group := f.groups["openai-compatibility"][0]
	var selected providerRoute
	for _, r := range f.routes() {
		if r.name == "Example" && r.key["api-key"] == "example-provider-key-two" {
			selected = r
		}
	}
	if selected.id == "" {
		t.Fatal("fixture route is absent")
	}
	config := RunConfig{AuthID: selected.id, Provider: selected.executor, Models: []string{"alias"}, QuestionID: "logic-boxes", Judge: Judge{Mode: "manual"}}
	rule, err := en.SaveRule(ctx, Rule{Name: "observed", Enabled: true, ObjectTypes: []string{objectProvider}, RunConfig: config}, false)
	if err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	group["disabled"] = true
	f.mu.Unlock()
	update := changes()
	for _, r := range f.routes() {
		if r.groupID != selected.groupID || r.name != selected.name || r.family != selected.family {
			continue
		}
		snapshot, err := f.host.CredentialSnapshot(ctx, r.id, r.index)
		if err != nil || !snapshot.Disabled || snapshot.ProviderMechanism != "group_disabled" {
			t.Fatal("disabled fixture route did not return a bound group snapshot", err)
		}
		update.dispositions = append(update.dispositions, &CredentialDisposition{
			AuthID: r.id, RuleID: rule.ID, ActionID: "persisted-provider-error", Operation: disableCredential,
			State: "disabled", UpstreamCondition: "unauthorized", Snapshot: snapshot,
		})
	}
	if len(update.dispositions) != 2 {
		t.Fatal("fixture did not bind both routes")
	}
	if err := en.commitLocked(update); err != nil {
		t.Fatal(err)
	}
	en.managed = nil
	if err := en.loadDispositions(); err != nil {
		t.Fatal(err)
	}
	old := en.managed[selected.id]
	if _, err := en.Submit(ctx, config); err == nil {
		t.Fatal("manual test admitted a provider still disabled in CPA")
	}

	f.mu.Lock()
	group["disabled"] = false
	f.mu.Unlock()
	target, err := f.host.pinnedTarget(ctx, selected.id, selected.index)
	if err != nil || target.RuntimeStatus != "registered" || !target.IsProvider || !target.EffectiveAvailable {
		t.Fatal("catalog-only route was not verified after being re-enabled", err, target)
	}
	var nativeCalls []pluginapi.HostModelExecutionRequest
	callback := f.host.Call
	f.host.Call = func(method string, request, response any) error {
		if method != "host.model.execute" {
			return callback(method, request, response)
		}
		nativeCalls = append(nativeCalls, request.(pluginapi.HostModelExecutionRequest))
		*response.(*pluginapi.HostModelExecutionResponse) = pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"choices":[{"message":{"content":"4"},"finish_reason":"stop"}]}`)}
		return nil
	}
	run, err := en.Submit(ctx, config)
	if err != nil || run.Config.SubjectIndex != selected.index || run.Config.ManualPolicyActionID != "" || run.Config.ManualAccountFingerprint == "" || run.Config.ManagedProbe != nil {
		t.Fatal("manual admission did not freeze the selected registered route and disposition", run, err)
	}
	en.executeTask(en.nextRunnableLocked())
	stored, err := en.Run(run.ID)
	if err != nil || stored.Status != "completed" || len(stored.Samples) != 1 || stored.Samples[0].Output != "4" ||
		len(nativeCalls) != 1 || nativeCalls[0].AuthID != selected.id || nativeCalls[0].ForcedProvider != selected.executor || nativeCalls[0].Model != "alias" {
		t.Fatal("manual test did not complete through the selected native route", stored, err, len(nativeCalls))
	}
	batch, err := en.dispatchRule(ctx, rule.ID, rule.Revision, false)
	if err != nil || batch.Count != 1 || batch.Skipped != 0 || activeDisposition(en.managed[selected.id]) || en.managed[selected.id].ActionID != old.ActionID {
		t.Fatal("manual admission failed to retire stale group exclusion", batch, err)
	}

	pending, err := en.Submit(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	group["headers"] = providerObject{"X-Route": "changed"}
	f.mu.Unlock()
	en.execute(pending.ID)
	stale, err := en.Run(pending.ID)
	if err != nil || stale.Status != "blocked" || len(nativeCalls) != 1 || f.attempts != 0 || f.calls != 0 {
		t.Fatal("changed in-flight provider configuration used an old frozen request", stale, err)
	}
	current, err := en.Submit(ctx, config)
	if err != nil {
		t.Fatal("a new manual test could not bind the current enabled configuration", err)
	}
	en.execute(current.ID)
	fresh, err := en.Run(current.ID)
	if err != nil || fresh.Status != "completed" || len(nativeCalls) != 2 || f.attempts != 0 || f.calls != 0 {
		t.Fatal("current configuration did not use its native route without management writes", fresh, err)
	}
}

func TestProviderAdapterGroupCASAndOfflineProbe(t *testing.T) {
	f := newConfigAdapterFixture(t)
	ctx := context.Background()
	var selected providerRoute
	for _, r := range f.routes() {
		if r.key["api-key"] == "example-provider-key-two" {
			selected = r
		}
	}
	original := fingerprint(f.groups["openai-compatibility"][1])
	active, err := f.host.CredentialSnapshot(ctx, selected.id, selected.index)
	if err != nil || len(active.ProviderMembers) != 2 || active.Disabled {
		t.Fatal(active, err)
	}
	f.reject = true
	if _, err = f.host.SetCredentialDisabled(ctx, active, true); !errors.Is(err, errProviderPrecondition) || f.writes != 0 || f.attempts != 1 {
		t.Fatal("412 was replayed or treated as an uncertain mutation", err)
	}
	f.reject = false
	disabled, err := f.host.SetCredentialDisabled(ctx, active, true)
	if err != nil || !disabled.Disabled || f.writes != 1 {
		t.Fatal(disabled, err)
	}
	targets, err := f.host.Targets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, target := range targets {
		if target.ProviderGroupID != disabled.ProviderGroupID {
			continue
		}
		found++
		if !target.ManualDisabled || !target.identityVerified || target.EffectiveAvailable || !slices.Equal(target.Models, []string{"alias"}) {
			t.Fatal(target)
		}
		member, err := f.host.CredentialSnapshot(ctx, target.AuthID, target.AuthIndex)
		if err != nil || !sameProviderGroupPlan(disabled, member) {
			t.Fatal("group signature differs by member or proxy override", err)
		}
	}
	if found != 2 {
		t.Fatal("unregistered disabled members missing", targets)
	}
	e := Execution{AuthID: selected.id, Provider: selected.executor, Model: "alias", Prompt: "offline", ManagedProbe: &disabled, Parameters: Parameters{MaxTokens: 8}}
	s := f.host.Execute(ctx, e)
	if s.ExecutionStatus != "completed" || s.Output != "4" || s.Evidence.Path != providerConfigProbe || f.calls != 1 {
		t.Fatal(s)
	}
	f.echo = true
	s = f.host.Execute(ctx, e)
	if s.ExecutionStatus == "completed" || strings.Contains(s.Output, "private-marker") {
		t.Fatal("private header echoed to history", s)
	}
	f.echo = false
	f.redirect = true
	s = f.host.Execute(ctx, e)
	if s.ExecutionStatus == "completed" || f.redirects != 0 || f.writes != 1 {
		t.Fatal("cross-origin redirect followed or probe changed status", s)
	}
	restored, err := f.host.SetCredentialDisabled(ctx, disabled, false)
	if err != nil || restored.Disabled || f.writes != 2 || original != fingerprint(f.groups["openai-compatibility"][1]) {
		t.Fatal(restored, err)
	}
	if val := f.groups["openai-compatibility"][0]["priority"]; fmt.Sprint(val) != "9007199254740993" {
		t.Fatal("lost numeric config field", val)
	}
}

func TestProviderAdapterRouteWildcardPreservesOtherExclusions(t *testing.T) {
	f := newConfigAdapterFixture(t)
	ctx := context.Background()
	var selected providerRoute
	for _, r := range f.routes() {
		if r.key["api-key"] == "single-key-one" {
			selected = r
		}
	}
	initial := fingerprint(f.groups)
	before, err := f.host.CredentialSnapshot(ctx, selected.id, selected.index)
	if err != nil {
		t.Fatal(err)
	}
	after, err := f.host.SetCredentialDisabled(ctx, before, true)
	if err != nil || !after.Disabled || after.ProviderMechanism != "excluded_models" || len(after.ProviderMembers) != 1 {
		t.Fatal(after, err)
	}
	group := f.groups["codex"][0]
	keys := configList(group["keys"])
	if !reflect.DeepEqual(configStrings(keys[0]["excluded-models"]), []string{"legacy-*", "*"}) || !reflect.DeepEqual(configStrings(keys[1]["excluded-models"]), []string{"other-*"}) {
		t.Fatal("wildcard write affected other patterns or keys")
	}
	restored, err := f.host.SetCredentialDisabled(ctx, after, false)
	if err != nil || restored.Disabled || initial != fingerprint(f.groups) || f.writes != 2 {
		t.Fatal("restore lost group inheritance or changed other settings", err)
	}
}

func TestProviderDirectoryCoverageAndUnverifiedRows(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		verified   bool
	}{
		{"empty", "{}", 200, true}, {"absent", `{"error":"not_found"}`, 404, true},
		{"unreadable", "", 503, false}, {"invalid", "{", 200, false}, {"ambiguous 404", "", 404, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, _ := NewManagementClient(server.URL)
			client.authorize(http.Header{"Authorization": {"Bearer offline"}})
			h := NativeHost{Management: client, Call: func(method string, request, response any) error {
				switch method {
				case "host.auth.list":
					return decode([]byte(`{"files":[{"id":"one.json","name":"one.json","auth_index":"one","provider":"codex","source":"file","status":"active"},{"name":"disk-only.json","source":"file"}]}`), response)
				case "host.auth.models":
					return decode([]byte(`{"credentials":[{"id":"one.json","auth_index":"one","provider":"codex","models":["m"]}]}`), response)
				default:
					t.Fatal("unexpected credential or model callback", method)
					return nil
				}
			}}
			state := &providerInventoryStatus{Verified: !tc.verified}
			targets, err := h.Targets(context.WithValue(context.Background(), providerInventoryStatusKey{}, state))
			if err != nil || state.Verified != tc.verified || len(targets) != 2 || objectType(targets[0]) != objectOAuth || !targets[0].EffectiveAvailable || objectType(targets[1]) != objectUnknown || !contains(targets[1].Reasons, "identity_missing") {
				t.Fatal(state, targets, err)
			}
		})
	}
}

func TestProviderPlanRejectsAmbiguousModelOrParameters(t *testing.T) {
	r := providerRoute{family: "openai-compatibility", name: "example", base: "https://offline.invalid", unit: "group", executor: "openai-compatible-example",
		group: providerObject{"models": []any{providerObject{"name": "wire-1", "alias": "same"}, providerObject{"name": "wire-2", "alias": "same"}, providerObject{"name": "other"}}}, disabled: true}
	r.id, r.index = r.identity()
	r.computeModels()
	target := configuredTarget(pluginapi.HostAuthModels{ID: r.id, AuthIndex: r.index, Provider: r.executor}, r)
	if !contains(target.Models, "same") || providerPlanProbeReason(target, []string{"same"}, Parameters{}) == "" || providerPlanProbeReason(target, []string{"other"}, Parameters{}) != "" {
		t.Fatal(target)
	}
	target.providerFamilyName = "gemini"
	if providerPlanProbeReason(target, []string{"other"}, Parameters{ReasoningEffort: "high"}) == "" {
		t.Fatal("unverifiable conversion did not block preview")
	}
}

func TestProviderConfigGroupsWithSameNameAndEndpointRemainDistinct(t *testing.T) {
	f := newConfigAdapterFixture(t)
	original := f.groups["openai-compatibility"][0]
	copy := withoutProviderIndex(original)
	copy["keys"] = []any{providerObject{"api-key": "another-fixture-key"}}
	f.groups["openai-compatibility"] = append(f.groups["openai-compatibility"], copy)
	groups := func() map[string]string {
		m := make(map[string]string)
		rows, err := f.host.providerDirectory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.name == "Example" && r.unit == "group" {
				if r.conflict {
					t.Fatal("disjoint route identities were rejected")
				}
				m[configText(r.key["api-key"])] = r.groupID
			}
		}
		return m
	}
	before := groups()
	if len(before) != 3 || before["another-fixture-key"] == before["example-provider-key-two"] {
		t.Fatal("merged two independent config groups")
	}
	slices.Reverse(f.groups["openai-compatibility"])
	if !reflect.DeepEqual(before, groups()) {
		t.Fatal("provider group binding relies on list position")
	}
}
