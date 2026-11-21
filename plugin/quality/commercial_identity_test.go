package quality

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestCommercialAcknowledgmentRequiresCurrentFileResolution(t *testing.T) {
	for _, state := range []string{"disabled", "attention"} {
		for _, scenario := range []string{"runtime-omission", "stale-enabled-runtime", "confirmed-removal", "confirmed-enable"} {
			t.Run(state+"/"+scenario, func(t *testing.T) {
				f := managementTestFixture(t)
				rule := f.rule(t, disableCredential, false, true)
				rule.UpstreamErrorActions = map[string]string{"unauthorized": disableCredential}
				var err error
				rule, err = f.engine.SaveRule(context.Background(), *rule, true)
				if err != nil {
					t.Fatal(err)
				}
				f.upstreamStatus, f.statusFailure = 401, state == "attention"
				if _, err := f.round(t, rule); err != nil {
					t.Fatal(err)
				}
				if state == "attention" {
					f.verifyManagement(t)
				}
				old := *f.engine.managed["one"]
				if old.State != state {
					t.Fatalf("expected %s setup, got %s", state, old.State)
				}
				beforeProbes, beforeWrites := f.probes, len(f.patches)
				callback := f.host.Call
				switch scenario {
				case "runtime-omission":
					f.host.Call = func(method string, in, out any) error {
						if method == "host.auth.list" {
							return json.Unmarshal([]byte(`{"files":[]}`), out)
						}
						if method == "host.auth.models" && in.(pluginapi.HostAuthModelsRequest).AuthIndex == "" {
							*out.(*pluginapi.HostAuthModelsResponse) = pluginapi.HostAuthModelsResponse{}
							return nil
						}
						return callback(method, in, out)
					}
				case "stale-enabled-runtime":
					f.host.Call = func(method string, in, out any) error {
						if method == "host.auth.list" {
							entry := f.entries["one"]
							entry.Disabled, entry.Status = false, "active"
							raw, _ := json.Marshal(map[string]any{"files": []pluginapi.HostAuthFileEntry{entry}})
							return json.Unmarshal(raw, out)
						}
						return callback(method, in, out)
					}
				case "confirmed-removal":
					delete(f.entries, "one")
				case "confirmed-enable":
					entry := f.entries["one"]
					entry.Disabled, entry.Status = false, "active"
					f.entries["one"] = entry
				}
				err = f.engine.acknowledgeHandling(context.Background(), rule.ID, "one", old.ActionID)
				wantResolved := scenario == "confirmed-removal" || scenario == "confirmed-enable"
				if (err == nil) != wantResolved || activeDisposition(f.engine.managed["one"]) == wantResolved {
					t.Fatalf("acknowledgment accepted unverified resolution or rejected verified resolution: state=%s err=%v", f.engine.managed["one"].State, err)
				}
				if f.probes != beforeProbes || len(f.patches) != beforeWrites || f.deletes != 0 || !f.engine.rules[rule.ID].Enabled {
					t.Fatal("local acknowledgment called upstream, wrote CPA, or stopped the rule")
				}
			})
		}
	}
}

type commercialGroupInventoryHost struct {
	*groupPolicyFixture
	omit       string
	unverified bool
}

func (h *commercialGroupInventoryHost) Targets(ctx context.Context) ([]Target, error) {
	if status, ok := ctx.Value(providerInventoryStatusKey{}).(*providerInventoryStatus); ok {
		status.Verified = !h.unverified
	}
	targets, err := h.groupPolicyFixture.Targets(ctx)
	var result []Target
	for _, target := range targets {
		if target.AuthID != h.omit {
			result = append(result, target)
		}
	}
	return result, err
}

