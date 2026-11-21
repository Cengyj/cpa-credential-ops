package quality

import (
	"context"
	"errors"
	"testing"
)

type groupPolicyFixture struct {
	Host
	snapshots map[string]CredentialSnapshot
	writes    []bool
	writeErr  error
}

func (h *groupPolicyFixture) Capabilities() HostCapabilities {
	return HostCapabilities{CredentialManagement: true, ManagementSession: true}
}
func (h *groupPolicyFixture) Targets(ctx context.Context) ([]Target, error) {
	var result []Target
	for _, s := range h.snapshots {
		target := scopedTarget(s.AuthID, "alias")
		target.IsProvider, target.ProviderGroupID, target.AuthIndex, target.Provider = true, s.ProviderGroupID, s.AuthIndex, s.Provider
		target.identityVerified, target.EffectiveAvailable = true, !s.Disabled
		target.ManualDisabled = s.Disabled
		target.ProviderDisableUnit = "group"
		if s.Disabled {
			target.RuntimeStatus = "disabled"
		}
		result = append(result, target)
	}
	return result, nil
}
func (h *groupPolicyFixture) CredentialSnapshot(ctx context.Context, id, index string) (CredentialSnapshot, error) {
	s := h.snapshots[id]
	if s.AuthIndex != index || id == "" {
		return CredentialSnapshot{}, errors.New("route changed")
	}
	return s, nil
}
func (h *groupPolicyFixture) SetCredentialDisabled(ctx context.Context, s CredentialSnapshot, disabled bool) (CredentialSnapshot, error) {
	if h.writeErr != nil {
		return CredentialSnapshot{}, h.writeErr
	}
	h.writes = append(h.writes, disabled)
	for id, entry := range h.snapshots {
		if entry.ProviderGroupID == s.ProviderGroupID {
			entry.Disabled = disabled
			h.snapshots[id] = entry
		}
	}
	return h.snapshots[s.AuthID], nil
}
func (h *groupPolicyFixture) DeleteCredential(context.Context, CredentialSnapshot) error {
	panic("provider deletion")
}

func TestGroupDispositionRequiresEveryFrozenRoute(t *testing.T) {
	for _, tc := range []struct {
		name                string
		authorize, disabled bool
		first, second       string
		want                int
	}{
		{"no group permission", false, false, "incorrect", "correct", 0},
		{"unknown answer", true, false, "incorrect", "unknown", 0},
		{"missing route", true, false, "incorrect", "", 0},
		{"one incorrect", true, false, "incorrect", "correct", 1},
		{"all correct on enabled", true, false, "correct", "correct", 0},
		{"partial recovery", true, true, "incorrect", "correct", 0},
		{"complete recovery", true, true, "correct", "correct", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			en, base, r := scopeFixture(t)
			h := &groupPolicyFixture{Host: base, snapshots: map[string]CredentialSnapshot{}}
			en.host = h
			r = cloneRule(en.rules[r.ID])
			r.TargetScope = scopeAllCredentials
			r.Models = []string{"alias"}
			r.Samples = 1
			r.LegacyProviderObservation = false
			r.ObjectTypes = []string{"provider"}
			r.Action = disableCredential
			r.AutoRestore = true
			if tc.authorize {
				r.AuthorizedGroups = []string{"group"}
			}
			for _, id := range []string{"a", "b", "other"} {
				s := CredentialSnapshot{AuthID: id, AuthIndex: "ix-" + id, Name: "group", Provider: "executor", ProviderGroupID: "group", ProviderMechanism: "group_disabled", ProviderMembers: []string{"a", "b"}, Disabled: tc.disabled, Fingerprint: "digest"}
				if id == "other" {
					s.ProviderGroupID = "other"
					s.ProviderMembers = []string{"other"}
					s.Disabled = false
				}
				h.snapshots[id] = s
			}
			for i, v := range []string{tc.first, tc.second} {
				if v == "" {
					continue
				}
				id := []string{"a", "b"}[i]
				s := h.snapshots[id]
				f := Frozen{RunConfig: RunConfig{AuthID: id, Models: []string{"alias"}, Provider: s.Provider, Samples: 1, Judge: Judge{Mode: "deterministic"}}, ManagedProbe: &s, RuleID: r.ID, RuleRevision: r.Revision, ScopeBatchID: "batch"}
				run := &Run{ID: id, Config: f, Source: "scheduled_probe", Status: "completed", Samples: []Sample{{Model: "alias", Index: 0, ExecutionStatus: "completed", QualityVerdict: v, Evidence: Evidence{Path: "provider_config_probe", AuthID: id, RequestedModel: "alias", ResolvedModel: "wire", PayloadFingerprint: "request"}}}}
				en.runs[id] = run
			}
			run := en.runs["a"]
			c := changes()
			c.runs[run.ID] = cloneRun(run)
			en.mu.Lock()
			en.decideManagedLocked(run, r, c)
			en.mu.Unlock()
			if len(h.writes) != tc.want || h.snapshots["other"].Disabled {
				t.Fatal(h.writes, en.runs["a"].ActionStatus)
			}
			if tc.want != 0 {
				if h.writes[0] == tc.disabled || en.managed["a"].ActionID != en.managed["b"].ActionID {
					t.Fatal("unexpected group ownership")
				}
				for _, id := range []string{"a", "b"} {
					var stored CredentialDisposition
					if err := en.store.Get("meta", dispositionPrefix+id, &stored); err != nil || stored.ActionID != en.managed[id].ActionID || stored.Snapshot.AuthID != id {
						t.Fatal(stored, err)
					}
				}
			}
		})
	}
}

