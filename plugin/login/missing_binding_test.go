package login

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/cliproxyapi-plugins/model-quality/login/account"
	codex "github.com/cliproxyapi-plugins/model-quality/login/oauth"
)

func TestSecurityWithMissingCPABinding(t *testing.T) {
	for _, mode := range []string{"rebind", "reset_only", "cpa_unavailable", "different_identity", "credential_returns", "quality_block"} {
		t.Run(mode, func(t *testing.T) {
			host := newFakeHost()
			opts := offlineOptions(t)
			var resetCalls atomic.Int32
			var differentIdentity atomic.Bool
			var armed atomic.Bool
			var earlyRead atomic.Bool
			opts.Call = func(method string, in, out any) error {
				if armed.Load() {
					if resetCalls.Load() == 0 {
						earlyRead.Store(true)
					}
					if mode == "cpa_unavailable" || mode == "reset_only" {
						return errors.New("offline CPA read failure")
					}
				}
				return host.call(method, in, out)
			}
			exchange := opts.Exchange
			opts.Exchange = func(ctx context.Context, code string, pkce *codex.PKCECodes, proxy string) (*codex.CodexAuthBundle, error) {
				if differentIdentity.Load() {
					return offlineBundle(t, time.Now(), "other-account"), nil
				}
				return exchange(ctx, code, pkce, proxy)
			}
			var oldID string
			var oldEntry account.Entry
			opts.Prepare = func(_ context.Context, id, identity, _ string) error {
				if mode == "quality_block" && id == oldID {
					return errors.New("quality_authority_unavailable")
				}
				if mode == "credential_returns" && oldID != "" && id != oldID {
					host.mu.Lock()
					host.entries[oldID] = oldEntry
					host.mu.Unlock()
				}
				return nil
			}
			opts.Runtime = offlineRuntime{run: func(_ context.Context, in RuntimeRequest) (string, error) {
				if in.Operation == "" {
					return offlineCallback(in.State), nil
				}
				resetCalls.Add(1)
				if err := writeSecurityFixture(in, securityFixtureJournal(in)); err != nil {
					return "", err
				}
				return offlineCallback(in.State), nil
			}}
			s := offlineService(t, opts)
			view := importOfflineAccount(t, s)
			original := startOfflineAccount(t, s, view, "", "committed")
			oldID = original.TargetAuthID
			detail, err := s.AccountDetail(view.ID)
			if err != nil || detail.Account.AuthID != oldID {
				t.Fatal("fixture credential was not bound before deletion")
			}
			host.mu.Lock()
			oldEntry = host.entries[oldID]
			delete(host.entries, oldID)
			if mode != "credential_returns" {
				delete(host.files, oldID)
			}
			host.mu.Unlock()
			inspected, err := s.InspectAccounts(view.ID)
			if err != nil || len(inspected) != 1 || inspected[0].HealthStatus != "missing" || inspected[0].HealthReason != "credential_deleted" {
				t.Fatalf("inspection did not report the missing credential: %v", err)
			}
			view = inspected[0]
			differentIdentity.Store(mode == "different_identity")
			reauthorize := mode != "reset_only"
			armed.Store(true)
			accepted := securityRequest(t, s, "security-after-cpa-removal", "/accounts/security", map[string]any{
				"id": view.ID, "revision": view.Revision, "action": "reset_totp",
				"logout_all_devices": true, "reauthorize": reauthorize, "upload_to_cpa": reauthorize,
			})
			if accepted.StatusCode != 200 {
				t.Fatalf("explicit operation rejected: %s", accepted.Body)
			}
			want := map[string]string{"rebind": "committed", "reset_only": "succeeded", "cpa_unavailable": "failed", "different_identity": "conflict", "credential_returns": "failed", "quality_block": "failed"}[mode]
			job := waitLogin(t, s, ojson.GetBytes(accepted.Body, "id").String(), want)
			if earlyRead.Load() {
				t.Fatal("security admission required a CPA credential lookup")
			}
			if !job.TOTPReset || !job.DevicesLoggedOut {
				t.Fatal("reset or logout result was lost")
			}
			if mode == "cpa_unavailable" && (job.ErrorCode != "cpa_credential_unavailable" || job.ErrorStage != "saving") {
				t.Fatalf("CPA failure was not reported during upload: %s / %s", job.ErrorCode, job.ErrorStage)
			}
			detail, err = s.AccountDetail(view.ID)
			if err != nil || detail.Account.SecurityPending || detail.Credentials.TOTPSecret != securityFixtureSeed || resetCalls.Load() != 1 {
				t.Fatalf("confirmed seed was not retained before the later authorization outcome: %v", err)
			}
			host.mu.Lock()
			defer host.mu.Unlock()
			if mode == "rebind" {
				if host.saves != 2 || detail.Account.AuthID == oldID || detail.Account.AuthID != job.TargetAuthID || !job.CredentialEnabled {
					t.Fatal("confirmed replacement did not bind and enable a new credential")
				}
			} else if host.saves != 1 || detail.Account.AuthID != oldID {
				t.Fatal("blocked or reset-only operation altered the binding")
			}
		})
	}
}
