package quality

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestOfficialCredentialLoginPreparationPreservesRetainedPause(t *testing.T) {
	for _, marker := range []string{"action", "blocked_credential"} {
		t.Run(marker, func(t *testing.T) {
			en, host, rule := semanticsEngine(t)
			en.host = &observationHost{host}
			authID := rule.AuthID
			if marker == "action" {
				rule.ActionID = "retained-action"
			} else {
				rule.AuthID = "dependent-credential"
				rule.BlockedAuthID = authID
			}
			rule.FailCount, rule.EvidenceSignature = 2, "retained-evidence"
			if err := en.store.Put("rules", rule.ID, rule); err != nil {
				t.Fatal(err)
			}
			en.rules[rule.ID] = cloneRule(rule)
			before, _ := json.Marshal(rule)
			reply, err := en.PrepareCredentialLogin(context.Background(), CredentialLoginPrepareRequest{
				AuthID: authID, PreparationID: "retained-pause-preparation",
			})
			if err == nil || !strings.HasPrefix(err.Error(), "unsupported:") || reply.Ready {
				t.Fatalf("retained quality pause admitted credential replacement: %+v, %v", reply, err)
			}
			after, _ := json.Marshal(en.rules[rule.ID])
			var persisted Rule
			if err := en.store.Get("rules", rule.ID, &persisted); err != nil {
				t.Fatal(err)
			}
			saved, _ := json.Marshal(persisted)
			if string(before) != string(after) || string(before) != string(saved) {
				t.Fatal("rejected preparation changed retained rule or evidence")
			}
			if err := en.store.db.View(func(tx *bolt.Tx) error {
				if tx.Bucket([]byte("meta")).Get([]byte(loginHoldPrefix+authID)) != nil {
					t.Fatal("rejected preparation persisted a ready marker")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if host.executeCalls != 0 || host.pauseCalls != 0 || host.releaseCalls != 0 {
				t.Fatal("rejected preparation mutated the host")
			}
		})
	}
}

func TestCredentialLoginPreparationStopsDependenciesAndWaitsForWorkers(t *testing.T) {
	en, h, rule := semanticsEngine(t)
	rule.FailCount = 2
	rule.EvidenceSignature = "old-evidence"
	secondary := cloneRule(rule)
	secondary.ID = "control-rule"
	secondary.AuthID = "other"
	secondary.ControlAuthID = rule.AuthID
	secondary.Enabled = true
	en.rules[secondary.ID] = secondary
	paused := cloneRule(rule)
	paused.ID = "paused-rule"
	paused.ActionID = "owned-pause"
	paused.BlockedAuthID = rule.AuthID
	paused.Status = "awaiting_release"
	paused.Enabled = false
	en.rules[paused.ID] = paused
	run := semanticRound(en, rule, "incorrect")
	run.Status = "running"
	ctx, cancel := context.WithCancel(context.Background())
	en.active[run.ID] = cancel
	en.busyAuth[rule.AuthID] = struct{}{}
	done := make(chan error, 1)
	go func() {
		reply, err := en.PrepareCredentialLogin(context.Background(), CredentialLoginPrepareRequest{AuthID: rule.AuthID, ExpectedIdentity: "identity-a", PreparationID: "prepare-1"})
		if err == nil && !reply.Ready {
			t.Error("missing ready ACK")
		}
		done <- err
	}()
	<-ctx.Done()
	select {
	case <-done:
		t.Fatal("ACK preceded actual worker exit")
	default:
	}
	en.mu.Lock()
	delete(en.busyAuth, rule.AuthID)
	en.notifyWorkersLocked()
	en.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, r := range []*Rule{rule, secondary} {
		current := en.rules[r.ID]
		if !current.Enabled || current.FailCount != 0 || current.EvidenceSignature != "" {
			t.Fatalf("dependent rule not reset: %s", r.ID)
		}
	}
	if en.ruleForDisplayLocked(en.rules[rule.ID]).Status != "credential_update_pending" || en.ruleForDisplayLocked(secondary).Status != "credential_update_pending" {
		t.Fatal("temporary hold was not visible independently of enabled intent")
	}
	if en.rules[paused.ID].ActionID != "owned-pause" || en.rules[paused.ID].Status != "awaiting_release" || en.rules[paused.ID].Enabled {
		t.Fatal("existing quality ownership changed")
	}
	revision := en.rules[rule.ID].Revision
	if _, err := en.PrepareCredentialLogin(context.Background(), CredentialLoginPrepareRequest{AuthID: rule.AuthID, ExpectedIdentity: "identity-a", PreparationID: "prepare-1"}); err != nil {
		t.Fatal(err)
	}
	if en.rules[rule.ID].Revision != revision {
		t.Fatal("replayed preparation changed rule revision")
	}
	if _, err := en.PrepareCredentialLogin(context.Background(), CredentialLoginPrepareRequest{AuthID: "another-auth", ExpectedIdentity: "identity-a", PreparationID: "prepare-1"}); err == nil {
		t.Fatal("preparation ID reused for another credential")
	}
	if h.executeCalls != 0 || h.releaseCalls != 0 || h.pauseCalls != 0 {
		t.Fatal("login preparation changed upstream or pause state")
	}
}
