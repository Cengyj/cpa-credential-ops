package quality

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/json"
)

func TestManagedUpstreamCondition(t *testing.T) {
	const model = "test-model"
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"401", 401, "", "unauthorized"},
		{"402", 402, "<html>payment required</html>", "payment_required"},
		{"quota code", 429, `{"error":{"type":"invalid_request_error","code":"insufficient_quota","message":"billing"}}`, "quota_exhausted"},
		{"codex usage type", 429, `{"error":{"type":"usage_limit_reached","resets_in_seconds":60}}`, "quota_exhausted"},
		{"quota message", 429, `{"error":{"message":"You exceeded your current quota, please check your plan and billing details."}}`, "quota_exhausted"},
		{"plain quota", 429, "Quota exhausted.", "quota_exhausted"},
		{"model code", 400, `{"error":{"code":"model_not_supported"}}`, "model_unsupported"},
		{"model type", 404, `{"error":{"type":"model_not_found"}}`, "model_unsupported"},
		{"flat model", 404, `{"code":"model_not_found","message":"Not Found"}`, "model_unsupported"},
		{"openai model", 404, "{\"error\":{\"message\":\"The model `test-model` does not exist or you do not have access to it.\"}}", "model_unsupported"},
		{"codex exact model", 400, `{"error":{"message":"The 'test-model' model is not supported when using Codex with a ChatGPT account."}}`, "model_unsupported"},
		{"codex top-level detail", 400, `{"detail":"The 'test-model' model is not supported when using Codex with a ChatGPT account."}`, "model_unsupported"},
		{"codex detail other model", 400, `{"detail":"The 'another-model' model is not supported when using Codex with a ChatGPT account."}`, ""},
		{"model code conflicts with field", 404, `{"error":{"code":"model_not_found","model":"other-model"}}`, ""},
		{"case-distinct model field", 404, `{"error":{"code":"model_not_found","model":"TEST-MODEL"}}`, ""},
		{"case-distinct model message", 400, `{"error":{"code":"unsupported_model","message":"The model TEST-MODEL is not supported."}}`, ""},
		{"same model with uppercase prose", 400, `{"error":{"message":"THE MODEL test-model IS NOT SUPPORTED."}}`, "model_unsupported"},
		{"not found with matching model", 404, `{"error":{"metadata":{"model":"test-model"},"message":"Model 'test-model' was not found."}}`, "model_unsupported"},
		{"unrelated suggested model", 404, `{"error":{"code":"model_not_found","metadata":{"model":"test-model"},"suggestions":[{"model":"replacement"}]}}`, "model_unsupported"},
		{"different model in error details", 404, `{"error":{"code":"model_not_found","details":[{"metadata":{"model":"TEST-MODEL"}}]}}`, ""},
		{"different model after error label", 404, `{"code":"model_not_found","message":"Model not found: other-model."}`, ""},
		{"account entitlement reset", 429, `{"error":{"type":"usage_limit_reached","message":"Weekly account allowance exhausted","quota_scope":"account"}}`, "quota_exhausted"},
		{"account billing mentions project administrator", 429, `{"error":{"code":"billing_hard_limit_reached","message":"The account quota was exhausted. Contact the project administrator."}}`, "quota_exhausted"},
		{"region quota in message", 429, `{"error":{"code":"quota_exceeded","message":"Regional API returned a region-level quota error."}}`, ""},
		{"model code conflicts with message", 400, `{"error":{"code":"unsupported_model","message":"The model 'other-model' is not supported."}}`, ""},
		{"transient", 429, `{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached for tokens per minute"}}`, ""},
		{"conflicting limit code", 429, `{"error":{"type":"usage_limit_reached","code":"rate_limit_exceeded"}}`, ""},
		{"conflicting limit message", 429, `{"error":{"type":"quota_exceeded","message":"Your requests per minute quota was exceeded."}}`, ""},
		{"model-specific message", 429, `{"error":{"code":"quota_exceeded","message":"Daily quota exceeded for this model."}}`, ""},
		{"model scope", 429, `{"error":{"code":"quota_exceeded","message":"Quota exceeded","metadata":{"scope":"per_model"}}}`, ""},
		{"model field", 429, `{"error":{"code":"quota_exceeded","message":"Quota exceeded","model":"test-model"}}`, ""},
		{"TPM message", 429, `{"error":{"code":"quota_exceeded","message":"TPM exhausted."}}`, ""},
		{"RPM metric", 429, `{"error":{"code":"quota_exceeded","limit":{"metric":"RPM"}}}`, ""},
		{"retry field", 429, `{"error":{"code":"quota_exceeded","message":"Quota exceeded"},"retry_after":2}`, ""},
		{"retry message", 429, `{"error":{"type":"insufficient_quota","message":"Please try again later"}}`, ""},
		{"generic", 429, `{"message":"Too many requests"}`, ""},
		{"incidental quota", 429, `{"message":"Unable to check whether your quota was exhausted","id":"quota_exhausted"}`, ""},
		{"invalid parameter", 400, `{"error":{"type":"invalid_request_error","message":"This model does not support temperature"}}`, ""},
		{"wrong model", 400, `{"error":{"message":"The model other-model is not supported."}}`, ""},
		{"generic 404", 404, `{"error":{"message":"Not Found","id":"model_not_found"}}`, ""},
		{"wrong status", 503, `{"error":{"code":"insufficient_quota"}}`, ""},
		{"duplicate code", 429, `{"error":{"code":"insufficient_quota","code":"rate_limit_exceeded"}}`, ""},
		{"non-string code", 400, `{"error":{"code":["model_not_found"],"message":"Unknown model"}}`, ""},
		{"oversize", 429, `{"error":{"code":"insufficient_quota","message":"` + strings.Repeat("m", 16<<10) + `"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := managedUpstreamCondition(tc.status, tc.body, model, nil); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestManagedUpstreamConditionRequiresVerifiedProbe(t *testing.T) {
	const sensitive = "response-body-must-remain-private"
	for _, mode := range []string{"verified", "outer_401", "outer_500", "replaced", "cancelled", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			f := managementTestFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			snapshot, err := f.host.CredentialSnapshot(ctx, "one", "one")
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v8/management/requests/api-call" {
					t.Error("unexpected management request")
				}
				body := `{"error":{"code":"insufficient_quota","message":"` + sensitive + `"}}`
				switch mode {
				case "outer_401":
					w.WriteHeader(401)
				case "outer_500":
					w.WriteHeader(500)
				case "replaced":
					f.mu.Lock()
					f.changedContent = true
					f.mu.Unlock()
				case "oversize":
					body += strings.Repeat(" ", 2048)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 429, "body": body})
			}))
			t.Cleanup(server.Close)
			f.host.Management, err = NewManagementClient(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			f.host.Management.authorize(http.Header{"Authorization": {"Bearer offline-fixture"}})
			if mode == "cancelled" {
				gets := 0
				call := f.host.Call
				f.host.Call = func(method string, in, out any) error {
					err := call(method, in, out)
					if method == "host.auth.get" {
						gets++
						if gets == 2 {
							cancel()
						}
					}
					return err
				}
			}
			s := f.host.Execute(ctx, Execution{AuthID: "one", Provider: "codex", Model: "a", Prompt: "synthetic",
				ResponseLimit: 1024, ManagedProbe: &snapshot})
			if s.ExecutionStatus == "completed" || s.QualityVerdict != "unknown" || s.Comparable || s.Evidence.Verified {
				t.Fatal("upstream failure became answer evidence", s)
			}
			raw, err := json.Marshal(s)
			if err != nil || bytes.Contains(raw, []byte(sensitive)) {
				t.Fatal("raw provider error entered the sample")
			}
			if mode == "verified" {
				if s.UpstreamCondition != "quota_exhausted" || s.ExecutionStatus != "failed" || s.Error != "upstream HTTP 429" ||
					s.Evidence.AuthID != "one" || s.Evidence.RequestedModel != "a" || s.Evidence.PayloadFingerprint == "" || s.Evidence.ResolvedModel != "" {
					t.Fatal("missing scoped management evidence", s)
				}
			} else if s.UpstreamCondition != "" {
				t.Fatal("unverified condition was classified", s)
			}
			if mode == "cancelled" && s.ExecutionStatus != "cancelled" || mode == "replaced" && !s.Evidence.ConditionsChanged {
				t.Fatal("interrupted verification not reflected in sample", s)
			}
		})
	}
}

func TestProviderProbeHonorsRateCounters(t *testing.T) {
	for _, tc := range []struct {
		name, remaining, want string
	}{
		{"depleted token window", "0", ""},
		{"available token window and exhausted account", "120", "quota_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Anthropic-Ratelimit-Tokens-Remaining", tc.remaining)
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"error":{"type":"usage_limit_reached","message":"Weekly account allowance exhausted","quota_scope":"account"}}`)
			}))
			t.Cleanup(upstream.Close)
			f := newConfigAdapterFixture(t)
			group := f.groups["openai-compatibility"][0]
			group["base-url"] = upstream.URL + "/v1"
			var route providerRoute
			for _, entry := range f.routes() {
				if entry.key["api-key"] == "example-provider-key-two" {
					route = entry
					break
				}
			}
			ctx := context.Background()
			snapshot, err := f.host.CredentialSnapshot(ctx, route.id, route.index)
			if err != nil {
				t.Fatal(err)
			}
			s := f.host.Execute(ctx, Execution{AuthID: route.id, Provider: route.executor, Model: "alias", Prompt: "offline", ManagedProbe: &snapshot})
			if s.Error != "upstream HTTP 429" || s.UpstreamCondition != tc.want || s.QualityVerdict != "unknown" || calls.Load() != 1 || f.attempts != 0 {
				t.Fatalf("unexpected provider result: error=%q condition=%q calls=%d writes=%d", s.Error, s.UpstreamCondition, calls.Load(), f.attempts)
			}
		})
	}
}

