package quality

import (
	"context"
	"testing"
)

func retainedProviderGroup(t *testing.T) (*Engine, *groupPolicyFixture, *Rule) {
	t.Helper()
	en, base, initial := scopeFixture(t)
	h := &groupPolicyFixture{Host: base, snapshots: map[string]CredentialSnapshot{}}
	en.host = h
	en.managed = make(map[string]*CredentialDisposition)
	rule := cloneRule(initial)
	rule.TargetScope, rule.Models, rule.Samples = scopeAllCredentials, []string{"alias"}, 1
	rule.LegacyProviderObservation, rule.ObjectTypes = false, []string{objectProvider}
	rule.Action, rule.AutoRestore = disableCredential, true
	rule.AuthorizedGroups = []string{"group"}
	en.rules[rule.ID] = rule
	for _, id := range []string{"a", "b"} {
		snapshot := CredentialSnapshot{AuthID: id, AuthIndex: "ix-" + id, Name: "group", Provider: "executor", ProviderGroupID: "group", ProviderMechanism: "group_disabled", ProviderMembers: []string{"a", "b"}, Disabled: true, Fingerprint: "old-config"}
		h.snapshots[id] = snapshot
		en.managed[id] = &CredentialDisposition{AuthID: id, RuleID: rule.ID, RuleRevision: rule.Revision, AnswerOwnerRuleID: rule.ID,
			State: "disabled", Operation: disableCredential, ActionID: "original-answer-action", Snapshot: snapshot, Models: []string{"alias"}}
	}
	return en, h, rule
}

func providerGroupRun(en *Engine, rule *Rule, snapshot CredentialSnapshot, verdict, batch string) *Run {
	cfg := Frozen{RunConfig: RunConfig{AuthID: snapshot.AuthID, Provider: snapshot.Provider, Models: []string{"alias"}, Samples: 1, Judge: Judge{Mode: "deterministic"}},
		ManagedProbe: &snapshot, RuleID: rule.ID, RuleRevision: rule.Revision, ScopeBatchID: batch, UpstreamErrorActions: rule.UpstreamErrorActions}
	run := &Run{ID: ID(), Config: cfg, Source: "scheduled_probe", Status: "completed", Samples: []Sample{{Model: "alias", Index: 0, ExecutionStatus: "completed", QualityVerdict: verdict,
		Evidence: Evidence{Path: "provider_config_probe", AuthID: snapshot.AuthID, RequestedModel: "alias", ResolvedModel: "wire", PayloadFingerprint: "frozen-request"}}}}
	en.runs[run.ID] = run
	return run
}

func decideProviderTestRun(en *Engine, rule *Rule, run *Run) {
	updates := changes()
	updates.runs[run.ID] = cloneRun(run)
	en.mu.Lock()
	en.decideManagedLocked(run, rule, updates)
	en.mu.Unlock()
}

func TestProviderRecoveryUsesNewCompleteGroupSnapshot(t *testing.T) {
	for _, scenario := range []string{"current-config", "missing-result", "unknown-result", "changed-during-round", "member-added"} {
		t.Run(scenario, func(t *testing.T) {
			en, host, rule := retainedProviderGroup(t)
			for id, snapshot := range host.snapshots {
				snapshot.Fingerprint = "current-config"
				host.snapshots[id] = snapshot
			}
			first := providerGroupRun(en, rule, host.snapshots["a"], "correct", "fresh")
			if scenario != "missing-result" {
				verdict := "correct"
				if scenario == "unknown-result" {
					verdict = "unknown"
				}
				providerGroupRun(en, rule, host.snapshots["b"], verdict, "fresh")
			}
			if scenario == "changed-during-round" || scenario == "member-added" {
				for id, snapshot := range host.snapshots {
					if scenario == "changed-during-round" {
						snapshot.Fingerprint = "newer-config"
					} else {
						snapshot.ProviderMembers = []string{"a", "b", "c"}
					}
					host.snapshots[id] = snapshot
				}
			}
			decideProviderTestRun(en, rule, first)
			if scenario == "current-config" {
				if len(host.writes) != 1 || host.writes[0] || host.snapshots["a"].Disabled || host.snapshots["b"].Disabled || en.runs[first.ID].ActionStatus != "credential_restored" {
					t.Fatal("complete current group did not restore", host.writes, en.runs[first.ID].ActionStatus)
				}
			} else if len(host.writes) != 0 || !host.snapshots["a"].Disabled || answerOwner(en.managed["a"]) != rule.ID {
				t.Fatal("partial or changed group evidence restored or lost original owner", scenario, host.writes)
			}
		})
	}
}

