package quality

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestOverdueManagedTimerWaitsForBackendWithoutLosingOccurrence(t *testing.T) {
	f := managementTestFixture(t)
	en := f.engine
	r := f.rule(t, disableCredential, true, true)
	r, err := en.setRuleTimer(context.Background(), r.ID, r.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	offline := &NativeHost{Call: f.host.Call}
	offline.Management, err = NewManagementClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	en.host = offline
	now := r.NextRun.Add(5*time.Hour + 3*time.Minute)
	en.now = func() time.Time { return now }
	for range 4 {
		en.Tick(now)
	}
	if !en.rules[r.ID].NextRun.Equal(r.NextRun) || len(en.runs) != 0 || f.probes != 0 {
		t.Fatalf("management outage consumed a pending timer: due=%v runs=%d probes=%d", en.rules[r.ID].NextRun, len(en.runs), f.probes)
	}
	// A newly verified backend and the current directory arrive without a browser read.
	f.add("added-during-outage")
	en.host = f.host
	for range 4 {
		en.Tick(now)
	}
	if len(en.runs) != 2 || !en.rules[r.ID].NextRun.Equal(nextRun(*r, now)) || len(en.queue) != 1 {
		t.Fatalf("expected one fresh two-child batch: runs=%d queue=%d next=%v", len(en.runs), len(en.queue), en.rules[r.ID].NextRun)
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	en.Tick(now)
	if f.probes != 4 || len(en.runs) != 2 || len(en.queue) != 0 {
		t.Fatalf("a consumed timer was repeated: runs=%d queue=%d probes=%d", len(en.runs), len(en.queue), f.probes)
	}
	audits, err := en.History()
	if err != nil {
		t.Fatal(err)
	}
	catchups := 0
	for _, event := range audits {
		if event.Kind == "rule_timer_catchup" && event.RuleID == r.ID {
			catchups++
		}
	}
	if catchups != 1 {
		t.Fatalf("expected one durable catch-up receipt, got %d", catchups)
	}
	t.Logf("outage=5h03m, recovered batches=1, fresh children=%d, model probes=%d", len(en.runs), f.probes)
}

func TestPendingScheduleRespectsChangesAndCoincidentCron(t *testing.T) {
	for _, change := range []string{"pause", "stop", "delete", "edit", "normal_due"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			en, h, rule := scopeFixture(t, scopedTarget("file", "a", "b"), policyProvider())
			rule.ObjectTypes = []string{objectOAuth, objectProvider}
			rule, err := en.saveRule(ctx, *rule, true, "start")
			if err != nil {
				t.Fatal(err)
			}
			healthy := en.host
			en.host = partialProviderDirectory{healthy}
			now := rule.NextRun
			en.now = func() time.Time { return now }
			en.Tick(now)
			en.host = healthy
			en.Tick(now)
			if len(en.runs) != 1 || en.rules[rule.ID].PendingSchedule == nil {
				t.Fatal("recovery started a second round before the accepted one finished")
			}
			for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
				en.executeTask(task)
			}
			switch change {
			case "pause":
				err = en.PauseRule(rule.ID, rule.Revision, true)
			case "stop":
				_, err = en.setRuleTimer(ctx, rule.ID, rule.Revision, false)
			case "delete":
				err = en.DeleteRule(ctx, rule.ID, rule.Revision)
			case "edit":
				rule.ObjectTypes = []string{objectOAuth}
				_, err = en.SaveRule(ctx, *rule, true)
			case "normal_due":
				now = en.rules[rule.ID].NextRun
			}
			if err != nil {
				t.Fatal(err)
			}
			en.Tick(now)
			en.Tick(now)
			wantCalls := 2
			if change == "normal_due" {
				wantCalls = 6
			}
			for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
				en.executeTask(task)
			}
			if h.executeCalls != wantCalls {
				t.Fatalf("stale or duplicate admission after %s: calls=%d want=%d", change, h.executeCalls, wantCalls)
			}
		})
	}
}

