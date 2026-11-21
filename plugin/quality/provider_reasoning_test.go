package quality

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func reasoningFixtureTarget(family, name, prefix string, models []any, exposed []string) Target {
	executor := family
	if family == "openai-compatibility" {
		executor = compatibilityExecutor(name)
	}
	route := providerRoute{family: family, executor: executor, name: name, prefix: prefix,
		base: "https://offline.invalid/v1", key: providerObject{"api-key": "fixture-reasoning-key"},
		group: providerObject{"models": models}}
	route.id, route.index = route.identity()
	route.computeModels()
	return configuredTarget(pluginapi.HostAuthModels{ID: route.id, AuthIndex: route.index, Provider: executor, Models: exposed}, route)
}

func displayedReasoning(t *testing.T, target Target) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(target.Reasoning)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProviderReasoningUsesBoundConfigurationAndWireCatalogue(t *testing.T) {
	reads := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads[r.URL.Path]++
		if r.Method != http.MethodGet || r.URL.Path != "/v8/management/routing/model-definitions/codex" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"channel":"codex","models":[{"id":"wire","thinking":{"levels":["high"]}},{"id":"public","thinking":{"levels":["low"]}},{"id":"budget","thinking":{"min":1024,"max":32768}},{"id":"plain"}]}`))
	}))
	defer server.Close()
	client, err := NewManagementClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.authorize(http.Header{"Authorization": {"Bearer offline"}})
	models := []any{providerObject{"name": "wire", "alias": "public"}, providerObject{"name": "wire(high)", "alias": "suffixed"}, providerObject{"name": "plain", "alias": "no-thought"}, providerObject{"name": "unknown", "alias": "private"}, providerObject{"name": "budget", "alias": "budget-alias"}}
	targets := []Target{
		reasoningFixtureTarget("codex", "route-one", "lab", models, []string{"public", "lab/public", "suffixed", "lab/no-thought", "private", "budget-alias"}),
		reasoningFixtureTarget("codex", "route-two", "", []any{providerObject{"name": "wire", "alias": "public", "thinking": providerObject{"levels": []any{" LOW ", "max", "low", "none", "auto"}, "min": 12, "max": 128000, "ignored-secret-field": "never-project-this"}}}, []string{"public"}),
		reasoningFixtureTarget("openai-compatibility", "custom", "", []any{providerObject{"name": "wire", "alias": "public"}}, []string{"public"}),
	}
	client.addModelReasoning(context.Background(), targets)
	first := displayedReasoning(t, targets[0])
	for _, model := range []string{"public", "lab/public", "suffixed"} {
		if string(first[model]) != `{"levels":["high"]}` {
			t.Fatalf("%s did not inherit its exact upstream model: %s", model, first[model])
		}
	}
	if string(first["lab/no-thought"]) != "null" || first["private"] != nil || len(first) != 5 {
		t.Fatalf("known unsupported and unknown models were conflated: %s", targets[0].Reasoning)
	}
	if string(first["budget-alias"]) != `{"min":1024,"max":32768}` {
		t.Fatal("budget capability lost its range", string(first["budget-alias"]))
	}
	second := displayedReasoning(t, targets[1])
	var support struct {
		Levels         []string `json:"levels"`
		Min            int      `json:"min"`
		Max            int      `json:"max"`
		ZeroAllowed    bool     `json:"zero_allowed"`
		DynamicAllowed bool     `json:"dynamic_allowed"`
	}
	if err := json.Unmarshal(second["public"], &support); err != nil || !reflect.DeepEqual(support.Levels, []string{"low", "max", "none", "auto"}) || support.Min != 12 || support.Max != 128000 || !support.ZeroAllowed || !support.DynamicAllowed {
		t.Fatalf("route-local explicit capability was lost or replaced: %s, %v", second["public"], err)
	}
	if got := displayedReasoning(t, targets[2]); string(got["public"]) != `{"levels":["low","medium","high"]}` {
		t.Fatalf("compatible route did not expose the official default: %s", got["public"])
	}
	if reads["/v8/management/routing/model-definitions/codex"] != 1 {
		t.Fatal("catalogue was not shared within a display request", reads)
	}
	public, err := json.Marshal(targets)
	if err != nil || strings.Contains(string(public), "fixture-reasoning-key") || strings.Contains(string(public), "offline.invalid") || strings.Contains(string(public), "never-project-this") {
		t.Fatal("reasoning projection exposed private or unsupported configuration", err)
	}
}

func TestProviderReasoningKeepsExplicitEmptyAndRejectsAmbiguousAlias(t *testing.T) {
	for _, tc := range []struct {
		name   string
		models []any
		want   string
	}{
		{"empty_is_not_nil", []any{providerObject{"name": "wire", "alias": "public", "thinking": providerObject{}}}, `{}`},
		{"null_uses_compatible_default", []any{providerObject{"name": "wire", "alias": "public", "thinking": nil}}, `{"levels":["low","medium","high"]}`},
		{"unknown_levels_are_not_unrestricted", []any{providerObject{"name": "wire", "alias": "public", "thinking": providerObject{"levels": []any{" FUTURE-level "}}}}, `{"levels":["future-level"]}`},
		{"secrets_are_not_capability_values", []any{providerObject{"name": "wire", "alias": "public", "thinking": providerObject{"levels": []any{"fixture-reasoning-key"}}}}, ""},
		{"same_wire_keeps_first_configuration", []any{providerObject{"name": "wire", "alias": "public", "thinking": providerObject{"levels": []any{"max"}}}, providerObject{"name": "wire", "alias": "public", "thinking": providerObject{"levels": []any{"low"}}}}, `{"levels":["max"]}`},
		{"different_wire_has_no_guessed_capability", []any{providerObject{"name": "wire", "alias": "public", "thinking": providerObject{"levels": []any{"max"}}}, providerObject{"name": "other", "alias": "public", "thinking": providerObject{"levels": []any{"low"}}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := reasoningFixtureTarget("openai-compatibility", "custom", "", tc.models, []string{"public"})
			if got := displayedReasoning(t, target); string(got["public"]) != tc.want {
				t.Fatalf("unexpected configured capability: got %s, want %s", got["public"], tc.want)
			}
		})
	}
}

func TestProviderReasoningDoesNotGuessWhenCatalogueCannotBeVerified(t *testing.T) {
	for _, failure := range []string{"unavailable", "wrong_channel", "unmapped_alias"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failure == "unavailable" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				channel := "codex"
				if failure == "wrong_channel" {
					channel = "claude"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"channel": channel, "models": []any{map[string]any{"id": "public", "thinking": map[string]any{"levels": []string{"low"}}}}})
			}))
			defer server.Close()
			client, err := NewManagementClient(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			client.authorize(http.Header{"Authorization": {"Bearer offline"}})
			targets := []Target{
				reasoningFixtureTarget("codex", "explicit", "", []any{providerObject{"name": "wire", "alias": "public", "thinking": providerObject{"levels": []any{"max"}}}}, []string{"public"}),
				reasoningFixtureTarget("codex", "unknown", "", []any{providerObject{"name": "wire", "alias": "public"}}, []string{"public", "unregistered"}),
			}
			client.addModelReasoning(context.Background(), targets)
			if got := displayedReasoning(t, targets[0]); string(got["public"]) != `{"levels":["max"]}` {
				t.Fatal("failed display catalogue erased route-local configuration", got)
			}
			if got := displayedReasoning(t, targets[1]); len(got) != 0 {
				t.Fatal("unverified catalogue or display alias supplied unrelated capability", got)
			}
		})
	}
}

func TestProviderReasoningDoesNotBorrowFromAggregatedCustomCatalogue(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		_, _ = w.Write([]byte(`{"channel":"openai-compatible-shared","models":[{"id":"public","thinking":{"levels":["max"]}}]}`))
	}))
	defer server.Close()
	client, err := NewManagementClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.authorize(http.Header{"Authorization": {"Bearer offline"}})
	targets := []Target{
		reasoningFixtureTarget("openai-compatibility", "shared", "", []any{providerObject{"name": "public", "alias": "visual", "image": true}}, []string{"visual"}),
		reasoningFixtureTarget("openai-compatibility", "shared", "", nil, []string{"public"}),
		{IsProvider: true, AuthID: "openai-compatibility:shared:abcdef012345", Provider: "openai-compatible-shared", Models: []string{"public"}},
		reasoningFixtureTarget("openai-compatibility", "shared", "", []any{providerObject{"name": "public", "alias": "explicit-visual", "image": true, "thinking": providerObject{"levels": []any{"low"}}}}, []string{"explicit-visual"}),
	}
	client.addModelReasoning(context.Background(), targets)
	for _, target := range targets[:3] {
		if got := displayedReasoning(t, target); len(got) != 0 {
			t.Fatal("route inherited another group's aliased capability", got)
		}
	}
	if got := displayedReasoning(t, targets[3]); string(got["explicit-visual"]) != `{"levels":["low"]}` || reads != 0 {
		t.Fatal("image route lost explicit support or queried an aggregated custom catalogue", got, reads)
	}
}

func TestProviderReasoningDoesNotProjectEscapedSecrets(t *testing.T) {
	for _, tc := range []struct{ secret, level string }{
		{"fixture\"quoted-key", "fixture\"quoted-key"},
		{`fixture\backslash-key`, `fixture\backslash-key`},
		{"fixture\nnewline-key", "fixture\nnewline-key"},
		{"fixture-密钥", "fixture-密钥"},
		{"fixture-lowercase-key", " FIXTURE-LOWERCASE-KEY "},
	} {
		route := providerRoute{family: "openai-compatibility", executor: "openai-compatible-secret", name: "secret", base: "https://offline.invalid/v1",
			key: providerObject{"api-key": tc.secret}, group: providerObject{"models": []any{providerObject{"name": "wire", "alias": "public", "thinking": providerObject{"levels": []any{tc.level}}}}}}
		route.id, route.index = route.identity()
		route.computeModels()
		target := configuredTarget(pluginapi.HostAuthModels{ID: route.id, AuthIndex: route.index, Provider: route.executor, Models: []string{"public"}}, route)
		if got := displayedReasoning(t, target); len(got) != 0 {
			t.Fatal("JSON escaping or normalization bypassed the secret filter for configured capability")
		}
	}
}

func TestProviderManualReasoningIsFrozenInNativeRequest(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			fixture := newConfigAdapterFixture(t)
			engine, _, _ := semanticsEngine(t)
			engine.host = fixture.host
			var selected providerRoute
			for _, route := range fixture.routes() {
				if route.family == "openai-compatibility" && route.name == "Example" {
					selected = route
					break
				}
			}
			calls := 0
			callback := fixture.host.Call
			fixture.host.Call = func(method string, request, response any) error {
				if method != "host.model.execute" {
					return callback(method, request, response)
				}
				calls++
				req := request.(pluginapi.HostModelExecutionRequest)
				var body map[string]any
				if err := json.Unmarshal(req.Body, &body); err != nil || req.AuthID != selected.id || req.ForcedProvider != selected.executor || body["model"] != "alias" || body["reasoning_effort"] != effort {
					t.Fatal("native request lost its route or selected reasoning", body, err)
				}
				*response.(*pluginapi.HostModelExecutionResponse) = pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"choices":[{"message":{"content":"4"},"finish_reason":"stop"}]}`)}
				return nil
			}
			config := RunConfig{AuthID: selected.id, Provider: selected.executor, Models: []string{"alias"}, QuestionID: "logic-boxes", Judge: Judge{Mode: "manual"}, Parameters: Parameters{ReasoningEffort: effort}}
			run, err := engine.Submit(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			config.Parameters.ReasoningEffort = ""
			engine.executeTask(engine.nextRunnableLocked())
			stored, err := engine.Run(run.ID)
			if err != nil || stored.Status != "completed" || stored.Config.Parameters.ReasoningEffort != effort || calls != 1 || fixture.calls != 0 || fixture.writes != 0 {
				t.Fatal("provider request was not frozen or invoked an extra path", stored, calls, fixture.calls, fixture.writes, err)
			}
		})
	}
}

