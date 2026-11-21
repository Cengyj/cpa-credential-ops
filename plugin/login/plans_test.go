package login

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	codex "github.com/cliproxyapi-plugins/model-quality/login/oauth"
	bolt "go.etcd.io/bbolt"
)

func offlinePlanBundle(t *testing.T, plan string) *codex.CodexAuthBundle {
	t.Helper()
	bundle := offlineBundle(t, time.Now(), "offline-account")
	parts := strings.Split(bundle.TokenData.IDToken, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	d := ojson.NewDoc(raw)
	if err := d.Set("https://api\\.openai\\.com/auth.chatgpt_plan_type", plan); err != nil {
		t.Fatal(err)
	}
	token := parts[0] + "." + base64.RawURLEncoding.EncodeToString(d.Bytes()) + ".fixture"
	bundle.TokenData.IDToken, bundle.TokenData.AccessToken = token, token
	return bundle
}

func planOptions(t *testing.T, h *fakeHost, plan string) Options {
	t.Helper()
	opts := offlineOptions(t)
	opts.Call = h.call
	opts.Exchange = func(context.Context, string, *codex.PKCECodes, string) (*codex.CodexAuthBundle, error) {
		return offlinePlanBundle(t, plan), nil
	}
	return opts
}

func setPlanExclusions(t *testing.T, s *Service, plans ...string) AccountSettings {
	t.Helper()
	settings, err := s.AccountSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.CPAUploadExcludedPlans = plans
	settings, err = s.UpdateAccountSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestPlanSettingsStrictPartialUpdateAndLegacyPersistence(t *testing.T) {
	opts := offlineOptions(t)
	journal, err := openJournal(opts.Dir)
	if err != nil {
		t.Fatal(err)
	}
	legacy := struct {
		Revision        uint64
		IntervalMinutes int
	}{Revision: 7, IntervalMinutes: 15}
	if err := journal.db.Update(func(tx *bolt.Tx) error { return putGob(tx, "meta", "account-settings", legacy) }); err != nil {
		t.Fatal(err)
	}
	_ = journal.db.Close()
	s := offlineService(t, opts)
	settings, _ := s.AccountSettings()
	if settings.Revision != 7 || settings.CPAUploadExcludedPlans == nil || len(settings.CPAUploadExcludedPlans) != 0 {
		t.Fatal("legacy settings did not preserve revision and default to no exclusions")
	}
	settings = setPlanExclusions(t, s, " CHATGPT_PLUS ", "business", "plus")
	if !reflect.DeepEqual(settings.CPAUploadExcludedPlans, []string{"plus", "team"}) {
		t.Fatal("settings did not normalize and deduplicate reference labels")
	}
	settings.CPAUploadExcludedPlans[0] = "free"
	settings, _ = s.AccountSettings()
	if settings.CPAUploadExcludedPlans[0] != "plus" {
		t.Fatal("caller mutated the persisted settings slice")
	}
	put := func(extra map[string]any) error {
		current, _ := s.AccountSettings()
		body := map[string]any{"revision": current.Revision, "inspection_enabled": false, "auto_relogin_enabled": false, "interval_minutes": 15}
		for k, v := range extra {
			body[k] = v
		}
		raw, _ := Encode(body)
		_, err := credentialAccountWrite(s, http.MethodPut, "/accounts/settings", raw)
		return err
	}
	if err := put(nil); err != nil {
		t.Fatal(err)
	}
	settings, _ = s.AccountSettings()
	if !reflect.DeepEqual(settings.CPAUploadExcludedPlans, []string{"plus", "team"}) {
		t.Fatal("older settings client cleared an omitted exclusion list")
	}
	for _, invalid := range []any{nil, "plus", []any{true}, []string{"not-a-plan"}, []string{""}, []string{"chatgpt_unknown"}} {
		if err := put(map[string]any{"cpa_upload_excluded_plans": invalid}); err == nil {
			t.Fatalf("invalid exclusion setting was accepted: %#v", invalid)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = offlineService(t, opts)
	settings, _ = s.AccountSettings()
	if !reflect.DeepEqual(settings.CPAUploadExcludedPlans, []string{"plus", "team"}) {
		t.Fatal("exclusions did not survive restart")
	}
	if err := put(map[string]any{"cpa_upload_excluded_plans": []string{}}); err != nil {
		t.Fatal(err)
	}
	settings, _ = s.AccountSettings()
	if len(settings.CPAUploadExcludedPlans) != 0 {
		t.Fatal("explicit empty list did not clear exclusions")
	}
}

func TestCPAPlanExclusionRetainsCandidateAndAccountSnapshot(t *testing.T) {
	for _, mode := range []string{"manual", "one_action", "late_policy", "restart"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			opts := planOptions(t, h, "chatgpt_plus")
			var s *Service
			var late atomic.Bool
			if mode == "late_policy" {
				opts.Prepare = func(context.Context, string, string, string) error {
					if late.CompareAndSwap(false, true) {
						setPlanExclusions(t, s, "plus")
					}
					return nil
				}
			}
			s = offlineService(t, opts)
			if mode != "late_policy" {
				setPlanExclusions(t, s, "plus")
			}
			a := importOfflineAccount(t, s)
			var job Job
			if mode == "manual" {
				job = startCandidate(t, s, offlineStart())
				var err error
				job, err = s.Commit(context.Background(), job.ID, job.CandidateID, job.Revision)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				job = startOfflineAccount(t, s, a, "", "awaiting_commit")
			}
			job = waitLogin(t, s, job.ID, "awaiting_commit")
			candidate, err := s.Candidate(job.CandidateID)
			if err != nil || candidate.PlanType != "plus" || job.PlanType != "plus" || job.ErrorCode != "cpa_plan_excluded" || h.saves != 0 {
				t.Fatalf("excluded plan wrote or lost review metadata: %+v %+v %v", job, candidate, err)
			}
			if mode != "manual" {
				items, err := s.Accounts()
				if err != nil || items[0].PlanType != "plus" || items[0].AuthID != "" {
					t.Fatal("excluded grant did not retain its account plan without claiming a CPA binding")
				}
			}
			if mode == "restart" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s = offlineService(t, opts)
				job, _ = s.Detail(job.ID)
				items, _ := s.Accounts()
				if job.Status != "interrupted" || job.CandidateID != "" || items[0].PlanType != "plus" || h.saves != 0 {
					t.Fatal("restart replayed a candidate or lost its observed plan")
				}
				return
			}
			setPlanExclusions(t, s)
			job = commitCandidate(t, s, job)
			if h.saves != 1 || h.files[job.TargetAuthID]["plan_type"] != "plus" {
				t.Fatal("explicit retry did not save plan metadata exactly once")
			}
		})
	}
}

func TestCPAPlanPolicyUsesKnownBoundPlanAndExplicitUnknown(t *testing.T) {
	for _, mode := range []string{"bound_plus", "unknown_blocked", "known_not_unknown"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			plan := "plus"
			if mode == "unknown_blocked" {
				plan = ""
			}
			opts := planOptions(t, h, plan)
			s := offlineService(t, opts)
			target := ""
			if mode == "bound_plus" {
				old := commitCandidate(t, s, startCandidate(t, s, offlineStart()))
				target = old.TargetAuthID
				s.opts.Exchange = func(context.Context, string, *codex.PKCECodes, string) (*codex.CodexAuthBundle, error) {
					return offlinePlanBundle(t, "free"), nil
				}
				setPlanExclusions(t, s, "plus")
			} else {
				setPlanExclusions(t, s, "unknown")
			}
			a := importOfflineAccount(t, s)
			want := "awaiting_commit"
			if mode == "known_not_unknown" {
				want = "committed"
			}
			job := startOfflineAccount(t, s, a, target, want)
			if want == "awaiting_commit" && job.ErrorCode != "cpa_plan_excluded" {
				t.Fatal("plan policy was not the reason the upload stopped")
			}
			if mode == "bound_plus" && h.saves != 1 || mode == "unknown_blocked" && h.saves != 0 || mode == "known_not_unknown" && h.saves != 1 {
				t.Fatal("plan exclusion matched the wrong known/unknown source")
			}
		})
	}
}