func TestCrossRuleProviderErrorExcludesWholeGroupWithoutTakingRecoveryOwnership(t *testing.T) {
	for _, authorize := range []bool{false, true} {
		en, host, owner := retainedProviderGroup(t)
		observer := cloneRule(owner)
		observer.ID, observer.Action, observer.AutoRestore = "observer", "observe_only", false
		observer.UpstreamErrorActions = map[string]string{"unauthorized": disableCredential}
		if !authorize {
			observer.AuthorizedGroups = nil
		}
		en.rules[observer.ID] = observer
		for id, snapshot := range host.snapshots {
			snapshot.Fingerprint = "fresh-content"
			host.snapshots[id] = snapshot
		}
		run := providerGroupRun(en, observer, host.snapshots["a"], "unknown", "error-batch")
		run.Samples[0].ExecutionStatus, run.Samples[0].UpstreamCondition = "failed", "unauthorized"
		decideProviderTestRun(en, observer, run)
		for _, id := range []string{"a", "b"} {
			d := en.managed[id]
			if len(host.writes) != 0 || answerOwner(d) != owner.ID || d.RuleID != owner.ID || d.ActionID != "original-answer-action" || !host.snapshots[id].Disabled {
				t.Fatal("error source rewrote CPA or answer ownership", id, d)
			}
			if authorize {
				if errorPolicyDisabled(d).Code == "" || d.ErrorSource == nil || d.ErrorSource.RuleID != observer.ID || d.ErrorSource.AuthID != "a" || canRecoverDisabled(owner, d) {
					t.Fatal("authorized error did not exclude the whole group", id, d)
				}
			} else if errorPolicyDisabled(d).Code != "" || !canRecoverDisabled(owner, d) {
				t.Fatal("route selection granted unapproved whole-group error authority", id, d)
			}
		}
	}
}

