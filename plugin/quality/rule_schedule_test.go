package quality

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type scheduledDirectoryHost struct {
	*observationHost
	mu          sync.Mutex
	readErr     error
	failedReads int
}

func (h *scheduledDirectoryHost) Targets(context.Context) ([]Target, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.readErr != nil {
		h.failedReads++
		return nil, h.readErr
	}
	return slices.Clone(h.targets), nil
}

func TestBackgroundTimerRematchesAfterUnavailableAndEmptyDirectory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := &scheduledDirectoryHost{observationHost: &observationHost{&semanticsHost{targets: []Target{scopedTarget("first", "a")}}}}
		cfg := DefaultConfig()
		cfg.DataDir = t.TempDir()
		en, err := New(cfg, host)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := en.Close(); err != nil {
				t.Error(err)
			}
		}()
		rule, err := en.saveRule(context.Background(), Rule{Name: "Fresh inventory", TargetScope: scopeAll,
			ObjectTypes: []string{objectOAuth, objectProvider}, Cron: "* * * * *", RunConfig: RunConfig{QuestionID: "logic-boxes", Models: []string{"a"}, Samples: 1}}, false, "start")
		if err != nil {
			t.Fatal(err)
		}
		nextRound := func() Rule {
			t.Helper()
			due := en.Rules()[0].NextRun
			time.Sleep(time.Until(due) + time.Second)
			synctest.Wait()
			r := en.Rules()[0]
			if r.Revision != rule.Revision || !r.timerActive || !r.NextRun.After(due) || r.LastScopeDispatch == nil || r.LastScopeDispatch.At.Before(due) {
				t.Fatalf("scheduled occurrence was not accounted for: %+v", r)
			}
			return r
		}
		first := nextRound()
		if first.LastScopeBatch == "" || host.executeCalls != 1 {
			t.Fatal("first scheduled run did not reach its target", first, host.executeCalls)
		}
		unavailable := errors.New("synthetic private directory contents")
		host.mu.Lock()
		host.readErr = unavailable
		host.mu.Unlock()
		due := en.Rules()[0].NextRun
		time.Sleep(time.Until(due) + time.Second)
		synctest.Wait()
		failed := en.Rules()[0]
		if !failed.NextRun.Equal(due) || !failed.timerActive {
			t.Fatal("directory failure consumed the pending timer")
		}
		if failed.LastScopeDispatch.DirectoryUnavailable || failed.LastScopeBatch != first.LastScopeBatch || !strings.Contains(failed.timerWaitReason, "目录尚未读到") || len(en.Runs()) != 1 || host.executeCalls != 1 {
			t.Fatal("directory failure lost its receipt or changed an earlier run", failed.LastScopeDispatch)
		}
		var persisted Rule
		if err := en.store.Get("rules", rule.ID, &persisted); err != nil || persisted.LastScopeDispatch == nil || !persisted.LastScopeDispatch.At.Equal(first.LastScopeDispatch.At) || strings.Contains(failed.timerWaitReason, unavailable.Error()) {
			t.Fatal("directory outage replaced an admitted dispatch or exposed a raw error", persisted.LastScopeDispatch, err)
		}
		host.mu.Lock()
		host.readErr = nil
		host.targets[0].ManualDisabled = true
		failedReads := host.failedReads
		host.mu.Unlock()
		if failedReads != 1 {
			t.Fatalf("duplicate directory read before the first retry deadline: %d", failedReads)
		}
		time.Sleep(4 * time.Second)
		synctest.Wait()
		empty := en.Rules()[0]
		if !empty.NextRun.After(due) {
			t.Fatal("fresh empty directory did not complete the overdue cycle")
		}
		if empty.LastScopeDispatch.DirectoryUnavailable || empty.LastScopeDispatch.Matched != 0 || !strings.Contains(empty.LastScopeDispatch.Reason, "没有可纳入") || len(en.Runs()) != 1 {
			t.Fatal("unexpected evidence for empty scope", empty.LastScopeDispatch)
		}
		host.mu.Lock()
		host.targets = append(host.targets, scopedTarget("added-after-timer-start", "a"))
		host.mu.Unlock()
		last := nextRound()
		var found bool
		for _, run := range en.Runs() {
			if run.Config.ScopeBatchID == last.LastScopeBatch {
				found = run.Config.Scheduled && run.Config.AuthID == "added-after-timer-start" && run.Status == "completed"
			}
		}
		if !found || last.LastScopeDispatch.Matched != 1 || host.executeCalls != 2 || len(en.Runs()) != 2 {
			t.Fatal("new credential was not dispatched by the background timer", last.LastScopeDispatch, host.executeCalls)
		}
	})
}

