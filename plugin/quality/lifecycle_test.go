package quality

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type lifecycleHost struct {
	mu      sync.Mutex
	calls   []Execution
	execute func(context.Context, Execution) Sample
	target  Target
	state   ActionState
	pauses  int
}

func (h *lifecycleHost) Capabilities() HostCapabilities {
	return HostCapabilities{Mode: "offline_verified_fixture", QualityPause: true, ManualRelease: true, VerifiedEvidence: true, ModelOperationCancel: true, StreamClose: true}
}

func (h *lifecycleHost) Targets(context.Context) ([]Target, error) { return []Target{h.target}, nil }
func (h *lifecycleHost) Actions(context.Context) ([]Action, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state.Status == "active" && h.state.Action != nil {
		return []Action{*h.state.Action}, nil
	}
	return []Action{}, nil
}
func (h *lifecycleHost) Pause(_ context.Context, a Action) (Action, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pauses++
	h.state = ActionState{Status: "active", Action: &a}
	return a, nil
}
func (h *lifecycleHost) Release(context.Context, Action) error { return nil }
func (h *lifecycleHost) Execute(ctx context.Context, e Execution) Sample {
	h.mu.Lock()
	h.calls = append(h.calls, e)
	h.mu.Unlock()
	if h.execute != nil {
		return h.execute(ctx, e)
	}
	return Sample{ExecutionStatus: "completed", Output: "4", Evidence: Evidence{Verified: true, RequestedModel: e.Model, ResolvedModel: e.Model, AuthID: e.AuthID, Path: "offline", PayloadFingerprint: "stable", PayloadConfigVersion: "payload-v1"}}
}
func (h *lifecycleHost) ActionStatus(context.Context, string) (ActionState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state, nil
}
func lifeFixture(t *testing.T) (*Engine, *lifecycleHost) {
	en, _, _ := semanticsEngine(t)
	h := &lifecycleHost{target: Target{AuthID: "auth-a", Provider: "offline", Models: []string{"a", "b"}, IdentityVersion: "identity-a", ComparisonIdentity: "distinct-a", PayloadConfigVersion: "payload-v1", CanPause: true, EffectiveAvailable: true}, state: ActionState{Status: "absent"}}
	en.host = h
	return en, h
}
func lifeConfig() RunConfig {
	return RunConfig{AuthID: "auth-a", Provider: "offline", Models: []string{"a"}, QuestionID: "logic-boxes", Samples: 1, Parameters: Parameters{MaxTokens: 64}, Judge: Judge{Mode: "deterministic"}}
}
func TestQueueIdempotencyAndSameKeyConflict(t *testing.T) {
	en, h := lifeFixture(t)
	en.queue = make(chan string, 1)
	c := lifeConfig()
	c.IdempotencyKey = "same"
	one, err := en.Submit(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	two, err := en.Submit(context.Background(), c)
	if err != nil || one.ID != two.ID {
		t.Fatal("duplicate submission", err)
	}
	c.Parameters.MaxTokens = 65
	if _, err = en.Submit(context.Background(), c); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatal("key conflict accepted", err)
	}
	c.IdempotencyKey = "other"
	if _, err = en.Submit(context.Background(), c); err == nil || !strings.Contains(err.Error(), "queue_full") {
		t.Fatal("unbounded queue", err)
	}
	if len(h.calls) != 0 {
		t.Fatal("submission called model before worker")
	}
	var persisted Run
	if err = en.store.Get("runs", one.ID, &persisted); err != nil || persisted.Status != "queued" {
		t.Fatal("job not durable before queue")
	}
}

