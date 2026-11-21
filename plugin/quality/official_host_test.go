package quality

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Isolated business fixtures supply a verified session without running a server lifecycle.
func (m *ManagementClient) authorize(headers http.Header) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.key = managementKey(headers)
	m.status.State = "ready"
}

func TestForwardedManagementKeyMatchesOfficialHeaderSelection(t *testing.T) {
	const key = "offline-panel-key"
	for _, tc := range []struct {
		name          string
		authorization string
		alternative   string
	}{
		{"bearer", "Bearer " + key, ""},
		{"mixed case bearer", "bEaReR " + key, ""},
		{"alternative only", "", key},
		{"raw authorization", key, ""},
		{"raw authorization takes precedence", key, "unused-fixture-key"},
		{"bearer takes precedence", "Bearer " + key, "unused-fixture-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+key {
					w.WriteHeader(http.StatusUnauthorized)
				}
			}))
			t.Cleanup(server.Close)
			client, err := NewManagementClient(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			headers := make(http.Header)
			headers.Set("Authorization", tc.authorization)
			headers.Set("X-Management-Key", tc.alternative)
			client.authorize(headers)
			if err := client.request(t.Context(), http.MethodGet, "/fixture", nil, nil, 64); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDisplayReasoningMatchesCredentialModelsAndChannel(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v8/management/routing/model-definitions/codex" {
			http.NotFound(w, r)
			return
		}
		reads++
		_, _ = w.Write([]byte(`{"channel":"codex","models":[{"id":"shared","thinking":{"levels":["low","max"]}},{"id":"visual"},{"id":"other","thinking":{"levels":["high"]}}]}`))
	}))
	defer server.Close()
	m, err := NewManagementClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	m.authorize(http.Header{"Authorization": {"Bearer offline"}})
	targets := []Target{
		{Provider: "codex", Models: []string{"shared", "visual"}},
		{Provider: "codex", Models: []string{"other"}},
		{Provider: "custom", Models: []string{"shared"}},
	}
	m.addModelReasoning(context.Background(), targets)
	for i, expected := range []string{`{"shared":{"levels":["low","max"]},"visual":null}`, `{"other":{"levels":["high"]}}`, "null"} {
		raw, err := json.Marshal(targets[i].Reasoning)
		if err != nil || string(raw) != expected {
			t.Fatalf("target %d: %s, %v", i, raw, err)
		}
	}
	if reads != 1 {
		t.Fatalf("expected one catalog read for the two Codex credentials, got %d", reads)
	}
}

func TestOfficialTargetsUseRuntimeStateAndRegisteredModels(t *testing.T) {
	states := map[string]pluginapi.HostAuthFileEntry{
		"active-index":   {ID: "active", AuthIndex: "active-index", Provider: "codex", Type: "codex", Status: "active", Source: "file"},
		"disabled-index": {ID: "disabled", AuthIndex: "disabled-index", Provider: "codex", Status: "disabled", Disabled: true},
		"cooling-index":  {ID: "cooling", AuthIndex: "cooling-index", Provider: "codex", Unavailable: true},
		"replaced-index": {ID: "another-credential", AuthIndex: "replaced-index", Provider: "codex"},
	}
	var called []string
	host := &NativeHost{Call: func(method string, request, response any) error {
		called = append(called, method)
		switch method {
		case "host.auth.list":
			return decode([]byte(`{"files":[{"id":"active","auth_index":"active-index"},{"id":"disabled","auth_index":"disabled-index"},{"id":"cooling","auth_index":"cooling-index"},{"id":"removed","auth_index":"removed-index"},{"id":"replaced","auth_index":"replaced-index"}]}`), response)
		case "host.auth.models":
			return decode([]byte(`{"credentials":[{"id":"active","auth_index":"active-index","provider":"codex","models":["model-a","model-b"]}]}`), response)
		case "host.auth.get_runtime":
			state, ok := states[request.(pluginapi.HostAuthGetRequest).AuthIndex]
			if !ok {
				return errors.New("credential removed")
			}
			response.(*pluginapi.HostAuthGetRuntimeResponse).Auth = state
			return nil
		default:
			t.Fatalf("used a non-official callback: %s", method)
			return nil
		}
	}}
	targets, err := host.Targets(context.Background())
	if err != nil || len(targets) != 5 || len(called) != 7 {
		t.Fatal(targets, called, err)
	}
	if !targets[0].EffectiveAvailable || targets[0].Provider != "codex" || !reflect.DeepEqual(targets[0].Models, []string{"model-a", "model-b"}) {
		t.Fatal("active target did not use its official runtime/catalog identity", targets[0])
	}
	for i, target := range targets {
		if i > 0 && target.EffectiveAvailable {
			t.Fatal("stale or unavailable target admitted", target)
		}
		if target.CanPause || target.CanRetest || target.QualityPaused || target.IdentityVersion != "" || target.ComparisonIdentity != "" || target.PayloadConfigVersion != "" || target.UnsupportedReason == "" {
			t.Fatal("official host advertised unsupported quality authority", target)
		}
	}
	if !targets[1].ManualDisabled || !contains(targets[2].Reasons, "native_unavailable") || !contains(targets[3].Reasons, "runtime_status_unavailable") || !contains(targets[4].Reasons, "runtime_status_unavailable") {
		t.Fatal("lost native availability reasons", targets)
	}
}