func TestScheduledDirectoryFailureDoesNotOverwriteEditedRule(t *testing.T) {
	en, _, rule := scopeFixture(t)
	ctx := context.Background()
	rule, err := en.saveRule(ctx, *rule, true, "start")
	if err != nil {
		t.Fatal(err)
	}
	_, err = en.dispatchWithInventory(ctx, rule.ID, rule.Revision, true, func(context.Context) ([]Target, error) {
		edit := *rule
		edit.Name = "Updated during directory read"
		if _, err := en.SaveRule(ctx, edit, true); err != nil {
			t.Fatal(err)
		}
		return nil, errors.New("synthetic directory read error")
	})
	if err == nil || en.rules[rule.ID].LastScopeDispatch != nil || en.rules[rule.ID].Revision == rule.Revision {
		t.Fatal("an obsolete timer occurrence changed the edited rule", err)
	}
}

func TestRuleCronUsesServerWallClock(t *testing.T) {
	for _, tc := range []struct{ zone, expr, from, want string }{
		{"Asia/Shanghai", defaultRuleCron, "2026-11-19T00:17:42Z", "2026-11-19T00:30:00Z"},
		{"Asia/Shanghai", defaultRuleCron, "2026-11-19T00:30:00Z", "2026-11-19T01:00:00Z"},
		{"Asia/Kathmandu", defaultRuleCron, "2026-11-19T00:05:09Z", "2026-11-19T00:15:00Z"},
		{"Asia/Shanghai", "0 9 * * *", "2026-11-19T00:05:00Z", "2026-11-19T01:00:00Z"},
		{"America/New_York", "0 9 * * *", "2026-03-07T14:00:00Z", "2026-03-08T13:00:00Z"},
	} {
		loc, err := time.LoadLocation(tc.zone)
		if err != nil {
			t.Fatal(err)
		}
		from, err := time.Parse(time.RFC3339, tc.from)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ruleNextInstant(&Rule{Cron: tc.expr}, from, loc)
		if err != nil || got.Location() != time.UTC || got.Format(time.RFC3339) != tc.want {
			t.Fatalf("%s %s after %s: got %s, %v; want %s", tc.zone, tc.expr, tc.from, got, err, tc.want)
		}
	}
}

func TestRuleCronDefaultsValidationAndPermissions(t *testing.T) {
	en, h, _ := scopeFixture(t, scopedTarget("one", "a"))
	write := func(method string, body map[string]any, status int) ruleView {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: method, Path: "/rules", Body: raw})
		if resp.StatusCode != status {
			t.Fatalf("%s: got %d %s, wanted %d", method, resp.StatusCode, resp.Body, status)
		}
		var out ruleView
		if err = json.Unmarshal(resp.Body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	b := map[string]any{"enabled": true, "target_scope": scopeAll, "models": []string{"a"}, "question_id": "logic-boxes"}
	manual := write("POST", b, 200)
	if manual.Cron != defaultRuleCron || manual.Timezone != "" || manual.IntervalSeconds != 0 || manual.SchedulingEnabled || !manual.NextRun.IsZero() || manual.NextRunLocal != "" || en.cfg.SchedulingEnabled {
		t.Fatal("unexpected default plan or permissions", manual)
	}
	for _, bad := range []any{"", "  ", nil, 5, "30 2 *", "0 0 * * * *", "@daily", "TZ=UTC 0 9 * * *", "65 9 * * *", "*/0 * * * *", "0 9 31 2 *"} {
		b["cron"] = bad
		write("POST", b, 400)
	}
	b["cron"] = "0  9\t* * *"
	b["timezone"] = "Asia/Shanghai"
	write("POST", b, 400)
	delete(b, "timezone")
	b["interval_seconds"] = 60
	write("POST", b, 400)
	delete(b, "interval_seconds")
	b["id"], b["revision"], b["scheduling_enabled"] = manual.ID, manual.Revision, true
	custom := write("PUT", b, 200)
	if custom.Cron != "0 9 * * *" || custom.NextRun.IsZero() || custom.NextRunLocal == "" {
		t.Fatal("missing normalized schedule", custom)
	}
	delete(b, "cron")
	b["revision"], b["name"] = custom.Revision, "changed"
	b["next_run"], b["next_run_local"] = "1970-01-01T00:00:00Z", "ignored"
	en.now = func() time.Time { return custom.NextRun.Add(24 * time.Hour) }
	edit := write("PUT", b, 200)
	if edit.Cron != custom.Cron || !edit.NextRun.Equal(custom.NextRun) || edit.NextRunLocal != custom.NextRunLocal {
		t.Fatal("unrelated edit re-anchored the schedule", edit)
	}
	en.Tick(en.now())
	if len(en.queue) != 0 || h.executeCalls != 0 {
		t.Fatal("global scheduling permission bypassed")
	}
	b["revision"], b["scheduling_enabled"] = edit.Revision, false
	stopped := write("PUT", b, 200)
	if stopped.Cron != custom.Cron || !stopped.NextRun.IsZero() || stopped.NextRunLocal != "" {
		t.Fatal("stopping lost the schedule or retained a due time", stopped)
	}
	if err := en.setScheduling(false, true); err != nil {
		t.Fatal(err)
	}
	en.Tick(en.now())
	if len(en.queue) != 0 {
		t.Fatal("rule-level opt-out bypassed")
	}
	b["revision"], b["scheduling_enabled"] = stopped.Revision, true
	restarted := write("PUT", b, 200)
	en.Tick(restarted.NextRun.Add(-time.Second))
	if len(en.queue) != 0 || !restarted.NextRun.After(en.now()) {
		t.Fatal("early execution or stale due time")
	}
	en.Tick(restarted.NextRun)
	en.Tick(restarted.NextRun)
	if len(en.queue) != 1 || !en.rules[manual.ID].NextRun.After(restarted.NextRun) {
		t.Fatal("expected exactly one admission at the boundary")
	}
}

