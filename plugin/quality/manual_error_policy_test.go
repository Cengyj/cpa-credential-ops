package quality

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type manualPolicyFixture struct {
	*managementFixture
	config    RunConfig
	owner     *CredentialDisposition
	file      map[string]any
	fileError bool
	afterCall func()
}

func manualPolicyTestFixture(t *testing.T) *manualPolicyFixture {
	t.Helper()
	f := &manualPolicyFixture{managementFixture: managementTestFixture(t), config: lifeConfig()}
	f.config.AuthID, f.config.Provider, f.config.Judge.Mode = "one", "codex", "manual"
	f.config.Samples, f.config.IdempotencyKey = 2, "manual-reenabled"
	f.file = map[string]any{"type": "codex", "email": "person@example.test", "account_id": "offline-account", "access_token": "old", "disabled": true}
	entry := f.entries["one"]
	entry.Email = "person@example.test"
	entry.Disabled, entry.Status = true, "disabled"
	f.entries["one"] = entry
	callback := f.host.Call
	f.host.Call = func(method string, in, out any) error {
		if method == "host.model.execute" {
			f.nativeCalls++
			if f.afterCall != nil {
				f.afterCall()
			}
			*out.(*pluginapi.HostModelExecutionResponse) = pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: []byte(`{"choices":[{"message":{"content":"4"},"finish_reason":"stop"}]}`)}
			return nil
		}
		if err := callback(method, in, out); err != nil {
			return err
		}
		if method == "host.auth.get" && in.(pluginapi.HostAuthGetRequest).AuthIndex == "one" {
			if f.fileError {
				return errors.New("synthetic read error")
			}
			raw, err := json.Marshal(f.file)
			if err != nil {
				return err
			}
			out.(*pluginapi.HostAuthGetResponse).JSON = raw
		}
		return nil
	}
	s, err := f.host.CredentialSnapshot(context.Background(), "one", "one")
	if err != nil {
		t.Fatal(err)
	}
	s.Disabled = true
	f.owner = &CredentialDisposition{AuthID: "one", RuleID: "owner", RunID: "source", ActionID: "old-action", Operation: disableCredential,
		State: "disabled", Snapshot: s, Email: entry.Email, UpstreamCondition: "unauthorized"}
	f.engine.managed["one"] = f.owner
	f.file["disabled"], f.file["access_token"] = false, "refreshed"
	entry.Disabled, entry.Status = false, "active"
	f.entries["one"] = entry
	return f
}

func TestManualTestWithExternallyEnabledErrorSuspension(t *testing.T) {
	for _, tc := range []string{"account fingerprint", "legacy email", "legacy unmarked enabled file", "ordinary quality suspension"} {
		t.Run(tc, func(t *testing.T) {
			f := manualPolicyTestFixture(t)
			switch tc {
			case "legacy email", "legacy unmarked enabled file":
				f.owner.Snapshot.AccountFingerprint = ""
				if tc == "legacy unmarked enabled file" {
					delete(f.file, "disabled")
				}
			case "ordinary quality suspension":
				f.owner.UpstreamCondition = ""
			}
			r, err := f.engine.Submit(context.Background(), f.config)
			if err != nil || r.Config.ManagedProbe != nil || r.Config.ManualPolicyActionID != "" || r.Config.ManualAccountFingerprint == "" {
				t.Fatal("manual test did not follow the native execution path", err)
			}
			f.file["access_token"] = "rotated-again"
			f.engine.execute(r.ID)
			row, err := f.engine.Run(r.ID)
			if err != nil || row.Status != "completed" || len(row.Samples) != 2 || row.Samples[1].Output != "4" ||
				f.nativeCalls != 2 || f.probes != 0 || len(f.patches)+f.deletes != 0 || activeDisposition(f.engine.managed["one"]) || f.engine.managed["one"].State != "externally_enabled" || f.engine.managed["one"].ActionID != f.owner.ActionID {
				t.Fatal("manual test changed ownership or used a management probe", err, row.Status, f.nativeCalls, f.probes)
			}
			f.file["disabled"] = true
			prior, err := f.engine.Submit(context.Background(), f.config)
			if err != nil || prior.ID != r.ID || f.nativeCalls != 2 {
				t.Fatal("resubmission should return the existing receipt", err)
			}
		})
	}
}