type observationHost struct{ *semanticsHost }

func (*observationHost) Capabilities() HostCapabilities { return officialCapabilities() }

func TestOfficialModeRejectsPauseRulesAndRetainsExistingOwnership(t *testing.T) {
	en, base, old := semanticsEngine(t)
	en.host = &observationHost{base}
	en.cfg.SchedulingEnabled = true
	old.AutomaticPause = true
	old.SchedulingEnabled = true
	old.NextRun = en.now()
	old.ActionID, old.ActionRevision, old.ActionRuleRevision = "retained-action", 3, old.Revision
	old.BlockedAuthID = old.AuthID
	old.FailCount, old.EvidenceSignature = 2, "old-evidence"
	if err := en.store.Put("rules", old.ID, old); err != nil {
		t.Fatal(err)
	}
	en.rules[old.ID] = cloneRule(old)
	before, _ := json.Marshal(old)
	assertUnsupported := func(err error) {
		t.Helper()
		if err == nil || !strings.HasPrefix(err.Error(), "unsupported:") {
			t.Fatalf("expected unsupported quality capability, got %v", err)
		}
	}
	_, err := en.SaveRule(context.Background(), Rule{Action: "pause_credential"}, false)
	assertUnsupported(err)
	_, err = en.SaveRule(context.Background(), Rule{Action: "observe_only", AutomaticPause: true}, false)
	assertUnsupported(err)
	_, err = en.RunRule(context.Background(), old.ID)
	assertUnsupported(err)
	assertUnsupported(en.PauseRule(old.ID, old.Revision, false))
	assertUnsupported(en.Release(context.Background(), old.ID, old.ActionID, old.ActionRevision, false))
	assertUnsupported(en.DeleteRule(context.Background(), old.ID, old.Revision))
	_, err = en.Preview(context.Background(), old.ID, old.ActionID, old.ActionRevision)
	assertUnsupported(err)
	assertUnsupported(en.UpdateConfig(en.cfg))
	if err = en.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	en.Tick(en.now().Add(time.Hour))
	if len(en.queue) != 0 || base.pauseCalls != 0 || base.releaseCalls != 0 || base.executeCalls != 0 {
		t.Fatal("official compatibility replayed a legacy action or rule")
	}
	after, _ := json.Marshal(en.rules[old.ID])
	var persisted Rule
	if err = en.store.Get("rules", old.ID, &persisted); err != nil {
		t.Fatal(err)
	}
	saved, _ := json.Marshal(persisted)
	if string(before) != string(after) || string(before) != string(saved) {
		t.Fatal("unsupported authority rewrote legacy ownership")
	}
	actions, err := en.Actions(context.Background())
	if err != nil || len(actions) != 1 || actions[0].ActionID != old.ActionID || actions[0].Status != "retained_unverified" {
		t.Fatal(actions, err)
	}
	if rules := en.Rules(); len(rules) != 1 || rules[0].Enabled || rules[0].Status != "unsupported" || !rules[0].NextRun.IsZero() {
		t.Fatal("legacy pause rule was presented as running", rules)
	}
	if _, err = en.Submit(context.Background(), lifeConfig()); err == nil {
		t.Fatal("retained pause stopped blocking local evaluations")
	}
	// Dependent pause markers must remain blockers even when their owner is unavailable.
	en.rules[old.ID].ActionID = ""
	if _, err = en.Submit(context.Background(), lifeConfig()); err == nil {
		t.Fatal("an unverified dependent pause marker was treated as released")
	}
}

func TestOfficialObservationRulesNeverAdvanceFailureEvidence(t *testing.T) {
	en, base, _ := semanticsEngine(t)
	base.targets[0].AuthIndex = "fixture-index"
	en.host = &observationHost{base}
	en.cfg.AutomaticPause = false
	rule, err := en.SaveRule(context.Background(), Rule{RunConfig: lifeConfig(), Enabled: true, Action: "observe_only"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		run := semanticRound(en, rule, "incorrect")
		decideSemantic(en, run)
		if got := en.runs[run.ID]; got.ActionStatus != "observe_only" || en.rules[rule.ID].FailCount != 0 || en.rules[rule.ID].EvidenceSignature != "" {
			t.Fatal("unverified evidence advanced an automatic decision", got)
		}
	}
	if base.pauseCalls != 0 || base.releaseCalls != 0 {
		t.Fatal("observation mutated official credentials")
	}
	if err = en.DeleteRule(context.Background(), rule.ID, rule.Revision); err != nil {
		t.Fatal("observation rule unnecessarily required pause authority", err)
	}
}

func TestOfficialSettingsAndTargetsExposeCapabilitiesOnAuthenticatedPrefix(t *testing.T) {
	en, base, _ := semanticsEngine(t)
	en.host = &observationHost{base}
	for _, endpoint := range []string{"settings", "targets"} {
		response := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: "/v0/management/plugins/model-quality/" + endpoint})
		caps := ojson.ParseBytes(response.Body).Get("capabilities")
		if response.StatusCode != 200 || caps.Get("mode").String() != "official_observation" || caps.Get("quality_pause").Bool() || caps.Get("verified_evidence").Bool() || !caps.Get("stream_close").Bool() {
			t.Fatal("missing official capability disclosure", string(response.Body))
		}
	}
	response := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: "/v8/management/plugins/model-quality/settings"})
	if response.StatusCode != 404 {
		t.Fatal("continued to advertise a patched v8 route")
	}
}

