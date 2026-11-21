package quality

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func commercialOrphanedHandling(t *testing.T, en *Engine) []HandlingSummary {
	t.Helper()
	response := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: http.MethodGet,
		Path: "/v0/management/plugins/model-quality/rules", Query: url.Values{"offset": {"100"}, "limit": {"1"}}})
	var body struct {
		Orphaned []HandlingSummary `json:"orphaned_handling"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(response.Body, &body) != nil || body.Orphaned == nil {
		t.Fatalf("orphaned records were absent from the public paged query: %s", response.Body)
	}
	if strings.Contains(string(response.Body), `"snapshot"`) || strings.Contains(string(response.Body), "offline-upstream-secret") {
		t.Fatal("public handling projection exposed private credential data")
	}
	return body.Orphaned
}

func TestCommercialDeletedRuleUncertainWriteRemainsExplicitlyResolvable(t *testing.T) {
	for _, resolution := range []string{"enabled", "removed", "legacy-missing-owner"} {
		t.Run(resolution, func(t *testing.T) {
			f := managementTestFixture(t)
			rule := f.rule(t, disableCredential, true, true)
			f.answers["a"], f.statusFailure = "5", true
			runs, err := f.round(t, rule)
			if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_action_uncertain" {
				t.Fatal("expected a real synthetic status write followed by HTTP 500", runs, err)
			}
			actionID := f.engine.managed["one"].ActionID
			beforeSnapshot := f.engine.managed["one"].Snapshot
			f.verifyManagement(t)
			if len(commercialOrphanedHandling(t, f.engine)) != 0 {
				t.Fatal("a rule outside the requested page was reported as orphaned")
			}
			if err := f.engine.acknowledgeHandling(context.Background(), "", "one", actionID); err == nil {
				t.Fatal("empty rule ID took over a live rule's uncertain action")
			}
			if err := f.engine.DeleteRule(context.Background(), rule.ID, rule.Revision); err != nil {
				t.Fatal(err)
			}
			if d := f.engine.managed["one"]; d.RuleID != "" || d.State != "attention" {
				t.Fatal("deletion did not preserve orphaned uncertainty", d)
			}
			if resolution == "legacy-missing-owner" {
				next := *f.engine.managed["one"]
				next.RuleID = rule.ID
				updates := changes()
				updates.disposition = &next
				if err := f.engine.commitLocked(updates); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.engine.loadDispositions(); err != nil {
				t.Fatal(err)
			}
			orphaned := commercialOrphanedHandling(t, f.engine)
			if len(orphaned) != 1 || orphaned[0].AuthID != "one" || orphaned[0].ActionID != actionID || orphaned[0].State != "attention" {
				t.Fatal("deleted rule hid the exact uncertain action", orphaned)
			}
			acknowledge := func(action string) pluginapi.ManagementResponse {
				t.Helper()
				body, _ := json.Marshal(map[string]string{"rule_id": "", "auth_id": "one", "action_id": action})
				return f.engine.Handle(context.Background(), pluginapi.ManagementRequest{Method: http.MethodPost,
					Path: "/v0/management/plugins/model-quality/rules/handling/acknowledge", Body: body})
			}
			if got := acknowledge(actionID); got.StatusCode == http.StatusOK {
				t.Fatal("orphaned uncertainty cleared while CPA remained disabled")
			}
			if resolution != "removed" {
				entry := f.entries["one"]
				entry.Disabled, entry.Status = false, "active"
				f.entries["one"] = entry
			} else {
				delete(f.entries, "one")
			}
			if got := acknowledge("stale-action"); got.StatusCode == http.StatusOK {
				t.Fatal("stale orphan acknowledgement was accepted")
			}
			if _, err := f.engine.visibleTargets(context.Background()); err != nil {
				t.Fatal(err)
			}
			if f.engine.managed["one"].State != "attention" {
				t.Fatal("ordinary refresh implicitly cleared uncertain ownership")
			}
			if got := acknowledge(actionID); got.StatusCode != http.StatusOK {
				t.Fatalf("deleted owner made verified uncertainty impossible to resolve: HTTP %d %s", got.StatusCode, got.Body)
			}
			if activeDisposition(f.engine.managed["one"]) || len(f.patches) != 1 || f.deletes != 0 || f.probes != 2 || f.nativeCalls != 0 {
				t.Fatal("explicit orphan resolution replayed work, mutated CPA or retained quarantine")
			}
			if len(commercialOrphanedHandling(t, f.engine)) != 0 || len(f.engine.rules) != 0 || !sameSnapshot(f.engine.managed["one"].Snapshot, beforeSnapshot) || f.engine.runs[runs[0].ID] == nil {
				t.Fatal("resolution recreated a rule, lost original evidence, or remained actionable")
			}
		})
	}
}

func TestCommercialOrphanedGroupResolutionPreservesAtomicOwnership(t *testing.T) {
	for _, scenario := range []string{"complete", "missing-member", "different-action", "busy-member", "still-disabled", "store-failure"} {
		t.Run(scenario, func(t *testing.T) {
			en, base, rule := retainedProviderGroup(t)
			update := changes()
			for _, old := range en.managed {
				next := *old
				next.State = "attention"
				update.dispositions = append(update.dispositions, &next)
			}
			if err := en.commitLocked(update); err != nil {
				t.Fatal(err)
			}
			if err := en.DeleteRule(context.Background(), rule.ID, rule.Revision); err != nil {
				t.Fatal(err)
			}
			if len(commercialOrphanedHandling(t, en)) != 2 {
				t.Fatal("incomplete orphaned group projection")
			}
			remainingRules := fingerprint(en.rules)
			for id, snapshot := range base.snapshots {
				snapshot.Disabled = scenario == "still-disabled" && id == "b"
				base.snapshots[id] = snapshot
			}
			host := &commercialGroupInventoryHost{groupPolicyFixture: base}
			en.host = host
			switch scenario {
			case "missing-member":
				host.omit = "b"
			case "different-action":
				en.managed["b"].ActionID = "different-uncertain-action"
			case "busy-member":
				en.busyAuth["b"] = struct{}{}
			case "store-failure":
				if err := en.store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			err := en.acknowledgeHandling(context.Background(), "", "a", en.managed["a"].ActionID)
			resolved := scenario == "complete"
			if (err == nil) != resolved {
				t.Fatal("unexpected orphan group resolution", scenario, err)
			}
			store := en.store
			if scenario == "store-failure" {
				if en.fault == nil {
					t.Fatal("failed acknowledgement persistence did not fault the engine")
				}
				var err error
				store, err = OpenStore(en.cfg.DataDir, en.cfg.MaxStorageBytes)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
			}
			for _, id := range []string{"a", "b"} {
				if activeDisposition(en.managed[id]) == resolved || en.managed[id].RuleID != "" {
					t.Fatal("orphan group resolution was partial or transferred ownership", scenario, id)
				}
				var stored CredentialDisposition
				if err := store.Get("meta", dispositionPrefix+id, &stored); err != nil || activeDisposition(&stored) == resolved {
					t.Fatal("orphan group state was not committed atomically", scenario, id, err)
				}
			}
			if len(base.writes) != 0 || en.rules[rule.ID] != nil || fingerprint(en.rules) != remainingRules {
				t.Fatal("orphan group verification wrote CPA or recreated scheduling")
			}
		})
	}
}