func TestProviderReasoningKeepsDirectedProtocolLimits(t *testing.T) {
	for _, family := range []string{"openai-compatibility", "codex", "meta", "xai", "claude", "gemini", "vertex", "interactions"} {
		t.Run(family, func(t *testing.T) {
			route := providerRoute{family: family, base: "https://offline.invalid/v1", key: providerObject{"api-key": "fixture-reasoning-key"}}
			request, body, err := route.probeRequest(context.Background(), Execution{Prompt: "offline", Parameters: Parameters{ReasoningEffort: "high", MaxTokens: 16}}, "wire")
			switch family {
			case "openai-compatibility":
				if err != nil || request == nil || body["reasoning_effort"] != "high" || body["reasoning"] != nil {
					t.Fatal("OpenAI protocol did not preserve reasoning effort", body, err)
				}
			case "codex", "meta", "xai":
				if err != nil || request == nil || !reflect.DeepEqual(body["reasoning"], map[string]string{"effort": "high"}) || body["reasoning_effort"] != nil {
					t.Fatal("Responses protocol did not preserve reasoning effort", body, err)
				}
			default:
				if err == nil || request != nil || providerPlanProbeReason(Target{IsProvider: true, providerFamilyName: family}, []string{"wire"}, Parameters{ReasoningEffort: "high"}) == "" {
					t.Fatal("directed probe bypassed an unsupported reasoning conversion", err)
				}
			}
		})
	}
}
