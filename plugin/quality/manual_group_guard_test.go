package quality

import (
	"context"
	"strings"
	"testing"
)

func TestManualProviderRespectsUncertainGroupWrite(t *testing.T) {
	for _, state := range []string{"attention", "disable_pending", "restore_pending", "delete_pending"} {
		for _, queued := range []bool{false, true} {
			t.Run(state+map[bool]string{false: "/submission", true: "/execution"}[queued], func(t *testing.T) {
				provider := policyProvider()
				provider.ProviderGroupID, provider.ProviderDisableUnit = "same-group", "group"
				en, host, _ := scopeFixture(t, provider)
				config := RunConfig{AuthID: provider.AuthID, Provider: provider.Provider, Models: []string{"a"}, QuestionID: "logic-boxes", Judge: Judge{Mode: "manual"}}
				var run *Run
				var err error
				if queued {
					run, err = en.Submit(context.Background(), config)
					if err != nil {
						t.Fatal(err)
					}
				}
				uncertain := &CredentialDisposition{AuthID: "previous-route", State: state,
					Snapshot: CredentialSnapshot{ProviderMechanism: "group_disabled", ProviderGroupID: provider.ProviderGroupID}}
				en.managed = map[string]*CredentialDisposition{"previous-route": uncertain}
				if queued {
					en.execute(run.ID)
					stored, readErr := en.Run(run.ID)
					if readErr != nil || stored.Status != "blocked" || !strings.Contains(stored.Error, "group") {
						t.Fatal("queued manual call bypassed uncertain group state", stored, readErr)
					}
				} else if _, err = en.Submit(context.Background(), config); err == nil || !strings.Contains(err.Error(), "group") {
					t.Fatal("manual submission bypassed uncertain group state", err)
				}
				if host.executeCalls != 0 || en.managed["previous-route"] != uncertain || uncertain.State != state {
					t.Fatal("unknown group write was replayed, cleared or used for a call")
				}
			})
		}
	}
}

func TestManualCurrentEnabledRouteIgnoresOldConfirmedGroupSource(t *testing.T) {
	provider := policyProvider()
	provider.ProviderGroupID = "same-group"
	en, host, _ := scopeFixture(t, provider)
	old := &CredentialDisposition{AuthID: "previous-route", State: "disabled", UpstreamCondition: "unauthorized",
		Snapshot: CredentialSnapshot{ProviderMechanism: "group_disabled", ProviderGroupID: provider.ProviderGroupID}}
	en.managed = map[string]*CredentialDisposition{"previous-route": old}
	run, err := en.Submit(context.Background(), RunConfig{AuthID: provider.AuthID, Provider: provider.Provider,
		Models: []string{"a"}, QuestionID: "logic-boxes", Judge: Judge{Mode: "manual"}})
	if err != nil {
		t.Fatal(err)
	}
	en.execute(run.ID)
	if host.executeCalls != 1 || en.managed["previous-route"] != old {
		t.Fatal("historical ordinary group source blocked a different current enabled route")
	}
}
