package quality

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestDeleteRuleRetainsErrorSourceWithoutGrantingRecovery(t *testing.T) {
	ctx := context.Background()
	f := managementTestFixture(t)
	en := f.engine
	r := f.rule(t, disableCredential, true, false)
	r.UpstreamErrorActions = map[string]string{"unauthorized": disableCredential}
	r, err := en.saveRule(ctx, *r, true, "start")
	if err != nil {
		t.Fatal(err)
	}
	f.upstreamStatus = http.StatusUnauthorized
	runs, err := f.round(t, r)
	if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_suspended" || !f.entries["one"].Disabled {
		t.Fatal(runs, err)
	}
	other := f.rule(t, "observe_only", true, false)
	c := changes()
	c.dispositions = []*CredentialDisposition{
		{AuthID: "removed", RuleID: r.ID, State: "deleted"},
		{AuthID: "restored", RuleID: r.ID, State: "restored"},
		{AuthID: "missing", RuleID: r.ID, State: "entry_missing"},
		{AuthID: "unrelated", RuleID: other.ID, State: "disabled"},
	}
	if err := en.commitLocked(c); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"id": r.ID, "revision": r.Revision})
	if err != nil {
		t.Fatal(err)
	}
	before := f.probes + f.nativeCalls
	resp := en.Handle(ctx, pluginapi.ManagementRequest{Method: http.MethodDelete, Path: "/v0/management/plugins/model-quality/rules", Body: body})
	if resp.StatusCode != http.StatusOK || string(resp.Body) != `{"ok":true}` {
		t.Fatal(resp.StatusCode, string(resp.Body))
	}
	if en.rules[r.ID] != nil || en.rules[other.ID] == nil || en.timers[r.ID] || en.runs[runs[0].ID] == nil {
		t.Fatal("rule deletion lost the retained error evidence")
	}
	for _, authID := range []string{"removed", "restored", "missing"} {
		if err := en.store.Get("meta", dispositionPrefix+authID, &CredentialDisposition{}); en.managed[authID] != nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatal(authID, err)
		}
	}
	if errorPolicyDisabled(en.managed["one"]).Code == "" || en.managed["unrelated"] == nil || !f.entries["one"].Disabled || len(f.patches) != 1 || f.deletes != 0 || f.probes+f.nativeCalls != before {
		t.Fatal("deletion touched CPA or another rule's handling")
	}
	if err := en.Close(); err != nil {
		t.Fatal(err)
	}
	en, err = New(en.cfg, f.host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = en.Close() })
	if en.rules[r.ID] != nil || en.timers[r.ID] || errorPolicyDisabled(en.managed["one"]).Code == "" || en.managed["unrelated"] == nil {
		t.Fatal("error source isolation did not survive restart")
	}
	other.TargetScope, other.Action, other.AutoRestore = scopeAllCredentials, disableCredential, true
	other.ID, other.Revision = "", 0
	other, err = en.SaveRule(ctx, *other, false)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := en.dispatchRule(ctx, other.ID, other.Revision, false)
	if err != nil || reply.Count != 0 || reply.RecoveryCount != 0 || f.probes+f.nativeCalls != before || len(f.patches) != 1 {
		t.Fatal("deleting the error rule granted a peer permission to recover", reply, err)
	}
}

func TestDeleteRuleLeavesUnknownWriteQuarantinedWithoutRule(t *testing.T) {
	ctx := context.Background()
	f := managementTestFixture(t)
	en := f.engine
	r := f.rule(t, disableCredential, true, true)
	f.answers["a"] = "5"
	f.statusFailure = true
	runs, err := f.round(t, r)
	if err != nil || len(runs) != 1 || runs[0].ActionStatus != "credential_action_uncertain" {
		t.Fatal(runs, err)
	}
	evidence := runs[0].ID
	intent := *en.managed["one"]
	pending := intent
	pending.State = "disable_pending"
	c := changes()
	c.disposition = &pending
	if err := en.commitLocked(c); err != nil {
		t.Fatal(err)
	}
	if err := en.DeleteRule(ctx, r.ID, r.Revision); err == nil || !strings.Contains(err.Error(), "写入尚在处理中") {
		t.Fatal("accepted deletion during a write", err)
	}
	c = changes()
	c.disposition = &intent
	if err := en.commitLocked(c); err != nil {
		t.Fatal(err)
	}
	if err := en.DeleteRule(ctx, r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	var retained CredentialDisposition
	if err := en.store.Get("meta", dispositionPrefix+"one", &retained); err != nil || retained.State != "attention" ||
		retained.RuleID != "" || retained.RuleRevision != 0 || retained.ActionID != intent.ActionID || !sameSnapshot(retained.Snapshot, intent.Snapshot) {
		t.Fatal("uncertain write lost its identity, snapshot or isolation", err)
	}
	if result, err := en.clearRuleHistory(); err != nil || result.Kept != 1 {
		t.Fatal("uncertain write lost its evidence", result, err)
	}
	if err := en.Prune(en.now().AddDate(0, 0, 365)); err != nil || en.runs[evidence] == nil {
		t.Fatal("uncertain evidence expired", err)
	}
	before := f.probes
	replacement := f.rule(t, disableCredential, true, true)
	replacement.TargetScope = scopeAllCredentials
	replacement, err = en.SaveRule(ctx, *replacement, true)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := en.dispatchRule(ctx, replacement.ID, replacement.Revision, false)
	if err != nil || reply.RecoveryCount != 0 || reply.Count != 0 || reply.Skipped != 1 {
		t.Fatal(reply, err)
	}
	if _, err := en.PrepareCredentialLogin(ctx, CredentialLoginPrepareRequest{AuthID: "one", PreparationID: "attempt"}); err == nil {
		t.Fatal("new credential authorization bypassed an unknown write")
	}
	if len(f.patches) != 1 || f.probes != before || !f.entries["one"].Disabled {
		t.Fatal("replayed or altered an uncertain write")
	}
}

func TestDeleteRuleClearsConfirmedProviderGroupTogether(t *testing.T) {
	en, base, owner := scopeFixture(t)
	h := &groupPolicyFixture{Host: base, snapshots: map[string]CredentialSnapshot{}}
	en.host = h
	members := []string{"route-a", "route-b"}
	c := changes()
	for _, id := range members {
		s := CredentialSnapshot{AuthID: id, ProviderGroupID: "group", ProviderMechanism: "group_disabled", ProviderMembers: members, Disabled: true}
		h.snapshots[id] = s
		c.dispositions = append(c.dispositions, &CredentialDisposition{AuthID: id, RuleID: owner.ID, State: "disabled", ActionID: "shared-action", Snapshot: s})
	}
	now := en.now().Add(-time.Minute)
	c.runs["first"] = &Run{ID: "first", Source: "scheduled_probe", Status: "completed", CompletedAt: &now, Config: Frozen{RuleID: owner.ID}}
	if err := en.commitLocked(c); err != nil {
		t.Fatal(err)
	}
	if err := en.DeleteRule(context.Background(), owner.ID, owner.Revision); err != nil {
		t.Fatal(err)
	}
	for _, id := range members {
		if en.managed[id] != nil || !h.snapshots[id].Disabled {
			t.Fatal("partial group cleanup or CPA config was changed", id)
		}
	}
	if len(h.writes) != 0 || len(en.runs) != 0 {
		t.Fatal("group deletion wrote or retained ordinary history")
	}
}
