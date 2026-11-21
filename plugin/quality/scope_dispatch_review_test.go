package quality

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Exercise the same single-route optimization as NativeHost during execution.
type boundScopeHost struct{ *observationHost }

func (h *boundScopeHost) pinnedTarget(_ context.Context, id, index string) (Target, error) {
	for _, target := range h.targets {
		if target.AuthID == id && target.AuthIndex == index {
			return target, nil
		}
	}
	return Target{}, errors.New("fixture route not found")
}

func TestExecutionRespectsCurrentBoundProviderGroup(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"route moved to another native group", "expanded anchor removed", "expanded anchor index changed", "expanded anchor conflicts", "expanded group unchanged"} {
		t.Run(scenario, func(t *testing.T) {
			native := providerRoute{family: "codex", executor: "codex", name: "A", base: "https://fixture.invalid", key: providerObject{"api-key": "synthetic"}}
			if scenario != "route moved to another native group" {
				native.family, native.executor = "openai-compatibility", "openai-compatible-a"
				native.keys = []providerObject{native.key, {"api-key": "synthetic-peer"}}
			}
			first, second := policyProvider(), policyProvider()
			first.AuthID, first.AuthIndex = native.identity()
			first.Provider, first.ProviderGroupID, first.ProviderDisableUnit = native.executor, "group-a", "group"
			peerRoute := native
			peerRoute.key = providerObject{"api-key": "synthetic-peer"}
			second.AuthID, second.AuthIndex = peerRoute.identity()
			second.Provider, second.ProviderGroupID, second.ProviderDisableUnit = native.executor, "group-a", "group"
			rule := Rule{Name: scenario, Enabled: true, TargetScope: "single", ObjectTypes: []string{objectProvider}, RunConfig: RunConfig{QuestionID: "logic-boxes", Models: []string{"a"}, Samples: 1}}
			if scenario == "route moved to another native group" {
				first.ProviderDisableUnit, second.ProviderDisableUnit, rule.TargetGroup = "route", "route", first.ProviderGroupID
				renamed := native
				renamed.name = "B"
				id, index := renamed.identity()
				if id != first.AuthID || index != first.AuthIndex {
					t.Fatal("native group rename no longer preserves the route address")
				}
			} else {
				rule.AuthID, rule.Provider, rule.TargetAuthIndex = first.AuthID, first.Provider, first.AuthIndex
				rule.AuthorizedGroups = []string{first.ProviderGroupID}
			}
			en, h, _ := scopeFixture(t, first, second)
			en.host = &boundScopeHost{en.host.(*observationHost)}
			r, err := en.SaveRule(ctx, rule, false)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
			if err != nil || receipt.Count != 2 {
				t.Fatal("initial group was not fully admitted", receipt, err)
			}
			wantCalls := 0
			switch scenario {
			case "route moved to another native group":
				h.targets[0].ProviderGroupID = "group-b"
				wantCalls = 1
			case "expanded anchor removed":
				h.targets = h.targets[1:]
			case "expanded anchor index changed":
				h.targets[0].AuthIndex = "replacement"
			case "expanded anchor conflicts":
				h.targets[0].identityConflict = true
			case "expanded group unchanged":
				wantCalls = 2
			}
			for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
				en.executeTask(task)
			}
			if h.executeCalls != wantCalls || h.pauseCalls != 0 || h.recoverCalls != 0 || h.releaseCalls != 0 {
				t.Fatalf("after scope change got %d model calls, want %d", h.executeCalls, wantCalls)
			}
			for _, run := range en.runs {
				shouldComplete := wantCalls == 2 || wantCalls == 1 && run.Config.AuthID == second.AuthID
				wantStatus := "blocked"
				if shouldComplete {
					wantStatus = "completed"
				} else if run.Config.AuthID == first.AuthID && (scenario == "expanded anchor removed" || scenario == "expanded anchor index changed") {
					// The anchor's own pinned lookup reports its disappearance.
					wantStatus = "failed"
				}
				if run.Status != wantStatus || !shouldComplete && len(run.Samples) != 0 {
					t.Fatalf("unexpected run status for %s: %s (%d samples)", run.Config.AuthID, run.Status, len(run.Samples))
				}
			}
		})
	}
}