func TestManagedProbePreservesLiteralPlaceholder(t *testing.T) {
	ctx := context.Background()
	f := managementTestFixture(t)
	snapshot, err := f.host.CredentialSnapshot(ctx, "one", "one")
	if err != nil {
		t.Fatal(err)
	}
	const prompt = `Document "$TOKEN$", $TOKEN$TOKEN$ and \$TOKEN$.`
	const model = "fixture-$TOKEN$"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Data   string
			Header map[string]string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(request.Data, "$TOKEN$") || request.Header["Authorization"] != "Bearer $TOKEN$" {
			t.Error("CPA's reserved token should occur only in the authentication header")
		}
		var payload struct {
			Model string
			Input []struct{ Content []struct{ Text string } }
		}
		if err := json.Unmarshal([]byte(request.Data), &payload); err != nil || payload.Model != model || len(payload.Input) != 1 ||
			len(payload.Input[0].Content) != 1 || payload.Input[0].Content[0].Text != prompt {
			t.Error("management transport changed the model or question text", err)
		}
		fmt.Fprint(w, `{"status_code":503,"header":{},"body":""}`)
	}))
	t.Cleanup(server.Close)
	f.host.Management, err = NewManagementClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	f.host.Management.authorize(http.Header{"Authorization": {"Bearer offline"}})
	s := f.host.Execute(ctx, Execution{AuthID: "one", Provider: "codex", Model: model, Prompt: prompt, ManagedProbe: &snapshot})
	if s.Error != "upstream HTTP 503" || s.UpstreamCondition != "" {
		t.Fatal("expected one diagnostic probe", s)
	}
}