func TestManualReenabledEntryUsesCurrentAccount(t *testing.T) {
	for _, mode := range []string{"account replaced", "legacy missing email", "old locator", "runtime email", "file email"} {
		t.Run(mode, func(t *testing.T) {
			f := manualPolicyTestFixture(t)
			switch mode {
			case "runtime email":
				entry := f.entries["one"]
				entry.Email = "different@example.test"
				f.entries["one"] = entry
			case "file email":
				f.file["email"] = "different@example.test"
			case "account replaced":
				f.file["account_id"] = "replacement"
				f.file["email"] = "replacement@example.test"
				entry := f.entries["one"]
				entry.Email = "replacement@example.test"
				f.entries["one"] = entry
			case "legacy missing email":
				f.owner.Snapshot.AccountFingerprint, f.owner.Email = "", ""
			case "old locator":
				f.owner.Snapshot.AuthIndex = "previous-index"
			}
			run, err := f.engine.Submit(context.Background(), f.config)
			if err != nil {
				t.Fatal(err)
			}
			f.engine.execute(run.ID)
			result, err := f.engine.Run(run.ID)
			if err != nil || result.Status != "completed" || f.nativeCalls != 2 || f.probes != 0 || len(f.patches) != 0 || f.deletes != 0 || activeDisposition(f.engine.managed["one"]) {
				t.Fatal("current enabled entry retained a historical account restriction", result, err)
			}
		})
	}
}

func TestManualErrorSuspensionFailsClosed(t *testing.T) {
	for _, reason := range []string{"runtime disabled", "file disabled", "malformed flag", "file read", "file type", "missing model", "conflicting catalog", "unknown write", "deleted", "quality pause", "control", "judge"} {
		t.Run(reason, func(t *testing.T) {
			f := manualPolicyTestFixture(t)
			e := f.entries["one"]
			switch reason {
			case "runtime disabled":
				e.Disabled, e.Status = true, "disabled"
			case "file disabled":
				f.file["disabled"] = true
			case "malformed flag":
				f.file["disabled"] = "false"
			case "file read":
				f.fileError = true
			case "file type":
				f.file["type"] = "other"
			case "runtime email":
				e.Email = "different@example.test"
			case "file email":
				f.file["email"] = "different@example.test"
			case "account replaced":
				f.file["account_id"] = "replacement"
			case "legacy missing email":
				f.owner.Snapshot.AccountFingerprint, f.owner.Email = "", ""
			case "wrong index":
				f.owner.Snapshot.AuthIndex = "old-index"
			case "missing model":
				f.config.Models = []string{"removed-model"}
			case "conflicting catalog":
				f.entries["duplicate"] = e
			case "unknown write":
				f.owner.State = "attention"
			case "deleted":
				f.owner.State = "deleted"
			case "quality pause":
				f.engine.rules["legacy"] = &Rule{ID: "legacy", RunConfig: RunConfig{AuthID: "one"}, ActionID: "independent"}
			case "control", "judge":
				f.add("two")
				f.config.AuthID = "two"
				if reason == "control" {
					f.config.ControlAuthID, f.config.ControlProvider = "one", "codex"
				} else {
					f.config.Judge = Judge{Mode: "llm", AuthID: "one", Provider: "codex", Model: "a"}
				}
			}
			f.entries["one"] = e
			_, err := f.engine.Submit(context.Background(), f.config)
			if err == nil || !strings.HasPrefix(err.Error(), "blocked:") && !strings.HasPrefix(err.Error(), "model not available") || len(f.engine.runs) != 0 || f.nativeCalls+f.probes+len(f.patches)+f.deletes != 0 {
				t.Fatal("unsafe manual admission", err)
			}
		})
	}
}

func TestManualErrorSuspensionRecheckedDuringExecution(t *testing.T) {
	for _, reason := range []string{"disabled between samples", "replaced legacy account", "new disposition", "never admitted for this disposition"} {
		t.Run(reason, func(t *testing.T) {
			f := manualPolicyTestFixture(t)
			f.owner.Snapshot.AccountFingerprint = ""
			if reason == "never admitted for this disposition" {
				delete(f.engine.managed, "one")
			}
			row, err := f.engine.Submit(context.Background(), f.config)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := 0
			switch reason {
			case "disabled between samples":
				wantCalls = 1
				f.afterCall = func() { f.file["disabled"] = true }
			case "replaced legacy account":
				f.file["account_id"] = "new-account-with-same-email"
			case "new disposition":
				newOwner := *f.owner
				newOwner.ActionID = "another-action"
				f.engine.managed["one"] = &newOwner
			case "never admitted for this disposition":
				f.engine.managed["one"] = f.owner
			}
			f.engine.execute(row.ID)
			result, err := f.engine.Run(row.ID)
			if err != nil || result.Status != "blocked" || len(result.Samples) != wantCalls || f.nativeCalls != wantCalls || f.probes+len(f.patches)+f.deletes != 0 {
				t.Fatal("execution did not stop before the next native call", err, result.Status, f.nativeCalls)
			}
		})
	}
}
