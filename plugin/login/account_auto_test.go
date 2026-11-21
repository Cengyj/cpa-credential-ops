package login

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	codex "github.com/cliproxyapi-plugins/model-quality/login/oauth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func importOfflineAccount(t *testing.T, s *Service) Account {
	t.Helper()
	accounts, err := s.ImportAccounts([]Credentials{offlineStart().Credentials})
	if err != nil {
		t.Fatal(err)
	}
	return accounts[0]
}

func startOfflineAccount(t *testing.T, s *Service, a Account, target, want string) Job {
	t.Helper()
	job, err := s.StartAccount(a.ID, a.Revision, target)
	if err != nil {
		t.Fatal(err)
	}
	return waitLogin(t, s, job.ID, want)
}

func TestAccountStartAPIUploadsAndEnablesExactlyOnce(t *testing.T) {
	h := newFakeHost()
	opts := offlineOptions(t)
	opts.Call = h.call
	s := offlineService(t, opts)
	a := importOfflineAccount(t, s)
	body, _ := Encode(map[string]any{"id": a.ID, "revision": a.Revision})
	req := pluginapi.ManagementRequest{Method: "POST", Path: RoutePrefix + "/accounts/start", Headers: http.Header{"Idempotency-Key": {"offline-account-start"}}, Body: body}
	first := s.Handle(context.Background(), req)
	second := s.Handle(context.Background(), req)
	id := ojson.GetBytes(first.Body, "id").String()
	if first.StatusCode != 200 || second.StatusCode != 200 || id == "" || ojson.GetBytes(second.Body, "id").String() != id {
		t.Fatal("single account action did not keep its idempotent job")
	}
	j := waitLogin(t, s, id, "committed")
	if !j.CredentialEnabled || !j.AutoCommit || !j.EnableCredential || j.CandidateID != "" {
		t.Fatal("account action required a candidate confirmation")
	}
	h.mu.Lock()
	if h.saves != 1 || h.files[j.TargetAuthID]["disabled"] != false || h.entries[j.TargetAuthID].Disabled {
		t.Fatal("account action did not save and enable exactly once")
	}
	h.mu.Unlock()
	accounts, err := s.Accounts()
	if err != nil || accounts[0].AuthID != j.TargetAuthID || accounts[0].LastJobStatus != "committed" || !accounts[0].LastJobCredentialEnabled {
		t.Fatal("completed login did not synchronize the account binding")
	}
	logs, _ := s.Logs(id)
	for _, entry := range logs.Entries {
		if entry.Status == "awaiting_commit" {
			t.Fatal("account path included manual review")
		}
	}
}

func TestAccountManualReloginEnablesFrozenDisabledTarget(t *testing.T) {
	h := newFakeHost()
	opts := offlineOptions(t)
	opts.Call = h.call
	s := offlineService(t, opts)
	old := commitCandidate(t, s, startCandidate(t, s, offlineStart()))
	h.mu.Lock()
	h.files[old.TargetAuthID]["custom_policy"] = map[string]any{"priority": "preserve"}
	h.files[old.TargetAuthID]["proxy_url"] = "http://fixture.proxy:8080"
	h.mu.Unlock()
	a := importOfflineAccount(t, s)
	j := startOfflineAccount(t, s, a, old.TargetAuthID, "committed")
	if !j.CredentialEnabled || j.Automatic || j.TargetAuthID != old.TargetAuthID {
		t.Fatal("manual account relogin did not enable its selected target")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.saves != 2 || h.files[old.TargetAuthID]["disabled"] != false || h.files[old.TargetAuthID]["custom_policy"] == nil || h.files[old.TargetAuthID]["proxy_url"] != "http://fixture.proxy:8080" {
		t.Fatal("account enable changed unrelated policy or wrote more than once")
	}
}

func TestAccountAutomaticCommitRejectsChangedPolicyAndQualityPause(t *testing.T) {
	for _, mode := range []string{"changed_policy", "changed_disabled", "historical_pause"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			opts := offlineOptions(t)
			opts.Call = h.call
			var alter atomic.Bool
			opts.Prepare = func(_ context.Context, id, _, _ string) error {
				if !alter.Load() {
					return nil
				}
				if mode == "historical_pause" {
					return errors.New("quality_authority_unavailable")
				}
				h.mu.Lock()
				defer h.mu.Unlock()
				if mode == "changed_policy" {
					h.files[id]["note"] = "changed after admission"
				} else {
					h.files[id]["disabled"] = false
					e := h.entries[id]
					e.Disabled, e.Status = false, "active"
					h.entries[id] = e
				}
				return nil
			}
			s := offlineService(t, opts)
			old := commitCandidate(t, s, startCandidate(t, s, offlineStart()))
			a := importOfflineAccount(t, s)
			alter.Store(true)
			j := startOfflineAccount(t, s, a, old.TargetAuthID, "failed")
			if j.CredentialEnabled || h.saves != 1 {
				t.Fatal("changed or paused target was saved")
			}
			if mode != "historical_pause" && j.ErrorCode != "policy_changed" {
				t.Fatal("policy change was not reported")
			}
		})
	}
}