func TestProviderGroupExternalEnableReconciliationIsAtomic(t *testing.T) {
	for _, scenario := range []string{"complete", "one-still-disabled", "new-membership", "missing-current-member"} {
		t.Run(scenario, func(t *testing.T) {
			en, host, _ := retainedProviderGroup(t)
			for id, snapshot := range host.snapshots {
				snapshot.Disabled, snapshot.Fingerprint = false, "updated-current-content"
				if scenario == "one-still-disabled" && id == "b" {
					snapshot.Disabled = true
				}
				if scenario == "new-membership" || scenario == "missing-current-member" {
					snapshot.ProviderMembers = []string{"a", "b", "c"}
				}
				host.snapshots[id] = snapshot
			}
			if scenario == "new-membership" {
				added := host.snapshots["a"]
				added.AuthID, added.AuthIndex = "c", "ix-c"
				host.snapshots["c"] = added
			}
			targets, err := host.Targets(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			err = en.observeEnabledProviderGroupLocked(context.Background(), targets, en.managed["a"], host.CredentialSnapshot)
			wantEnabled := scenario == "complete" || scenario == "new-membership"
			if (err == nil) != wantEnabled || len(host.writes) != 0 {
				t.Fatal(scenario, err, host.writes)
			}
			for _, id := range []string{"a", "b"} {
				if activeDisposition(en.managed[id]) == wantEnabled {
					t.Fatal("partial local group transition", scenario, id, en.managed[id].State)
				}
			}
		})
	}
}

func TestProviderKeyReplacementKeepsOldOwnershipOffCurrentEnabledRoutes(t *testing.T) {
	f := newConfigAdapterFixture(t)
	en, _, _ := scopeFixture(t)
	en.host = f.host
	en.managed = make(map[string]*CredentialDisposition)
	group := f.groups["openai-compatibility"][0]
	group["disabled"] = true
	ctx := context.Background()
	routes, err := f.host.providerDirectory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var oldIDs []string
	for _, route := range routes {
		if route.family != "openai-compatibility" || route.name != "Example" {
			continue
		}
		snapshot, err := f.host.CredentialSnapshot(ctx, route.id, route.index)
		if err != nil {
			t.Fatal(err)
		}
		oldIDs = append(oldIDs, route.id)
		en.managed[route.id] = &CredentialDisposition{AuthID: route.id, RuleID: "old-owner", AnswerOwnerRuleID: "old-owner", ActionID: "old-action",
			State: "disabled", Operation: disableCredential, Snapshot: snapshot, UpstreamCondition: "unauthorized"}
	}
	if len(oldIDs) != 2 {
		t.Fatal("expected two retained group members", oldIDs)
	}
	for i, key := range configList(group["keys"]) {
		key["api-key"] = []string{"replacement-key-one", "replacement-key-two"}[i]
	}
	group["disabled"] = false
	routes, err = f.host.providerDirectory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var selected providerRoute
	for _, route := range routes {
		if route.family == "openai-compatibility" && route.name == "Example" {
			if contains(oldIDs, route.id) || en.managed[route.id] != nil {
				t.Fatal("mutable key identity incorrectly inherited old ownership")
			}
			selected = route
		}
	}
	config := RunConfig{AuthID: selected.id, Provider: selected.executor, Models: []string{"alias"}, QuestionID: "logic-boxes", Judge: Judge{Mode: "manual"}}
	run, err := en.Submit(ctx, config)
	if err != nil || run == nil || run.Config.AuthID != selected.id || f.calls != 0 || f.attempts != 0 {
		t.Fatal("old error source blocked current enabled replacement route", run, err)
	}
	for _, id := range oldIDs {
		if en.managed[id] == nil || answerOwner(en.managed[id]) != "old-owner" {
			t.Fatal("unmatched historical ownership was discarded", id)
		}
	}
}

func TestProviderAllKeysRotatedRetainsErrorExclusionUntilCurrentGroupEnabled(t *testing.T) {
	f := newConfigAdapterFixture(t)
	en, _, _ := scopeFixture(t)
	en.host = f.host
	en.managed = make(map[string]*CredentialDisposition)
	group := f.groups["openai-compatibility"][0]
	group["disabled"] = true
	ctx := context.Background()
	routes, err := f.host.providerDirectory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var groupID string
	var originalIDs []string
	for _, route := range routes {
		if route.family != "openai-compatibility" || route.name != "Example" {
			continue
		}
		groupID = route.groupID
		originalIDs = append(originalIDs, route.id)
		en.managed[route.id] = &CredentialDisposition{AuthID: route.id, RuleID: "owner", AnswerOwnerRuleID: "owner", ActionID: "original-action",
			State: "disabled", Operation: disableCredential, Snapshot: route.snapshot(), UpstreamCondition: "unauthorized"}
	}
	if len(originalIDs) != 2 {
		t.Fatal("fixture did not expose both original group members")
	}
	for i, key := range configList(group["keys"]) {
		key["api-key"] = []string{"fresh-one", "fresh-two"}[i]
	}
	rule := &Rule{ID: "new-rule", Revision: 1, Name: "all providers with manual recovery authorization", Enabled: true, TargetScope: scopeAllCredentials,
		ObjectTypes: []string{objectProvider}, Action: disableCredential, AutoRestore: true, AuthorizedGroups: []string{groupID},
		RunConfig: RunConfig{Models: []string{"alias"}, Samples: 1, QuestionID: "logic-boxes", Judge: Judge{Mode: "deterministic"}}}
	en.rules[rule.ID] = rule
	countGroup := func() (int, int) {
		t.Helper()
		en.mu.Lock()
		_, matches, _, err := en.collectScopeLocked(ctx, rule, f.host.Targets, true)
		en.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		count, recovery := 0, 0
		for _, match := range matches {
			if match.target.ProviderGroupID == groupID {
				count++
				if match.recovery {
					recovery++
				}
			}
		}
		return count, recovery
	}
	if count, recovery := countGroup(); count != 0 || recovery != 0 {
		t.Fatal("key rotation bypassed the group's error exclusion", count, recovery)
	}
	for _, id := range originalIDs {
		if errorPolicyDisabled(en.managed[id]).Code == "" || answerOwner(en.managed[id]) != "owner" {
			t.Fatal("key rotation erased the retained source or answer owner", id)
		}
	}
	group["disabled"] = false
	if count, recovery := countGroup(); count != 2 || recovery != 0 {
		t.Fatal("current enabled group did not rejoin ordinary detection", count, recovery)
	}
	for _, id := range originalIDs {
		if en.managed[id] == nil || activeDisposition(en.managed[id]) || en.managed[id].State != "entry_missing" {
			t.Fatal("unmatched source was not retained as inactive history", id)
		}
	}
	group["disabled"] = true
	if count, recovery := countGroup(); count != 2 || recovery != 2 {
		t.Fatal("old error source returned after an observed enable and a new manual disable", count, recovery)
	}
	if f.calls != 0 || f.attempts != 0 {
		t.Fatal("state reconciliation performed an upstream or CPA mutation")
	}
}