func TestLegacyRuleSchedulePreservedUntilConverted(t *testing.T) {
	for _, expr := range []string{"", "0 9 * * *"} {
		en, _, r := scopeFixture(t)
		// Seed a real pre-upgrade representation, including the inactive interval on a Cron rule.
		r.IntervalSeconds, r.Timezone, r.Cron, r.SchedulingEnabled = 750, "Asia/Kathmandu", expr, true
		r.NextRun = nextRun(*r, en.now())
		en.rules[r.ID] = cloneRule(r)
		anchor := r.NextRun
		if anchor.IsZero() || (expr == "" && anchor.Sub(en.now()) != 750*time.Second) || (expr != "" && viewRule(*r).NextRunLocal != "2026-11-14T09:00:00+05:45") {
			t.Fatal("legacy schedule semantics changed", r)
		}
		now := en.now().Add(2 * time.Minute)
		en.now = func() time.Time { return now }
		in := *r
		in.Name, in.IntervalSeconds, in.Timezone, in.Cron = "renamed", 0, "", ""
		saved, err := en.SaveRule(context.Background(), in, true)
		if err != nil || !sameRuleSchedule(saved, r) || !saved.NextRun.Equal(anchor) {
			t.Fatal("legacy plan changed on omission", saved, err)
		}
		in = *saved
		if saved, err = en.SaveRule(context.Background(), in, true); err != nil || !saved.NextRun.Equal(anchor) {
			t.Fatal("legacy round-trip", saved, err)
		}
		in = *saved
		in.IntervalSeconds++
		if _, err = en.SaveRule(context.Background(), in, true); err == nil {
			t.Fatal("accepted a legacy plan mutation")
		}
		in.IntervalSeconds, in.Timezone = 0, ""
		if expr == "" {
			in.Cron = defaultRuleCron
		}
		converted, err := en.SaveRule(context.Background(), in, true)
		if err != nil || converted.IntervalSeconds != 0 || converted.Timezone != "" || converted.Cron != in.Cron || !converted.NextRun.Equal(nextRun(in, now)) {
			t.Fatal("explicit conversion failed", converted, err)
		}
		var stored Rule
		if err = en.store.Get("rules", in.ID, &stored); err != nil || !sameRuleSchedule(&stored, converted) || !stored.NextRun.Equal(converted.NextRun) {
			t.Fatal("converted plan not persisted", err)
		}
	}
}

func TestInvalidStoredCronDoesNotFallBackOrDispatch(t *testing.T) {
	en, _, r := scopeFixture(t, scopedTarget("one", "a"))
	r.Cron, r.IntervalSeconds, r.SchedulingEnabled, r.NextRun = "75 * * * *", 3600, true, en.now()
	en.rules[r.ID] = r
	en.cfg.SchedulingEnabled = true
	en.Tick(en.now())
	if len(en.queue) != 0 || !en.rules[r.ID].NextRun.IsZero() {
		t.Fatal("invalid schedule admitted or rearmed")
	}
}
