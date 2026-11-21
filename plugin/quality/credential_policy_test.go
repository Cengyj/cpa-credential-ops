package quality

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type managementFixture struct {
	mu             sync.Mutex
	managementKey  string
	entries        map[string]pluginapi.HostAuthFileEntry
	answers        map[string]string
	changedContent bool
	statusFailure  bool
	fileStatus     int
	incomplete     bool
	upstreamStatus int
	upstreamBody   string
	probeEnvelope  string
	patches        []bool
	deletes        int
	probes         int
	disabledProbes int
	nativeCalls    int
	targetReads    int
	host           *NativeHost
	engine         *Engine
}

// Advance the isolated verifier after the fixture's injected server failure ends.
func (f *managementFixture) verifyManagement(t *testing.T) {
	t.Helper()
	client := f.host.Management
	f.mu.Lock()
	key := f.managementKey
	f.mu.Unlock()
	outcome := client.probe(t.Context(), key)
	if outcome.state != "ready" {
		t.Fatal(outcome)
	}
	client.mu.Lock()
	client.applyLocked(outcome)
	client.mu.Unlock()
}

func managementTestFixture(t *testing.T) *managementFixture {
	t.Helper()
	f := &managementFixture{managementKey: "offline-management-only", entries: map[string]pluginapi.HostAuthFileEntry{}, answers: map[string]string{"a": "4", "b": "4"}}
	f.add("one")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+f.managementKey {
			w.WriteHeader(401)
			return
		}
		write := func(value any) {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(value); err != nil {
				t.Error(err)
			}
		}
		switch r.Method + " " + r.URL.Path {
		case "GET " + readinessPath:
			write(map[string]int{"in_flight": 0})
		case "GET /v8/management/config/api-keys":
			write(map[string]any{})
		case "POST /v8/management/requests/api-call":
			var body struct {
				AuthIndex         string `json:"auth_index"`
				Method, URL, Data string
				Header            map[string]string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			var request struct {
				Model     string            `json:"model"`
				Stream    bool              `json:"stream"`
				Reasoning map[string]string `json:"reasoning"`
			}
			if err := json.Unmarshal([]byte(body.Data), &request); err != nil {
				t.Error(err)
			}
			if body.Method != "POST" || body.URL != "https://chatgpt.com/backend-api/codex/responses" || body.Header["Authorization"] != "Bearer $TOKEN$" || !request.Stream || request.Reasoning["effort"] != "high" {
				t.Errorf("unexpected controlled probe request")
			}
			f.probes++
			if f.probeEnvelope != "" {
				_, _ = w.Write([]byte(f.probeEnvelope))
				return
			}
			if entry, ok := f.entries[body.AuthIndex]; !ok {
				w.WriteHeader(400)
				return
			} else if entry.Disabled {
				f.disabledProbes++
			}
			status := f.upstreamStatus
			if status == 0 {
				status = 200
			}
			terminal := map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "model": request.Model, "output": []any{map[string]any{"type": "message", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": f.answers[request.Model]}}}}, "usage": map[string]int{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3}}}
			raw, _ := json.Marshal(terminal)
			bodyText := "data: " + string(raw) + "\n\n"
			if f.incomplete {
				bodyText = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"4\"}\n\n"
			}
			if f.upstreamBody != "" {
				bodyText = f.upstreamBody
			}
			write(map[string]any{"status_code": status, "body": bodyText})
		case "GET /v8/management/credentials/download":
			if f.fileStatus != 0 {
				w.WriteHeader(f.fileStatus)
				return
			}
			for _, entry := range f.entries {
				if entry.Name == r.URL.Query().Get("name") {
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			write(map[string]string{"error": "file not found"})
		case "PATCH /v8/management/credentials/status":
			var body struct {
				Name     string `json:"name"`
				Index    string `json:"auth_index"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			entry, ok := f.entries[body.Index]
			if !ok || entry.Name != body.Name {
				t.Error("status request did not identify one exact credential")
				w.WriteHeader(404)
				return
			}
			f.patches = append(f.patches, body.Disabled)
			entry.Disabled = body.Disabled
			entry.UpdatedAt = entry.UpdatedAt.Add(time.Second)
			entry.Status = "active"
			if entry.Disabled {
				entry.Status = "disabled"
			}
			f.entries[body.Index] = entry
			if f.statusFailure {
				w.WriteHeader(500)
				return
			}
			write(map[string]any{"status": "ok", "disabled": body.Disabled})
		case "DELETE /v8/management/credentials":
			if len(r.URL.Query()) != 1 || r.URL.Query().Get("name") == "" {
				t.Error("unsafe bulk delete")
				w.WriteHeader(400)
				return
			}
			f.deletes++
			for index, entry := range f.entries {
				if entry.Name == r.URL.Query().Get("name") {
					delete(f.entries, index)
				}
			}
			write(map[string]string{"status": "ok"})
		default:
			t.Errorf("unexpected management route: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewManagementClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.authorize(http.Header{"Authorization": []string{"Bearer offline-management-only"}})
	f.host = &NativeHost{Management: client, Call: func(method string, in, out any) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch method {
		case "host.auth.list":
			files := []pluginapi.HostAuthFileEntry{}
			for _, a := range f.entries {
				files = append(files, a)
			}
			raw, _ := json.Marshal(map[string]any{"files": files})
			return json.Unmarshal(raw, out)
		case "host.auth.models":
			req := in.(pluginapi.HostAuthModelsRequest)
			if req.AuthIndex != "" {
				f.targetReads++
			}
			var rows []pluginapi.HostAuthModels
			for _, a := range f.entries {
				if req.AuthIndex != "" && req.AuthIndex != a.AuthIndex {
					continue
				}
				models := []string{"a", "b"}
				if a.Disabled {
					models = nil
				}
				rows = append(rows, pluginapi.HostAuthModels{ID: a.ID, AuthIndex: a.AuthIndex, Provider: a.Provider, Models: models})
			}
			out.(*pluginapi.HostAuthModelsResponse).Credentials = rows
			return nil
		case "host.auth.get_runtime":
			f.targetReads++
			a, ok := f.entries[in.(pluginapi.HostAuthGetRequest).AuthIndex]
			if !ok {
				return fmt.Errorf("missing")
			}
			out.(*pluginapi.HostAuthGetRuntimeResponse).Auth = a
			return nil
		case "host.auth.get":
			f.targetReads++
			a, ok := f.entries[in.(pluginapi.HostAuthGetRequest).AuthIndex]
			if !ok {
				return fmt.Errorf("missing")
			}
			token := "offline-upstream-secret"
			if f.changedContent {
				token += "-replaced"
			}
			raw, _ := json.Marshal(map[string]any{"type": "codex", "access_token": token, "account_id": "synthetic-account", "disabled": a.Disabled})
			*out.(*pluginapi.HostAuthGetResponse) = pluginapi.HostAuthGetResponse{AuthIndex: a.AuthIndex, Name: a.Name, JSON: raw}
			return nil
		case "host.model.execute", "host.model.execute_stream":
			f.nativeCalls++
			return fmt.Errorf("offline native model execution rejected")
		default:
			return fmt.Errorf("unexpected callback %s", method)
		}
	}}
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	store, err := OpenStore(cfg.DataDir, cfg.MaxStorageBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	f.engine = &Engine{store: store, host: f.host, cfg: cfg, rules: map[string]*Rule{}, runs: map[string]*Run{}, active: map[string]context.CancelFunc{}, busyAuth: map[string]struct{}{}, queue: make(chan string, 32), ctx: context.Background(), now: func() time.Time { return time.Now().UTC() }}
	if err := f.engine.loadDispositions(); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *managementFixture) add(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[id] = pluginapi.HostAuthFileEntry{ID: id, AuthIndex: id, Name: id + ".json", Provider: "codex", Type: "codex", Source: "file", Status: "active", UpdatedAt: time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC)}
}

func (f *managementFixture) rule(t *testing.T, action string, all, restore bool) *Rule {
	t.Helper()
	cfg := lifeConfig()
	cfg.AuthID = "one"
	cfg.Provider = "codex"
	cfg.Models = []string{"a", "b"}
	r := Rule{Name: "Offline credential handling", RunConfig: cfg, Action: action, AutoRestore: restore, Enabled: true}
	if all {
		r.TargetScope = scopeAll
		r.AuthID = ""
		r.Provider = ""
	}
	saved, err := f.engine.SaveRule(context.Background(), r, false)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func (f *managementFixture) round(t *testing.T, r *Rule) ([]*Run, error) {
	t.Helper()
	en := f.engine
	receipt, err := en.dispatchRule(context.Background(), r.ID, en.rules[r.ID].Revision, false)
	if err != nil {
		return nil, err
	}
	var result []*Run
	for i := 0; i < receipt.Count; i++ {
		en.mu.Lock()
		task := en.nextRunnableLocked()
		en.mu.Unlock()
		if task == nil {
			t.Fatal("missing runnable child")
		}
		en.executeTask(task)
		run, err := en.Run(task.id)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, run)
	}
	return result, nil
}

func TestManagedPauseAndWholeRoundRestoreWithoutGlobalEnable(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			f := managementTestFixture(t)
			r := f.rule(t, disableCredential, all, true)
			f.answers["a"] = "5"
			runs, err := f.round(t, r)
			if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_suspended" {
				t.Fatal(runs, err)
			}
			if len(f.patches) != 1 || !f.patches[0] || !f.entries["one"].Disabled {
				t.Fatal("suspension not applied")
			}
			if runs[0].Samples[0].Comparable || runs[0].Samples[0].Evidence.Verified {
				t.Fatal("management evidence impersonated host business evidence")
			}
			runs, err = f.round(t, r)
			if err != nil || runs[0].ActionStatus != "credential_kept_disabled" || len(f.patches) != 1 {
				t.Fatal("partly correct round restored credential", runs, err)
			}
			f.answers["a"] = "4"
			runs, err = f.round(t, r)
			if err != nil || runs[0].ActionStatus != "credential_restored" || f.entries["one"].Disabled {
				t.Fatal(runs, err)
			}
			if f.disabledProbes != 4 || len(f.patches) != 2 || f.patches[1] || f.nativeCalls != 0 {
				t.Fatal("recovery bypassed the disabled management probe path", f.disabledProbes, f.patches, f.nativeCalls)
			}
			if err := f.engine.loadDispositions(); err != nil || f.engine.managed["one"].State != "restored" {
				t.Fatal("ownership state did not survive reload", err)
			}
			raw, err := os.ReadFile(filepath.Join(f.engine.cfg.DataDir, "quality.db"))
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"offline-management-only", "offline-upstream-secret"} {
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatal("secret persisted in quality store")
				}
			}
		})
	}
}

func TestManagedPolicyDoesNotTreatErrorsOrIncompleteResultsAsWrong(t *testing.T) {
	for _, mode := range []string{"incomplete", "http-error", "unknown-answer"} {
		t.Run(mode, func(t *testing.T) {
			f := managementTestFixture(t)
			r := f.rule(t, deleteCredential, false, false)
			switch mode {
			case "incomplete":
				f.incomplete = true
			case "http-error":
				f.upstreamStatus = 429
			case "unknown-answer":
				f.answers["a"] = "not an integer"
			}
			runs, err := f.round(t, r)
			if err != nil || len(runs) != 1 || runs[0].ActionStatus != "insufficient_evidence" || f.deletes != 0 || len(f.patches) != 0 {
				t.Fatal(runs, err, f.deletes, f.patches)
			}
		})
	}
}

func TestManagedResponsesRequireTextEvidence(t *testing.T) {
	// A malformed part must invalidate the entire answer even if another part
	// supplies a strictly judgeable integer.
	for _, tc := range []struct{ name, model, content string }{
		{"number", `"b"`, `[{"type":"output_text","text":5}]`},
		{"null", `"b"`, `[{"type":"output_text","text":null},{"type":"output_text","text":"5"}]`},
		{"absent", `"b"`, `[{"type":"output_text"},{"type":"output_text","text":"5"}]`},
		{"object_content", `"b"`, `{"type":"output_text","text":"5"}`},
		{"number_model", "123", `[{"type":"output_text","text":"5"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`data: {"type":"response.completed","response":{"status":"completed","model":%s,"output":[{"type":"message","status":"completed","content":%s}]}}`, tc.model, tc.content)
			if _, _, _, err := parseManagedResponses(body); err == nil {
				t.Fatal("malformed answer accepted as management probe evidence")
			}
		})
	}
}

func TestManagedResponsesSelectOneFinalAssistantAnswer(t *testing.T) {
	const legacy = `{"type":"message","status":"completed","content":[{"type":"output_text","text":"4"}]}`
	message := func(fields string, text string) string {
		return fmt.Sprintf(`{"type":"message","status":"completed",%s,"content":[{"type":"output_text","text":%q}]}`, fields, text)
	}
	progress := message(`"role":"assistant","phase":"commentary"`, "5")
	final := message(`"role":"assistant","phase":"final_answer"`, "4")
	for _, tc := range []struct{ name, items, want string }{
		{"legacy", legacy, "4"},
		{"legacy null phase", message(`"role":"assistant","phase":null`, "4"), "4"},
		{"commentary before final", progress + "," + final, "4"},
		{"commentary only", progress, ""},
		{"multiple unphased messages", legacy + "," + legacy, ""},
		{"additional message after final", final + "," + progress, ""},
		{"input role", message(`"role":"user"`, "4"), ""},
		{"unknown phase", message(`"phase":"other"`, "4"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`data: {"type":"response.completed","response":{"status":"completed","model":"model-a","output":[%s]}}`, tc.items)
			got, _, _, err := parseManagedResponses(body)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("answer %q, error %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestManagedRecoveryRetainsEntryOwnershipAndChecksRulePermissions(t *testing.T) {
	for _, mode := range []string{"operator-disable", "replacement", "recovery-off", "rule-stopped"} {
		t.Run(mode, func(t *testing.T) {
			f := managementTestFixture(t)
			r := f.rule(t, disableCredential, false, true)
			f.answers["a"] = "5"
			if _, err := f.round(t, r); err != nil {
				t.Fatal(err)
			}
			f.answers["a"] = "4"
			switch mode {
			case "operator-disable":
				a := f.entries["one"]
				a.UpdatedAt = a.UpdatedAt.Add(time.Second)
				f.entries["one"] = a
			case "replacement":
				f.changedContent = true
			case "recovery-off":
				edit := *f.engine.rules[r.ID]
				edit.AutoRestore = false
				var err error
				r, err = f.engine.SaveRule(context.Background(), edit, true)
				if err != nil {
					t.Fatal(err)
				}
			case "rule-stopped":
				if err := f.engine.PauseRule(r.ID, r.Revision, true); err != nil {
					t.Fatal(err)
				}
			}
			before := f.probes
			if mode == "operator-disable" || mode == "replacement" {
				runs, err := f.round(t, r)
				if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_restored" || f.probes != before+2 || len(f.patches) != 2 || f.entries["one"].Disabled {
					t.Fatal("same-entry update invalidated answer recovery ownership", runs, err)
				}
				return
			}
			receipt, err := f.engine.dispatchRule(context.Background(), r.ID, f.engine.rules[r.ID].Revision, false)
			if mode == "rule-stopped" {
				if err == nil || receipt.BatchID != "" || len(f.engine.runs) != 1 {
					t.Fatal("stopped rule produced another round", receipt, err)
				}
			} else {
				want := "snapshot_changed"
				if mode == "recovery-off" {
					want = "recovery_disabled"
				}
				if err != nil || receipt.Count != 0 || receipt.Skipped != 1 || len(receipt.SkipDetails) != 1 || receipt.SkipDetails[0].Code != want ||
					receipt.RunID == "" || receipt.SkipDetails[0].RunID != receipt.RunID || f.engine.runs[receipt.RunID].Status != "skipped" || len(f.engine.queue) != 0 {
					t.Fatal("recovery refusal missing from history", receipt, err)
				}
			}
			if f.probes != before || len(f.patches) != 1 || !f.entries["one"].Disabled {
				t.Fatal("interference caused a restore")
			}
		})
	}
}

func TestManagedUncertainWriteIsDurableAndNeverReplayed(t *testing.T) {
	f := managementTestFixture(t)
	r := f.rule(t, disableCredential, true, true)
	r.Samples = 2
	r, err := f.engine.SaveRule(context.Background(), *r, true)
	if err != nil {
		t.Fatal(err)
	}
	f.answers["a"] = "5"
	f.statusFailure = true
	runs, err := f.round(t, r)
	if err != nil || runs[0].ActionStatus != "credential_action_uncertain" {
		t.Fatal(runs, err)
	}
	f.statusFailure = false
	f.answers["a"] = "4"
	if err := f.engine.Close(); err != nil {
		t.Fatal(err)
	}
	f.engine, err = New(f.engine.cfg, f.host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.engine.Close(); err != nil {
			t.Error(err)
		}
	})
	for range 3 {
		receipt, err := f.engine.dispatchRule(context.Background(), r.ID, r.Revision, false)
		if err != nil || receipt.Count != 0 || receipt.RecoveryCount != 0 || len(f.engine.queue) != 0 {
			t.Fatal(receipt, err)
		}
	}
	if len(f.patches) != 1 || f.probes != 4 || !f.entries["one"].Disabled || f.engine.managed["one"].State != "attention" {
		t.Fatal("uncertain mutation was replayed")
	}
	if _, err := f.engine.PrepareCredentialLogin(context.Background(), CredentialLoginPrepareRequest{AuthID: "one", PreparationID: "new-login"}); err == nil {
		t.Fatal("account repair ignored uncertain credential action")
	}
}

func TestManagedDeleteStopsDependentsAndDoesNotRecreate(t *testing.T) {
	f := managementTestFixture(t)
	r := f.rule(t, deleteCredential, false, false)
	observer := f.rule(t, "observe_only", false, false)
	f.answers["a"] = "5"
	runs, err := f.round(t, r)
	if err != nil || runs[0].ActionStatus != "credential_removed" || f.deletes != 1 || len(f.entries) != 0 {
		t.Fatal(runs, err)
	}
	if !f.engine.rules[r.ID].Enabled || !f.engine.rules[observer.ID].Enabled {
		t.Fatal("credential removal changed rule enablement")
	}
	f.add("one")
	if _, err := f.engine.PrepareCredentialLogin(context.Background(), CredentialLoginPrepareRequest{AuthID: "one", PreparationID: "recreate"}); err == nil {
		t.Fatal("automatic login can recreate removed credential")
	}
}

func TestManagedDynamicScopeAddsNewCredentialsAndSkipsOperatorDisabled(t *testing.T) {
	f := managementTestFixture(t)
	r := f.rule(t, disableCredential, true, true)
	runs, err := f.round(t, r)
	if err != nil || len(runs) != 1 {
		t.Fatal(runs, err)
	}
	f.add("two")
	f.add("operator")
	a := f.entries["operator"]
	a.Disabled = true
	a.Status = "disabled"
	f.entries["operator"] = a
	runs, err = f.round(t, r)
	if err != nil || len(runs) != 2 {
		t.Fatal(runs, err)
	}
	if len(f.patches) != 0 || f.deletes != 0 {
		t.Fatal("passing probes mutated credentials")
	}
}

func TestAllCredentialsRestoresManualAndOwnedDisablesOnlyAfterFullPass(t *testing.T) {
	f := managementTestFixture(t)
	r := f.rule(t, disableCredential, true, true)
	f.answers["a"] = "5"
	first, err := f.round(t, r)
	if err != nil || len(first) != 1 || first[0].ActionStatus != "credential_suspended" {
		t.Fatal(first, err)
	}
	next := *cloneRule(f.engine.rules[r.ID])
	next.TargetScope = scopeAllCredentials
	r, err = f.engine.SaveRule(context.Background(), next, true)
	if err != nil {
		t.Fatal(err)
	}
	f.add("operator")
	operator := f.entries["operator"]
	operator.Status, operator.Disabled = "disabled", true
	f.entries["operator"] = operator
	partial, err := f.round(t, r)
	if err != nil || len(partial) != 2 || len(f.patches) != 1 || !f.entries["operator"].Disabled || f.engine.managed["operator"] != nil {
		t.Fatal(partial, err)
	}
	for _, run := range partial {
		if run.ActionStatus != "credential_kept_disabled" {
			t.Fatal(run.Config.AuthID, run.ActionStatus)
		}
	}
	f.answers["a"] = "4"
	f.add("new")
	runs, err := f.round(t, r)
	if err != nil || len(runs) != 3 || f.entries["one"].Disabled || f.entries["operator"].Disabled || len(f.patches) != 3 || f.disabledProbes != 8 {
		t.Fatal(runs, err)
	}
	var stored Rule
	if err = f.engine.store.Get("rules", r.ID, &stored); err != nil || stored.TargetScope != scopeAllCredentials || len(stored.LastManualSkips) != 0 {
		t.Fatal(stored, err)
	}
	delete(f.entries, "one")
	delete(f.entries, "new")
	f.entries["operator"] = operator
	stored.AutoRestore = false
	r, err = f.engine.SaveRule(context.Background(), stored, true)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.engine.dispatchRule(context.Background(), r.ID, r.Revision, false)
	if err != nil || receipt.Count != 1 || receipt.Skipped != 0 || receipt.RecoveryCount != 0 {
		t.Fatal(receipt, err)
	}
	f.engine.executeTask(f.engine.nextRunnableLocked())
	if !f.entries["operator"].Disabled || f.disabledProbes != 10 || len(f.patches) != 3 {
		t.Fatal("operator-disabled credential should have been probed without a write")
	}
}

type manualEditHost struct {
	*NativeHost
	fixture *managementFixture
}

func (h *manualEditHost) Execute(ctx context.Context, e Execution) Sample {
	s := h.NativeHost.Execute(ctx, e)
	if e.Model == "b" {
		h.fixture.mu.Lock()
		entry := h.fixture.entries[e.AuthID]
		entry.UpdatedAt = entry.UpdatedAt.Add(time.Second)
		h.fixture.entries[e.AuthID] = entry
		h.fixture.mu.Unlock()
	}
	return s
}

func TestManualEnableRequiresUnchangedSnapshotAfterLastAnswer(t *testing.T) {
	f := managementTestFixture(t)
	f.engine.host = &manualEditHost{f.host, f}
	r := f.rule(t, disableCredential, true, true)
	r.TargetScope = scopeAllCredentials
	r, err := f.engine.SaveRule(context.Background(), *r, true)
	if err != nil {
		t.Fatal(err)
	}
	entry := f.entries["one"]
	entry.Disabled, entry.Status = true, "disabled"
	f.entries["one"] = entry
	runs, err := f.round(t, r)
	if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_changed" || len(f.patches) != 0 || !f.entries["one"].Disabled || f.disabledProbes != 2 {
		t.Fatal(runs, err)
	}
}

func TestAllEnabledCronKeepsTestingAfterLastCredentialSuspended(t *testing.T) {
	f := managementTestFixture(t)
	en := f.engine
	f.add("manual")
	manual := f.entries["manual"]
	manual.Status, manual.Disabled = "disabled", true
	f.entries["manual"] = manual
	r := f.rule(t, disableCredential, true, true)
	r.Cron, r.Samples = "*/5 * * * *", 2
	r, err := en.saveRule(context.Background(), *r, true, "start")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ a, b, action string }{
		{"5", "5", "credential_suspended"},
		{"4", "5", "credential_kept_disabled"},
		{"4", "4", "credential_restored"},
		{"4", "4", "observe_only"},
	} {
		f.answers["a"], f.answers["b"] = step.a, step.b
		due := en.rules[r.ID].NextRun
		en.Tick(due)
		if !en.timerAllowed(en.rules[r.ID]) || en.rules[r.ID].NextRun.Sub(due) != 5*time.Minute {
			t.Fatal("rule timer lost its cadence", step)
		}
		task := en.nextRunnableLocked()
		if task == nil || task.frozen.AuthID != "one" || !task.frozen.Scheduled {
			t.Fatal("missing scheduled owner probe", step)
		}
		en.executeTask(task)
		if got := en.runs[task.id].ActionStatus; got != step.action || len(en.queue) != 0 {
			t.Fatal("unexpected scheduled round", got, step)
		}
	}
	if !f.entries["manual"].Disabled || f.entries["one"].Disabled || f.disabledProbes != 8 || len(f.patches) != 2 {
		t.Fatal("wrong credential or premature recovery", f.disabledProbes, f.patches)
	}
}

type stalledProfiles struct {
	*NativeHost
	entered chan string
	resume  chan struct{}
}

type stalledStatusWrites struct {
	*NativeHost
	entered chan string
	resume  chan struct{}
}

func (h *stalledStatusWrites) SetCredentialDisabled(ctx context.Context, s CredentialSnapshot, disabled bool) (CredentialSnapshot, error) {
	h.entered <- s.AuthID
	<-h.resume
	return h.NativeHost.SetCredentialDisabled(ctx, s, disabled)
}

func TestScheduledCredentialWorkersCanFinishIndependentRoundsConcurrently(t *testing.T) {
	f := managementTestFixture(t)
	f.add("two")
	f.answers["a"] = "5"
	en := f.engine
	host := &stalledStatusWrites{f.host, make(chan string, 2), make(chan struct{})}
	en.host = host
	r := f.rule(t, disableCredential, true, true)
	r, err := en.setRuleTimer(context.Background(), r.ID, r.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	en.Tick(r.NextRun)
	en.ctx, en.cancel = context.WithCancel(context.Background())
	for range en.cfg.Workers {
		en.wg.Add(1)
		go en.worker()
	}
	defer func() {
		close(host.resume)
		en.cancel()
		en.wg.Wait()
	}()
	seen := map[string]bool{}
	for range 2 {
		select {
		case id := <-host.entered:
			seen[id] = true
		case <-time.After(3 * time.Second):
			t.Fatal("independent credential stalled behind another CPA write")
		}
	}
	if !seen["one"] || !seen["two"] {
		t.Fatal(seen)
	}
	if _, err := en.dispatchRule(context.Background(), r.ID, r.Revision, false); err == nil {
		t.Fatal("admitted a new round while CPA writes were pending")
	}
}

func (h *stalledProfiles) CredentialSnapshot(ctx context.Context, id, index string) (CredentialSnapshot, error) {
	h.entered <- id
	<-h.resume
	return h.NativeHost.CredentialSnapshot(ctx, id, index)
}

func TestCronReadsProfilesConcurrentlyAndRejectsStoppedScan(t *testing.T) {
	f := managementTestFixture(t)
	for _, id := range []string{"two", "three", "four"} {
		f.add(id)
	}
	en := f.engine
	host := &stalledProfiles{f.host, make(chan string, 4), make(chan struct{})}
	en.host = host
	r := f.rule(t, disableCredential, true, true)
	r, err := en.setRuleTimer(context.Background(), r.ID, r.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		en.Tick(r.NextRun)
		close(done)
	}()
	var release sync.Once
	unblock := func() { release.Do(func() { close(host.resume) }) }
	defer func() { unblock(); <-done }()
	for range en.cfg.Workers {
		select {
		case <-host.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("independent snapshots were serialized")
		}
	}
	stopped := make(chan error, 1)
	go func() {
		_, err := en.setRuleTimer(context.Background(), r.ID, r.Revision, false)
		stopped <- err
	}()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-host.entered:
		t.Fatal("snapshot concurrency exceeded configured workers")
	case <-time.After(3 * time.Second):
		t.Fatal("credential scan blocked rule controls")
	}
	unblock()
	<-done
	if len(en.runs) != 0 || f.probes != 0 || len(f.patches) != 0 {
		t.Fatal("stopping a rule did not invalidate the in-flight scan")
	}
}

func TestManagementSessionAndURLBoundaries(t *testing.T) {
	for _, base := range []string{"https://example.com", "http://localhost:8317", "http://127.0.0.1:8317/path", "http://user:secret@127.0.0.1:8317", "http://127.0.0.1:8317?secret=1"} {
		if _, err := NewManagementClient(base); err == nil {
			t.Fatal("unsafe management URL accepted")
		}
	}
	f := managementTestFixture(t)
	r := f.rule(t, disableCredential, true, true)
	f.host.Management.key = ""
	receipt, err := f.engine.dispatchRule(context.Background(), r.ID, r.Revision, false)
	if err != nil || len(f.engine.runs) != 1 || len(f.engine.queue) != 0 {
		t.Fatal(receipt, err)
	}
	for _, run := range f.engine.runs {
		if run.Status != "blocked" || run.PreflightDetail == nil || run.PreflightDetail.Code != "management_session_required" {
			t.Fatal(run)
		}
	}
	if f.probes != 0 || len(f.patches) != 0 {
		t.Fatal("missing session used ambient authority")
	}
	response := f.engine.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: "/v0/management/plugins/model-quality/settings", Headers: http.Header{"Authorization": []string{"Bearer offline-management-only"}}})
	if response.StatusCode != 200 || f.host.Management.ready() {
		t.Fatal("ordinary panel GET established background session")
	}
	if bytes.Contains(response.Body, []byte("offline-management-only")) {
		t.Fatal("session key returned in settings")
	}
}

func TestManagedManualEvaluationAndMissingSamplesNeverMutate(t *testing.T) {
	f := managementTestFixture(t)
	r := f.rule(t, disableCredential, false, true)
	f.answers["a"] = "5"
	runs, err := f.round(t, r)
	if err != nil {
		t.Fatal(err)
	}
	run := cloneRun(runs[0])
	run.ID = "manual-copy"
	run.Source = "manual_test"
	f.engine.runs[run.ID] = run
	f.engine.decideLocked(run)
	if len(f.patches) != 1 {
		t.Fatal("manual test invoked policy")
	}
	run.Samples = run.Samples[:1]
	if got := managedRoundVerdict(run); got != "unknown" {
		t.Fatal("missing sample accepted", got)
	}
}

func TestManagedInvalidSampleSetsNeverWrite(t *testing.T) {
	for _, action := range []string{disableCredential, deleteCredential} {
		t.Run(action, func(t *testing.T) {
			f := managementTestFixture(t)
			r := f.rule(t, action, false, false)
			r.Samples = 2
			r, err := f.engine.SaveRule(context.Background(), *r, true)
			if err != nil {
				t.Fatal(err)
			}
			good, err := f.round(t, r)
			if err != nil || len(good) != 1 || len(good[0].Samples) != 4 {
				t.Fatal(good, err)
			}
			for _, cause := range []string{"missing", "duplicate", "cancelled"} {
				t.Run(cause, func(t *testing.T) {
					run := cloneRun(good[0])
					run.ID = ID()
					run.Samples[0].Output, run.Samples[0].QualityVerdict = "5", "incorrect"
					switch cause {
					case "missing":
						run.Samples = run.Samples[:3]
					case "duplicate":
						run.Samples[3] = run.Samples[2]
					case "cancelled":
						run.Status = "cancelled"
					}
					f.engine.decideLocked(run)
					if f.engine.runs[run.ID].ActionStatus != "insufficient_evidence" ||
						len(f.patches) != 0 || f.deletes != 0 || f.entries["one"].Disabled {
						t.Fatal("invalid round produced an action", f.engine.runs[run.ID].ActionStatus, f.patches, f.deletes)
					}
				})
			}
		})
	}
}

func TestManualDisabledRecoveryCannotBorrowAnotherRulesAuthority(t *testing.T) {
	f := managementTestFixture(t)
	ctx := context.Background()
	var restricted []*Rule
	for _, tc := range []struct {
		scope, action string
		restore       bool
	}{
		{"single", disableCredential, true},
		{scopeAll, disableCredential, true},
		{scopeAllCredentials, disableCredential, false},
		{scopeAllCredentials, "observe_only", false},
		{scopeAllCredentials, deleteCredential, false},
	} {
		r := f.rule(t, tc.action, tc.scope != "single", tc.restore)
		r.Samples, r.TargetScope = 2, tc.scope
		saved, err := f.engine.SaveRule(ctx, *r, true)
		if err != nil {
			t.Fatal(err)
		}
		restricted = append(restricted, saved)
	}
	authorized := f.rule(t, disableCredential, true, true)
	authorized.Samples, authorized.TargetScope = 2, scopeAllCredentials
	authorized, err := f.engine.SaveRule(ctx, *authorized, true)
	if err != nil {
		t.Fatal(err)
	}
	entry := f.entries["one"]
	entry.Disabled, entry.Status = true, "disabled"
	f.entries["one"] = entry
	for _, r := range restricted {
		if r.TargetScope == scopeAllCredentials {
			runs, err := f.round(t, r)
			if err != nil || len(runs) != 1 || !f.entries["one"].Disabled || len(f.patches) != 0 || f.deletes != 0 {
				t.Fatal("manual disable must remain in force during observation", runs, err)
			}
			continue
		}
		receipt, err := f.engine.dispatchRule(ctx, r.ID, r.Revision, false)
		if err != nil || receipt.Count != 0 || len(f.engine.queue) != 0 {
			t.Fatal("operator-disabled credential entered an unapproved rule", r.Action, r.TargetScope, receipt, err)
		}
		if r.TargetScope == scopeAll {
			if receipt.Skipped != 0 || receipt.BatchID != "" {
				t.Fatal("all-enabled rule included an operator-disabled credential", receipt)
			}
		} else if receipt.Skipped != 1 || receipt.BatchID == "" || len(receipt.SkipDetails) != 1 || f.engine.runs[receipt.SkipDetails[0].RunID].Status != "skipped" {
			t.Fatal("missing history for known disabled credential", receipt)
		}
	}
	if f.probes != 12 || f.disabledProbes != 12 || f.nativeCalls != 0 || f.deletes != 0 || len(f.patches) != 0 {
		t.Fatal("unexpected disabled probe or write count", f.probes, f.disabledProbes)
	}
	restore, err := f.round(t, authorized)
	if err != nil || len(restore) != 1 || restore[0].ActionStatus != "credential_restored" ||
		f.disabledProbes != 16 || len(f.patches) != 1 || f.patches[0] {
		t.Fatal("manual disable was not restored after all four checks", err, f.disabledProbes, f.patches)
	}
	f.answers["a"] = "5"
	owner := restricted[1]
	if runs, err := f.round(t, owner); err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_suspended" {
		t.Fatal("could not establish a different rule's disposition", runs, err)
	}
	f.answers["a"] = "4"
	if runs, err := f.round(t, authorized); err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_kept_disabled" || f.disabledProbes != 20 ||
		f.engine.managed["one"].RuleID != owner.ID || !f.entries["one"].Disabled || len(f.patches) != 2 {
		t.Fatal("all-credentials manual recovery overrode another rule's disposition", runs, err)
	}
}

func TestManagedAcknowledgmentIsExplicitAndNeverEnablesCredential(t *testing.T) {
	f := managementTestFixture(t)
	r := f.rule(t, disableCredential, false, true)
	f.answers["a"] = "5"
	if _, err := f.round(t, r); err != nil {
		t.Fatal(err)
	}
	d := f.engine.managed["one"]
	if err := f.engine.acknowledgeHandling(context.Background(), r.ID, "one", d.ActionID); err == nil {
		t.Fatal("acknowledgment released a still-disabled credential")
	}
	a := f.entries["one"]
	a.Disabled = false
	a.Status = "active"
	a.UpdatedAt = a.UpdatedAt.Add(time.Second)
	f.entries["one"] = a
	if err := f.engine.acknowledgeHandling(context.Background(), r.ID, "one", "stale"); err == nil {
		t.Fatal("stale ownership accepted")
	}
	if err := f.engine.acknowledgeHandling(context.Background(), r.ID, "one", d.ActionID); err != nil {
		t.Fatal(err)
	}
	if !f.engine.rules[r.ID].Enabled || f.engine.rules[r.ID].SchedulingEnabled || activeDisposition(f.engine.managed["one"]) || len(f.patches) != 1 {
		t.Fatal("acknowledgment changed CPA or rule settings")
	}
}
