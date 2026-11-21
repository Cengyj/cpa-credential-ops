package quality

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestExternalEnableCancelsStaleRecoveryWithoutChangingRulePermissions(t *testing.T) {
	f := managementTestFixture(t)
	rule := f.rule(t, disableCredential, true, true)
	f.answers["a"] = "5"
	if runs, err := f.round(t, rule); err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_suspended" {
		t.Fatal(runs, err)
	}
	f.answers["a"] = "4"
	receipt, err := f.engine.dispatchRule(context.Background(), rule.ID, rule.Revision, false)
	if err != nil || receipt.Count != 1 {
		t.Fatal(receipt, err)
	}
	var queued *Run
	for _, run := range f.engine.runs {
		if run.Status == "queued" {
			queued = run
		}
	}
	if queued == nil {
		t.Fatal("missing queued recovery")
	}
	before := f.probes
	entry := f.entries["one"]
	entry.Disabled, entry.Status = false, "active"
	f.entries["one"] = entry
	if targets, err := f.engine.visibleTargets(context.Background()); err != nil || len(targets) != 1 || targets[0].CredentialHandling != "" {
		t.Fatal(targets, err)
	}
	if !queued.CancelRequested || queued.Status != "blocked" || f.engine.nextRunnableLocked() != nil ||
		f.probes != before || len(f.patches) != 1 || !f.engine.rules[rule.ID].Enabled || activeDisposition(f.engine.managed["one"]) {
		t.Fatal("external enable replayed an old task or changed rule permissions", queued)
	}
	if runs, err := f.round(t, rule); err != nil || len(runs) != 1 || runs[0].Status != "completed" || runs[0].Config.ManagedProbe.Disabled {
		t.Fatal("fresh round did not select current enabled state", runs, err)
	}
	if len(f.patches) != 1 {
		t.Fatal("old recovery performed a CPA write")
	}
}

func TestRefreshPreservesAdministratorStoppedRuleAndReadFailure(t *testing.T) {
	f := managementTestFixture(t)
	rule := f.rule(t, disableCredential, false, true)
	f.answers["a"] = "5"
	if _, err := f.round(t, rule); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.PauseRule(rule.ID, rule.Revision, true); err != nil {
		t.Fatal(err)
	}
	entry := f.entries["one"]
	entry.Disabled, entry.Status = false, "active"
	f.entries["one"] = entry
	callback := f.host.Call
	f.host.Call = func(method string, in, out any) error {
		if method == "host.auth.get" {
			return errors.New("synthetic current-file read failure")
		}
		return callback(method, in, out)
	}
	targets, err := f.engine.visibleTargets(context.Background())
	if err != nil || len(targets) != 1 || targets[0].ErrorPolicyBlock != nil || targets[0].HandlingStateReason == "" || targets[0].ManualDisabled {
		t.Fatal("failed current read claimed a historical disabled state", targets, err)
	}
	if !activeDisposition(f.engine.managed["one"]) || f.engine.rules[rule.ID].Enabled {
		t.Fatal("failed read discarded history or restarted a stopped rule")
	}
	f.host.Call = callback
	if _, err := f.engine.visibleTargets(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.engine.rules[rule.ID].Enabled || f.engine.rules[rule.ID].SchedulingEnabled || activeDisposition(f.engine.managed["one"]) || len(f.patches) != 1 {
		t.Fatal("successful refresh changed administrator permissions or CPA state")
	}
}

func TestObservedRemovalDoesNotGrantRecreatedEntryOldRecoveryAuthority(t *testing.T) {
	f := managementTestFixture(t)
	rule := f.rule(t, disableCredential, true, true)
	f.answers["a"] = "5"
	if _, err := f.round(t, rule); err != nil {
		t.Fatal(err)
	}
	original := *f.engine.managed["one"]
	delete(f.entries, "one")
	if _, err := f.engine.visibleTargets(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := f.engine.managed["one"]; d.State != "entry_missing" || activeDisposition(d) || d.ActionID != original.ActionID {
		t.Fatal("observed removal did not preserve history without authority", d)
	}
	f.add("one")
	entry := f.entries["one"]
	entry.Disabled, entry.Status = true, "disabled"
	f.entries["one"] = entry
	receipt, err := f.engine.dispatchRule(context.Background(), rule.ID, rule.Revision, false)
	if err != nil || receipt.Count != 0 || receipt.RecoveryCount != 0 || len(f.patches) != 1 {
		t.Fatal("recreated disabled entry inherited old recovery exception", receipt, err)
	}
	history, err := f.engine.History()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range history {
		if event.Kind == "credential_entry_missing" && strings.Contains(event.Detail, "history retained") {
			return
		}
	}
	t.Fatal("missing identity-continuity audit")
}

func TestTransientRuntimeOmissionPreservesFileErrorExclusion(t *testing.T) {
	f := managementTestFixture(t)
	rule := f.rule(t, disableCredential, true, true)
	rule.UpstreamErrorActions = map[string]string{"unauthorized": disableCredential}
	var err error
	rule, err = f.engine.SaveRule(context.Background(), *rule, true)
	if err != nil {
		t.Fatal(err)
	}
	f.upstreamStatus = 401
	if rows, err := f.round(t, rule); err != nil || len(rows) != 1 || rows[0].ActionStatus != "credential_suspended" {
		t.Fatal(rows, err)
	}
	old := f.engine.managed["one"]
	callback := f.host.Call
	f.host.Call = func(method string, in, out any) error {
		switch method {
		case "host.auth.list":
			// The runtime may temporarily omit the still-existing file.
			return json.Unmarshal([]byte(`{"files":[]}`), out)
		case "host.auth.models":
			if in.(pluginapi.HostAuthModelsRequest).AuthIndex == "" {
				*out.(*pluginapi.HostAuthModelsResponse) = pluginapi.HostAuthModelsResponse{}
				return nil
			}
		}
		return callback(method, in, out)
	}
	if rows, err := f.engine.visibleTargets(context.Background()); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if f.engine.managed["one"] != old || old.State != "disabled" || errorPolicyDisabled(old).Code == "" {
		t.Fatal("runtime omission discarded the error source without a file-specific 404")
	}
	f.host.Call, f.upstreamStatus = callback, 200
	before := f.probes
	if rows, err := f.round(t, rule); err != nil || len(rows) != 0 || f.probes != before || len(f.patches) != 1 {
		t.Fatal("returning disabled file resumed probes after a transient directory omission", rows, err)
	}
}
