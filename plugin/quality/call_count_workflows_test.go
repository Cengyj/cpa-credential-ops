package quality

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

func seedWorkflowCallCount(t *testing.T, en *Engine, calls int) {
	t.Helper()
	en.budget = Budget{Day: en.now().UTC().Format("2006-01-02"), Calls: calls, ReservedTokens: int64(calls * 64), KnownTokens: int64(calls)}
	for _, key := range []string{"budget", "budget:" + en.budget.Day} {
		if err := en.store.Put("meta", key, en.budget); err != nil {
			t.Fatal(err)
		}
	}
}

func checkWorkflowCallCount(t *testing.T, en *Engine, calls, reserved, known int) {
	t.Helper()
	want := Budget{Day: en.now().UTC().Format("2006-01-02"), Calls: calls, ReservedTokens: int64(reserved), KnownTokens: int64(known)}
	for _, key := range []string{"budget", "budget:" + want.Day} {
		var got Budget
		if err := en.store.Get("meta", key, &got); err != nil || got != want {
			t.Fatalf("durable %s = %+v, want %+v, err=%v", key, got, want, err)
		}
	}
}

type recordingWorkflowHost struct {
	*manualOnlyHost
	calls []Execution
}

func (h *recordingWorkflowHost) Execute(_ context.Context, request Execution) Sample {
	h.calls = append(h.calls, request)
	if request.Judge {
		return Sample{ExecutionStatus: "completed", Output: `{"verdict":"correct","reason":"fixture"}`, Usage: &Usage{Total: 3}}
	}
	return Sample{ExecutionStatus: "completed", Output: "4", Usage: &Usage{Total: 2}}
}

func TestRuleCommandControlAndJudgeBeyond200Calls(t *testing.T) {
	t.Run("control starts at 200", func(t *testing.T) { checkRuleCommandCalls(t, 200) })
	t.Run("judge starts at 200", func(t *testing.T) { checkRuleCommandCalls(t, 199) })
}

func checkRuleCommandCalls(t *testing.T, initialCalls int) {
	t.Helper()
	en, base, _ := manualOnlyFixture(t)
	host := &recordingWorkflowHost{manualOnlyHost: base}
	en.host = host
	seedWorkflowCallCount(t, en, initialCalls)
	cfg := lifeConfig()
	cfg.ControlAuthID, cfg.ControlProvider = "auth-b", "offline"
	cfg.Judge = Judge{Mode: "llm", AuthID: "auth-c", Provider: "offline", Model: "a"}
	rule, err := en.SaveRule(t.Context(), Rule{RunConfig: cfg, Enabled: true, Action: "observe_only"}, false)
	if err != nil {
		t.Fatal(err)
	}
	const key = "over-200-rule"
	body := fmt.Sprintf(`{"id":%q,"revision":%d,"idempotency_key":%q}`, rule.ID, rule.Revision, key)
	query := url.Values{"idempotency_key": {key}}
	submit := func() ruleCommandReceipt {
		t.Helper()
		if r := ruleAPI(en, "POST", "/rules/run", body, nil); r.StatusCode != 202 {
			t.Fatalf("rule submit: HTTP %d", r.StatusCode)
		}
		en.wg.Wait()
		r := ruleAPI(en, "GET", "/rules/commands", "", query)
		var receipt ruleCommandReceipt
		if r.StatusCode != 200 || json.Unmarshal(r.Body, &receipt) != nil || receipt.State != "completed" ||
			receipt.Result == nil || receipt.Result.Count != 1 || receipt.Result.RunID == "" {
			t.Fatalf("missing accepted run in durable command receipt: HTTP %d, %+v", r.StatusCode, receipt)
		}
		return receipt
	}
	receipt := submit()
	task := en.nextRunnableLocked()
	if task == nil || task.id != receipt.Result.RunID {
		t.Fatal("queue and durable command receipt disagree")
	}
	en.executeTask(task)
	var run Run
	if err := en.store.Get("runs", receipt.Result.RunID, &run); err != nil || run.Status != "completed" ||
		run.Config.RuleID != rule.ID || len(run.Controls) != 1 || len(run.Samples) != 1 {
		t.Fatalf("rule result: status=%s error=%s controls=%d subjects=%d read_error=%v", run.Status, run.Error, len(run.Controls), len(run.Samples), err)
	}
	for _, sample := range []Sample{run.Controls[0], run.Samples[0]} {
		if sample.Output != "4" || sample.QualityVerdict != "correct" || sample.JudgeUsage == nil || sample.JudgeUsage.Total != 3 {
			t.Fatalf("lost answer or judge result: %+v", sample)
		}
	}
	wantIDs := []string{"auth-b", "auth-c", "auth-a", "auth-c"}
	wantSources := []string{"scheduled_probe", "judge", "scheduled_probe", "judge"}
	if len(host.calls) != len(wantIDs) {
		t.Fatalf("fake model calls=%d, want %d", len(host.calls), len(wantIDs))
	}
	for i, call := range host.calls {
		if call.AuthID != wantIDs[i] || call.Source != wantSources[i] || call.Judge != (call.Source == "judge") {
			t.Fatalf("unexpected fake call %d: auth=%s source=%s judge=%v", i, call.AuthID, call.Source, call.Judge)
		}
	}
	if repeat := submit(); !reflect.DeepEqual(receipt, repeat) || en.nextRunnableLocked() != nil || len(en.runs) != 1 {
		t.Fatal("repeated command admitted more work or replaced its receipt")
	}
	checkWorkflowCallCount(t, en, initialCalls+4, initialCalls*64+2*cfg.Parameters.MaxTokens+2*512, initialCalls+10)
}

