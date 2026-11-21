package quality

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestActiveCredentialWithChangedFingerprintCanRun(t *testing.T) {
	ctx := context.Background()
	for _, scope := range []string{"single", scopeAll, scopeAllCredentials} {
		t.Run(scope, func(t *testing.T) {
			f := managementTestFixture(t)
			r := f.rule(t, disableCredential, scope != "single", true)
			r.TargetScope, r.Samples = scope, 2
			r.UpstreamErrorActions = map[string]string{"unauthorized": disableCredential}
			r, err := f.engine.SaveRule(ctx, *r, true)
			if err != nil {
				t.Fatal(err)
			}
			f.answers["a"] = "5"
			rows, err := f.round(t, r)
			if err != nil || len(rows) != 1 || rows[0].ActionStatus != "credential_suspended" {
				t.Fatal(rows, err)
			}
			original := *f.engine.managed["one"]
			before := f.probes
			f.changedContent, f.upstreamStatus, f.answers["a"] = true, 200, "4"
			entry := f.entries["one"]
			entry.Status, entry.Disabled, entry.UpdatedAt = "active", false, entry.UpdatedAt.Add(time.Second)
			f.entries["one"] = entry
			current, err := f.host.CredentialSnapshot(ctx, "one", "one")
			if err != nil || current.Disabled || sameCredential(current, original.Snapshot) {
				t.Fatal("fixture did not replace the active credential", err)
			}
			r.Name = "Same test, renamed"
			r, err = f.engine.SaveRule(ctx, *r, true)
			if err != nil || f.engine.managed["one"].State != "externally_enabled" || f.probes != before {
				t.Fatal("editing the rule failed to observe CPA enablement without probing", err)
			}
			for i := 1; i <= 2; i++ {
				rows, err = f.round(t, r)
				if err != nil || len(rows) != 1 || rows[0].Status != "completed" || rows[0].ActionStatus != "observe_only" ||
					len(rows[0].Samples) != 4 || rows[0].Config.ManagedProbe == nil || !sameSnapshot(*rows[0].Config.ManagedProbe, current) ||
					f.probes != before+4*i || f.disabledProbes != 0 || f.nativeCalls != 0 || len(f.patches) != 1 || f.deletes != 0 || f.entries["one"].Disabled {
					t.Fatal("live replacement did not run using its own snapshot", rows, err, f.probes, f.patches)
				}
			}
			d := f.engine.managed["one"]
			if activeDisposition(d) || d.State != "externally_enabled" || d.RunID != original.RunID || d.RuleID != original.RuleID ||
				d.ActionID != original.ActionID || !sameSnapshot(d.Snapshot, original.Snapshot) || d.UpstreamCondition != original.UpstreamCondition {
				t.Fatal("old suspension lost its evidence or still owns the active replacement", d)
			}
			audits, err := f.engine.History()
			count := 0
			for _, a := range audits {
				if a.Kind == "credential_external_change" && a.RuleID == original.RuleID && a.RunID == original.RunID && a.ActionID == original.ActionID {
					count++
				}
			}
			if err != nil || count != 1 {
				t.Fatal("expected one durable, linked external-change audit", count, err)
			}
			var persisted CredentialDisposition
			if err := f.engine.store.Get("meta", dispositionPrefix+"one", &persisted); err != nil ||
				persisted.State != "externally_enabled" || !sameSnapshot(persisted.Snapshot, original.Snapshot) || persisted.ActionID != original.ActionID {
				t.Fatal("external change lost its original disposition on disk", err)
			}
		})
	}
}

func TestActiveSnapshotReconciliationKeepsUnconfirmedOwners(t *testing.T) {
	f := managementTestFixture(t)
	r := f.rule(t, disableCredential, true, true)
	targets, err := f.host.Targets(context.Background())
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	snapshot, err := f.host.CredentialSnapshot(context.Background(), "one", "one")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ mode, wantCode string }{
		{"pending", "handling_blocked"}, {"attention", "handling_blocked"}, {"deleted", "handling_blocked"},
		{"retained pause", "quality_paused"}, {"local pause", "quality_paused"}, {"runtime disabled", "snapshot_runtime_conflict"},
		{"stale id", "identity_conflict"}, {"stale index", "identity_conflict"}, {"stale provider", "identity_conflict"},
		{"unverified catalog", "identity_conflict"}, {"absent fingerprint", "identity_conflict"}, {"disabled replacement", "error_policy_disabled"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			live, current := targets[0], snapshot
			d := &CredentialDisposition{AuthID: "one", RuleID: r.ID, State: "disabled", Snapshot: snapshot, RunID: "original", ActionID: "old"}
			d.Snapshot.Disabled, d.Snapshot.Fingerprint = true, "previous"
			switch tc.mode {
			case "pending":
				d.State = "disable_pending"
			case "attention", "deleted":
				d.State = tc.mode
			case "retained pause":
				live.RetainedPause = true
			case "local pause":
				f.engine.rules["legacy"] = &Rule{ID: "legacy", RunConfig: RunConfig{AuthID: "one"}, ActionID: "independent"}
				defer delete(f.engine.rules, "legacy")
			case "runtime disabled":
				live.RuntimeStatus, live.ManualDisabled = "disabled", true
			case "stale id":
				current.AuthID = "different"
			case "stale index":
				current.AuthIndex = "different"
			case "stale provider":
				current.Provider = "different"
			case "unverified catalog":
				live.catalogVerified, live.identityVerified = false, false
			case "absent fingerprint":
				current.Fingerprint = ""
			case "disabled replacement":
				live.RuntimeStatus, live.ManualDisabled, current.Disabled = "disabled", true, true
				d.UpstreamCondition = "unauthorized"
			}
			f.engine.managed["one"] = d
			_, _, err := f.engine.admitSnapshotLocked(r, live, current, false)
			var denied *scopeRejection
			if !errors.As(err, &denied) || denied.detail.Code != tc.wantCode || f.engine.managed["one"] != d ||
				f.probes != 0 || len(f.patches) != 0 || f.deletes != 0 {
				t.Fatal("unverified target retired ownership", tc.mode, err, denied)
			}
		})
	}
}