func TestOfficialManualEvaluationCompletesWithoutPrivateHostExtensions(t *testing.T) {
	en, _, _ := semanticsEngine(t)
	modelCalls := 0
	en.host = &NativeHost{Call: func(method string, request, response any) error {
		switch method {
		case "host.auth.list":
			return decode([]byte(`{"files":[{"id":"auth-a","auth_index":"index-a","provider":"offline"}]}`), response)
		case "host.auth.models":
			return decode([]byte(`{"credentials":[{"id":"auth-a","auth_index":"index-a","provider":"offline","models":["a"]}]}`), response)
		case "host.auth.get_runtime":
			return decode([]byte(`{"auth":{"id":"auth-a","auth_index":"index-a","provider":"offline","status":"active","source":"file"}}`), response)
		case "host.model.execute":
			modelCalls++
			req := request.(pluginapi.HostModelExecutionRequest)
			if req.AuthID != "auth-a" || req.ForcedProvider != "offline" || req.Model != "a" {
				t.Fatal("manual evaluation lost its exact credential binding", req)
			}
			raw, err := json.Marshal(req)
			if err != nil || ojson.GetBytes(raw, "operation_id").Exists() || ojson.GetBytes(raw, "traffic_source").Exists() || ojson.GetBytes(raw, "quality_retest").Exists() {
				t.Fatal("sent private request fields to the official host")
			}
			reply := response.(*pluginapi.HostModelExecutionResponse)
			reply.StatusCode = 200
			reply.Body = []byte(`{"choices":[{"message":{"content":"4"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
			return nil
		default:
			t.Fatalf("manual evaluation used an unavailable callback: %s", method)
			return nil
		}
	}}
	run, err := en.Submit(context.Background(), lifeConfig())
	if err != nil {
		t.Fatal(err)
	}
	en.execute(<-en.queue)
	result, err := en.Run(run.ID)
	if err != nil || result.Status != "completed" || modelCalls != 1 || len(result.Samples) != 1 {
		t.Fatal(result, modelCalls, err)
	}
	sample := result.Samples[0]
	if sample.ExecutionStatus != "completed" || sample.Output != "4" || sample.QualityVerdict != "correct" || sample.Evidence.Verified || sample.Comparable || RoundVerdict(result) != "unknown" {
		t.Fatal("manual result was lost or promoted to authoritative evidence", sample)
	}
}

func TestOfficialScannerStreamChunksPreserveCompletionAndEOFChecks(t *testing.T) {
	for _, test := range []struct {
		name, finish, terminal, status string
	}{
		{"complete", "stop", "[DONE]", "completed"},
		{"missing_finish", "", "[DONE]", "failed"},
		{"missing_terminal", "stop", "", "failed"},
		{"truncated", "length", "[DONE]", "incomplete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			chunks := []string{
				": offline-heartbeat", "event: completion", "id: 1",
				`data: {"model":"offline","choices":[{"delta":{"content":"4"},"finish_reason":null}]}`,
				`data: {"choices":[{"delta":{},"finish_reason":"` + test.finish + `"}]}`,
			}
			if test.terminal != "" {
				chunks = append(chunks, "data: "+test.terminal, test.terminal)
			}
			closed := 0
			host := &NativeHost{Call: func(method string, request, response any) error {
				switch method {
				case "host.model.execute_stream":
					return decode([]byte(`{"stream_id":"offline-stream","status_code":200}`), response)
				case "host.model.stream_read":
					chunk := response.(*pluginapi.HostModelStreamReadResponse)
					if len(chunks) == 0 {
						chunk.Done = true
					} else {
						chunk.Payload = []byte(chunks[0])
						chunks = chunks[1:]
					}
					return nil
				case "host.model.stream_close":
					closed++
					return nil
				default:
					t.Fatalf("unexpected official callback: %s", method)
					return nil
				}
			}}
			sample := host.Execute(context.Background(), Execution{AuthID: "offline", Provider: "kimi", Model: "kimi-k2", Parameters: Parameters{MaxTokens: 64, Stream: true}})
			if sample.ExecutionStatus != test.status || sample.Output != "4" || sample.Evidence.Verified || sample.Comparable || closed != 1 {
				t.Fatalf("scanner stream classification or cleanup failed: %+v, close=%d", sample, closed)
			}
		})
	}
}