func TestRuleScopeRetainsProviderAndOAuthBlockers(t *testing.T) {
	for _, kind := range []string{objectProvider, objectOAuth} {
		for _, tc := range []struct {
			name, code string
			setup      func(*Target) *CredentialDisposition
		}{
			{"other owner without probe capability", "manual_disabled", func(t *Target) *CredentialDisposition {
				return &CredentialDisposition{AuthID: t.AuthID, RuleID: "another", State: "disabled"}
			}},
			{"unconfirmed write", "handling_blocked", func(t *Target) *CredentialDisposition {
				return &CredentialDisposition{AuthID: t.AuthID, State: "attention"}
			}},
			{"independent pause", "quality_paused", func(t *Target) *CredentialDisposition { t.RetainedPause = true; return nil }},
			{"identity conflict", "identity_conflict", func(t *Target) *CredentialDisposition { t.identityConflict = true; return nil }},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				target := scopedTarget("subject", "a", "b")
				target.IsProvider = kind == objectProvider
				target.ManualDisabled, target.RuntimeStatus, target.EffectiveAvailable = true, "disabled", false
				if target.IsProvider {
					target.ProviderGroupID, target.ProviderDisableUnit = "group", "group"
				}
				d := tc.setup(&target)
				en, host, base := scopeFixture(t, target)
				r := *base
				r.ID, r.Revision = "", 0
				r.ObjectTypes = []string{kind}
				r.TargetScope = scopeAllCredentials
				ctx := context.Background()
				saved, err := en.saveRule(ctx, r, false, "start")
				if err != nil {
					t.Fatal(err)
				}
				if d != nil {
					en.managed = map[string]*CredentialDisposition{target.AuthID: d}
				}
				preview, err := en.previewRuleScope(ctx, *saved)
				if err != nil || preview.Count != 0 || preview.Skipped != 1 || len(preview.SkipDetails) != 1 || preview.SkipDetails[0].Code != tc.code {
					t.Fatal(preview, err)
				}
				for _, scheduled := range []bool{false, true} {
					receipt, err := en.dispatchRule(ctx, saved.ID, saved.Revision, scheduled)
					if err != nil || receipt.Count != 0 || receipt.Skipped != 1 || receipt.SkipDetails[0].Code != tc.code || en.nextRunnableLocked() != nil || host.executeCalls != 0 {
						t.Fatal(receipt, err)
					}
				}
			})
		}
	}
}