func TestCommercialGroupAcknowledgmentRequiresCompleteCurrentMembership(t *testing.T) {
	for _, scenario := range []string{"complete", "omitted-member", "member-added", "added-member-omitted", "rotated-enabled", "rotated-disabled", "confirmed-removal", "unverified-removal"} {
		t.Run(scenario, func(t *testing.T) {
			en, base, rule := retainedProviderGroup(t)
			for id, snapshot := range base.snapshots {
				snapshot.Disabled = false
				base.snapshots[id] = snapshot
			}
			host := &commercialGroupInventoryHost{groupPolicyFixture: base}
			blocked := false
			switch scenario {
			case "omitted-member":
				host.omit = "b"
				blocked = true
			case "member-added", "added-member-omitted":
				added := base.snapshots["a"]
				added.AuthID, added.AuthIndex = "c", "ix-c"
				base.snapshots["c"] = added
				for id, snapshot := range base.snapshots {
					snapshot.ProviderMembers = []string{"a", "b", "c"}
					base.snapshots[id] = snapshot
				}
				if scenario == "added-member-omitted" {
					host.omit, blocked = "c", true
				}
			case "rotated-enabled", "rotated-disabled":
				current := base.snapshots["a"]
				base.snapshots = map[string]CredentialSnapshot{}
				blocked = scenario == "rotated-disabled"
				for _, id := range []string{"c", "d"} {
					current.AuthID, current.AuthIndex = id, "ix-"+id
					current.Disabled, current.ProviderMembers = blocked, []string{"c", "d"}
					base.snapshots[id] = current
				}
			case "confirmed-removal", "unverified-removal":
				base.snapshots = map[string]CredentialSnapshot{}
				host.unverified = scenario == "unverified-removal"
				blocked = host.unverified
			}
			en.host = host
			err := en.acknowledgeHandling(context.Background(), rule.ID, "a", en.managed["a"].ActionID)
			if (err != nil) != blocked {
				t.Fatalf("unexpected complete-group verification: scenario=%s err=%v", scenario, err)
			}
			for _, id := range []string{"a", "b"} {
				if activeDisposition(en.managed[id]) != blocked {
					t.Fatal("group acknowledgment partially or prematurely cleared ownership", id)
				}
			}
			if len(base.writes) != 0 {
				t.Fatal("local acknowledgment wrote CPA")
			}
		})
	}
}

type commercialConfigFailureTransport struct {
	base http.RoundTripper
	code int
}

func (h commercialConfigFailureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == providerConfigPath {
		return &http.Response{StatusCode: h.code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	}
	return h.base.RoundTrip(r)
}

func TestCommercialFileAcknowledgmentIsIndependentOfProviderConfig(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable} {
		f := managementTestFixture(t)
		rule := f.rule(t, disableCredential, false, true)
		f.answers["a"] = "5"
		if _, err := f.round(t, rule); err != nil {
			t.Fatal(err)
		}
		before := f.probes
		entry := f.entries["one"]
		entry.Disabled, entry.Status = false, "active"
		f.entries["one"] = entry
		client := f.host.Management.client
		client.Transport = commercialConfigFailureTransport{base: client.Transport, code: code}
		if err := f.engine.acknowledgeHandling(context.Background(), rule.ID, "one", f.engine.managed["one"].ActionID); err != nil {
			t.Fatalf("unrelated provider config HTTP %d blocked verified file resolution: %v", code, err)
		}
		if activeDisposition(f.engine.managed["one"]) || len(f.patches) != 1 || f.probes != before {
			t.Fatal("file acknowledgment changed CPA state or invoked a model")
		}
	}
}