func TestPartialScheduledScopeDoesNotRepeatAcceptedSDKCallsAfterRestart(t *testing.T) {
	ctx := context.Background()
	file := scopedTarget("file", "a", "b")
	provider := policyProvider()
	en, h, rule := scopeFixture(t, file, provider)
	base := en.host
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	rule.ObjectTypes = []string{objectOAuth, objectProvider}
	rule, err := en.saveRule(ctx, *rule, true, "start")
	if err != nil {
		t.Fatal(err)
	}
	now := rule.NextRun
	en.now = func() time.Time { return now }
	en.host = partialProviderDirectory{base}
	en.Tick(now)
	if p := en.rules[rule.ID].PendingSchedule; p == nil || !p.Due.Equal(now) || len(p.Accepted) != 1 || p.Accepted[0] != file.AuthID || len(en.runs) != 1 {
		t.Fatal("independent SDK admission and the pending directory were not checkpointed together", p)
	}
	// No pre-restart run may be sent, including a durable but unclaimed file test.
	en.rules, en.runs, en.queue = map[string]*Rule{}, map[string]*Run{}, make(chan string, en.cfg.Queue)
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	en.host = base
	h.targets = append(h.targets, scopedTarget("new-file", "a", "b"))
	en.Tick(now)
	en.Tick(now)
	var interrupted, queued int
	for _, run := range en.runs {
		switch run.Status {
		case "interrupted":
			interrupted++
			if run.Config.AuthID != file.AuthID {
				t.Fatal("wrong old admission")
			}
		case "queued":
			queued++
			if run.Config.AuthID == file.AuthID {
				t.Fatal("old SDK admission was replayed")
			}
		}
	}
	if interrupted != 1 || queued != 2 || en.rules[rule.ID].PendingSchedule != nil || h.executeCalls != 0 {
		t.Fatalf("expected one retained interruption, current provider and newly added file: interrupted=%d queued=%d", interrupted, queued)
	}
}