func TestAccountAutomaticCommitUnknownAndEnableConflictNeverReplay(t *testing.T) {
	for _, mode := range []string{"unknown", "runtime_missing", "runtime_disabled"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			h.saveError, h.adoptError, h.runtimeDisabled = mode == "unknown", mode == "runtime_missing", mode == "runtime_disabled"
			opts := offlineOptions(t)
			opts.Call = h.call
			s := offlineService(t, opts)
			a := importOfflineAccount(t, s)
			want := "saved_not_adopted"
			if mode == "unknown" {
				want = "commit_unknown"
			}
			j := startOfflineAccount(t, s, a, "", want)
			if j.CredentialEnabled || h.saves != 1 {
				t.Fatal("uncertain commit was reported enabled or replayed")
			}
			if mode == "runtime_disabled" && j.ErrorCode != "credential_enable_conflict" {
				t.Fatal("runtime enable conflict was not explicit")
			}
			if _, err := s.Commit(context.Background(), j.ID, j.CandidateID, j.Revision); err == nil {
				t.Fatal("uncertain write was submitted again")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s2 := offlineService(t, opts)
			old, err := s2.Detail(j.ID)
			if err != nil || old.Status != want || old.CredentialEnabled || h.saves != 1 {
				t.Fatal("restart changed an uncertain result or replayed save")
			}
		})
	}
}

func TestInspectionSkipsDisabledAndRechecksConsentBeforeSave(t *testing.T) {
	for _, mode := range []string{"disabled", "consent_changed", "disabled_during_prepare"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			opts := offlineOptions(t)
			opts.Call = h.call
			var change atomic.Bool
			var s *Service
			opts.Prepare = func(_ context.Context, id, _, _ string) error {
				if !change.Load() {
					return nil
				}
				if mode == "disabled_during_prepare" {
					h.mu.Lock()
					e := h.entries[id]
					e.Status = "disabled"
					h.entries[id] = e
					h.mu.Unlock()
					return nil
				}
				settings, err := s.AccountSettings()
				if err != nil {
					return err
				}
				settings.AutoReloginEnabled = false
				_, err = s.UpdateAccountSettings(settings)
				return err
			}
			s = offlineService(t, opts)
			a := importOfflineAccount(t, s)
			first := startOfflineAccount(t, s, a, "", "committed")
			items, _ := s.Accounts()
			a, err := s.UpdateAccount(AccountUpdate{ID: a.ID, Revision: items[0].Revision, AutoRelogin: true})
			if err != nil {
				t.Fatal(err)
			}
			settings, _ := s.AccountSettings()
			settings.InspectionEnabled, settings.AutoReloginEnabled = true, true
			if _, err = s.UpdateAccountSettings(settings); err != nil {
				t.Fatal(err)
			}
			h.mu.Lock()
			e := h.entries[first.TargetAuthID]
			e.Status, e.StatusMessage = "error", "unauthorized (refresh token invalid)"
			if mode == "disabled" {
				e.Status = "disabled"
			}
			h.entries[first.TargetAuthID] = e
			h.mu.Unlock()
			change.Store(true)
			s.accountMu.Lock()
			err = s.inspectAccountLocked(s.accounts[a.ID], true)
			s.accountMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			items, _ = s.Accounts()
			if mode == "disabled" {
				if items[0].LastJobID != first.ID {
					t.Fatal("inspection logged in a disabled account")
				}
			} else {
				j := waitLogin(t, s, items[0].LastJobID, "failed")
				if j.CredentialEnabled {
					t.Fatal("withdrawn automatic permission still enabled account")
				}
			}
			h.mu.Lock()
			if h.saves != 1 {
				t.Fatal("disabled or revoked automatic task reached save")
			}
			h.mu.Unlock()
		})
	}
}

func TestAccountCancellationDuringExchangeDoesNotCommitLateSuccess(t *testing.T) {
	h := newFakeHost()
	opts := offlineOptions(t)
	opts.Call = h.call
	entered, release := make(chan struct{}), make(chan struct{})
	opts.Exchange = func(context.Context, string, *codex.PKCECodes, string) (*codex.CodexAuthBundle, error) {
		close(entered)
		<-release
		return offlineBundle(t, time.Now(), "offline-account"), nil
	}
	s := offlineService(t, opts)
	a := importOfflineAccount(t, s)
	j, err := s.StartAccount(a.ID, a.Revision, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("exchange did not start")
	}
	_, err = s.Cancel(j.ID, false)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	j = waitLogin(t, s, j.ID, "cancelled")
	if j.CredentialEnabled || h.saves != 0 {
		t.Fatal("cancelled account login saved the late candidate")
	}
}