func TestAutomaticPlanRetryPreservesEveryRepairPermission(t *testing.T) {
	for _, mode := range []string{"only_exclusion", "global_disabled", "account_disabled", "credential_disabled"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			s := offlineService(t, planOptions(t, h, "plus"))
			a := importOfflineAccount(t, s)
			first := startOfflineAccount(t, s, a, "", "committed")
			items, _ := s.Accounts()
			if _, err := s.UpdateAccount(AccountUpdate{ID: a.ID, Revision: items[0].Revision, AutoRelogin: true}); err != nil {
				t.Fatal(err)
			}
			settings, _ := s.AccountSettings()
			settings.InspectionEnabled, settings.AutoReloginEnabled = true, true
			settings.CPAUploadExcludedPlans = []string{"plus"}
			if _, err := s.UpdateAccountSettings(settings); err != nil {
				t.Fatal(err)
			}
			h.mu.Lock()
			entry := h.entries[first.TargetAuthID]
			entry.Status, entry.StatusMessage = "error", "unauthorized (refresh token invalid)"
			h.entries[first.TargetAuthID] = entry
			h.mu.Unlock()
			s.accountMu.Lock()
			err := s.inspectAccountLocked(s.accounts[a.ID], true)
			s.accountMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			items, _ = s.Accounts()
			job := waitLogin(t, s, items[0].LastJobID, "awaiting_commit")
			settings, _ = s.AccountSettings()
			settings.CPAUploadExcludedPlans = []string{}
			if mode == "global_disabled" {
				settings.AutoReloginEnabled = false
			}
			if _, err := s.UpdateAccountSettings(settings); err != nil {
				t.Fatal(err)
			}
			if mode == "account_disabled" {
				items, _ = s.Accounts()
				if _, err := s.UpdateAccount(AccountUpdate{ID: a.ID, Revision: items[0].Revision, AutoRelogin: false}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "credential_disabled" {
				h.mu.Lock()
				entry = h.entries[first.TargetAuthID]
				entry.Disabled, entry.Status = true, "disabled"
				h.entries[first.TargetAuthID] = entry
				h.mu.Unlock()
			}
			_, err = s.Commit(context.Background(), job.ID, job.CandidateID, job.Revision)
			if mode == "only_exclusion" {
				if err != nil {
					t.Fatal(err)
				}
				waitLogin(t, s, job.ID, "committed")
				if h.saves != 2 {
					t.Fatal("exclusion-only change prevented permitted explicit repair retry")
				}
			} else {
				if err == nil {
					waitLogin(t, s, job.ID, "failed")
				}
				if h.saves != 1 {
					t.Fatal("disabled repair permission or credential was bypassed by explicit retry")
				}
			}
		})
	}
}

