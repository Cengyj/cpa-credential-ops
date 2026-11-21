package quality

import (
	"context"
	"testing"
	"time"
)

func assertErrorPolicySkip(t *testing.T, f *managementFixture, rule *Rule, model string) {
	t.Helper()
	count := f.probes
	writes := len(f.patches)
	records := len(f.engine.runs)
	reads := f.targetReads
	receipt, err := f.engine.dispatchRule(context.Background(), rule.ID, rule.Revision, false)
	if err != nil || receipt.Count != 0 || receipt.Skipped != 0 || len(receipt.SkipDetails) != 0 || receipt.RunID != "" || receipt.BatchID != "" {
		t.Fatal("terminal credential reentered the round", receipt, err)
	}
	d := f.engine.managed["one"]
	if errorPolicyDisabled(d).Code == "" || d.CatalogMissingModel != model || len(f.engine.runs) != records ||
		len(f.engine.queue) != 0 || f.targetReads != reads || f.probes != count || f.nativeCalls != 0 || len(f.patches) != writes || f.deletes != 0 {
		t.Fatal("terminal credential accessed or new evidence created", f.targetReads, f.probes, f.patches, f.deletes)
	}
}

func TestErrorPolicySuspensionsSkipEveryScope(t *testing.T) {
	for _, tc := range []struct {
		condition string
		status    int
		body      string
	}{
		{"unauthorized", 401, ""},
		{"payment_required", 402, ""},
		{"quota_exhausted", 429, "quota exhausted"},
		{"model_unsupported", 400, "model not supported"},
		{"model_unsupported", 404, "model not found"},
	} {
		t.Run(tc.condition, func(t *testing.T) {
			ctx := context.Background()
			f := managementTestFixture(t)
			owner := f.rule(t, disableCredential, false, true)
			owner.UpstreamErrorActions = map[string]string{tc.condition: disableCredential}
			owner.Samples = 2
			owner, err := f.engine.SaveRule(ctx, *owner, true)
			if err != nil {
				t.Fatal(err)
			}
			enabled := f.rule(t, "observe_only", true, false)
			fixed := f.rule(t, "observe_only", false, false)
			all := f.rule(t, disableCredential, true, true)
			all.TargetScope = scopeAllCredentials
			all, err = f.engine.SaveRule(ctx, *all, true)
			if err != nil {
				t.Fatal(err)
			}
			f.upstreamStatus, f.upstreamBody = tc.status, tc.body
			rows, err := f.round(t, owner)
			if err != nil || len(rows) != 1 || rows[0].ActionStatus != "credential_suspended" || rows[0].UpstreamCondition != tc.condition ||
				len(f.patches) != 1 || !f.patches[0] || f.probes != 1 || !f.entries["one"].Disabled {
				t.Fatal("first error did not suspend exactly once", rows, err, f.probes, f.patches)
			}
			f.upstreamStatus, f.upstreamBody = 200, ""
			owner.Name = "Updated owner"
			owner.Cron = "*/5 * * * *"
			owner, err = f.engine.saveRule(ctx, *owner, true, "start")
			if err != nil {
				t.Fatal("unchanged fixed target must allow config-only edits and timer activation", err)
			}
			for _, r := range []*Rule{owner, enabled, fixed, all} {
				assertErrorPolicySkip(t, f, r, "")
			}
			f.engine.Tick(f.engine.rules[owner.ID].NextRun)
			timer, err := f.engine.Run(f.engine.rules[owner.ID].LastRun)
			if err != nil || timer.ID != rows[0].ID || timer.ActionStatus != "credential_suspended" ||
				len(f.engine.runs) != 1 || len(f.engine.queue) != 0 || f.disabledProbes != 0 || f.probes != 1 || len(f.patches) != 1 {
				t.Fatal("timer replayed a probe or lost the reason", timer, err, f.probes, f.patches)
			}
			// Normal dispatch observes CPA enablement and retires the old source.
			// Fresh evidence can still trigger the current policy in a later round.
			entry := f.entries["one"]
			entry.Status, entry.Disabled = "active", false
			entry.UpdatedAt = entry.UpdatedAt.Add(time.Second)
			f.entries["one"] = entry
			f.changedContent = true
			for _, current := range []*Rule{owner, all} {
				rows, err = f.round(t, current)
				if err != nil || len(rows) != 1 || rows[0].Status != "completed" || rows[0].ActionStatus != "observe_only" || f.entries["one"].Disabled {
					t.Fatal("CPA enablement did not resume current rule selection", rows, err)
				}
			}
			if activeDisposition(f.engine.managed["one"]) || len(f.patches) != 1 {
				t.Fatal("enablement replayed the old error or retained old recovery authority")
			}
		})
	}
}

