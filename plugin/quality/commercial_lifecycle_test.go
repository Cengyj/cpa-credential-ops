package quality

import (
	"context"
	"errors"
	"testing"
)

func commercialScheduledFixture(t *testing.T) (*Engine, *semanticsHost, *Rule, *Run) {
	t.Helper()
	en, host, rule := scopeFixture(t, scopedTarget("one", "a", "b"))
	if err := en.setScheduling(false, true); err != nil {
		t.Fatal(err)
	}
	rule.SchedulingEnabled = true
	rule, err := en.SaveRule(context.Background(), *rule, true)
	if err != nil {
		t.Fatal(err)
	}
	en.Tick(rule.NextRun)
	for _, run := range en.runs {
		if run.Config.RuleID == rule.ID && run.Status == "queued" && run.Config.Scheduled {
			return en, host, rule, run
		}
	}
	t.Fatal("scheduled fixture did not queue a run")
	return nil, nil, nil, nil
}

func TestCommercialGlobalScheduleStopCannotReplayQueuedWorkAfterRestartingTimer(t *testing.T) {
	for _, entrypoint := range []string{"schedule_switch", "settings"} {
		t.Run(entrypoint, func(t *testing.T) {
			en, host, rule, queued := commercialScheduledFixture(t)
			change := func(enabled bool) {
				t.Helper()
				var err error
				if entrypoint == "settings" {
					cfg := en.Config()
					cfg.SchedulingEnabled = enabled
					err = en.UpdateConfig(cfg)
				} else {
					err = en.setScheduling(!enabled, enabled)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			change(false)
			change(true)
			for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
				en.executeTask(task)
			}
			if host.executeCalls != 0 || !queued.CancelRequested || queued.CompletedAt == nil {
				t.Fatalf("revoked scheduled work resumed: calls=%d status=%s cancelled=%t", host.executeCalls, queued.Status, queued.CancelRequested)
			}
			var persisted Run
			if err := en.store.Get("runs", queued.ID, &persisted); err != nil || !persisted.CancelRequested || persisted.CompletedAt == nil {
				t.Fatal("schedule revocation was not durable", err)
			}
			if en.rules[rule.ID].Revision != rule.Revision || !en.rules[rule.ID].Enabled || !en.rules[rule.ID].SchedulingEnabled {
				t.Fatal("global schedule stop changed the rule's own configuration")
			}
			if receipt, err := en.dispatchRule(context.Background(), rule.ID, rule.Revision, true); err != nil || receipt.Count != 1 {
				t.Fatal("a fresh authorized occurrence could not run", receipt, err)
			}
		})
	}
}

func TestCommercialGlobalScheduleStopCancelsClaimedTaskContext(t *testing.T) {
	en, host, _, run := commercialScheduledFixture(t)
	en.mu.Lock()
	task := en.nextRunnableLocked()
	en.mu.Unlock()
	if task == nil || task.id != run.ID {
		t.Fatal("missing claimed scheduled task")
	}
	defer func() {
		task.cancel()
		en.executeTask(task)
	}()
	if err := en.setScheduling(true, false); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(task.ctx.Err(), context.Canceled) || !en.runs[task.id].CancelRequested || host.executeCalls != 0 {
		t.Fatal("turning off scheduling did not cancel the in-flight task context")
	}
}

func TestCommercialGlobalScheduleStopKeepsExplicitManualRequests(t *testing.T) {
	en, host, _, scheduled := commercialScheduledFixture(t)
	ctx := context.Background()
	cfg := lifeConfig()
	cfg.AuthID = "one"
	manual, err := en.Submit(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	immediateRule, err := en.SaveRule(ctx, Rule{Name: "explicit", TargetScope: scopeAll, Enabled: true, RunConfig: commercialConfigWithoutSubject(cfg)}, false)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := en.dispatchRule(ctx, immediateRule.ID, immediateRule.Revision, false)
	if err != nil || receipt.Count != 1 {
		t.Fatal(receipt, err)
	}
	if err = en.setScheduling(true, false); err != nil {
		t.Fatal(err)
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if host.executeCalls != 2 || en.runs[manual.ID].CancelRequested || !scheduled.CancelRequested {
		t.Fatalf("schedule stop crossed an explicit request boundary: calls=%d manual_cancelled=%t scheduled_cancelled=%t", host.executeCalls, en.runs[manual.ID].CancelRequested, scheduled.CancelRequested)
	}
}

func commercialConfigWithoutSubject(c RunConfig) RunConfig {
	c.AuthID, c.Provider = "", ""
	return c
}

type commercialCancelManualHost struct {
	*NativeHost
	cancel context.CancelFunc
}

func (h *commercialCancelManualHost) verifyEnabledManualFile(ctx context.Context, target Target, previous *CredentialDisposition) (string, error) {
	fingerprint, err := h.NativeHost.verifyEnabledManualFile(ctx, target, previous)
	h.cancel()
	return fingerprint, err
}

func TestCommercialCancelledManualPreflightCannotCommitRunOrReceipt(t *testing.T) {
	f := managementTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.engine.host = &commercialCancelManualHost{NativeHost: f.host, cancel: cancel}
	f.engine.managed["one"] = &CredentialDisposition{AuthID: "one", State: "externally_enabled"}
	cfg := lifeConfig()
	cfg.AuthID, cfg.Provider, cfg.IdempotencyKey = "one", "codex", "cancel-before-commit"
	run, err := f.engine.Submit(ctx, cfg)
	if !errors.Is(err, context.Canceled) || run != nil {
		t.Fatalf("cancelled preflight admitted work: run=%v err=%v", run != nil, err)
	}
	if len(f.engine.runs) != 0 || len(f.engine.queue) != 0 || f.probes != 0 || f.nativeCalls != 0 {
		t.Fatal("cancelled preflight left executable work")
	}
	if prior, err := f.engine.runByReceipt(cfg.IdempotencyKey); err == nil || prior != nil {
		t.Fatal("cancelled preflight consumed the submission receipt")
	}
	f.engine.host = f.host
	if fresh, err := f.engine.Submit(context.Background(), cfg); err != nil || fresh == nil || fresh.Status != "queued" {
		t.Fatal("cancelled preflight prevented an explicit retry", fresh, err)
	}
}

func TestCommercialPausedRecoveryRuleCanResumeWithoutEnablingCredential(t *testing.T) {
	f := managementTestFixture(t)
	rule := f.rule(t, disableCredential, false, true)
	f.answers["a"] = "5"
	if rows, err := f.round(t, rule); err != nil || len(rows) != 1 || rows[0].ActionStatus != "credential_suspended" {
		t.Fatal(rows, err)
	}
	if err := f.engine.PauseRule(rule.ID, rule.Revision, true); err != nil {
		t.Fatal(err)
	}
	stopped := cloneRule(f.engine.rules[rule.ID])
	before := f.probes
	if err := f.engine.PauseRule(stopped.ID, stopped.Revision, false); err != nil {
		t.Fatal("explicit resume rejected the owner's allowed recovery branch", err)
	}
	if f.probes != before || len(f.patches) != 1 || !f.entries["one"].Disabled {
		t.Fatal("resuming a rule must not test or enable its credential")
	}
	f.answers["a"] = "4"
	resumed := f.engine.rules[rule.ID]
	if rows, err := f.round(t, resumed); err != nil || len(rows) != 1 || !rows[0].Config.RecoveryCheck || rows[0].ActionStatus != "credential_restored" {
		t.Fatal("fresh complete recovery did not run after explicit resume", rows, err)
	}
	if len(f.patches) != 2 || !f.patches[0] || f.patches[1] || f.entries["one"].Disabled {
		t.Fatal("recovery did not use the existing owned status operation", f.patches)
	}
}

func TestCommercialRecoveryResumeDoesNotBorrowOrInventPermission(t *testing.T) {
	for _, state := range []string{"restore_off", "foreign_owner", "manual_disabled", "unknown_write", "retained_pause"} {
		t.Run(state, func(t *testing.T) {
			f := managementTestFixture(t)
			rule := f.rule(t, disableCredential, false, true)
			f.answers["a"] = "5"
			if _, err := f.round(t, rule); err != nil {
				t.Fatal(err)
			}
			if err := f.engine.PauseRule(rule.ID, rule.Revision, true); err != nil {
				t.Fatal(err)
			}
			stopped := f.engine.rules[rule.ID]
			switch state {
			case "restore_off":
				stopped.AutoRestore = false
			case "foreign_owner":
				f.engine.managed["one"].AnswerOwnerRuleID = "another-rule"
			case "manual_disabled":
				delete(f.engine.managed, "one")
			case "unknown_write":
				f.engine.managed["one"].State = "attention"
			case "retained_pause":
				f.engine.rules["retained"] = &Rule{ID: "retained", RunConfig: RunConfig{AuthID: "one"}, ActionID: "retained-independent-pause"}
			}
			before := f.probes
			if err := f.engine.PauseRule(stopped.ID, stopped.Revision, false); err == nil {
				t.Fatal("rule resume bypassed the current recovery boundary", state)
			}
			if f.engine.rules[rule.ID].Enabled || len(f.patches) != 1 || f.probes != before || !f.entries["one"].Disabled {
				t.Fatal("rejected resume changed rule permission or CPA state", state)
			}
		})
	}
}

func TestCommercialInstanceStopRevokesIndividualQueuedTimerWithoutChangingRule(t *testing.T) {
	en, host, rule := scopeFixture(t, scopedTarget("one", "a", "b"))
	rule, err := en.setRuleTimer(context.Background(), rule.ID, rule.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	en.Tick(rule.NextRun)
	if err = en.UpdateConfig(en.Config()); err != nil {
		t.Fatal(err)
	}
	for _, run := range en.runs {
		if run.Config.RuleID == rule.ID && (run.CancelRequested || run.Status != "queued") {
			t.Fatal("saving unchanged settings cancelled an authorized individual timer")
		}
	}
	if err = en.setScheduling(false, false); err != nil {
		t.Fatal(err)
	}
	if err = en.setScheduling(false, true); err != nil {
		t.Fatal(err)
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if host.executeCalls != 0 || en.rules[rule.ID].Revision != rule.Revision || !en.rules[rule.ID].Enabled {
		t.Fatal("instance stop left an old individual timer executable or rewrote the rule")
	}
}

func TestCommercialScheduleRevocationDuringDirectoryScanCannotBeReauthorized(t *testing.T) {
	en, host, rule, queued := commercialScheduledFixture(t)
	if err := en.Cancel(queued.ID); err != nil {
		t.Fatal(err)
	}
	receipt, err := en.dispatchWithInventory(context.Background(), rule.ID, rule.Revision, true, func(context.Context) ([]Target, error) {
		if err := en.setScheduling(true, false); err != nil {
			t.Fatal(err)
		}
		if err := en.setScheduling(false, true); err != nil {
			t.Fatal(err)
		}
		return host.targets, nil
	})
	if err == nil || receipt.Count != 0 || len(en.runs) != 1 {
		t.Fatal("revoked in-flight scan was admitted after scheduling restarted", receipt, err)
	}
	if host.executeCalls != 0 {
		t.Fatal("cancelled scan executed a model")
	}
}

type commercialInventoryHookHost struct {
	*observationHost
	once func()
}

func (h *commercialInventoryHookHost) Targets(ctx context.Context) ([]Target, error) {
	if hook := h.once; hook != nil {
		h.once = nil
		hook()
	}
	return h.observationHost.Targets(ctx)
}

func TestCommercialOneRevocationInvalidatesEveryCapturedDueRule(t *testing.T) {
	en, host, rule, queued := commercialScheduledFixture(t)
	if err := en.Cancel(queued.ID); err != nil {
		t.Fatal(err)
	}
	sibling := *cloneRule(rule)
	sibling.ID, sibling.Revision, sibling.Name = "", 0, "second due rule"
	second, err := en.SaveRule(context.Background(), sibling, false)
	if err != nil {
		t.Fatal(err)
	}
	// Both occurrences are captured before the shared directory lookup begins.
	en.rules[rule.ID].NextRun = second.NextRun
	en.host = &commercialInventoryHookHost{observationHost: en.host.(*observationHost), once: func() {
		if err := en.setScheduling(true, false); err != nil {
			t.Fatal(err)
		}
		if err := en.setScheduling(false, true); err != nil {
			t.Fatal(err)
		}
	}}
	en.Tick(second.NextRun)
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if len(en.runs) != 1 || host.executeCalls != 0 {
		t.Fatalf("captured occurrences survived permission revocation: runs=%d calls=%d", len(en.runs), host.executeCalls)
	}
}

func TestCommercialRevokedDirectoryFailureCannotOverwriteLastDispatch(t *testing.T) {
	en, _, rule, queued := commercialScheduledFixture(t)
	if err := en.Cancel(queued.ID); err != nil {
		t.Fatal(err)
	}
	previous := fingerprint(en.rules[rule.ID].LastScopeDispatch)
	_, err := en.dispatchWithInventory(context.Background(), rule.ID, rule.Revision, true, func(context.Context) ([]Target, error) {
		if err := en.setScheduling(true, false); err != nil {
			t.Fatal(err)
		}
		if err := en.setScheduling(false, true); err != nil {
			t.Fatal(err)
		}
		return nil, errors.New("synthetic directory failure after revocation")
	})
	if err == nil || fingerprint(en.rules[rule.ID].LastScopeDispatch) != previous {
		t.Fatal("revoked scan replaced the last dispatch receipt", err)
	}
}

func TestCommercialUnchangedSettingsDuringScanPreservePermission(t *testing.T) {
	en, host, rule, queued := commercialScheduledFixture(t)
	if err := en.Cancel(queued.ID); err != nil {
		t.Fatal(err)
	}
	receipt, err := en.dispatchWithInventory(context.Background(), rule.ID, rule.Revision, true, func(context.Context) ([]Target, error) {
		cfg := en.Config()
		cfg.RetentionDays++
		if err := en.UpdateConfig(cfg); err != nil {
			t.Fatal(err)
		}
		return host.targets, nil
	})
	if err != nil || receipt.Count != 1 {
		t.Fatal("unrelated settings revoked a valid scheduled scan", receipt, err)
	}
}

func TestCommercialManualScanSurvivesScheduleRevocation(t *testing.T) {
	en, host, rule, queued := commercialScheduledFixture(t)
	if err := en.Cancel(queued.ID); err != nil {
		t.Fatal(err)
	}
	receipt, err := en.dispatchWithInventory(context.Background(), rule.ID, rule.Revision, false, func(context.Context) ([]Target, error) {
		if err := en.setScheduling(true, false); err != nil {
			t.Fatal(err)
		}
		return host.targets, nil
	})
	if err != nil || receipt.Count != 1 {
		t.Fatal("schedule revocation cancelled an explicit manual scan", receipt, err)
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if host.executeCalls != 2 {
		t.Fatal("explicit manual scan did not execute its current plan", host.executeCalls)
	}
}