func TestUnconfirmedUpstreamErrorsNeverRemoveCredential(t *testing.T) {
	const privateHeader = "header-must-not-enter-run-history"
	for _, tc := range []struct {
		name, envelope, upstreamBody string
		status                       int
		header                       http.Header
	}{
		{name: "duplicate inner status", envelope: `{"status_code":503,"status_code":401,"body":""}`},
		{name: "case variant inner status", envelope: `{"status_code":503,"STATUS_CODE":401,"body":""}`},
		{name: "duplicate inner body", envelope: `{"status_code":429,"body":"Too many requests","body":"Quota exhausted."}`},
		{name: "missing inner body", envelope: `{"status_code":402}`},
		{name: "null inner body", envelope: `{"status_code":401,"body":null}`},
		{name: "missing inner status", envelope: `{"body":"irrelevant"}`},
		{name: "invalid inner status", envelope: `{"status_code":703,"body":""}`},
		{name: "duplicate inner headers", envelope: `{"status_code":429,"header":{"X-RateLimit-Remaining-Requests":["0"]},"header":{},"body":"Quota exhausted."}`},
		{name: "duplicate quota counters", envelope: `{"status_code":429,"header":{"X-RateLimit-Remaining-Requests":["0"],"X-RateLimit-Remaining-Requests":["40"]},"body":"Quota exhausted."}`},
		{name: "invalid quota headers", envelope: `{"status_code":429,"header":{"X-RateLimit-Remaining-Requests":0},"body":"Quota exhausted."}`},
		{name: "daily request limit", upstreamBody: `{"error":{"code":"quota_exceeded","message":"Requests per day quota reached."}}`},
		{name: "daily request count", upstreamBody: `{"error":{"code":"quota_exceeded","message":"Daily request limit exceeded."}}`},
		{name: "hourly token limit", upstreamBody: `{"error":{"type":"insufficient_quota","metadata":{"metric":"tokens_per_hour"}}}`},
		{name: "daily request metric", upstreamBody: `{"error":{"code":"quota_exceeded","metadata":{"metric":"RPD"}}}`},
		{name: "deployment scope", upstreamBody: `{"error":{"code":"quota_exceeded","metadata":{"quota_scope":"per_deployment"}}}`},
		{name: "deployment in error message", upstreamBody: `{"error":{"code":"quota_exceeded","message":"Quota exhausted for this deployment."}}`},
		{name: "request window in headers", upstreamBody: `{"error":{"code":"quota_exceeded","message":"Quota exceeded"}}`,
			header: http.Header{"retry-after": {"1"}, "x-ratelimit-remaining-requests": {"0"}, "X-RateLimit-Reset-Requests": {"1s"}, "Set-Cookie": {privateHeader}}},
		{name: "different model in error metadata", status: 404, upstreamBody: `{"error":{"code":"model_not_found","metadata":{"model":"unselected"}}}`},
		{name: "different model in not-found message", status: 404, upstreamBody: `{"error":{"code":"model_not_found","message":"Model 'unselected' not found."}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := managementTestFixture(t)
			f.probeEnvelope, f.upstreamStatus, f.upstreamBody = tc.envelope, 429, tc.upstreamBody
			if tc.status != 0 {
				f.upstreamStatus = tc.status
			}
			if tc.header != nil {
				raw, err := json.Marshal(map[string]any{"status_code": f.upstreamStatus, "header": tc.header, "body": tc.upstreamBody})
				if err != nil {
					t.Fatal(err)
				}
				f.probeEnvelope = string(raw)
			}
			rule := f.rule(t, "observe_only", false, false)
			rule.UpstreamErrorActions = map[string]string{"unauthorized": deleteCredential, "payment_required": deleteCredential, "quota_exhausted": deleteCredential, "model_unsupported": deleteCredential}
			rule, err := f.engine.SaveRule(context.Background(), *rule, true)
			if err != nil {
				t.Fatal(err)
			}
			runs, err := f.round(t, rule)
			if err != nil || len(runs) != 1 {
				t.Fatal("expected one diagnostic run", err)
			}
			if f.deletes != 0 || len(f.patches) != 0 || f.probes != len(rule.Models)*rule.Samples {
				t.Fatalf("ambiguous evidence changed the credential or stopped the round: deletes=%d patches=%d probes=%d", f.deletes, len(f.patches), f.probes)
			}
			for _, sample := range runs[0].Samples {
				if sample.QualityVerdict != "unknown" || sample.UpstreamCondition != "" {
					t.Fatal("unconfirmed error became actionable", sample)
				}
				if tc.envelope != "" && strings.Contains(sample.Error, "upstream HTTP") {
					t.Fatal("invalid management receipt presented as a confirmed upstream status", sample.Error)
				}
				if tc.envelope == "" && sample.Error != fmt.Sprintf("upstream HTTP %d", f.upstreamStatus) {
					t.Fatal("actual upstream status lost", sample.Error)
				}
			}
			raw, err := json.Marshal(runs)
			if err != nil || bytes.Contains(raw, []byte(privateHeader)) {
				t.Fatal("private upstream header entered run history")
			}
		})
	}
}
