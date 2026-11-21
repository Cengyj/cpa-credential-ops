package login

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/cliproxyapi-plugins/model-quality/login/account"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	bolt "go.etcd.io/bbolt"
)

func queryJobCommand(s *Service, key string) pluginapi.ManagementResponse {
	return s.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: RoutePrefix + "/commands", Query: url.Values{"id": {key}}})
}

func seedJobCommandReceipt(t *testing.T, s *Service) (string, string) {
	t.Helper()
	const key, id = "durable-only-command", "fixture-local-job"
	record := idempotency{Fingerprint: "fixture-command-fingerprint", JobPath: "/accounts/start", JobID: id, Pending: true}
	job := storedJob{View: Job{ID: id, AccountID: "fixture-account", Status: "committing", Revision: 2}}
	if err := s.journal.db.Update(func(tx *bolt.Tx) error {
		if err := putGob(tx, "idempotency", s.journal.fingerprint(key), record); err != nil {
			return err
		}
		return putGob(tx, "jobs", id, job)
	}); err != nil {
		t.Fatal(err)
	}
	return key, id
}

func TestJobCommandQueryDuringUnrelatedServiceLock(t *testing.T) {
	s := offlineService(t, offlineOptions(t))
	key, id := seedJobCommandReceipt(t, s)
	s.mu.Lock()
	replies := make(chan pluginapi.ManagementResponse, 1)
	go func() { replies <- queryJobCommand(s, key) }()
	var response pluginapi.ManagementResponse
	select {
	case response = <-replies:
		s.mu.Unlock()
	case <-time.After(time.Second):
		s.mu.Unlock()
		<-replies
		t.Fatal("durable receipt waited for the login execution lock")
	}
	v := ojson.ParseBytes(response.Body)
	if response.StatusCode != 200 || v.Get("job_id").String() != id || v.Get("job.status").String() != "committing" {
		t.Fatalf("durable job missing from receipt: %d %s", response.StatusCode, response.Body)
	}
}

func readOnlyJobFixture(t *testing.T) (*Service, *session, string) {
	t.Helper()
	original := offlineService(t, offlineOptions(t))
	key, id := seedJobCommandReceipt(t, original)
	path := original.journal.db.Path()
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	job := &session{record: storedJob{View: Job{ID: id, AccountID: "fixture-account", Status: "committing", Revision: 2}}}
	s := &Service{journal: &journal{db: db, key: original.journal.key, now: original.journal.now}, ctx: ctx, cancel: cancel,
		opts: original.opts, jobs: map[string]*session{id: job}}
	return s, job, key
}

func TestJobCommandNeverReportsUnpersistedSuccess(t *testing.T) {
	s, job, key := readOnlyJobFixture(t)
	s.mu.Lock()
	err := s.transitionLocked(job, "succeeded", "complete", "")
	s.mu.Unlock()
	if err == nil || !s.fault || job.record.View.Status != "succeeded" {
		t.Fatal("read-only evidence did not produce an actual failed terminal write")
	}
	v := ojson.ParseBytes(queryJobCommand(s, key).Body)
	if v.Get("status").String() != "accepted" || v.Get("job.status").String() != "committing" || v.Get("job.revision").Uint() != 2 {
		t.Fatal("receipt exposed a terminal state which never crossed the storage barrier")
	}
}

