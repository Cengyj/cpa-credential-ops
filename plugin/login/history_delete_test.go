package login

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestDeleteLoginHistoryPreservesBindingRateLimitAndRestart(t *testing.T) {
	host := newFakeHost()
	opts := offlineOptions(t)
	opts.Call = host.call
	s := offlineService(t, opts)
	a := importOfflineAccount(t, s)
	old := startOfflineAccount(t, s, a, "", "committed")
	detail, err := s.AccountDetail(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	rateKey := s.journal.fingerprint(offlineStart().Credentials.Email)
	for i := 0; i < 3; i++ {
		record := historyJob(fmt.Sprintf("offline-failed-%d", i), detail.Account.ID, detail.Account.Revision+uint64(i), time.Now())
		record.View.Status, record.View.ErrorCode = "failed", "invalid_callback"
		record.RateKey = rateKey
		s.mu.Lock()
		err = s.journal.save(record)
		if err == nil {
			s.jobs[record.View.ID] = &session{record: record}
		}
		s.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	const id = "offline-failed-2"
	req := func(method, path string, revision uint64, key string) pluginapi.ManagementResponse {
		r := pluginapi.ManagementRequest{Method: method, Path: RoutePrefix + path, Query: url.Values{"id": {id}}}
		if method == http.MethodDelete {
			r.Query = nil
			r.Headers = http.Header{"Idempotency-Key": {key}}
			r.Body, _ = Encode(map[string]any{"job_id": id, "revision": revision})
		}
		return s.Handle(context.Background(), r)
	}
	if got := req(http.MethodDelete, "/jobs", 99, "stale-revision"); got.StatusCode != 409 {
		t.Fatalf("stale revision returned %d", got.StatusCode)
	}
	for i := 0; i < 2; i++ {
		if got := req(http.MethodDelete, "/jobs", 1, "delete-history-record"); got.StatusCode != 200 {
			t.Fatalf("delete or idempotent reply returned %d: %s", got.StatusCode, got.Body)
		}
	}
	check := func() {
		t.Helper()
		for _, path := range []string{"/detail", "/logs"} {
			if got := req(http.MethodGet, path, 0, ""); got.StatusCode != 404 {
				t.Fatalf("deleted record reachable via %s: %d", path, got.StatusCode)
			}
		}
		history, _, err := s.JobsPage(detail.Account.ID, 200)
		if err != nil || len(history) != 3 || history[0].ID != "offline-failed-1" || history[2].ID != old.ID {
			t.Fatalf("incorrect remaining history: %v %v", historyIDs(history), err)
		}
		after, err := s.AccountDetail(detail.Account.ID)
		if err != nil || after.Account.LastJobID != history[0].ID || after.Account.AuthID != old.TargetAuthID || after.Account.LastLoginAt != detail.Account.LastLoginAt || after.Credentials != detail.Credentials {
			t.Fatal("history deletion changed account state or failed to find the previous record")
		}
		if _, err = s.StartAccount(detail.Account.ID, after.Account.Revision, ""); SafeCode(err) != "rate_limited" {
			t.Fatalf("history deletion affected the login rate limit: %v", err)
		}
	}
	check()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = offlineService(t, opts)
	check()
	s.mu.Lock()
	removed := s.jobs[id].record
	s.mu.Unlock()
	if !removed.HistoryDeleted || len(removed.LogEntries) != 0 || removed.View.ErrorCode != "" {
		t.Fatal("deleted history still carries diagnostic data")
	}
}

func TestDeleteLoginHistoryRejectsActiveAndUnreconciledRecords(t *testing.T) {
	s := offlineService(t, offlineOptions(t))
	for _, tc := range []struct{ status, action string }{
		{"running", ""}, {"awaiting_commit", ""}, {"commit_unknown", ""},
		{"saved_not_adopted", ""}, {"security_uncertain", ""}, {"failed", "reset_totp"},
	} {
		record := historyJob(tc.status, "", 0, time.Now())
		record.View.Status, record.View.Action = tc.status, tc.action
		// Public job detail is a journal read. Seed the accepted record as well
		// as the live session on which deletion admission operates.
		if err := s.journal.save(record); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		s.jobs[record.View.ID] = &session{record: record}
		s.mu.Unlock()
		if err := s.DeleteJob(record.View.ID, record.View.Revision); SafeCode(err) != "conflict" {
			t.Errorf("deleting %s returned %v", tc.status, err)
		}
		if job, err := s.Detail(record.View.ID); err != nil || job.CanDelete {
			t.Errorf("protected record %s is absent or marked deletable", tc.status)
		}
	}
}
