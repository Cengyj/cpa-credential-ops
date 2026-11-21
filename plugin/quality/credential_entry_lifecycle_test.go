package quality

import (
	"context"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAnswerOwnershipUsesCurrentEntryContentEachRound(t *testing.T) {
	for _, change := range []string{"token", "account", "unobserved-enable-disable"} {
		t.Run(change, func(t *testing.T) {
			f := managementTestFixture(t)
			rule := f.rule(t, disableCredential, true, true)
			f.answers["a"] = "5"
			if runs, err := f.round(t, rule); err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_suspended" {
				t.Fatal(runs, err)
			}
			old := f.engine.managed["one"]
			f.answers["a"] = "4"
			switch change {
			case "token":
				f.changedContent = true
			case "account":
				call := f.host.Call
				f.host.Call = func(method string, in, out any) error {
					if err := call(method, in, out); err != nil {
						return err
					}
					if method == "host.auth.get" {
						result := out.(*pluginapi.HostAuthGetResponse)
						var data map[string]any
						if err := json.Unmarshal(result.JSON, &data); err != nil {
							return err
						}
						data["account_id"], data["access_token"] = "replacement-account", "replacement-offline-token"
						result.JSON, _ = json.Marshal(data)
					}
					return nil
				}
			case "unobserved-enable-disable":
				entry := f.entries["one"]
				entry.UpdatedAt = entry.UpdatedAt.Add(time.Minute)
				entry.ModTime = entry.ModTime.Add(time.Minute)
				f.entries["one"] = entry
			}
			if err := f.engine.loadDispositions(); err != nil {
				t.Fatal(err)
			}
			runs, err := f.round(t, rule)
			if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_restored" || len(f.patches) != 2 || f.patches[1] || f.disabledProbes != 2 {
				t.Fatal("same entry did not complete a fresh recovery round", runs, err, f.patches, f.disabledProbes)
			}
			if f.engine.managed["one"].ActionID != old.ActionID || answerOwner(f.engine.managed["one"]) != rule.ID {
				t.Fatal("entry update transferred answer ownership")
			}
		})
	}
}

func TestOwnedRecoveryRejectsMutationInsideRoundButKeepsOwnership(t *testing.T) {
	f := managementTestFixture(t)
	rule := f.rule(t, disableCredential, true, true)
	f.answers["a"] = "5"
	if _, err := f.round(t, rule); err != nil {
		t.Fatal(err)
	}
	owner := f.engine.managed["one"]
	f.answers["a"] = "4"
	f.engine.host = &manualEditHost{f.host, f}
	runs, err := f.round(t, rule)
	if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_changed" || len(f.patches) != 1 || !f.entries["one"].Disabled || f.engine.managed["one"] != owner {
		t.Fatal("stale round wrote or lost long-term ownership", runs, err)
	}
	f.engine.host = f.host
	runs, err = f.round(t, rule)
	if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_restored" || len(f.patches) != 2 || f.disabledProbes != 4 {
		t.Fatal("new snapshot did not permit a new complete round", runs, err)
	}
}

func TestCrossRuleErrorSourceRetainsAnswerOwnerWithoutRepeatedCPAWrite(t *testing.T) {
	for _, policy := range []string{"observe_only", disableCredential} {
		t.Run(policy, func(t *testing.T) {
			f := managementTestFixture(t)
			owner := f.rule(t, disableCredential, true, true)
			observer := f.rule(t, "observe_only", true, false)
			observer.TargetScope = scopeAllCredentials
			observer.UpstreamErrorActions = map[string]string{"unauthorized": policy}
			var err error
			observer, err = f.engine.SaveRule(context.Background(), *observer, true)
			if err != nil {
				t.Fatal(err)
			}
			f.answers["a"] = "5"
			if _, err := f.round(t, owner); err != nil {
				t.Fatal(err)
			}
			original := f.engine.managed["one"]
			f.upstreamStatus = 401
			runs, err := f.round(t, observer)
			if err != nil || len(runs) != 1 || len(f.patches) != 1 || !f.entries["one"].Disabled || f.engine.managed["one"].RuleID != owner.ID {
				t.Fatal("cross-rule detection wrote CPA or transferred answer ownership", runs, err)
			}
			f.upstreamStatus = 0
			f.answers["a"] = "4"
			if policy == "observe_only" {
				if f.engine.managed["one"] != original || !canRecoverDisabled(owner, original) {
					t.Fatal("observation-only error changed original recovery authority")
				}
				if runs, err := f.round(t, owner); err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_restored" || len(f.patches) != 2 {
					t.Fatal(runs, err)
				}
				return
			}
			disposition := f.engine.managed["one"]
			if disposition.ActionID != original.ActionID || answerOwner(disposition) != owner.ID || disposition.ErrorSource == nil || disposition.ErrorSource.RuleID != observer.ID || disposition.ErrorSource.RunID != runs[0].ID || disposition.ErrorSource.Evidence.PayloadFingerprint == "" {
				t.Fatal("cross-rule trigger or answer ownership was not retained", disposition)
			}
			before := f.probes
			for _, rule := range []*Rule{owner, observer} {
				if runs, err := f.round(t, rule); err != nil || len(runs) != 0 || f.probes != before || len(f.patches) != 1 {
					t.Fatal("error-disabled entry was probed again", runs, err)
				}
			}
			if _, err := f.engine.clearRuleHistory(); err != nil {
				t.Fatal(err)
			}
			for _, rule := range []*Rule{observer, owner} {
				if err := f.engine.DeleteRule(context.Background(), rule.ID, f.engine.rules[rule.ID].Revision); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.engine.loadDispositions(); err != nil {
				t.Fatal(err)
			}
			disposition = f.engine.managed["one"]
			if errorPolicyDisabled(disposition).Code == "" || disposition.ErrorSource == nil || disposition.ErrorSource.RuleID != observer.ID || answerOwner(disposition) != owner.ID || len(f.patches) != 1 {
				t.Fatal("history or rule deletion erased error exclusion", disposition)
			}
			entry := f.entries["one"]
			entry.Disabled, entry.Status = false, "active"
			entry.UpdatedAt = entry.UpdatedAt.Add(time.Second)
			f.entries["one"] = entry
			if _, err := f.engine.visibleTargets(context.Background()); err != nil || activeDisposition(f.engine.managed["one"]) || len(f.patches) != 1 {
				t.Fatal("normal refresh did not retire confirmed error source after CPA enable", err)
			}
		})
	}
}

func TestOtherRuleAllScopeCannotDeleteAnswerOwnedDisabledEntry(t *testing.T) {
	f := managementTestFixture(t)
	owner := f.rule(t, disableCredential, true, true)
	other := f.rule(t, deleteCredential, true, false)
	other.TargetScope = scopeAllCredentials
	var err error
	other, err = f.engine.SaveRule(context.Background(), *other, true)
	if err != nil {
		t.Fatal(err)
	}
	f.answers["a"] = "5"
	if _, err := f.round(t, owner); err != nil {
		t.Fatal(err)
	}
	original := f.engine.managed["one"]
	runs, err := f.round(t, other)
	if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_kept_disabled" || f.deletes != 0 || len(f.patches) != 1 || f.disabledProbes != 2 || f.engine.managed["one"] != original {
		t.Fatal("read-only inclusion granted delete authority over another rule's disposition", runs, err)
	}
}