func TestJobCommandCorruptAndMissingStoredLinks(t *testing.T) {
	for _, mode := range []string{"missing_job", "legacy_missing_job", "wrong_job_id", "bad_job_encoding", "bad_key_encoding", "missing_fingerprint", "missing_bucket", "closed"} {
		t.Run(mode, func(t *testing.T) {
			s := offlineService(t, offlineOptions(t))
			key, id := seedJobCommandReceipt(t, s)
			err := s.journal.db.Update(func(tx *bolt.Tx) error {
				keyHash := s.journal.fingerprint(key)
				switch mode {
				case "legacy_missing_job":
					if err := putGob(tx, "idempotency", keyHash, idempotency{Fingerprint: "fixture", JobID: id}); err != nil {
						return err
					}
					fallthrough
				case "missing_job":
					return tx.Bucket([]byte("jobs")).Delete([]byte(id))
				case "wrong_job_id":
					return putGob(tx, "jobs", id, storedJob{View: Job{ID: "another", Status: "succeeded"}})
				case "bad_job_encoding":
					return tx.Bucket([]byte("jobs")).Put([]byte(id), []byte("fixture-not-a-job"))
				case "bad_key_encoding":
					return tx.Bucket([]byte("idempotency")).Put([]byte(keyHash), []byte("fixture-not-a-command"))
				case "missing_fingerprint":
					return putGob(tx, "idempotency", keyHash, idempotency{JobPath: "/start", JobID: id})
				case "missing_bucket":
					return tx.DeleteBucket([]byte("jobs"))
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "closed" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			response := queryJobCommand(s, key)
			v := ojson.ParseBytes(response.Body)
			switch mode {
			case "missing_job":
				if response.StatusCode != 200 || v.Get("status").String() != "expired" || v.Get("job_id").String() != id || v.Get("job").Exists() {
					t.Fatal("missing stored history was exposed or treated as new permission")
				}
			case "legacy_missing_job":
				if response.StatusCode != 200 || v.Get("status").String() != "reconciliation_required" || !v.Get("outcome_unknown").Bool() || v.Get("job_id").Exists() {
					t.Fatal("uncorrelated historical record invented a link")
				}
			default:
				if response.StatusCode != 503 || v.Get("code").String() != "storage_unavailable" || v.Get("source").String() != "plugin" {
					t.Fatalf("corrupt or unavailable local storage did not fail closed: %d %s", response.StatusCode, response.Body)
				}
			}
		})
	}
}

func TestJobCommandReadsLinkAndJobInOneSnapshot(t *testing.T) {
	s := offlineService(t, offlineOptions(t))
	const key = "snapshot-reader-command"
	ids := [2]string{"fixture-first-job", "fixture-second-job"}
	switchLink := func(index int) error {
		return s.journal.db.Update(func(tx *bolt.Tx) error {
			id := ids[index]
			if err := tx.Bucket([]byte("jobs")).Delete([]byte(ids[1-index])); err != nil {
				return err
			}
			if err := putGob(tx, "jobs", id, storedJob{View: Job{ID: id, Status: "running"}}); err != nil {
				return err
			}
			return putGob(tx, "idempotency", s.journal.fingerprint(key), idempotency{Fingerprint: "fixture", JobPath: "/start", JobID: id})
		})
	}
	if err := switchLink(0); err != nil {
		t.Fatal(err)
	}
	writer := make(chan error, 1)
	go func() {
		for i := 1; i <= 100; i++ {
			if err := switchLink(i % 2); err != nil {
				writer <- err
				return
			}
		}
		writer <- nil
	}()
	defer func() {
		if err := <-writer; err != nil {
			t.Error(err)
		}
	}()
	for range 100 {
		v, err := s.lookupJobCommand(key)
		if err != nil || v.Status != "accepted" || v.Job == nil || v.Job.ID != v.JobID || v.Job.Status != "running" {
			t.Fatal("mixed job and command revisions in one response", v, err)
		}
	}
}

func TestJobCreationCommandsHaveExactReadOnlyReceipt(t *testing.T) {
	for _, path := range []string{"/start", "/accounts/start", "/accounts/security"} {
		t.Run(path, func(t *testing.T) {
			opts := offlineOptions(t)
			host := newFakeHost()
			opts.Call = host.call
			var calls atomic.Int32
			runtime := opts.Runtime
			opts.Runtime = offlineRuntime{run: func(ctx context.Context, request RuntimeRequest) (string, error) {
				calls.Add(1)
				if request.Operation == "reset_totp" {
					return "", writeSecurityFixture(request, securityFixtureJournal(request))
				}
				return runtime.Run(ctx, request)
			}}
			s := offlineService(t, opts)
			a := importOfflineAccount(t, s)
			var body any
			switch path {
			case "/start":
				body = map[string]any{"consumer_plugin": "model-quality", "method": "browser", "intent": "import"}
			case "/accounts/start":
				body = map[string]any{"id": a.ID, "revision": a.Revision, "upload_to_cpa": false}
			default:
				body = map[string]any{"id": a.ID, "revision": a.Revision, "action": "reset_totp", "logout_all_devices": false, "reauthorize": false, "upload_to_cpa": false}
			}
			const key = "same-client-account-command"
			r := securityRequest(t, s, key, path, body)
			job := ojson.GetBytes(r.Body, "id").String()
			if r.StatusCode != 200 || job == "" {
				t.Fatalf("setup %s status=%d", path, r.StatusCode)
			}
			check := func() {
				t.Helper()
				receipt := queryJobCommand(s, key)
				v := ojson.ParseBytes(receipt.Body)
				if receipt.StatusCode != 200 || v.Get("id").String() != key || v.Get("path").String() != path || v.Get("job_id").String() != job || v.Get("job.id").String() != job || v.Get("status").String() != "accepted" {
					t.Fatalf("missing association between original request and accepted job: code=%d", receipt.StatusCode)
				}
				if v.Get("job.authorization_url").String() != "" || bytes.Contains(receipt.Body, []byte("OFFLINE_PASSWORD")) {
					t.Fatal("receipt included transient or private material")
				}
			}
			check()
			if again := securityRequest(t, s, key, path, body); again.StatusCode != 200 || ojson.GetBytes(again.Body, "id").String() != job {
				t.Fatal("same key created a second job")
			}
			if other := securityRequest(t, s, key, "/accounts/import", map[string]any{"entries": []Credentials{offlineStart().Credentials}}); other.StatusCode != 409 || ojson.GetBytes(other.Body, "code").String() != "idempotency_conflict" {
				t.Fatal("key crossed command namespaces")
			}
			if path == "/accounts/start" {
				waitLogin(t, s, job, "awaiting_commit")
			} else if path == "/accounts/security" {
				waitLogin(t, s, job, "succeeded")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			s = offlineService(t, opts)
			check()
			if again := securityRequest(t, s, key, path, body); again.StatusCode != 200 || ojson.GetBytes(again.Body, "id").String() != job {
				t.Fatal("restart lost job association")
			}
			if len(s.Jobs()) != 1 || calls.Load() != before || host.saves != 0 {
				t.Fatal("read/replay dispatched another login or upload")
			}
		})
	}
}

func TestSDKDirectoryFailureHasSDKSource(t *testing.T) {
	opts := offlineOptions(t)
	opts.Call = func(string, any, any) error { return errors.New("fixture-local-sdk-read") }
	s := offlineService(t, opts)
	r := s.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: RoutePrefix + "/targets"})
	v := ojson.ParseBytes(r.Body)
	if r.StatusCode != 503 || v.Get("source").String() != "sdk" || v.Get("code").String() != "cpa_credential_unavailable" || !v.Get("retryable").Bool() {
		t.Fatalf("SDK failure attributed to local storage: %d %s", r.StatusCode, r.Body)
	}
	if v := ojson.ParseBytes(errorReply(errors.New("storage_unavailable")).Body); v.Get("source").String() != "plugin" {
		t.Fatal("local bbolt failure lost its source")
	}
}

func BenchmarkLoginAdmissionWithHistory(b *testing.B) {
	h := newFakeHost()
	s, err := New(Options{Dir: filepath.Join(b.TempDir(), "account-journal"), Call: h.call})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	entries := make([]idempotency, 2048)
	for i := range entries {
		entries[i] = idempotency{Fingerprint: fmt.Sprintf("offline-reserved-%d", i), Retired: true}
	}
	err = s.journal.db.Update(func(tx *bolt.Tx) error {
		for i, row := range entries {
			if err := putGob(tx, "idempotency", fmt.Sprint(i), row); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	var seq int
	for b.Loop() {
		seq++
		j, err := s.Start(StartRequest{ConsumerPlugin: "model-quality", Method: "browser", Intent: "import", Credentials: Credentials{Email: fmt.Sprintf("run-%d@example.test", seq)}})
		if err != nil {
			b.Fatal(err)
		}
		if _, err = s.Cancel(j.ID, true); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReauthAdmissionLockDuringSlowSDK(b *testing.B) {
	benchmarkReauthAdmissionDetail(b, "missing-job")
}

func BenchmarkHistoricalDetailDuringSlowSDK(b *testing.B) {
	benchmarkReauthAdmissionDetail(b, "previous-job")
}

func benchmarkReauthAdmissionDetail(b *testing.B, queryID string) {
	host := newFakeHost()
	const id = "offline-bound.json"
	host.entries[id] = account.Entry{ID: id, Index: id, Name: id, Provider: "codex", Status: "active"}
	const payload = "eyJlbWFpbCI6Im9mZmxpbmVAZXhhbXBsZS50ZXN0IiwiaHR0cHM6Ly9hcGkub3BlbmFpLmNvbS9hdXRoIjp7ImNoYXRncHRfYWNjb3VudF9pZCI6ImFjY291bnQtZml4dHVyZSIsImNoYXRncHRfdXNlcl9pZCI6InVzZXItZml4dHVyZSJ9fQ"
	host.files[id] = map[string]any{"type": "codex", "email": "offline@example.test", "access_token": "e30." + payload + ".offline"}
	seen := make(chan struct{})
	var armed atomic.Bool
	var gets atomic.Int32
	s, err := New(Options{Dir: filepath.Join(b.TempDir(), "accounts"), Call: func(method string, in, out any) error {
		if method == "host.auth.get" {
			n := gets.Add(1)
			if armed.Load() && n == 3 {
				seen <- struct{}{}
				time.Sleep(16 * time.Millisecond)
			}
		}
		return host.call(method, in, out)
	}})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	if queryID != "missing-job" {
		record := historyJob(queryID, "", 1, time.Now())
		s.mu.Lock()
		err := s.journal.save(record)
		s.jobs[queryID] = &session{record: record}
		s.mu.Unlock()
		if err != nil {
			b.Fatal(err)
		}
	}
	snapshot, err := s.opts.Manager.LoginSnapshot(id)
	if err != nil {
		b.Fatal(err)
	}
	var wait time.Duration
	var totalGets int32
	for b.Loop() {
		gets.Store(0)
		armed.Store(true)
		ret := make(chan error, 1)
		go func() {
			j, e := s.Start(StartRequest{ConsumerPlugin: "model-quality", Method: "browser", Intent: "reauthorize", TargetAuthID: id, ExpectedIdentityVersion: snapshot.IdentityVersion, ProxyURL: "direct"})
			if e == nil {
				_, e = s.Cancel(j.ID, true)
			}
			// Test fixture removes its completed job from the in-memory rate gate.
			if e == nil {
				s.mu.Lock()
				delete(s.jobs, j.ID)
				s.mu.Unlock()
			}
			ret <- e
		}()
		select {
		case <-seen:
			start := time.Now()
			_, readErr := s.Detail(queryID)
			wait += time.Since(start)
			if (queryID == "missing-job" && SafeCode(readErr) != "not_found") || (queryID != "missing-job" && readErr != nil) {
				b.Fatalf("unexpected detail outcome: %v", readErr)
			}
			if err := <-ret; err != nil {
				b.Fatal(err)
			}
		case err := <-ret:
			if err != nil {
				b.Fatal(err)
			}
		}
		totalGets += gets.Load()
	}
	b.ReportMetric(float64(wait.Nanoseconds())/float64(b.N), "unrelated-wait-ns/op")
	b.ReportMetric(float64(totalGets)/float64(b.N), "sdk-gets/op")
}

func TestJobCommandReceiptStopsAtAtomicAcceptance(t *testing.T) {
	opts := offlineOptions(t)
	s := offlineService(t, opts)
	a := importOfflineAccount(t, s)
	const key = "durable-job-link"
	body := map[string]any{"id": a.ID, "revision": a.Revision, "upload_to_cpa": false}
	response := securityRequest(t, s, key, "/accounts/start", body)
	jobID := ojson.GetBytes(response.Body, "id").String()
	if response.StatusCode != 200 || jobID == "" {
		t.Fatal("fixture was not admitted")
	}
	job := waitLogin(t, s, jobID, "awaiting_commit")
	if _, err := s.Cancel(job.ID, true); err != nil {
		t.Fatal(err)
	}
	ref := s.journal.fingerprint(key)
	record, err := s.journal.commandRecord(ref)
	if err != nil || record == nil {
		t.Fatal("failed to load command")
	}
	// Older completed reservations already have an exact job ID. A read
	// can recover that relation without guessing from an account's last job.
	legacy := *record
	legacy.JobPath = ""
	if err := s.journal.db.Update(func(tx *bolt.Tx) error {
		return putGob(tx, "idempotency", s.journal.fingerprint("resolved-legacy-command"), legacy)
	}); err != nil {
		t.Fatal(err)
	}
	lv := ojson.ParseBytes(queryJobCommand(s, "resolved-legacy-command").Body)
	if lv.Get("status").String() != "accepted" || lv.Get("job_id").String() != jobID || lv.Get("path").String() != "/accounts/start" {
		t.Fatal("historical exact link was not readable")
	}

	// Preserve the committed job link, but drop the HTTP-result journal write.
	record.Pending = true
	record.Response = nil
	if err := s.journal.db.Update(func(tx *bolt.Tx) error { return putGob(tx, "idempotency", ref, *record) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = offlineService(t, opts)
	accepted := ojson.ParseBytes(queryJobCommand(s, key).Body)
	if accepted.Get("status").String() != "accepted" || accepted.Get("job_id").String() != jobID || accepted.Get("job.status").String() != "discarded" {
		t.Fatal("restart lost atomically bound job")
	}
	replay := securityRequest(t, s, key, "/accounts/start", body)
	if replay.StatusCode != 409 || ojson.GetBytes(replay.Body, "code").String() != "idempotency_unknown" || len(s.Jobs()) != 1 {
		t.Fatal("pending HTTP result was treated as permission to execute")
	}
	// A different request cannot claim that job merely because it's the latest.
	const other = "pending-before-job-acceptance"
	err = s.journal.db.Update(func(tx *bolt.Tx) error {
		return putGob(tx, "idempotency", s.journal.fingerprint(other), idempotency{JobPath: "/accounts/start", Pending: true, Fingerprint: "other-fixture"})
	})
	if err != nil {
		t.Fatal(err)
	}
	v := ojson.ParseBytes(queryJobCommand(s, other).Body)
	if v.Get("status").String() != "reconciliation_required" || !v.Get("outcome_unknown").Bool() || v.Get("job_id").Exists() {
		t.Fatal("a pre-admission reservation guessed which account job was accepted")
	}
	if r := queryJobCommand(s, "nonexistent-command"); r.StatusCode != 404 || ojson.GetBytes(r.Body, "code").String() != "command_not_accepted" {
		t.Fatalf("missing command %d", r.StatusCode)
	}
	j, err := s.Detail(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteJob(jobID, j.Revision); err != nil {
		t.Fatal(err)
	}
	v = ojson.ParseBytes(queryJobCommand(s, key).Body)
	if v.Get("status").String() != "expired" || v.Get("job_id").String() != jobID || v.Get("job").Exists() {
		t.Fatal("deleted history was exposed or permitted another job")
	}
}

func TestJobCommandFailureAndUnknownLegacyRecordCanBeQueried(t *testing.T) {
	s := offlineService(t, offlineOptions(t))
	const key = "failed-login-admission"
	r := securityRequest(t, s, key, "/accounts/start", map[string]any{"id": "missing-account", "revision": uint64(1)})
	if r.StatusCode != 404 {
		t.Fatal("missing account admitted")
	}
	v := ojson.ParseBytes(queryJobCommand(s, key).Body)
	if v.Get("status").String() != "failed" || v.Get("code").String() != "not_found" || v.Get("job_id").Exists() {
		t.Fatal("rejected admission reported as started")
	}
	for _, historical := range []struct {
		key    string
		record idempotency
		want   string
	}{
		{"legacy-uncorrelated-receipt", idempotency{Pending: true, Fingerprint: "fixture"}, "reconciliation_required"},
		{"expired-command-receipt", idempotency{Fingerprint: "fixture", Retired: true}, "expired"},
	} {
		err := s.journal.db.Update(func(tx *bolt.Tx) error {
			return putGob(tx, "idempotency", s.journal.fingerprint(historical.key), historical.record)
		})
		if err != nil {
			t.Fatal(err)
		}
		v = ojson.ParseBytes(queryJobCommand(s, historical.key).Body)
		if v.Get("status").String() != historical.want || v.Get("job_id").Exists() {
			t.Fatal("historical receipt invented a job")
		}
	}
}
