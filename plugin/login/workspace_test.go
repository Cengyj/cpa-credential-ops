package login

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	codex "github.com/cliproxyapi-plugins/model-quality/login/oauth"
)

func TestBoundLoginOperationsPinExpectedWorkspace(t *testing.T) {
	for _, mode := range []string{"manual_oauth", "account", "automatic", "security"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			opts := offlineOptions(t)
			opts.Call = h.call
			requests := make(chan RuntimeRequest, 4)
			opts.Runtime = offlineRuntime{run: func(_ context.Context, in RuntimeRequest) (string, error) {
				requests <- in
				if in.Operation != "" {
					if err := writeSecurityFixture(in, securityFixtureJournal(in)); err != nil {
						return "", err
					}
				}
				return offlineCallback(in.State), nil
			}}
			s := offlineService(t, opts)
			a := importOfflineAccount(t, s)
			first := startOfflineAccount(t, s, a, "", "committed")
			if in := <-requests; in.WorkspaceID != "" {
				t.Fatal("new account unexpectedly inherited a workspace")
			}
			items, _ := s.Accounts()
			a = items[0]
			var next Job
			var err error
			switch mode {
			case "manual_oauth":
				snapshot, snapshotErr := s.opts.Manager.LoginSnapshot(first.TargetAuthID)
				if snapshotErr != nil {
					t.Fatal(snapshotErr)
				}
				r := offlineStart()
				r.Intent, r.TargetAuthID, r.ExpectedIdentityVersion = "reauthorize", first.TargetAuthID, snapshot.IdentityVersion
				next = startCandidate(t, s, r)
			case "account":
				next = startOfflineAccount(t, s, a, "", "committed")
			case "automatic":
				if _, err = s.UpdateAccount(AccountUpdate{ID: a.ID, Revision: a.Revision, AutoRelogin: true}); err != nil {
					t.Fatal(err)
				}
				settings, _ := s.AccountSettings()
				settings.InspectionEnabled, settings.AutoReloginEnabled = true, true
				if _, err = s.UpdateAccountSettings(settings); err != nil {
					t.Fatal(err)
				}
				h.mu.Lock()
				entry := h.entries[first.TargetAuthID]
				entry.Status, entry.StatusMessage = "error", "unauthorized (refresh token invalid)"
				h.entries[first.TargetAuthID] = entry
				h.mu.Unlock()
				s.accountMu.Lock()
				err = s.inspectAccountLocked(s.accounts[a.ID], true)
				s.accountMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				items, _ = s.Accounts()
				next = waitLogin(t, s, items[0].LastJobID, "committed")
			case "security":
				next, err = s.StartAccountSecurity(a.ID, a.Revision, "reset_totp", true, true)
				if err != nil {
					t.Fatal(err)
				}
				next = waitLogin(t, s, next.ID, "committed")
			}
			in := <-requests
			if in.WorkspaceID != "offline-account" || next.TargetAuthID != first.TargetAuthID {
				t.Fatalf("%s did not pin its expected workspace: %q", mode, in.WorkspaceID)
			}
			if mode == "security" && (in.Operation != "reset_totp" || !in.Reauthorize || !in.LogoutAfterReset) {
				t.Fatal("bound security operation lost its requested flow")
			}
		})
	}
}

