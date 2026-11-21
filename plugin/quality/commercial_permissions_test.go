package quality

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCommercialDormantTimerStopRevokesPermission(t *testing.T) {
	for _, state := range []string{"global_gate_closed", "paused_global_timer", "paused_individual_timer"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			en, host, rule := scopeFixture(t, scopedTarget("one", "a", "b"))
			var err error
			if state == "paused_individual_timer" {
				rule, err = en.setRuleTimer(ctx, rule.ID, rule.Revision, true)
			} else {
				if state == "paused_global_timer" {
					if err = en.setScheduling(false, true); err != nil {
						t.Fatal(err)
					}
				}
				rule.SchedulingEnabled = true
				rule, err = en.SaveRule(ctx, *rule, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			paused := state != "global_gate_closed"
			if paused {
				if err = en.PauseRule(rule.ID, rule.Revision, true); err != nil {
					t.Fatal(err)
				}
				rule = cloneRule(en.rules[rule.ID])
			}
			if stale, err := en.setRuleTimer(ctx, rule.ID, rule.Revision+1, false); err == nil || stale != nil || en.rules[rule.ID].Revision != rule.Revision || !en.rules[rule.ID].SchedulingEnabled {
				t.Fatal("a stale click changed the dormant timer", stale, err)
			}
			stopped, err := en.setRuleTimer(ctx, rule.ID, rule.Revision, false)
			if err != nil {
				t.Fatal(err)
			}
			if stopped.SchedulingEnabled || stopped.timerActive || en.timers[rule.ID] || stopped.Revision != rule.Revision+1 || stopped.Enabled != rule.Enabled {
				t.Errorf("explicit stop did not revoke the dormant timer without changing rule enablement: scheduling=%t active=%t grant=%t revision=%d enabled=%t", stopped.SchedulingEnabled, stopped.timerActive, en.timers[rule.ID], stopped.Revision, stopped.Enabled)
			}
			var persisted Rule
			if err = en.store.Get("rules", rule.ID, &persisted); err != nil || persisted.SchedulingEnabled || persisted.Revision != stopped.Revision {
				t.Error("timer stop was not persisted", err)
			}
			if repeated, err := en.setRuleTimer(ctx, stopped.ID, stopped.Revision, false); err != nil || repeated.Revision != stopped.Revision || repeated.Enabled != stopped.Enabled {
				t.Fatal("repeating a dormant timer stop changed the rule", repeated, err)
			}
			if paused {
				if err = en.PauseRule(rule.ID, stopped.Revision, false); err != nil {
					t.Fatal(err)
				}
			} else if err = en.setScheduling(false, true); err != nil {
				t.Fatal(err)
			}
			en.Tick(en.now().Add(time.Hour))
			for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
				en.executeTask(task)
			}
			if host.executeCalls != 0 || len(en.runs) != 0 {
				t.Fatalf("a later enablement replayed an explicitly stopped timer: calls=%d runs=%d", host.executeCalls, len(en.runs))
			}
		})
	}
}