func TestCommercialUncertainRecoveryRemainsQuarantinedUntilExplicitResolution(t *testing.T) {
	f := managementTestFixture(t)
	rule := f.rule(t, disableCredential, true, true)
	f.answers["a"] = "5"
	if _, err := f.round(t, rule); err != nil {
		t.Fatal(err)
	}
	f.answers["a"], f.statusFailure = "4", true
	runs, err := f.round(t, rule)
	if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_action_uncertain" || f.entries["one"].Disabled {
		t.Fatal("expected a recovery written to CPA before its HTTP error", runs, err)
	}
	before := f.probes
	f.statusFailure = false
	if err := f.engine.Close(); err != nil {
		t.Fatal(err)
	}
	f.engine, err = New(f.engine.cfg, f.host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.engine.Close() })
	if _, err := f.engine.visibleTargets(context.Background()); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.engine.dispatchRule(context.Background(), rule.ID, rule.Revision, false)
	if err != nil || len(receipt.PreflightDetails) != 1 || receipt.PreflightDetails[0].Code != "handling_blocked" || f.engine.nextRunnableLocked() != nil || f.engine.managed["one"].State != "attention" || len(f.patches) != 2 || f.probes != before {
		t.Fatal("restart, enabled observation or dispatch replayed an uncertain write", receipt, err)
	}
	if err := f.engine.acknowledgeHandling(context.Background(), rule.ID, "one", f.engine.managed["one"].ActionID); err != nil {
		t.Fatal("explicit verification could not resolve the enabled credential", err)
	}
	if activeDisposition(f.engine.managed["one"]) || len(f.patches) != 2 || f.probes != before {
		t.Fatal("explicit verification made an extra model call or CPA write")
	}
}

type commercialPreconditionHost struct {
	*NativeHost
	beforeWrite func()
}

func (h *commercialPreconditionHost) SetCredentialDisabled(ctx context.Context, snapshot CredentialSnapshot, disabled bool) (CredentialSnapshot, error) {
	h.beforeWrite()
	return h.NativeHost.SetCredentialDisabled(ctx, snapshot, disabled)
}

func (h *commercialPreconditionHost) DeleteCredential(ctx context.Context, snapshot CredentialSnapshot) error {
	h.beforeWrite()
	return h.NativeHost.DeleteCredential(ctx, snapshot)
}

func TestCommercialLocalPreconditionFailureDoesNotQuarantineCredential(t *testing.T) {
	for _, operation := range []string{disableCredential, deleteCredential, "restore_credential"} {
		for _, change := range []string{"file content changed", "runtime source changed"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				f := managementTestFixture(t)
				action := operation
				if operation == "restore_credential" {
					action = disableCredential
				}
				rule := f.rule(t, action, change == "file content changed", operation == "restore_credential")
				f.answers["a"] = "5"
				var prior *CredentialDisposition
				if operation == "restore_credential" {
					if _, err := f.round(t, rule); err != nil {
						t.Fatal(err)
					}
					prior = f.engine.managed["one"]
					f.answers["a"] = "4"
				}
				beforeWrites, beforeDeletes, beforeProbes := len(f.patches), f.deletes, f.probes
				replaceSource := func(source string) {
					entry := f.entries["one"]
					entry.Source = source
					f.entries["one"] = entry
				}
				f.engine.host = &commercialPreconditionHost{NativeHost: f.host, beforeWrite: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					if change == "runtime source changed" {
						replaceSource("memory")
					} else {
						f.changedContent = true
					}
				}}
				runs, err := f.round(t, rule)
				if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_changed" {
					t.Fatalf("local precondition rejection became an uncertain write: runs=%v err=%v", runs, err)
				}
				if len(f.patches) != beforeWrites || f.deletes != beforeDeletes || f.probes-beforeProbes != len(rule.Models)*rule.Samples {
					t.Fatal("the completed round did not stop before the CPA mutation", f.patches, f.deletes, f.probes-beforeProbes)
				}
				current := f.engine.managed["one"]
				if prior != nil {
					if current.State != "disabled" || current.ActionID != prior.ActionID || answerOwner(current) != rule.ID {
						t.Fatal("failed recovery discarded confirmed answer ownership", current)
					}
				} else if activeDisposition(current) {
					t.Fatal("definitely unwritten action quarantined the credential", current)
				}
				if change == "runtime source changed" {
					f.mu.Lock()
					replaceSource("file")
					f.mu.Unlock()
					f.engine.host = f.host
				}
				runs, err = f.round(t, rule)
				want := map[string]string{disableCredential: "credential_suspended", deleteCredential: "credential_removed", "restore_credential": "credential_restored"}[operation]
				if err != nil || len(runs) != 1 || runs[0].ActionStatus != want {
					t.Fatal("fresh evidence could not use the current credential after local precondition rejection", runs, err)
				}
			})
		}
	}
}