func TestFixedProviderDependencyDirectoryEvidence(t *testing.T) {
	ctx := context.Background()
	for _, role := range []string{"judge", "control"} {
		for _, present := range []bool{false, true} {
			t.Run(role+map[bool]string{true: "-partial-row", false: "-missing-row"}[present], func(t *testing.T) {
				file, provider := scopedTarget("one.json", "a"), policyProvider()
				file.ComparisonIdentity, provider.ComparisonIdentity = "account", "external"
				provider.AuthID, provider.Provider = "codex:apikey:0123456789ab", "codex"
				en, h, _ := scopeFixture(t, file, provider)
				config := RunConfig{AuthID: file.AuthID, Provider: file.Provider, Models: []string{"a"}, QuestionID: "logic-boxes"}
				if role == "judge" {
					config.Judge = Judge{Mode: "llm", Provider: provider.Provider, AuthID: provider.AuthID, Model: "a"}
				} else {
					config.ControlAuthID, config.ControlProvider = provider.AuthID, provider.Provider
				}
				rule, err := en.SaveRule(ctx, Rule{Name: role, TargetScope: "single", ObjectTypes: []string{objectOAuth}, Enabled: true, RunConfig: config}, false)
				if err != nil {
					t.Fatal(err)
				}
				original := en.host
				en.host = partialProviderDirectory{original}
				if !present {
					h.targets = h.targets[:1]
				} else {
					h.targets[1].EffectiveAvailable = false
				}
				preview, err := en.previewRuleScope(ctx, *rule)
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := en.dispatchRule(ctx, rule.ID, rule.Revision, false)
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range []ruleDispatch{preview, receipt} {
					if d.Verified || d.Count != 0 || d.Skipped != 1 || len(d.SkipDetails) != 1 || d.SkipDetails[0].Code != "provider_directory_unavailable" || !strings.Contains(d.SkipDetails[0].Reason, "目录") || !strings.Contains(d.SkipDetails[0].Reason, map[string]string{"judge": "判题", "control": "对照"}[role]) {
						t.Fatalf("missing dependency coverage or cause for %s: %+v", role, d)
					}
				}
				run := en.runs[receipt.RunID]
				if len(en.queue) != 0 || h.executeCalls != 0 || run == nil || run.Status != "skipped" || run.SkipDetail == nil || run.SkipDetail.AuthID != file.AuthID || run.Config.AuthID != file.AuthID || run.Config.ProviderObservation || run.UpstreamCondition != "" {
					t.Fatal("dependency failure changed the subject or triggered execution", run)
				}
				en.host, h.targets = original, []Target{file, provider}
				restored, err := en.dispatchRule(ctx, rule.ID, rule.Revision, false)
				if err != nil || !restored.Verified || restored.Count != 1 || restored.Skipped != 0 {
					t.Fatal("restored provider dependency was not readmitted", restored, err)
				}
			})
		}
	}
}

func TestProviderDirectoryOutageDoesNotMaskMissingFileControl(t *testing.T) {
	ctx := context.Background()
	file, control := scopedTarget("subject.json", "a"), scopedTarget("control.json", "a")
	file.Provider, control.Provider = "codex", "codex"
	file.ComparisonIdentity, control.ComparisonIdentity = "subject", "control"
	en, h, _ := scopeFixture(t, file, control)
	rule, err := en.SaveRule(ctx, Rule{Name: "independent files", Enabled: true, TargetScope: "single", ObjectTypes: []string{objectOAuth},
		RunConfig: RunConfig{AuthID: file.AuthID, Provider: file.Provider, Models: []string{"a"}, QuestionID: "logic-boxes", ControlAuthID: control.AuthID, ControlProvider: control.Provider}}, false)
	if err != nil {
		t.Fatal(err)
	}
	en.host, h.targets = partialProviderDirectory{en.host}, []Target{file}
	receipt, err := en.dispatchRule(ctx, rule.ID, rule.Revision, false)
	if err != nil || !receipt.Verified || receipt.Count != 0 || len(receipt.SkipDetails) != 1 || receipt.SkipDetails[0].Code != "dependency_unavailable" || len(en.queue) != 0 || h.executeCalls != 0 {
		t.Fatal("file control was conflated with a missing provider configuration", receipt, err)
	}
}