func TestCommercialTimerStopCancelsClaimedWorkBeforeRearming(t *testing.T) {
	en, host, rule, run := commercialScheduledFixture(t)
	ctx := context.Background()
	en.mu.Lock()
	task := en.nextRunnableLocked()
	en.mu.Unlock()
	if task == nil || task.id != run.ID {
		t.Fatal("missing claimed scheduled task")
	}
	defer task.cancel()
	stopped, err := en.setRuleTimer(ctx, rule.ID, rule.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(task.ctx.Err(), context.Canceled) || !en.runs[run.ID].CancelRequested || host.executeCalls != 0 {
		t.Fatal("stopping the timer did not cancel its claimed work")
	}
	restarted, err := en.setRuleTimer(ctx, stopped.ID, stopped.Revision, true)
	if err != nil || !restarted.timerActive {
		t.Fatal("explicitly starting the timer failed", err)
	}
	en.executeTask(task)
	if host.executeCalls != 0 || en.runs[run.ID].CompletedAt == nil {
		t.Fatal("rearming the timer replayed old claimed work")
	}
	en.Tick(restarted.NextRun)
	for next := en.nextRunnableLocked(); next != nil; next = en.nextRunnableLocked() {
		en.executeTask(next)
	}
	if host.executeCalls != 2 {
		t.Fatalf("a fresh authorized timer occurrence did not execute: calls=%d", host.executeCalls)
	}
}

func TestCommercialTimerStopKeepsSiblingScheduleAndExplicitExecution(t *testing.T) {
	en, host, rule, queued := commercialScheduledFixture(t)
	ctx := context.Background()
	sibling, err := en.SaveRule(ctx, Rule{Name: "sibling", TargetScope: scopeAll, RunConfig: rule.RunConfig, Enabled: true, SchedulingEnabled: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	en.Tick(sibling.NextRun)
	generation := en.scheduleGeneration
	stopped, err := en.setRuleTimer(ctx, rule.ID, rule.Revision, false)
	if err != nil || !stopped.Enabled || stopped.SchedulingEnabled || !stopped.NextRun.IsZero() {
		t.Fatal("could not stop the active timer", stopped, err)
	}
	repeated, err := en.setRuleTimer(ctx, stopped.ID, stopped.Revision, false)
	if err != nil || repeated.Revision != stopped.Revision || en.scheduleGeneration != generation {
		t.Fatal("repeated stop was not idempotent or revoked the instance", err)
	}
	if !queued.CancelRequested || queued.CompletedAt == nil || en.rules[sibling.ID].Revision != sibling.Revision || !en.timerAllowed(en.rules[sibling.ID]) {
		t.Fatal("stopping one timer did not cancel only its own work")
	}
	var persisted Run
	if err = en.store.Get("runs", queued.ID, &persisted); err != nil || !persisted.CancelRequested || persisted.CompletedAt == nil {
		t.Fatal("timer cancellation was not durable", err)
	}
	if receipt, err := en.dispatchRule(ctx, stopped.ID, stopped.Revision, false); err != nil || receipt.Count != 1 {
		t.Fatal("stopping a timer blocked explicit rule execution", receipt, err)
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if host.executeCalls != 4 {
		t.Fatalf("sibling timer and explicit execution should each make two calls: got %d", host.executeCalls)
	}
}

func TestCommercialDormantTimerStopSurvivesReloadWithoutRevokingSibling(t *testing.T) {
	en, host, rule := scopeFixture(t, scopedTarget("one", "a", "b"))
	ctx := context.Background()
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	on, err := en.setRuleTimer(ctx, rule.ID, rule.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := en.saveRule(ctx, Rule{Name: "sibling", TargetScope: scopeAll, RunConfig: rule.RunConfig}, false, "start")
	if err != nil {
		t.Fatal(err)
	}
	if err = en.PauseRule(on.ID, on.Revision, true); err != nil {
		t.Fatal(err)
	}
	stopped, err := en.setRuleTimer(ctx, on.ID, en.rules[on.ID].Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	en.rules, en.timers = map[string]*Rule{}, nil
	if err = en.load(); err != nil {
		t.Fatal(err)
	}
	if en.rules[stopped.ID].Enabled || en.rules[stopped.ID].SchedulingEnabled || en.timers[stopped.ID] || !en.timers[sibling.ID] {
		t.Fatal("reloading lost the stopped permission or the sibling authorization")
	}
	if err = en.PauseRule(stopped.ID, stopped.Revision, false); err != nil {
		t.Fatal(err)
	}
	en.Tick(en.now().Add(time.Hour))
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if host.executeCalls != 2 || len(en.runs) != 1 {
		t.Fatalf("reload did not preserve the independent timer choices: calls=%d runs=%d", host.executeCalls, len(en.runs))
	}
	for _, run := range en.runs {
		if run.Config.RuleID != sibling.ID || !run.Config.Scheduled {
			t.Fatal("reloading admitted the stopped timer")
		}
	}
}