func TestAccountCurrentDisplayKeepsLocalPlanUploadVeto(t *testing.T) {
	h := newFakeHost()
	s := offlineService(t, planOptions(t, h, "plus"))
	a := importOfflineAccount(t, s)
	first := startOfflineAccount(t, s, a, "", "committed")
	if _, err := s.Accounts(); err != nil {
		t.Fatal(err)
	}
	// Simulate a previously observed local grant without changing the bound
	// credential: both observations must retain their separate exclusion veto.
	s.accountMu.Lock()
	stored := s.accounts[a.ID]
	stored.View.PlanType = "pro"
	err := s.saveAccountLocked(stored)
	s.accountMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	setPlanExclusions(t, s, "pro")
	items, err := s.Accounts()
	if err != nil || items[0].PlanType != "plus" {
		t.Fatal("account did not display the current identity-matched CPA plan")
	}
	job := startOfflineAccount(t, s, items[0], "", "awaiting_commit")
	if job.ErrorCode != "cpa_plan_excluded" || job.PlanType != "plus" || h.saves != 1 {
		t.Fatal("current display plan hid an excluded local observation from upload policy")
	}
	h.mu.Lock()
	h.files[first.TargetAuthID]["plan_type"] = "enterprise"
	h.mu.Unlock()
	items, err = s.Accounts()
	if err != nil || items[0].PlanType != "enterprise" {
		t.Fatal("account plan remained stale after CPA metadata changed")
	}
}

func TestSecurityPlanExclusionPreservesConfirmedSeed(t *testing.T) {
	h := newFakeHost()
	opts := planOptions(t, h, "plus")
	opts.Runtime = offlineRuntime{run: func(_ context.Context, in RuntimeRequest) (string, error) {
		if err := writeSecurityFixture(in, securityFixtureJournal(in)); err != nil {
			return "", err
		}
		return offlineCallback(in.State), nil
	}}
	s := offlineService(t, opts)
	setPlanExclusions(t, s, "plus")
	a := importOfflineAccount(t, s)
	job, err := s.StartAccountSecurity(a.ID, a.Revision, "reset_totp", true, true)
	if err != nil {
		t.Fatal(err)
	}
	job = waitLogin(t, s, job.ID, "awaiting_commit")
	detail, err := s.AccountDetail(a.ID)
	if err != nil || detail.Credentials.TOTPSecret != securityFixtureSeed || !job.TOTPReset || !job.DevicesLoggedOut || job.ErrorCode != "cpa_plan_excluded" || h.saves != 0 {
		t.Fatal("upload exclusion lost a confirmed security mutation or saved an excluded credential")
	}
}