func TestQueuedCatalogCheckKeepsCurrentGroupAndModelEvidence(t *testing.T) {
	ctx := context.Background()
	for _, change := range []string{"unchanged", "left the group", "model registered", "catalog unverified"} {
		t.Run(change, func(t *testing.T) {
			member := policyProvider()
			member.ProviderGroupID, member.ProviderDisableUnit = "bound group", "route"
			en, h, _ := scopeFixture(t, member)
			en.host = &boundScopeHost{en.host.(*observationHost)}
			rule, err := en.SaveRule(ctx, Rule{Name: "catalog", Enabled: true, TargetScope: "single", TargetGroup: member.ProviderGroupID, ObjectTypes: []string{objectProvider},
				RunConfig: RunConfig{QuestionID: "logic-boxes", Models: []string{"missing"}}}, false)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := en.dispatchRule(ctx, rule.ID, rule.Revision, false)
			if err != nil || receipt.Count != 1 {
				t.Fatal(receipt, err)
			}
			switch change {
			case "left the group":
				h.targets[0].ProviderGroupID = "another group"
			case "model registered":
				h.targets[0].Models = append(h.targets[0].Models, "missing")
			case "catalog unverified":
				h.targets[0].catalogVerified = false
			}
			task := en.nextRunnableLocked()
			if task == nil || task.frozen.CatalogMissingModel != "missing" {
				t.Fatal("missing frozen catalog task")
			}
			en.executeTask(task)
			run := en.runs[task.id]
			want := "blocked"
			if change == "unchanged" {
				want = "completed"
			}
			if run.Status != want || run.CredentialAction != "" || h.executeCalls != 0 || len(run.Samples) != 0 {
				t.Fatal("catalog check ignored current evidence or caused a model call", run.Status, run.Error, h.executeCalls)
			}
		})
	}
}