func TestReauthorizationRefusesUnreadableOrReplacedWorkspaceBeforeRuntime(t *testing.T) {
	for _, mode := range []string{"unreadable", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			opts := offlineOptions(t)
			var armed atomic.Bool
			var reads, runtimeCalls atomic.Int32
			opts.Call = func(method string, in, out any) error {
				if armed.Load() && method == "host.auth.get" && reads.Add(1) == 2 {
					if mode == "unreadable" {
						return errors.New("offline read failure")
					}
					changed := offlineBundle(t, time.Now(), "changed-workspace")
					h.mu.Lock()
					for _, file := range h.files {
						file["id_token"], file["access_token"], file["account_id"] = changed.TokenData.IDToken, changed.TokenData.AccessToken, "changed-workspace"
					}
					h.mu.Unlock()
				}
				return h.call(method, in, out)
			}
			opts.Runtime = offlineRuntime{run: func(_ context.Context, in RuntimeRequest) (string, error) {
				runtimeCalls.Add(1)
				return offlineCallback(in.State), nil
			}}
			s := offlineService(t, opts)
			first := commitCandidate(t, s, startCandidate(t, s, offlineStart()))
			snapshot, err := s.opts.Manager.LoginSnapshot(first.TargetAuthID)
			if err != nil {
				t.Fatal(err)
			}
			r := offlineStart()
			r.Intent, r.TargetAuthID, r.ExpectedIdentityVersion, r.ProxyURL = "reauthorize", first.TargetAuthID, snapshot.IdentityVersion, "direct"
			armed.Store(true)
			_, err = s.Start(r)
			code := "identity_changed"
			if mode == "unreadable" {
				code = "storage_unavailable"
			}
			if err == nil || err.Error() != code || runtimeCalls.Load() != 1 || h.saves != 1 {
				t.Fatalf("changed workspace started an unpinned login: %v", err)
			}
		})
	}
}

func TestSecurityReauthorizationRejectsDifferentWorkspaceAfterConfirmedReset(t *testing.T) {
	h := newFakeHost()
	opts := offlineOptions(t)
	opts.Call = h.call
	opts.Runtime = offlineRuntime{run: func(_ context.Context, in RuntimeRequest) (string, error) {
		if err := writeSecurityFixture(in, securityFixtureJournal(in)); err != nil {
			return "", err
		}
		return offlineCallback(in.State), nil
	}}
	opts.Exchange = func(context.Context, string, *codex.PKCECodes, string) (*codex.CodexAuthBundle, error) {
		return offlineBundle(t, time.Now(), "different-workspace"), nil
	}
	s := offlineService(t, opts)
	a := importOfflineAccount(t, s)
	job, err := s.StartAccountSecurity(a.ID, a.Revision, "reset_totp", true, true)
	if err != nil {
		t.Fatal(err)
	}
	job = waitLogin(t, s, job.ID, "conflict")
	detail, err := s.AccountDetail(a.ID)
	if err != nil || job.ErrorCode != "identity_conflict" || !job.TOTPReset || !job.DevicesLoggedOut || detail.Credentials.TOTPSecret != securityFixtureSeed || h.saves != 0 {
		t.Fatal("security reauthorization saved a different workspace or lost the confirmed new seed")
	}
	s.mu.Lock()
	pinned := s.jobs[job.ID].record.SecurityAccountID
	s.mu.Unlock()
	if pinned != "offline-account" {
		t.Fatal("verified security workspace was not retained in the job")
	}
}

func TestBoundReauthorizationRejectsDifferentWorkspaceCallback(t *testing.T) {
	h := newFakeHost()
	opts := offlineOptions(t)
	opts.Call = h.call
	var changed atomic.Bool
	opts.Exchange = func(context.Context, string, *codex.PKCECodes, string) (*codex.CodexAuthBundle, error) {
		workspace := "offline-account"
		if changed.Load() {
			workspace = "different-workspace"
		}
		return offlineBundle(t, time.Now(), workspace), nil
	}
	s := offlineService(t, opts)
	a := importOfflineAccount(t, s)
	first := startOfflineAccount(t, s, a, "", "committed")
	items, _ := s.Accounts()
	changed.Store(true)
	job := startOfflineAccount(t, s, items[0], "", "conflict")
	if job.ErrorCode != "identity_conflict" || h.saves != 1 || h.files[first.TargetAuthID]["account_id"] != "offline-account" {
		t.Fatal("upstream ignored the workspace pin and replaced the bound credential")
	}
}
