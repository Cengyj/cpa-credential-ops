package quality

import (
	"context"
	"testing"
)

func TestCredentialRewriteKeepsUserIntentAndBlocksFreshAdmission(t *testing.T) {
	en, host, rule := semanticsEngine(t)
	r := en.rules[rule.ID]
	r.SchedulingEnabled = true
	en.timers = map[string]bool{r.ID: true}
	request := CredentialLoginPrepareRequest{AuthID: r.AuthID, ExpectedIdentity: "fixture-account", PreparationID: "job-1"}
	if _, err := en.PrepareCredentialLogin(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !r.Enabled || !r.SchedulingEnabled || !en.timerAllowed(r) {
		t.Error("preparing a credential write changed the user's rule or timer intent")
	}
	en.mu.Lock()
	err := en.checkTargetsLocked(r.RunConfig, host.targets)
	en.mu.Unlock()
	if err == nil {
		t.Error("a fresh evaluation can enter between preparation and save")
	}
}

func TestConfirmedCredentialRewriteUsesCurrentRulesAndFreshEvidence(t *testing.T) {
	f := managementTestFixture(t)
	en := f.engine
	first := f.rule(t, disableCredential, false, false)
	edited := f.rule(t, "observe_only", false, false)
	paused := f.rule(t, "observe_only", false, false)
	revoked := f.rule(t, "observe_only", false, false)
	deleted := f.rule(t, "observe_only", false, false)
	if err := en.PauseRule(paused.ID, paused.Revision, true); err != nil {
		t.Fatal(err)
	}
	r, err := en.setRuleTimer(t.Context(), first.ID, first.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = en.setRuleTimer(t.Context(), revoked.ID, revoked.Revision, true); err != nil {
		t.Fatal(err)
	}
	before, err := en.dispatchRule(t.Context(), first.ID, r.Revision, false)
	if err != nil || before.Count != 1 {
		t.Fatal(before, err)
	}
	queued := en.runs[before.RunID]
	if queued == nil {
		queued = en.runs[en.scopeBatches[before.BatchID][0]]
	}
	oldSnapshot := *queued.Config.ManagedProbe
	en.rules[first.ID].FailCount, en.rules[first.ID].EvidenceSignature = 3, "before-write"
	request := CredentialLoginPrepareRequest{AuthID: "one", ExpectedIdentity: "same-account", PreparationID: "commit-r1"}
	if _, err := en.PrepareCredentialLogin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if queued.Status != "cancelled" || queued.BlockReason != "credential_authorization_changed" {
		t.Fatal("old queued work survived preparation", queued.Status, queued.BlockReason)
	}
	if err := en.PauseRule(revoked.ID, en.rules[revoked.ID].Revision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := en.setRuleTimer(t.Context(), revoked.ID, en.rules[revoked.ID].Revision, false); err != nil {
		t.Fatal(err)
	}
	if err := en.DeleteRule(t.Context(), deleted.ID, en.rules[deleted.ID].Revision); err != nil {
		t.Fatal(err)
	}
	replacement := *cloneRule(en.rules[edited.ID])
	replacement.Models = []string{"b"}
	if _, err := en.SaveRule(t.Context(), replacement, true); err != nil {
		t.Fatal("user could not edit a rule during credential update", err)
	}
	f.changedContent = true
	if err := en.FinishCredentialLogin("one", "old-commit", "same-account", "committed"); err != nil {
		t.Fatal(err)
	}
	if en.loginWaitLocked(r.RunConfig) == nil {
		t.Fatal("stale callback cleared a newer credential hold")
	}
	if err := en.FinishCredentialLogin("one", request.PreparationID, "same-account", "committed"); err != nil {
		t.Fatal(err)
	}
	generation := en.loginGeneration
	if err := en.FinishCredentialLogin("one", request.PreparationID, "same-account", "committed"); err != nil || en.loginGeneration != generation {
		t.Fatal("duplicate success consumed the hold twice", err)
	}
	if en.rules[first.ID].FailCount != 0 || en.rules[first.ID].EvidenceSignature != "" || !en.timerAllowed(en.rules[first.ID]) ||
		en.rules[paused.ID].Enabled || en.rules[revoked.ID].Enabled || en.timerAllowed(en.rules[revoked.ID]) || en.rules[deleted.ID] != nil ||
		len(en.rules[edited.ID].Models) != 1 || en.rules[edited.ID].Models[0] != "b" || f.probes != 0 || f.nativeCalls != 0 || len(f.patches) != 0 {
		t.Fatal("success overrode newer user policy, preserved old evidence, or started a model/write")
	}
	runs, err := f.round(t, first)
	if err != nil || len(runs) != 1 || runs[0].ID == queued.ID || runs[0].Config.ManagedProbe == nil ||
		sameSnapshot(oldSnapshot, *runs[0].Config.ManagedProbe) || f.probes != 2 || len(f.patches) != 0 {
		t.Fatal("post-login evaluation did not use new evidence", err, runs)
	}
	t.Logf("queued old rounds cancelled=1 completion transitions=1 immediately issued probes=0 next round models=%d", f.probes)
}

func TestCredentialRewriteRecoveryNeverReplaysUnknownOrFailedOperations(t *testing.T) {
	for _, tc := range []struct{ state, identity, want string }{
		{"aborted", "", "failed"}, {"commit_unknown", "", "unknown"}, {"saved_not_adopted", "", "unknown"},
		{"committed", "different-account", "identity_conflict"}, {"committed", "same-account", "completed"},
	} {
		t.Run(tc.state+tc.identity, func(t *testing.T) {
			f := managementTestFixture(t)
			rule := f.rule(t, "observe_only", false, false)
			en := f.engine
			request := CredentialLoginPrepareRequest{AuthID: "one", ExpectedIdentity: "same-account", PreparationID: "real-receipt-id"}
			if _, err := en.PrepareCredentialLogin(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if len(en.PendingCredentialLogins()) != 0 {
				t.Fatal("read-only recovery tried to resolve a live writer")
			}
			if err := en.loadLoginHolds(); err != nil || en.PendingCredentialLogins()["one"] != request.PreparationID {
				t.Fatal("persistent prepare missing after restart", err)
			}
			if err := en.FinishCredentialLogin("one", request.PreparationID, tc.identity, tc.state); err != nil {
				t.Fatal(err)
			}
			if err := en.loadLoginHolds(); err != nil || len(en.PendingCredentialLogins()) != 0 || en.loginHolds["one"].State != tc.want {
				t.Fatal("second restart resurrected a finalized receipt", err)
			}
			if tc.want != "completed" && en.loginWaitLocked(rule.RunConfig) == nil {
				t.Fatal("an unconfirmed or conflicting credential became eligible")
			}
			if tc.want == "unknown" {
				request.PreparationID = "new-job"
				if _, err := en.PrepareCredentialLogin(t.Context(), request); err == nil {
					t.Fatal("new login bypassed an uncertain write")
				}
			}
			if !en.rules[rule.ID].Enabled || f.nativeCalls != 0 || f.probes != 0 || len(f.patches) != 0 {
				t.Fatal("receipt reconciliation replayed work or changed user intent")
			}
		})
	}
}

func TestLoginClearsOnlyConfirmedCurrentOrdinaryDisable(t *testing.T) {
	f := managementTestFixture(t)
	r := f.rule(t, disableCredential, false, false)
	f.answers["a"] = "5"
	if _, err := f.round(t, r); err != nil || !f.entries["one"].Disabled {
		t.Fatal("fixture did not suspend the credential", err)
	}
	req := CredentialLoginPrepareRequest{AuthID: "one", PreparationID: "relogin", ExpectedIdentity: "account"}
	if _, err := f.engine.PrepareCredentialLogin(t.Context(), req); err == nil {
		t.Fatal("a live suspension allowed account authorization")
	}
	entry := f.entries["one"]
	entry.Disabled, entry.Status = false, "active"
	f.entries["one"] = entry
	f.host.Management = nil
	if _, err := f.engine.PrepareCredentialLogin(t.Context(), req); err != nil {
		t.Fatal("account entry still blocked by stale ordinary disable after verified CPA enablement", err)
	}
	if f.engine.managed["one"].State != "externally_enabled" || len(f.patches) != 1 {
		t.Fatal("account path rewrote CPA or retained stale source")
	}
}

func TestNewEntryAtOldLocatorCannotInheritRulePermission(t *testing.T) {
	f := managementTestFixture(t)
	rule := f.rule(t, "observe_only", false, false)
	unrelated := f.rule(t, "observe_only", true, false)
	request := CredentialLoginPrepareRequest{AuthID: "one", PreparationID: "create-new-entry"}
	if _, err := f.engine.PrepareCredentialLogin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.FinishCredentialLogin("one", request.PreparationID, "new-account", "committed"); err != nil {
		t.Fatal(err)
	}
	if f.engine.rules[rule.ID].Enabled || !f.engine.rules[unrelated.ID].Enabled || f.engine.loginWaitLocked(rule.RunConfig) != nil {
		t.Fatal("replacement inherited the previous binding or blocked fresh admissions")
	}
}

func TestCredentialRewriteDuringDirectoryReadOnlyDefersAffectedMembers(t *testing.T) {
	f := managementTestFixture(t)
	f.add("two")
	rule := f.rule(t, "observe_only", true, false)
	ctx, en := t.Context(), f.engine
	request := CredentialLoginPrepareRequest{AuthID: "one", ExpectedIdentity: "account", PreparationID: "scope-read"}
	inventory := func(ctx context.Context) ([]Target, error) {
		previous, err := f.host.Targets(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := en.PrepareCredentialLogin(ctx, request); err != nil {
			return nil, err
		}
		return previous, en.FinishCredentialLogin(request.AuthID, request.PreparationID, request.ExpectedIdentity, "committed")
	}
	first, err := en.dispatchWithInventory(ctx, rule.ID, rule.Revision, false, inventory)
	if err != nil || first.Count != 1 || first.Skipped != 1 || len(first.SkipDetails) != 1 ||
		first.SkipDetails[0].AuthID != request.AuthID || first.SkipDetails[0].Code != "credential_update_pending" {
		t.Fatalf("old inventory crossed a credential write: %+v / %v", first, err)
	}
	en.mu.Lock()
	task := en.nextRunnableLocked()
	en.mu.Unlock()
	if task == nil || task.frozen.AuthID != "two" {
		t.Fatal("the unrelated directory member did not retain admission")
	}
	en.executeTask(task)
	next, err := en.dispatchRule(ctx, rule.ID, rule.Revision, false)
	if err != nil || next.Count != 2 {
		t.Fatal("fresh directory did not admit the updated member", next, err)
	}
}
