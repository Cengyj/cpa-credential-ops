package login

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cliproxyapi-plugins/model-quality/quality"
)

// This offline host exposes only the account fixture's known file entries.
// The tests perform no model executions or credential disposition operations.
type accountRuleHost struct {
	quality.Host
	accounts *fakeHost
}

func (h accountRuleHost) Targets(context.Context) ([]quality.Target, error) {
	h.accounts.mu.Lock()
	defer h.accounts.mu.Unlock()
	var items []quality.Target
	for _, entry := range h.accounts.entries {
		items = append(items, quality.Target{AuthID: entry.ID, AuthIndex: entry.Index, Provider: entry.Provider, ObjectType: "oauth",
			RuntimeStatus: entry.Status, ManualDisabled: entry.Disabled, EffectiveAvailable: !entry.Disabled,
			IdentityVersion: "offline", Models: []string{"fixture-model"}})
	}
	return items, nil
}

func TestAccountReauthorizationCoordinatesCurrentRules(t *testing.T) {
	for _, mode := range []string{"manual", "automatic", "restart_after_receipt"} {
		t.Run(mode, func(t *testing.T) {
			host := newFakeHost()
			cfg := quality.DefaultConfig()
			cfg.DataDir = filepath.Join(t.TempDir(), "quality")
			open := func() *quality.Engine {
				en, err := quality.New(cfg, accountRuleHost{accounts: host})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = en.Close() })
				return en
			}
			engine := open()
			opts := offlineOptions(t)
			opts.Call = host.call
			var prepared, finished atomic.Int32
			var armed atomic.Bool
			configure := func(en *quality.Engine) {
				opts.PendingWrites = en.PendingCredentialLogins
				opts.Prepare = func(ctx context.Context, id, identity, preparation string) error {
					_, err := en.PrepareCredentialLogin(ctx, quality.CredentialLoginPrepareRequest{
						AuthID: id, ExpectedIdentity: identity, PreparationID: preparation,
					})
					if err == nil && armed.Load() && identity != "" {
						rules := en.Rules()
						if len(rules) != 1 || !rules[0].Enabled || rules[0].Status != "credential_update_pending" {
							return errors.New("fixture failed to isolate active rule")
						}
						prepared.Add(1)
					}
					return err
				}
				opts.FinishWrite = func(id, preparation, identity, state string) error {
					if mode == "restart_after_receipt" && prepared.Load() == 1 && armed.Load() {
						return errors.New("offline crash between stores")
					}
					if state == "committed" && prepared.Load() > 0 {
						finished.Add(1)
					}
					return en.FinishCredentialLogin(id, preparation, identity, state)
				}
			}
			configure(engine)
			service := offlineService(t, opts)
			a := importOfflineAccount(t, service)
			original := startOfflineAccount(t, service, a, "", "committed")
			_, err := engine.SaveRule(t.Context(), quality.Rule{
				RunConfig: quality.RunConfig{AuthID: original.TargetAuthID, Provider: "codex", Models: []string{"fixture-model"}, QuestionID: "logic-boxes"},
				Name:      "bound account", Action: "observe_only", Enabled: true,
			}, false)
			if err != nil {
				t.Fatal(err)
			}
			items, err := service.Accounts()
			if err != nil {
				t.Fatal(err)
			}
			armed.Store(true)
			if mode == "automatic" {
				if _, err = service.UpdateAccount(AccountUpdate{ID: a.ID, Revision: items[0].Revision, AutoRelogin: true}); err != nil {
					t.Fatal(err)
				}
				settings, _ := service.AccountSettings()
				settings.InspectionEnabled, settings.AutoReloginEnabled = true, true
				if _, err = service.UpdateAccountSettings(settings); err != nil {
					t.Fatal(err)
				}
				host.mu.Lock()
				entry := host.entries[original.TargetAuthID]
				entry.Status, entry.StatusMessage = "error", "invalid_grant"
				host.entries[entry.ID] = entry
				host.mu.Unlock()
				service.accountMu.Lock()
				err = service.inspectAccountLocked(service.accounts[a.ID], true)
				service.accountMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				items, _ = service.Accounts()
				result := waitLogin(t, service, items[0].LastJobID, "committed")
				if !result.Automatic || result.TargetAuthID != original.TargetAuthID {
					t.Fatal("automatic reauthorization lost the bound entry")
				}
			} else {
				result := startOfflineAccount(t, service, items[0], "", "committed")
				if result.TargetAuthID != original.TargetAuthID {
					t.Fatal("manual reauthorization changed the binding")
				}
			}
			if mode == "restart_after_receipt" {
				if engine.Rules()[0].Status != "credential_update_pending" || finished.Load() != 0 {
					t.Fatal("missing local acknowledgement was reported as resumed")
				}
				if err := service.Close(); err != nil {
					t.Fatal(err)
				}
				if err := engine.Close(); err != nil {
					t.Fatal(err)
				}
				engine = open()
				configure(engine)
				armed.Store(false)
				service = offlineService(t, opts)
				deadline := time.Now().Add(4 * time.Second)
				for time.Now().Before(deadline) && engine.Rules()[0].Status != "detecting" {
					time.Sleep(15 * time.Millisecond)
				}
				service.reconcileCredentialReceipts()
			}
			rules := engine.Rules()
			host.mu.Lock()
			saves := host.saves
			host.mu.Unlock()
			if prepared.Load() != 1 || finished.Load() != 1 || saves != 2 || len(rules) != 1 || !rules[0].Enabled || rules[0].Status != "detecting" || rules[0].SchedulingEnabled {
				t.Fatalf("prepared=%d completed=%d saves=%d rules=%+v", prepared.Load(), finished.Load(), saves, rules)
			}
			t.Logf("fresh authorizations=1 quality releases=1 total host saves=2 (initial + reauthorization)")
		})
	}
}