func TestFixedProviderHistoricalStopNeedsCurrentDirectory(t *testing.T) {
	ctx := context.Background()
	for _, binding := range []string{"route", "group"} {
		t.Run(binding, func(t *testing.T) {
			provider := policyProvider()
			provider.AuthID, provider.Provider, provider.ProviderGroupID, provider.ProviderDisableUnit = "codex:apikey:0123456789ab", "codex", "group", "group"
			en, h, _ := scopeFixture(t, provider)
			draft := Rule{Name: binding, Enabled: true, TargetScope: "single", ObjectTypes: []string{objectProvider}, RunConfig: RunConfig{QuestionID: "logic-boxes", Models: []string{"a"}}}
			if binding == "route" {
				draft.AuthID, draft.Provider, draft.TargetAuthIndex = provider.AuthID, provider.Provider, provider.AuthIndex
			} else {
				draft.TargetGroup = provider.ProviderGroupID
			}
			rule, err := en.SaveRule(ctx, draft, false)
			if err != nil {
				t.Fatal(err)
			}
			old := &CredentialDisposition{AuthID: provider.AuthID, State: "disabled", RuleID: rule.ID, Operation: disableCredential, UpstreamCondition: "unauthorized", Snapshot: CredentialSnapshot{AuthID: provider.AuthID, AuthIndex: provider.AuthIndex, Provider: provider.Provider, ProviderGroupID: provider.ProviderGroupID, ProviderMechanism: "group_disabled", Disabled: true}}
			en.managed = map[string]*CredentialDisposition{provider.AuthID: old}
			original := en.host
			en.host, h.targets = partialProviderDirectory{original}, nil
			preview, err := en.previewRuleScope(ctx, *rule)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := en.dispatchRule(ctx, rule.ID, rule.Revision, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range []ruleDispatch{preview, receipt} {
				if d.Verified || d.Count != 0 || d.Skipped != 1 || len(d.SkipDetails) != 1 || d.SkipDetails[0].Code != "provider_directory_unavailable" || !strings.Contains(d.Reason, "目录") {
					t.Fatal("missing current provider status was reported as a new confirmed stop", d)
				}
			}
			provider.ManualDisabled, provider.EffectiveAvailable, provider.RuntimeStatus = true, false, "disabled"
			en.host, h.targets = original, []Target{provider}
			current, err := en.dispatchRule(ctx, rule.ID, rule.Revision, false)
			if err != nil || !current.Verified || current.Count != 0 || current.Skipped != 0 || len(en.queue) != 0 || h.executeCalls != 0 || en.managed[provider.AuthID] != old {
				t.Fatal("verified disabled route lost its suppression or original history", current, err)
			}
		})
	}
}

type finalGroupHost struct {
	*groupPolicyFixture
	fileID string
}

func (h finalGroupHost) Targets(ctx context.Context) ([]Target, error) {
	targets, err := h.groupPolicyFixture.Targets(ctx)
	for i := range targets {
		if targets[i].AuthID == h.fileID {
			targets[i].IsProvider, targets[i].ProviderGroupID, targets[i].ObjectType = false, "", objectOAuth
		}
	}
	return targets, err
}

func (h finalGroupHost) pinnedTarget(ctx context.Context, id, index string) (Target, error) {
	targets, _ := h.Targets(ctx)
	for _, target := range targets {
		if target.AuthID == id && target.AuthIndex == index {
			return target, nil
		}
	}
	return Target{}, errors.New("fixture route not found")
}

func TestBoundProviderGroupAtFinalDisposition(t *testing.T) {
	for _, evidence := range []string{"wrong answer", "upstream 401", "catalog omission", "complete recovery"} {
		for _, change := range []string{"unchanged", "anchor index changed", "anchor left group", "anchor removed", "anchor became a file"} {
			t.Run(evidence+"/"+change, func(t *testing.T) {
				en, host, rule := retainedProviderGroup(t)
				en.host = finalGroupHost{groupPolicyFixture: host}
				restore := evidence == "complete recovery"
				if !restore {
					en.managed = make(map[string]*CredentialDisposition)
					for id, s := range host.snapshots {
						s.Disabled = false
						host.snapshots[id] = s
					}
				}
				rule.TargetScope, rule.AuthID, rule.Provider, rule.TargetAuthIndex = "single", "a", "executor", "ix-a"
				rule.TargetObjectType, rule.ObjectTypes = objectProvider, []string{objectOAuth, objectProvider}
				rule.UpstreamErrorActions = map[string]string{"unauthorized": disableCredential, "model_unsupported": disableCredential}
				run := providerGroupRun(en, rule, host.snapshots["b"], "incorrect", "frozen")
				switch evidence {
				case "wrong answer":
					providerGroupRun(en, rule, host.snapshots["a"], "correct", "frozen")
				case "upstream 401":
					run.Samples[0].ExecutionStatus, run.Samples[0].QualityVerdict, run.Samples[0].UpstreamCondition = "failed", "unknown", "unauthorized"
				case "catalog omission":
					rule.Models, run.Config.Models, run.Samples = []string{"missing"}, []string{"missing"}, nil
					queueCatalogRun(run, "missing")
				case "complete recovery":
					run.Samples[0].QualityVerdict = "correct"
					providerGroupRun(en, rule, host.snapshots["a"], "correct", "frozen")
				}
				// Change only after the last response (or catalog check) was collected.
				anchor := host.snapshots["a"]
				switch change {
				case "anchor index changed":
					anchor.AuthIndex = "replacement"
					host.snapshots["a"] = anchor
				case "anchor left group":
					anchor.ProviderGroupID = "other"
					host.snapshots["a"] = anchor
				case "anchor removed":
					delete(host.snapshots, "a")
				case "anchor became a file":
					en.host = finalGroupHost{groupPolicyFixture: host, fileID: "a"}
				}
				decideProviderTestRun(en, rule, run)
				if change == "unchanged" {
					wantStatus := "credential_suspended"
					if restore {
						wantStatus = "credential_restored"
					}
					if len(host.writes) != 1 || host.writes[0] == restore || host.snapshots["a"].Disabled == restore || host.snapshots["b"].Disabled == restore || en.runs[run.ID].ActionStatus != wantStatus {
						t.Fatal("current whole-group authorization did not produce exactly one write", host.writes, en.runs[run.ID].ActionStatus)
					}
				} else {
					if len(host.writes) != 0 || host.snapshots["b"].Disabled != restore || en.runs[run.ID].ActionStatus != "credential_changed" {
						t.Fatal("group changed after evidence was collected but the plugin still wrote", host.writes, en.runs[run.ID].ActionStatus)
					}
					if restore {
						for _, id := range []string{"a", "b"} {
							if owner := en.managed[id]; owner == nil || owner.State != "disabled" || answerOwner(owner) != rule.ID || owner.ActionID != "original-answer-action" {
								t.Fatal("blocked recovery lost the original owner", id, owner)
							}
						}
					} else if len(en.managed) != 0 {
						t.Fatal("blocked group write incorrectly claimed ownership", en.managed)
					}
				}
			})
		}
	}
}

func TestProviderRecoveryRejectsContradictoryMemberEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Run)
	}{
		{"sample error", func(r *Run) { r.Samples[0].Error = "connection closed" }},
		{"upstream condition", func(r *Run) { r.Samples[0].UpstreamCondition = "unauthorized" }},
		{"refusal", func(r *Run) { r.Samples[0].ExecutionStatus = "refused" }},
		{"truncation", func(r *Run) { r.Samples[0].ExecutionStatus = "incomplete" }},
		{"run error", func(r *Run) { r.Error = "connection closed" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			en, host, owner := retainedProviderGroup(t)
			first := providerGroupRun(en, owner, host.snapshots["a"], "correct", "frozen")
			peer := providerGroupRun(en, owner, host.snapshots["b"], "correct", "frozen")
			tc.change(peer)
			decideProviderTestRun(en, owner, first)
			if len(host.writes) != 0 || !host.snapshots["a"].Disabled || !host.snapshots["b"].Disabled || answerOwner(en.managed["a"]) != owner.ID || answerOwner(en.managed["b"]) != owner.ID {
				t.Fatal("contradictory member evidence restored a provider group", host.writes, en.runs[first.ID].ActionStatus)
			}
		})
	}
}
