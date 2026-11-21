package quality

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	bolt "go.etcd.io/bbolt"
)

func TestStorageAdmissionReusesFreedPages(t *testing.T) {
	f := managementTestFixture(t)
	en := f.engine
	en.cfg.MaxStorageBytes = 1 << 20
	if err := en.store.Write(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("meta")).Put([]byte("fixture"), make([]byte, 2<<20))
	}); err != nil {
		t.Fatal(err)
	}
	if err := en.storageAdmission(); err == nil {
		t.Fatal("live data should use the budget")
	}
	if err := en.store.Write(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("meta")).Delete([]byte("fixture"))
	}); err != nil {
		t.Fatal(err)
	}
	// Once earlier readers finish, the next write makes retired pages reusable.
	if err := en.store.Put("meta", "maintenance", true); err != nil {
		t.Fatal(err)
	}
	if err := en.storageAdmission(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteNeedsCompleteDirectoryAndMissingFile(t *testing.T) {
	for _, response := range []string{"missing_directory", "unconfirmed_file"} {
		t.Run(response, func(t *testing.T) {
			f := managementTestFixture(t)
			ctx := context.Background()
			snapshot, err := f.host.CredentialSnapshot(ctx, "one", "one")
			if err != nil {
				t.Fatal(err)
			}
			list := f.host.Call
			f.host.Call = func(method string, request, reply any) error {
				if method == "host.auth.list" && response == "missing_directory" {
					return nil
				}
				return list(method, request, reply)
			}
			if response == "unconfirmed_file" {
				f.fileStatus = http.StatusServiceUnavailable
			}
			err = f.host.DeleteCredential(ctx, snapshot)
			if err == nil || f.deletes != 1 {
				t.Fatal("unconfirmed deletion accepted or replayed", err)
			}
		})
	}
}

func TestManualDeletionOnlyRemovesMarkerAndExplicitClearPreservesRecovery(t *testing.T) {
	f := managementTestFixture(t)
	en := f.engine
	ctx := context.Background()
	r := f.rule(t, disableCredential, true, true)
	r, err := en.setRuleTimer(ctx, r.ID, r.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	f.add("survivor")
	f.answers["a"] = "5"
	rows, err := f.round(t, r)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	runs := map[string]string{}
	for _, row := range rows {
		runs[row.Config.AuthID] = row.ID
	}
	c := changes()
	r = cloneRule(en.rules[r.ID])
	r.LastRun = runs["one"]
	r.LastManualSkips = []string{"one", "another"}
	c.rules[r.ID] = r
	manual := cloneRun(en.runs[r.LastRun])
	manual.ID, manual.Source, manual.Config.RuleID = ID(), "manual_test", ""
	c.runs[manual.ID] = manual
	if err := en.commitLocked(c); err != nil {
		t.Fatal(err)
	}
	gone := en.managed["one"]
	delete(f.entries, "one")
	check := func() error {
		en.noteRoundEndLocked(r.ID)
		return en.checkFinishedRounds(ctx)
	}
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
		f.fileStatus = status
		if err := check(); err != nil || en.managed["one"] != gone {
			t.Fatal(status, err)
		}
	}
	f.fileStatus = 0
	f.verifyManagement(t)
	// A replacement with the same filename must also keep the record.
	f.entries["replacement"] = pluginapi.HostAuthFileEntry{ID: "replacement", AuthIndex: "replacement", Name: gone.Snapshot.Name}
	if err := check(); err != nil || en.managed["one"] != gone {
		t.Fatal(err)
	}
	delete(f.entries, "replacement")
	en.busyAuth["one"] = struct{}{}
	if err := check(); err != nil || en.managed["one"] != gone {
		t.Fatal(err)
	}
	delete(en.busyAuth, "one")
	for range 2 {
		if err := check(); err != nil {
			t.Fatal(err)
		}
	}
	if en.managed["one"] != nil || en.managed["survivor"] == nil || en.runs[runs["one"]] == nil || en.runs[manual.ID] == nil {
		t.Fatal("incorrect cleanup scope")
	}
	var saved Rule
	if err := en.store.Get("rules", r.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.LastRun != runs["one"] || saved.LastScopeBatch != r.LastScopeBatch || !saved.Enabled || !en.timerAllowed(&saved) || saved.NextRun != r.NextRun || saved.Revision != r.Revision || len(saved.LastManualSkips) != 2 {
		t.Fatal(saved)
	}
	audits, err := en.History()
	if err != nil {
		t.Fatal(err)
	}
	foundAudit := false
	for _, a := range audits {
		if a.RunID == gone.RunID {
			foundAudit = true
		}
	}
	if !foundAudit {
		t.Fatal("marker cleanup removed run audits")
	}
	result, err := en.clearRuleHistory()
	if err != nil || result.Deleted != 2 || result.Kept != 0 {
		t.Fatal(result, err)
	}
	if en.runs[manual.ID] == nil || en.managed["survivor"] == nil {
		t.Fatal("clear removed unrelated state")
	}
	saved = Rule{}
	if err := en.store.Get("rules", r.ID, &saved); err != nil || saved.LastRun != "" || saved.LastScopeBatch != "" || !en.timerAllowed(&saved) || saved.Revision != r.Revision {
		t.Fatal(saved, err)
	}
	audits, err = en.History()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range audits {
		if a.RunID == gone.RunID {
			t.Fatal("manual clear retained run audit")
		}
	}
	if err := en.store.Get("runs", runs["one"], &Run{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := en.loadDispositions(); err != nil || en.managed["one"] != nil {
		t.Fatal(err)
	}
	f.answers["a"] = "4"
	receipt, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
	if err != nil || receipt.Count != 1 {
		t.Fatal(receipt, err)
	}
	result, err = en.clearRuleHistory()
	if err != nil || result.Deleted != 0 || result.Kept != 1 {
		t.Fatal(result, err)
	}
	task := en.nextRunnableLocked()
	en.executeTask(task)
	if en.runs[task.id].ActionStatus != "credential_restored" {
		t.Fatal(en.runs[task.id].ActionStatus)
	}
}

func TestCleanupPreservesUncertainAndHistoricalOwnership(t *testing.T) {
	f := managementTestFixture(t)
	r := f.rule(t, disableCredential, true, true)
	f.answers["a"] = "5"
	if _, err := f.round(t, r); err != nil {
		t.Fatal(err)
	}
	delete(f.entries, "one")
	d := f.engine.managed["one"]
	for _, state := range []string{"disable_pending", "restore_pending", "delete_pending", "attention", "deleted"} {
		d.State = state
		f.engine.noteRoundEndLocked(r.ID)
		if err := f.engine.checkFinishedRounds(context.Background()); err != nil || f.engine.managed["one"] != d {
			t.Fatal(state, err)
		}
	}
	d.State = "disabled"
	f.engine.rules["historic"] = &Rule{ID: "historic", ActionID: "legacy", RunConfig: RunConfig{AuthID: "one"}}
	f.engine.noteRoundEndLocked(r.ID)
	if err := f.engine.checkFinishedRounds(context.Background()); err != nil || f.engine.managed["one"] != d {
		t.Fatal(err)
	}
}

func TestObservedRemovalRetainsHistoricalMarkerWithoutRecoveryAuthority(t *testing.T) {
	f := managementTestFixture(t)
	en, ctx := f.engine, context.Background()
	r := f.rule(t, disableCredential, true, true)
	other := f.rule(t, "observe_only", true, false)
	f.answers["a"] = "5"
	if _, err := f.round(t, r); err != nil {
		t.Fatal(err)
	}
	if err := en.checkFinishedRounds(ctx); err != nil {
		t.Fatal(err)
	}
	delete(f.entries, "one")
	if err := en.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.round(t, other); err != nil {
		t.Fatal(err)
	}
	if err := en.checkFinishedRounds(ctx); err != nil || en.managed["one"] == nil {
		t.Fatal("idle or other rule cleared ownership", err)
	}
	f.add("two")
	f.add("three")
	receipt, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
	if err != nil || receipt.Count != 2 {
		t.Fatal(receipt, err)
	}
	en.executeTask(en.nextRunnableLocked())
	if err := en.checkFinishedRounds(ctx); err != nil || en.managed["one"] == nil {
		t.Fatal("early cleanup", err)
	}
	task := en.nextRunnableLocked()
	en.executeTask(task)
	if err := en.checkFinishedRounds(ctx); err != nil || en.managed["one"] == nil || en.managed["one"].State != "entry_missing" || activeDisposition(en.managed["one"]) {
		t.Fatal("round completion lost removed-entry history or retained recovery authority", err)
	}
	delete(f.entries, "two")
	delete(f.entries, "three")
	receipt, err = en.dispatchRule(ctx, r.ID, r.Revision, false)
	if err != nil || receipt.Count != 0 {
		t.Fatal(receipt, err)
	}
	if err := en.checkFinishedRounds(ctx); err != nil {
		t.Fatal(err)
	}
	for _, d := range en.managed {
		if d.State != "entry_missing" || activeDisposition(d) {
			t.Fatal("empty verified directory retained recovery authority", d)
		}
	}
}