func TestAccountBindingRechecksAfterTransientSDKRead(t *testing.T) {
	host := newFakeHost()
	opts := offlineOptions(t)
	var temporarilyUnavailable atomic.Bool
	var offset atomic.Int64
	var rejected atomic.Int32
	base := time.Now().UTC()
	opts.Now = func() time.Time { return base.Add(time.Duration(offset.Load())) }
	opts.Call = func(method string, in, out any) error {
		if temporarilyUnavailable.Load() && method == "host.auth.list" {
			rejected.Add(1)
			return errors.New("fixture SDK bootstrap delayed")
		}
		return host.call(method, in, out)
	}
	opts.FinishWrite = func(_, _, _, state string) error {
		temporarilyUnavailable.Store(state == "committed")
		return nil
	}
	service := offlineService(t, opts)
	a := importOfflineAccount(t, service)
	job := startOfflineAccount(t, service, a, "", "committed")
	for range 4 {
		if _, err := service.Accounts(); err != nil {
			t.Fatal(err)
		}
	}
	temporarilyUnavailable.Store(false)
	offset.Store(int64(time.Minute))
	items, err := service.Accounts()
	if err != nil || len(items) != 1 || items[0].AuthID != job.TargetAuthID || items[0].AttentionRequired || rejected.Load() != 1 {
		t.Fatalf("committed receipt never re-bound after transient read: accounts=%v rejected=%d err=%v", len(items), rejected.Load(), err)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.saves != 1 {
		t.Fatal("binding review repeated an SDK save")
	}
}

func TestAcceptedCredentialWriteWithMissingReceiptRemainsUnknown(t *testing.T) {
	opts := offlineOptions(t)
	host := newFakeHost()
	opts.Call = host.call
	service := offlineService(t, opts)
	consumer := &Service{ctx: t.Context(), opts: Options{Manager: service.opts.Manager}}
	pending := map[string]string{"fixture.json": "accepted-before-restart"}
	consumer.opts.PendingWrites = func() map[string]string { return pending }
	var state string
	consumer.opts.FinishWrite = func(id, job, _ string, outcome string) error {
		if pending[id] != job {
			t.Fatal("recovered a different operation")
		}
		state = outcome
		delete(pending, id)
		return nil
	}
	consumer.reconcileCredentialReceipts()
	consumer.reconcileCredentialReceipts()
	if state != "commit_unknown" || host.saves != 0 {
		t.Fatal("accepted write with missing receipt was reclassified or replayed", state, host.saves)
	}
}
