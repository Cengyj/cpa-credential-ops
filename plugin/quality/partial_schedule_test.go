package quality

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPartialTimerWaitsForEachCredentialLogin(t *testing.T) {
	f := managementTestFixture(t)
	en, ctx := f.engine, t.Context()
	f.add("two")
	f.add("three")
	r := f.rule(t, disableCredential, true, false)
	r, err := en.setRuleTimer(ctx, r.ID, r.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	now := r.NextRun
	en.now = func() time.Time { return now }
	for _, id := range []string{"one", "three"} {
		_, err := en.PrepareCredentialLogin(ctx, CredentialLoginPrepareRequest{AuthID: id, PreparationID: "rewrite-" + id, ExpectedIdentity: "same-" + id})
		if err != nil {
			t.Fatal(err)
		}
	}
	checkRuns := func(want ...string) {
		t.Helper()
		got := map[string]int{}
		for _, run := range en.runs {
			if run.Config.RuleID == r.ID {
				got[run.Config.AuthID]++
			}
		}
		if len(got) != len(want) {
			t.Fatalf("admissions=%v want=%v", got, want)
		}
		for _, id := range want {
			if got[id] != 1 {
				t.Fatalf("duplicated or omitted member %v want %v", got, want)
			}
		}
	}
	en.Tick(now)
	checkRuns("two")
	if en.rules[r.ID].PendingSchedule == nil {
		t.Fatal("login wait was consumed as a completed timer")
	}
	// Restore the durable ledger with the first batch still unclaimed.
	en.rules, en.runs, en.queue = map[string]*Rule{}, map[string]*Run{}, make(chan string, en.cfg.Queue)
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	if err := en.FinishCredentialLogin("one", "old-receipt", "same-one", "committed"); err != nil {
		t.Fatal(err)
	}
	en.Tick(now)
	checkRuns("two")
	if err := en.FinishCredentialLogin("one", "rewrite-one", "same-one", "committed"); err != nil {
		t.Fatal(err)
	}
	en.Tick(now)
	checkRuns("one", "two")
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if err := en.FinishCredentialLogin("three", "rewrite-three", "different", "committed"); err != nil {
		t.Fatal(err)
	}
	en.Tick(now.Add(10 * time.Second))
	checkRuns("one", "two")
	if en.rules[r.ID].PendingSchedule == nil {
		t.Fatal("identity conflict lost waiting state")
	}
	if err := en.PauseRule(r.ID, r.Revision, true); err != nil {
		t.Fatal(err)
	}
	en.Tick(now.Add(20 * time.Second))
	checkRuns("one", "two")
	if len(f.patches) != 0 || f.probes != 2 {
		t.Fatal("replayed a model or issued an external mutation", f.probes, f.patches)
	}
}

func TestPartialManualCommandAndTimerShareLoginRemainder(t *testing.T) {
	f := managementTestFixture(t)
	en, ctx := f.engine, t.Context()
	f.add("two")
	r := f.rule(t, disableCredential, true, false)
	r, err := en.setRuleTimer(ctx, r.ID, r.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	now := r.NextRun.Add(4 * time.Hour)
	en.now = func() time.Time { return now }
	if _, err := en.PrepareCredentialLogin(ctx, CredentialLoginPrepareRequest{"one", "same-one", "rewrite-one"}); err != nil {
		t.Fatal(err)
	}
	command := func(key string) *ruleDispatch {
		t.Helper()
		if _, err := en.acceptRuleRun(ctx, key, r.ID, r.Revision); err != nil {
			t.Fatal(err)
		}
		en.wg.Wait()
		receipt, err := en.ruleCommandStatus(key)
		if err != nil || receipt.State != "completed" || receipt.Result == nil {
			t.Fatal("missing terminal command", receipt, err)
		}
		return receipt.Result
	}
	b := command("partial-first-file")
	if b.Count != 1 || b.Skipped != 1 || b.SkipDetails[0].RunID != "" || b.SkipDetails[0].Code != "credential_update_pending" {
		t.Fatal("unexpected admission or external attempt", b)
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	next := en.rules[r.ID].NextRun
	if _, err := en.clearRuleHistory(); err != nil {
		t.Fatal(err)
	}
	f.add("new-file")
	en.Tick(now)
	if len(en.runs) != 0 || f.probes != 2 {
		t.Fatal("replayed a cleared child", len(en.runs), f.probes)
	}
	if err := en.FinishCredentialLogin("one", "rewrite-one", "same-one", "committed"); err != nil {
		t.Fatal(err)
	}
	a := command("partial-recovered-file")
	en.Tick(now)
	if a.Count != 1 || len(en.runs) != 1 || en.rules[r.ID].PendingSchedule != nil || !en.rules[r.ID].NextRun.Equal(next) {
		t.Fatal("manual remainder consumed wrong scope or cursor", a, len(en.runs))
	}
	for _, run := range en.runs {
		if run.Config.AuthID != "one" {
			t.Fatal("login-only continuation expanded a verified scope", run.Config.AuthID)
		}
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if duplicate := command("partial-recovered-file"); duplicate.BatchID != a.BatchID || len(en.runs) != 1 || f.probes != 4 {
		t.Fatal("duplicate command repeated a model call", duplicate, f.probes)
	}
}

func TestPartialAllLoginsWaitingSurviveUnknownAndTimerRevocation(t *testing.T) {
	f := managementTestFixture(t)
	en, ctx := f.engine, t.Context()
	f.add("two")
	r := f.rule(t, "observe_only", true, false)
	r, err := en.setRuleTimer(ctx, r.ID, r.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	now := r.NextRun
	en.now = func() time.Time { return now }
	for _, id := range []string{"one", "two"} {
		if _, err := en.PrepareCredentialLogin(ctx, CredentialLoginPrepareRequest{id, "same-" + id, "rewrite-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	en.Tick(now)
	p := en.rules[r.ID].PendingSchedule
	if p == nil || len(p.Accepted) != 0 || len(p.WaitingLogins) != 2 || len(en.runs) != 0 || !strings.Contains(en.Rules()[0].timerWaitReason, "回执") {
		t.Fatal("all-waiting occurrence lost its diagnostic or created dispatch history", p)
	}
	if err := en.FinishCredentialLogin("one", "rewrite-one", "", "unknown"); err != nil {
		t.Fatal(err)
	}
	if reason := en.Rules()[0].timerWaitReason; !strings.Contains(reason, "写入结果待核对") {
		t.Fatal("timer hid an uncertain write", reason)
	}
	reads := f.targetReads
	for second := 1; second <= 30; second++ {
		en.Tick(now.Add(time.Duration(second) * time.Second))
	}
	if f.targetReads != reads || f.nativeCalls != 0 || f.probes != 0 {
		t.Fatal("quarantined subjects caused background polling or a model execution", f.targetReads-reads)
	}
	if _, err := en.setRuleTimer(ctx, r.ID, r.Revision, false); err != nil {
		t.Fatal(err)
	}
	if err := en.FinishCredentialLogin("two", "rewrite-two", "same-two", "committed"); err != nil {
		t.Fatal(err)
	}
	en.rules, en.runs = map[string]*Rule{}, map[string]*Run{}
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	en.Tick(now.Add(2 * time.Hour))
	if en.rules[r.ID].PendingSchedule != nil || en.loginHolds["one"].State != "unknown" || len(en.runs) != 0 || f.targetReads != reads {
		t.Fatal("revoked timer or unknown write resumed after restart")
	}
}

type partialManagedDirectory struct {
	*NativeHost
	verified bool
}

func (h *partialManagedDirectory) Targets(ctx context.Context) ([]Target, error) {
	t, err := h.NativeHost.Targets(ctx)
	if coverage, ok := ctx.Value(providerInventoryStatusKey{}).(*providerInventoryStatus); ok {
		coverage.Verified = h.verified
	}
	return t, err
}

func TestPartialManagedTimerRunsVerifiedFilesDuringProviderOutage(t *testing.T) {
	f := managementTestFixture(t)
	ctx, en := t.Context(), f.engine
	r := f.rule(t, disableCredential, true, false)
	r.ObjectTypes = []string{objectOAuth, objectProvider}
	r, err := en.saveRule(ctx, *r, true, "start")
	if err != nil {
		t.Fatal(err)
	}
	h := &partialManagedDirectory{f.host, false}
	en.host = h
	now := r.NextRun
	en.now = func() time.Time { return now }
	en.Tick(now)
	en.Tick(now)
	if len(en.runs) != 1 || en.rules[r.ID].PendingSchedule == nil {
		t.Fatalf("working OAuth management branch not admitted: runs=%d pending=%+v", len(en.runs), en.rules[r.ID].PendingSchedule)
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	h.verified = true
	en.Tick(now.Add(10 * time.Second))
	if len(en.runs) != 1 || en.rules[r.ID].PendingSchedule != nil || f.probes != 2 || len(f.patches) != 0 {
		t.Fatal("provider recovery repeated the accepted file or failed to recheck", len(en.runs), f.probes)
	}
}

type mixedScheduleHost struct {
	*providerPolicyHost
	verified bool
}

func (h *mixedScheduleHost) Targets(ctx context.Context) ([]Target, error) {
	targets, err := h.providerPolicyHost.Targets(ctx)
	if coverage, ok := ctx.Value(providerInventoryStatusKey{}).(*providerInventoryStatus); ok {
		coverage.Verified = h.verified
	}
	return targets, err
}

func TestPartialMixedRuleReleasesEachBranchOnce(t *testing.T) {
	f := managementTestFixture(t)
	en, ctx := f.engine, t.Context()
	f.add("two")
	h := &mixedScheduleHost{providerPolicyHost: &providerPolicyHost{Host: f.host, managedHost: f.host, native: f.host, provider: policyProvider()}, verified: true}
	en.host = h
	r := f.rule(t, deleteCredential, true, false)
	r.ObjectTypes = []string{objectOAuth, objectProvider}
	r, err := en.saveRule(ctx, *r, true, "start")
	if err != nil {
		t.Fatal(err)
	}
	now := r.NextRun
	en.now = func() time.Time { return now }
	if _, err := en.PrepareCredentialLogin(ctx, CredentialLoginPrepareRequest{"one", "same-one", "rewrite-one"}); err != nil {
		t.Fatal(err)
	}
	h.verified = false
	tick := func(want string) {
		t.Helper()
		en.Tick(now)
		en.Tick(now)
		task := en.nextRunnableLocked()
		if task == nil || en.runs[task.id].Config.AuthID != want {
			t.Fatal("missing recovered branch", want)
		}
		en.executeTask(task)
		if extra := en.nextRunnableLocked(); extra != nil {
			t.Fatal("repeated or premature admission", en.runs[extra.id].Config.AuthID)
		}
	}
	tick("two")
	if err := en.FinishCredentialLogin("one", "rewrite-one", "same-one", "committed"); err != nil {
		t.Fatal(err)
	}
	tick("one")
	h.verified = true
	tick(h.provider.AuthID)
	en.Tick(now.Add(time.Second))
	if en.rules[r.ID].PendingSchedule != nil || len(en.runs) != 3 || f.probes != 4 || len(h.requests) != 2 || f.deletes != 0 || len(f.patches) != 0 {
		t.Fatal("incorrect mixed occurrence", len(en.runs), f.probes, len(h.requests), f.deletes, len(f.patches))
	}
}