func TestSDKOnlyTimerRunsDuringManagementOutage(t *testing.T) {
	f := managementTestFixture(t)
	r := f.rule(t, "observe_only", false, false)
	r, err := f.engine.setRuleTimer(context.Background(), r.ID, r.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	offline, err := NewManagementClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	f.host.Management = offline
	f.engine.Tick(r.NextRun)
	for task := f.engine.nextRunnableLocked(); task != nil; task = f.engine.nextRunnableLocked() {
		f.engine.executeTask(task)
	}
	if f.nativeCalls != 2 || f.probes != 0 || len(f.engine.runs) != 1 {
		t.Fatalf("ordinary OAuth observation incorrectly needed managed HTTP: sdk calls=%d probes=%d runs=%d", f.nativeCalls, f.probes, len(f.engine.runs))
	}
}

func TestRotatedBackendKeyAndRestartKeepAnswerRecoveryOwner(t *testing.T) {
	f := managementTestFixture(t)
	en := f.engine
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	r := f.rule(t, disableCredential, true, true)
	r, err := en.setRuleTimer(context.Background(), r.ID, r.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	f.answers["a"] = "5"
	if runs, err := f.round(t, r); err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_suspended" {
		t.Fatal("could not establish answer-handling ownership", err)
	}
	action := en.managed["one"].ActionID
	base, volume := f.host.Management.base, en.cfg.DataDir
	const rotated = "new-synthetic-panel-key"
	f.mu.Lock()
	f.managementKey = rotated
	f.mu.Unlock()
	m, err := NewManagementClient(base)
	if err != nil {
		t.Fatal(err)
	}
	m.Start(volume, "")
	if _, err := editConnection(t, m, http.MethodPut, rotated, "configure", m.snapshot().Revision); err != nil {
		m.Close()
		t.Fatal(err)
	}
	m.Close()
	f.host.Management, err = NewManagementClient(base)
	if err != nil {
		t.Fatal(err)
	}
	f.host.Management.Start(volume, "")
	defer f.host.Management.Close()
	for until := time.Now().Add(3 * time.Second); !f.host.Management.ready() && time.Now().Before(until); {
		time.Sleep(time.Millisecond)
	}
	en.rules, en.runs, en.timers = map[string]*Rule{}, map[string]*Run{}, nil
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	owner := en.managed["one"]
	if !f.host.Management.ready() || owner.ActionID != action || answerOwner(owner) != r.ID || !en.timerAllowed(en.rules[r.ID]) {
		t.Fatal("rotating the panel key or reloading the volume changed recovery authority")
	}
	f.answers["a"] = "4"
	en.Tick(en.rules[r.ID].NextRun)
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if f.disabledProbes != 2 || len(f.patches) != 2 || !f.patches[0] || f.patches[1] || f.entries["one"].Disabled ||
		en.managed["one"].ActionID != action || en.managed["one"].State != "restored" {
		t.Fatalf("reloaded owner did not verify both new samples before its sole recovery write: disabled probes=%d writes=%v", f.disabledProbes, f.patches)
	}
}

func TestQualityConfigChangePreservesIndependentTimerAndPanelSettings(t *testing.T) {
	en, _, r := scopeFixture(t, scopedTarget("one", "a", "b"))
	hostConfig := en.cfg
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	r, err := en.setRuleTimer(context.Background(), r.ID, r.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	saved := en.Config()
	saved.DailyTokens = 5800
	if err := en.UpdateConfig(saved); err != nil {
		t.Fatal(err)
	}
	hostConfig.RetentionDays++
	en.cfg = hostConfig
	en.timers = nil
	if err = en.load(); err != nil {
		t.Fatal(err)
	}
	if !en.timerAllowed(en.rules[r.ID]) || en.cfg.SchedulingEnabled || en.cfg.DailyTokens != 5800 || en.cfg.RetentionDays != hostConfig.RetentionDays {
		t.Fatalf("unrelated host edit changed panel intent: timer=%v global=%v tokens=%d retention=%d", en.timers[r.ID], en.cfg.SchedulingEnabled, en.cfg.DailyTokens, en.cfg.RetentionDays)
	}
}

func TestColdGlobalEnableOnlyCatchesUpPreviouslyAuthorizedTimers(t *testing.T) {
	en, _, dormant := scopeFixture(t, scopedTarget("one", "a", "b"))
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	dormant.SchedulingEnabled = true
	dormant, err := en.SaveRule(context.Background(), *dormant, true)
	if err != nil {
		t.Fatal(err)
	}
	active, err := en.saveRule(context.Background(), Rule{Name: "individual grant", Enabled: true, TargetScope: scopeAll,
		RunConfig: dormant.RunConfig}, false, "start")
	if err != nil {
		t.Fatal(err)
	}
	now := dormant.NextRun.Add(3 * time.Hour)
	en.now = func() time.Time { return now }
	en.cfg.SchedulingEnabled = true
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	en.Tick(now)
	if len(en.runs) != 1 || !en.rules[dormant.ID].NextRun.After(now) {
		t.Fatalf("cold host reconfiguration caught up a period without permission: runs=%d", len(en.runs))
	}
	for _, run := range en.runs {
		if run.Config.RuleID != active.ID {
			t.Fatal("wrong timer caught up", run.Config.RuleID)
		}
	}
}

func TestProviderTimerWaitsAcrossRestartsWithoutConsumingExistingRun(t *testing.T) {
	en, rule, host, _, _, _ := providerDiagnosticFixture(t)
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	rule, err := en.setRuleTimer(context.Background(), rule.ID, rule.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	now := rule.NextRun.Add(3*time.Hour + time.Minute)
	en.now = func() time.Time { return now }
	en.Tick(now)
	en.timers, en.rules = nil, map[string]*Rule{}
	if err = en.load(); err != nil {
		t.Fatal(err)
	}
	host.Management.authorize(http.Header{"Authorization": {"Bearer offline-panel"}})
	en.Tick(now)
	if len(en.runs) != 1 || len(en.queue) != 1 || !en.rules[rule.ID].NextRun.Equal(nextRun(*rule, now)) {
		t.Fatalf("restart lost the overdue occurrence: runs=%d queue=%d next=%v", len(en.runs), len(en.queue), en.rules[rule.ID].NextRun)
	}
	for id, run := range en.runs {
		if run.Status != "queued" || !run.Config.Scheduled {
			t.Fatalf("no fresh scheduled run after recovery: %s", run.Status)
		}
		// Simulate process interruption after durable admission but before a worker claim.
		en.timers, en.rules, en.runs = nil, map[string]*Rule{}, map[string]*Run{}
		en.queue = make(chan string, en.cfg.Queue)
		if err := en.load(); err != nil {
			t.Fatal(err)
		}
		en.Tick(now)
		if len(en.runs) != 1 || en.runs[id].Status != "interrupted" || len(en.queue) != 0 {
			t.Fatal("an admitted operation was replayed after restart")
		}
	}
}
