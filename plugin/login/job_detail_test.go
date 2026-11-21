package login

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	auth "github.com/cliproxyapi-plugins/model-quality/login/account"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	bolt "go.etcd.io/bbolt"
)

func getJobDetail(s *Service, id string) pluginapi.ManagementResponse {
	return s.Handle(context.Background(), pluginapi.ManagementRequest{
		Method: http.MethodGet, Path: RoutePrefix + "/detail", Query: url.Values{"id": {id}},
	})
}

func blockedHistoricalTargetRead(t *testing.T) (*Service, storedJob, <-chan pluginapi.ManagementResponse, func()) {
	t.Helper()
	opts := offlineOptions(t)
	host := newFakeHost()
	const target = "blocked-detail.json"
	token := offlineBundle(t, time.Now(), "old-detail-principal").TokenData.AccessToken
	host.entries[target] = auth.Entry{ID: target, Index: target, Name: target, Provider: "codex", Status: "active"}
	host.files[target] = map[string]any{"type": "codex", "email": "offline@example.test", "access_token": token}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var armed atomic.Bool
	opts.Call = func(method string, in, out any) error {
		if method == "host.auth.get" && armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return host.call(method, in, out)
	}
	s := offlineService(t, opts)
	t.Cleanup(unblock)
	snapshot, err := s.opts.Manager.LoginSnapshot(target)
	if err != nil {
		t.Fatal(err)
	}
	record := historyJob("existing-sdk-detail", "", 1, time.Now())
	record.AdoptedPrincipal, record.View.TargetAuthID = snapshot.Principal, target
	if err := s.journal.save(record); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.jobs[record.View.ID] = &session{record: record}
	s.mu.Unlock()
	armed.Store(true)
	done := make(chan pluginapi.ManagementResponse, 1)
	go func() { done <- getJobDetail(s, record.View.ID) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fixture failed to enter the historical display SDK read")
	}
	return s, record, done, unblock
}

func TestDetailRechecksDeletionAfterSlowTargetRead(t *testing.T) {
	s, record, done, unblock := blockedHistoricalTargetRead(t)
	deleted := make(chan error, 1)
	go func() { deleted <- s.DeleteJob(record.View.ID, record.View.Revision) }()
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(350 * time.Millisecond):
		t.Fatal("unrelated SDK display read retained admission lock")
	}
	unblock()
	result := <-done
	if result.StatusCode != 404 || strings.Contains(string(result.Body), "offline@example.test") {
		t.Fatal("late SDK result disclosed a deleted history entry")
	}
}

func TestDetailShutdownDrainsSDKAndRejectsNewReaders(t *testing.T) {
	s, record, done, unblock := blockedHistoricalTargetRead(t)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-s.ctx.Done():
	case <-time.After(350 * time.Millisecond):
		t.Fatal("service shutdown could not stop admitting readers")
	}
	if result := getJobDetail(s, record.View.ID); result.StatusCode != 503 {
		t.Fatal("new display query was admitted during shutdown")
	}
	select {
	case <-closed:
		t.Fatal("journal closed underneath an admitted SDK read")
	default:
	}
	unblock()
	if result := <-done; result.StatusCode != 200 {
		t.Fatal("in-flight historical query lost its journal")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestDetailUnreadableJournalIsNotReportedAsMissing(t *testing.T) {
	s := offlineService(t, offlineOptions(t))
	if err := s.journal.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("jobs")).Put([]byte("broken-job"), []byte{0}) }); err != nil {
		t.Fatal(err)
	}
	if r := getJobDetail(s, "broken-job"); r.StatusCode != 503 || !strings.Contains(string(r.Body), "storage_unavailable") {
		t.Fatal("unreadable job was exposed as an unaccepted or missing record")
	}
}

func TestDetailBlockedOnLiveSessionRejectsUnpersistedCompletion(t *testing.T) {
	s, j, _ := readOnlyJobFixture(t)
	id := j.record.View.ID
	db := s.journal.db
	reads := db.Stats().TxN
	s.mu.Lock()
	done := make(chan pluginapi.ManagementResponse, 1)
	go func() { done <- getJobDetail(s, id) }()
	// No workers in this fixture. The one completed read transaction proves
	// that the request saw "committing" and passed its initial lifecycle check.
	deadline := time.Now().Add(time.Second)
	for {
		stats := db.Stats()
		if stats.TxN > reads && stats.OpenTxN == 0 {
			break
		}
		if time.Now().After(deadline) {
			s.mu.Unlock()
			t.Fatal("detail did not read the durable active record")
		}
		time.Sleep(time.Millisecond)
	}
	err := s.transitionLocked(j, "succeeded", "complete", "")
	s.mu.Unlock()
	if err == nil {
		t.Fatal("read-only journal accepted a terminal transition")
	}
	r := <-done
	if r.StatusCode != 503 || !strings.Contains(string(r.Body), "storage_unavailable") || strings.Contains(string(r.Body), "succeeded") {
		t.Fatal("in-flight public detail returned an unpersisted completion")
	}
	stored, err := s.journal.detail(id)
	if err != nil || stored.View.Status != "committing" {
		t.Fatal("durable status changed on a read-only journal")
	}
}