func TestCatalogSuspensionSkipsRecovery(t *testing.T) {
	ctx := context.Background()
	f, r, mode := catalogFixture(t, disableCredential, true)
	r.Action, r.AutoRestore, r.TargetScope = disableCredential, true, scopeAllCredentials
	r, err := f.engine.SaveRule(ctx, *r, true)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.engine.dispatchRule(ctx, r.ID, r.Revision, false)
	if err != nil || receipt.Count != 1 || len(receipt.SkipDetails) != 0 {
		t.Fatal(receipt, err)
	}
	task := f.engine.nextRunnableLocked()
	if task == nil {
		t.Fatal("missing catalog decision")
	}
	f.engine.executeTask(task)
	*mode = "registered"
	for range 2 {
		assertErrorPolicySkip(t, f, r, "b")
	}
	if len(f.patches) != 1 || !f.patches[0] || f.probes != 0 || !f.entries["one"].Disabled {
		t.Fatal("catalog disable must produce one write and no model calls", f.patches, f.probes)
	}
	cleared, err := f.engine.clearRuleHistory()
	if err != nil || cleared != (historyClearResult{Archived: 1}) {
		t.Fatal("confirmed catalog suspension should leave the history", cleared, err)
	}
	if again, err := f.engine.clearRuleHistory(); err != nil || again != (historyClearResult{}) {
		t.Fatal("already cleared rows must not count a second time", again, err)
	}
	if err := f.engine.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(f.engine.cfg, f.host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	f.engine = reopened
	if err := reopened.Prune(reopened.now().AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	binding := reopened.managed["one"]
	evidence := reopened.runs[task.id]
	if evidence == nil || !evidence.HistoryCleared || evidence.Config.CatalogMissingModel != "b" ||
		binding == nil || binding.RunID != task.id || binding.CatalogMissingModel != "b" {
		t.Fatal("clearing history lost the catalog evidence or binding", evidence, binding)
	}
	if _, err := reopened.Run(task.id); err == nil || len(reopened.Runs()) != 0 {
		t.Fatal("cleared evidence is still visible", err)
	}
	page, err := reopened.QueryRuns(map[string][]string{"source": {"scheduled_probe"}, "scope_batch_id": {receipt.BatchID}, "view": {"summary"}})
	if err != nil || page["total"] != 0 {
		t.Fatal("cleared evidence was returned by a batch query", page, err)
	}
	assertErrorPolicySkip(t, f, r, "b")
	if len(f.patches) != 1 || f.probes != 0 || !f.entries["one"].Disabled {
		t.Fatal("clearing history or reopening issued an extra call", f.patches, f.probes)
	}
}

func TestQualitySuspensionMeetsErrorDuringRecovery(t *testing.T) {
	ctx := context.Background()
	f := managementTestFixture(t)
	r := f.rule(t, disableCredential, true, true)
	r.UpstreamErrorActions = map[string]string{"unauthorized": disableCredential}
	r.Samples = 2
	r, err := f.engine.SaveRule(ctx, *r, true)
	if err != nil {
		t.Fatal(err)
	}
	f.answers["a"] = "5"
	rows, err := f.round(t, r)
	if err != nil || len(rows) != 1 || rows[0].ActionStatus != "credential_suspended" || len(rows[0].Samples) != 4 {
		t.Fatal(rows, err)
	}
	original := *f.engine.managed["one"]
	f.answers["a"], f.upstreamStatus = "4", 401
	rows, err = f.round(t, r)
	if err != nil || len(rows) != 1 || rows[0].ActionStatus != "credential_kept_disabled" || len(rows[0].Samples) != 1 {
		t.Fatal(rows, err)
	}
	d := f.engine.managed["one"]
	if d.ActionID != original.ActionID || d.RunID != original.RunID || !sameSnapshot(d.Snapshot, original.Snapshot) ||
		d.UpstreamCondition != "unauthorized" || d.CreatedAt != original.CreatedAt || f.probes != 5 || f.disabledProbes != 1 || len(f.patches) != 1 || !f.patches[0] {
		t.Fatal("reclassification lost the ownership or issued a second write", d, f.probes, f.patches)
	}
	f.upstreamStatus = 200
	assertErrorPolicySkip(t, f, r, "")

	// Recreate a pre-upgrade marker. The retained recovery run and frozen
	// disabled snapshot are the only historical proof of the changed source.
	legacy := *d
	legacy.UpstreamCondition = ""
	c := changes()
	c.disposition = &legacy
	if err := f.engine.commitLocked(c); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(f.engine.cfg, f.host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	f.engine = reopened
	got := f.engine.managed["one"]
	if got.UpstreamCondition != "unauthorized" || got.ActionID != original.ActionID || !sameSnapshot(got.Snapshot, original.Snapshot) {
		t.Fatal("lost historical recovery error", got)
	}
	assertErrorPolicySkip(t, f, r, "")
	if _, err := f.engine.clearRuleHistory(); err != nil {
		t.Fatal(err)
	}
	var persisted CredentialDisposition
	if err := reopened.store.Get("meta", dispositionPrefix+"one", &persisted); err != nil || persisted.UpstreamCondition != "unauthorized" {
		t.Fatal("source was not retained independently of the recovery run", err)
	}
	if len(f.patches) != 1 || f.deletes != 0 || f.probes != 5 || f.disabledProbes != 1 || !f.entries["one"].Disabled {
		t.Fatal("restart or pruning replayed an action", f.patches, f.probes)
	}
}

func TestQueuedRecoveryChecksErrorPolicyAgain(t *testing.T) {
	f := managementTestFixture(t)
	r := f.rule(t, disableCredential, true, true)
	f.answers["a"] = "5"
	rows, err := f.round(t, r)
	if err != nil || len(rows) != 1 || rows[0].ActionStatus != "credential_suspended" {
		t.Fatal(rows, err)
	}
	receipt, err := f.engine.dispatchRule(context.Background(), r.ID, r.Revision, false)
	if err != nil || receipt.Count != 1 {
		t.Fatal(receipt, err)
	}
	task := f.engine.nextRunnableLocked()
	if task == nil || !task.frozen.ManagedProbe.Disabled {
		t.Fatal("expected an admitted disabled recovery")
	}
	d := *f.engine.managed["one"]
	d.UpstreamCondition = "payment_required"
	c := changes()
	c.disposition = &d
	if err := f.engine.commitLocked(c); err != nil {
		t.Fatal(err)
	}
	f.engine.executeTask(task)
	row, err := f.engine.Run(task.id)
	if err != nil || row.Status != "blocked" || row.Config.AuthID != "one" ||
		len(row.Samples) != 0 || f.disabledProbes != 0 || f.probes != 2 {
		t.Fatal("pre-admitted work probed after the source changed", row, err, f.probes)
	}
	// A decision arriving with complete correct evidence still has to check
	// the current disposition immediately before issuing a restore.
	stale := cloneRun(rows[0])
	stale.ID, stale.Config.ManagedProbe = "late-decision", &d.Snapshot
	for i := range stale.Samples {
		stale.Samples[i].QualityVerdict = "correct"
	}
	if managedRoundVerdict(stale) != "correct" {
		t.Fatal("the fixture must exercise the otherwise eligible write path")
	}
	f.engine.mu.Lock()
	f.engine.decideLocked(stale)
	f.engine.mu.Unlock()
	row, err = f.engine.Run(stale.ID)
	if err != nil || row.ActionStatus != "credential_kept_disabled" || !f.entries["one"].Disabled || len(f.patches) != 1 || f.deletes != 0 {
		t.Fatal("a stale all-correct decision restored an error suspension", row, err, f.patches)
	}
}

func TestHistoricalSourceRecoveryRequiresLinkedEvidence(t *testing.T) {
	for _, mode := range []string{"original", "sample", "catalog", "unknown", "other_rule", "attention", "interrupted", "changed_snapshot", "older_recheck"} {
		t.Run(mode, func(t *testing.T) {
			f := managementTestFixture(t)
			at := f.engine.now()
			snapshot := CredentialSnapshot{AuthID: "one", AuthIndex: "index", Provider: "codex", Name: "one.json", Fingerprint: "fixture", UpdatedAt: at, Disabled: true}
			d := CredentialDisposition{AuthID: "one", RuleID: "owner", RuleRevision: 7, RunID: "source", ActionID: "owned", State: "disabled", Snapshot: snapshot, CreatedAt: at}
			snapshot.Disabled = false
			run := &Run{ID: "source", Source: "scheduled_probe", Status: "completed", CreatedAt: at.Add(time.Second), ActionStatus: "credential_suspended",
				Config: Frozen{RunConfig: RunConfig{AuthID: "one", Models: []string{"a"}, Samples: 1}, RuleID: d.RuleID, RuleRevision: d.RuleRevision, ManagedProbe: &snapshot,
					UpstreamErrorActions: map[string]string{"unauthorized": disableCredential, "model_unsupported": disableCredential}}, UpstreamCondition: "unauthorized"}
			wantCondition, wantModel := "", ""
			switch mode {
			case "original":
				wantCondition = "unauthorized"
			case "sample":
				run.UpstreamCondition = ""
				run.Samples = []Sample{{Model: "a", ExecutionStatus: "failed", QualityVerdict: "unknown", UpstreamCondition: "unauthorized",
					Evidence: Evidence{Path: "cpa_management_probe", AuthID: "one", RequestedModel: "a", PayloadFingerprint: "fixture"}}}
				wantCondition = "unauthorized"
			case "catalog":
				run.Status, run.UpstreamCondition, run.Config.CatalogMissingModel = "skipped", "", "absent"
				wantModel = "absent"
			case "unknown":
				run.UpstreamCondition = ""
				run.Error = "HTTP 401 Unauthorized"
			case "other_rule":
				run.Config.RuleID = "other"
			case "attention":
				d.State = "attention"
			case "interrupted":
				run.Status = "interrupted"
			case "changed_snapshot", "older_recheck":
				run.ID, run.ActionStatus = "historical-recheck", "credential_kept_disabled"
				snapshot.Disabled = true
				if mode == "changed_snapshot" {
					snapshot.UpdatedAt = at.Add(time.Minute)
				} else {
					run.CreatedAt = at.Add(-time.Second)
				}
			}
			c := changes()
			c.disposition, c.runs[run.ID] = &d, run
			if err := f.engine.commitLocked(c); err != nil {
				t.Fatal(err)
			}
			if err := f.engine.recoverDisabledSources(); err != nil {
				t.Fatal(err)
			}
			got := f.engine.managed["one"]
			if got.UpstreamCondition != wantCondition || got.CatalogMissingModel != wantModel || got.ActionID != d.ActionID ||
				len(f.patches)+f.deletes+f.probes+f.nativeCalls != 0 {
				t.Fatal("migration misclassified a disposition or called CPA", got)
			}
		})
	}
}