func TestManualReceiptReadAndRetention(t *testing.T) {
	en, host := lifeFixture(t)
	config := lifeConfig()
	config.IdempotencyKey = "synthetic-request"
	lookup := func(want int) *Run {
		t.Helper()
		req := pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/model-quality/runs/detail", Query: url.Values{"idempotency_key": {config.IdempotencyKey}}}
		res := en.Handle(context.Background(), req)
		if res.StatusCode != want {
			t.Fatalf("status %d, want %d: %s", res.StatusCode, want, res.Body)
		}
		if want != http.StatusOK {
			var reply struct {
				Error queryFailure `json:"error"`
			}
			if err := json.Unmarshal(res.Body, &reply); err != nil {
				t.Fatal(err)
			}
			if want == http.StatusNotFound && reply.Error.Code != "command_not_accepted" || want == http.StatusGone && reply.Error.Code != "gone" {
				t.Fatalf("ambiguous command lookup %d: %s", want, res.Body)
			}
			return nil
		}
		var run Run
		if err := json.Unmarshal(res.Body, &run); err != nil {
			t.Fatal(err)
		}
		return &run
	}
	lookup(http.StatusNotFound)
	run, err := en.Submit(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if got := lookup(http.StatusOK); got.ID != run.ID || got.Config.Question.Prompt == "" || got.Config.IdempotencyKey != config.IdempotencyKey {
		t.Fatal("receipt did not identify the original frozen run")
	}
	if err := en.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	if err := en.DeleteManualRun(run.ID); err != nil {
		t.Fatal(err)
	}
	lookup(http.StatusGone)
	if len(en.runs) != 0 || len(host.calls) != 0 {
		t.Fatal("receipt lookup executed or recreated a run")
	}
	config.IdempotencyKey = strings.Repeat("k", 129)
	lookup(http.StatusBadRequest)
}

func TestCancelRunningJobAndBudgetNoReplay(t *testing.T) {
	en, h := lifeFixture(t)
	entered := make(chan struct{})
	h.execute = func(ctx context.Context, e Execution) Sample {
		close(entered)
		<-ctx.Done()
		return Sample{ExecutionStatus: "cancelled"}
	}
	run, err := en.Submit(context.Background(), lifeConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { en.execute(<-en.queue); close(done) }()
	<-entered
	if err = en.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	if err = en.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	<-done
	r, _ := en.Run(run.ID)
	if r.Status != "cancelled" || len(r.Samples) != 1 || en.budget.Calls != 1 {
		t.Fatal(r, en.budget)
	}
	if h.calls[0].Prompt == "4" || strings.Contains(h.calls[0].Prompt, "Answer: 4") {
		t.Fatal("reference leaked to target")
	}
	en.execute(run.ID)
	if len(h.calls) != 1 {
		t.Fatal("terminal run replayed")
	}
}
func TestRestartInterruptsIncompleteRunsPreservingSamples(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	s, e := OpenStore(cfg.DataDir, cfg.MaxStorageBytes)
	if e != nil {
		t.Fatal(e)
	}
	original := Run{ID: "interrupted", Status: "running", Source: "manual_test", CreatedAt: time.Now().UTC(), Samples: []Sample{{Output: "already consumed", ExecutionStatus: "completed", QualityVerdict: "unknown"}}}
	if e = s.Put("runs", original.ID, original); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	h := &lifecycleHost{}
	en, e := New(cfg, h)
	if e != nil {
		t.Fatal(e)
	}
	r, e := en.Run(original.ID)
	if e != nil || r.Status != "interrupted" || len(r.Samples) != 1 || r.Samples[0].Output != "already consumed" {
		t.Fatal(r, e)
	}
	if e = en.Close(); e != nil {
		t.Fatal(e)
	}
	if e = en.Close(); e != nil {
		t.Fatal("shutdown not idempotent", e)
	}
	if len(h.calls) != 0 {
		t.Fatal("restart consumed extra model budget")
	}
}
func TestDuplicateTickAndPausedRuleDoNotSubmit(t *testing.T) {
	en, h := lifeFixture(t)
	en.cfg.SchedulingEnabled = true
	var rule *Rule
	for _, r := range en.rules {
		rule = r
	}
	rule.SchedulingEnabled = true
	now := en.now()
	rule.NextRun = now
	en.Tick(now)
	en.Tick(now)
	if len(en.queue) != 1 {
		t.Fatal("duplicate tick jobs", len(en.queue))
	}
	if len(h.calls) != 0 {
		t.Fatal("tick bypassed durable worker queue")
	}
	if err := en.PauseRule(rule.ID, rule.Revision, true); err != nil {
		t.Fatal(err)
	}
	en.Tick(now.Add(24 * time.Hour))
	if len(en.queue) != 1 {
		t.Fatal("paused rule was scheduled")
	}
}
func TestModelExecutionParametersFrozenAndPayloadDriftBlocked(t *testing.T) {
	en, h := lifeFixture(t)
	c := lifeConfig()
	c.Samples = 2
	v := 0.3
	c.Parameters.Temperature = &v
	r, e := en.Submit(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	v = 1.4
	en.execute(<-en.queue)
	out, _ := en.Run(r.ID)
	if len(out.Samples) != 2 || !out.Samples[0].Comparable || *h.calls[0].Parameters.Temperature != 0.3 || *h.calls[1].Parameters.Temperature != 0.3 {
		t.Fatal("frozen parameters were mutable", out)
	}
	h.execute = func(_ context.Context, e Execution) Sample {
		return Sample{ExecutionStatus: "completed", Output: "0", Evidence: Evidence{Verified: true, AuthID: e.AuthID, RequestedModel: e.Model, ResolvedModel: e.Model, PayloadFingerprint: "changed", Path: "offline", PayloadConfigVersion: "payload-v2"}}
	}
	r, e = en.Submit(context.Background(), lifeConfig())
	if e != nil {
		t.Fatal(e)
	}
	en.execute(<-en.queue)
	out, _ = en.Run(r.ID)
	if out.Samples[0].Comparable {
		t.Fatal("payload config drift accepted")
	}
}
func TestIntentReconciliationNeverReplaysReleasedAction(t *testing.T) {
	en, h := lifeFixture(t)
	var rule *Rule
	for _, r := range en.rules {
		rule = r
	}
	run := semanticRound(en, rule, "incorrect", "incorrect")
	a := Action{OwnerPlugin: "model-quality", AuthID: rule.AuthID, RuleID: rule.ID, RuleRevision: rule.Revision, ActionID: "intent", ActionRevision: 1, EvidenceRunID: run.ID, IdentityVersion: "identity-a"}
	rule.ActionID = a.ActionID
	rule.ActionRuleRevision = a.RuleRevision
	rule.Enabled = false
	rule.BlockedAuthID = rule.AuthID
	rule.Status = "awaiting_release"
	rule.Revision++
	rule.IntentRevision = rule.Revision
	rule.IdentityVersion = a.IdentityVersion
	if e := en.store.Put("meta", "intent:"+a.ActionID, a); e != nil {
		t.Fatal(e)
	}
	if e := en.Reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	if h.pauses != 1 || h.state.Status != "active" {
		t.Fatal("unapplied intent not reconciled")
	}
	if e := en.Reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	if h.pauses != 1 {
		t.Fatal("active intent duplicated")
	}
	h.state = ActionState{Status: "released", Action: &a}
	if e := en.Reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	if rule.ActionID != "" || h.pauses != 1 {
		t.Fatal("released intent replayed")
	}
}
func TestDailyBudgetPersistsAndUsageUnknown(t *testing.T) {
	en, _ := lifeFixture(t)
	en.cfg.DailyTokens = 64
	if _, e := en.reserve(64); e != nil {
		t.Fatal(e)
	}
	if e := en.account(en.budget.Day, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := en.reserve(64); e == nil {
		t.Fatal("unknown usage released the reserved token budget")
	}
	var b Budget
	if e := en.store.Get("meta", "budget", &b); e != nil {
		t.Fatal(e)
	}
	if b.Calls != 1 || b.UnknownUsage != 1 || b.KnownTokens != 0 {
		t.Fatal(fmt.Sprint(b))
	}
	en.now = func() time.Time { return time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) }
	if _, e := en.reserve(64); e != nil {
		t.Fatal("UTC day did not advance", e)
	}
}