func detailWhileAdmissionLocked(t *testing.T, s *Service, id string) pluginapi.ManagementResponse {
	t.Helper()
	ch := make(chan pluginapi.ManagementResponse, 1)
	s.mu.Lock()
	go func() { ch <- getJobDetail(s, id) }()
	select {
	case result := <-ch:
		s.mu.Unlock()
		return result
	case <-time.After(350 * time.Millisecond):
		s.mu.Unlock()
		<-ch
		t.Fatal("detail depended on the unrelated admission lock")
		return pluginapi.ManagementResponse{}
	}
}

func TestDetailReadsTerminalAndMissingWithoutAdmissionLock(t *testing.T) {
	opts := offlineOptions(t)
	var ownerValid atomic.Bool
	ownerValid.Store(true)
	opts.Owner = func() (Owner, error) { return Owner{ID: "offline", Valid: ownerValid.Load}, nil }
	s := offlineService(t, opts)
	j, err := s.Start(StartRequest{ConsumerPlugin: "model-quality", Method: "browser", Intent: "import",
		Credentials: Credentials{Email: "transient-detail@example.test"}})
	if err != nil || j.AuthorizationURL == "" {
		t.Fatal("local browser fixture did not produce an authorization URL")
	}
	ownerValid.Store(false)
	if current, err := s.Detail(j.ID); err != nil || current.AuthorizationURL != "" {
		t.Fatal("inactive owner retained a current authorization URL")
	}
	finished, err := s.Cancel(j.ID, false)
	if err != nil || !finished.CanDelete || finished.Email != "transient-detail@example.test" || finished.AuthorizationURL != "" {
		t.Fatal("fixture did not retain its transient display identity")
	}
	t.Run("same-process terminal", func(t *testing.T) {
		got := detailWhileAdmissionLocked(t, s, j.ID)
		var decoded Job
		if got.StatusCode != 200 || json.Unmarshal(got.Body, &decoded) != nil || decoded != finished {
			t.Fatal("terminal detail differs from the current session's complete public view")
		}
	})
	t.Run("missing", func(t *testing.T) {
		if got := detailWhileAdmissionLocked(t, s, "never-accepted"); got.StatusCode != 404 {
			t.Fatal("missing durable job was not reported")
		}
	})
	if err := s.DeleteJob(j.ID, finished.Revision); err != nil {
		t.Fatal(err)
	}
	t.Run("tombstone", func(t *testing.T) {
		if got := detailWhileAdmissionLocked(t, s, j.ID); got.StatusCode != 404 || strings.Contains(string(got.Body), finished.Email) {
			t.Fatal("deleted job or its transient email remained visible")
		}
	})
}

func TestDetailRestoredJobPreservesIdentityAndLegacyDiagnostics(t *testing.T) {
	opts := offlineOptions(t)
	host := newFakeHost()
	opts.Call = host.call
	s := offlineService(t, opts)
	a := importOfflineAccount(t, s)
	const target = "fake-detail.json"
	token := offlineBundle(t, time.Now(), "detail-identity").TokenData.AccessToken
	host.mu.Lock()
	host.entries[target] = auth.Entry{ID: target, Index: target, Name: target, Provider: "codex", Status: "active"}
	host.files[target] = map[string]any{"type": "codex", "access_token": token, "email": a.Email}
	host.mu.Unlock()
	snapshot, err := s.opts.Manager.LoginSnapshot(target)
	if err != nil {
		t.Fatal(err)
	}
	bound := historyJob("previous-account-job", a.ID, 1, time.Now())
	bound.View.Status, bound.View.ErrorCode = "failed", "runtime_failed"
	bound.View.ErrorStage, bound.View.RuntimeCode, bound.View.PlanType = "password", "service_error", "chatgpt_plus"
	bound.LogEntries = []LogEntry{{Event: "request", Phase: "password", HTTPStatus: 503}, {Event: "state", RuntimeCode: "service_error", ErrorStage: "password"}}
	verified := historyJob("previous-verified-target", "", 2, time.Now())
	verified.View.TargetAuthID, verified.AdoptedPrincipal = target, snapshot.Principal
	blocked := historyJob("previous-uncertain-target", "", 3, time.Now())
	blocked.View.TargetAuthID, blocked.AdoptedPrincipal = target, "different-principal"
	blocked.View.Status = "commit_unknown"
	blocked.View.AuthorizationURL, blocked.View.Email = "https://example.test/old-url", "stale@example.test"
	if err := s.journal.db.Update(func(tx *bolt.Tx) error {
		for _, record := range []storedJob{bound, verified, blocked} {
			if err := putGob(tx, "jobs", record.View.ID, record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = offlineService(t, opts)
	for _, tc := range []struct {
		id, email, plan string
		httpStatus      int
		canDelete       bool
	}{
		{bound.View.ID, a.Email, "plus", 503, true},
		{verified.View.ID, a.Email, "unknown", 0, true},
		{blocked.View.ID, "", "unknown", 0, false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			r := detailWhileAdmissionLocked(t, s, tc.id)
			var got Job
			if r.StatusCode != 200 || json.Unmarshal(r.Body, &got) != nil || got.Email != tc.email ||
				got.CanDelete != tc.canDelete || got.PlanType != tc.plan || got.HTTPStatus != tc.httpStatus || got.AuthorizationURL != "" {
				t.Fatal("restored job lost verified identity, diagnostics, or its deletion restrictions")
			}
			if strings.Contains(string(r.Body), token) || strings.Contains(string(r.Body), offlineStart().Credentials.Password) {
				t.Fatal("private account material entered a display response")
			}
		})
	}
}