func TestScheduledNormalAndOwnedRecoveryBeyond200Calls(t *testing.T) {
	t.Run("normal round starts at 200", func(t *testing.T) { checkScheduledCalls(t, 200) })
	t.Run("recovery round starts at 200", func(t *testing.T) { checkScheduledCalls(t, 198) })
}

func checkScheduledCalls(t *testing.T, initialCalls int) {
	t.Helper()
	f := managementTestFixture(t)
	en := f.engine
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	en.now = func() time.Time { return now }
	seedWorkflowCallCount(t, en, initialCalls)
	r := f.rule(t, disableCredential, false, true)
	r, err := en.setRuleTimer(t.Context(), r.ID, r.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	for i, cycle := range []struct{ answer, action string }{{"5", "credential_suspended"}, {"4", "credential_restored"}} {
		f.answers["a"] = cycle.answer
		now = en.rules[r.ID].NextRun
		if now.IsZero() {
			t.Fatal("rule lost timer permission")
		}
		en.Tick(now)
		task := en.nextRunnableLocked()
		if task == nil {
			t.Fatal("authorized due timer did not enqueue its round")
		}
		en.executeTask(task)
		var run Run
		if err := en.store.Get("runs", task.id, &run); err != nil || run.Status != "completed" ||
			run.Config.RuleID != r.ID || run.Source != "scheduled_probe" || run.Config.RecoveryCheck != (i == 1) ||
			run.ActionStatus != cycle.action || len(run.Samples) != 2 {
			t.Fatalf("timer result in cycle %d: status=%s action=%s error=%s samples=%d read_error=%v", i, run.Status, run.ActionStatus, run.Error, len(run.Samples), err)
		}
		for j, sample := range run.Samples {
			if sample.ExecutionStatus != "completed" || sample.Evidence.Path != "cpa_management_probe" ||
				sample.Model != r.Models[j] || sample.Output != f.answers[sample.Model] {
				t.Fatalf("wrong probe result in cycle %d: %+v", i, sample)
			}
		}
		en.Tick(now)
		if en.nextRunnableLocked() != nil || len(en.runs) != i+1 {
			t.Fatal("duplicate tick produced an additional round")
		}
		calls := 2 * (i + 1)
		checkWorkflowCallCount(t, en, initialCalls+calls, initialCalls*64+calls*r.Parameters.MaxTokens, initialCalls+calls*3)
	}
	if f.probes != 4 || f.disabledProbes != 2 || f.nativeCalls != 0 || f.deletes != 0 || !reflect.DeepEqual(f.patches, []bool{true, false}) ||
		f.entries["one"].Disabled || en.managed["one"].State != "restored" {
		t.Fatalf("unexpected probe/restore effects: probes=%d disabled=%d native=%d writes=%v", f.probes, f.disabledProbes, f.nativeCalls, f.patches)
	}
}
