package login

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	bolt "go.etcd.io/bbolt"
)

func getStoredLogs(s *Service, id string) pluginapi.ManagementResponse {
	return s.Handle(context.Background(), pluginapi.ManagementRequest{
		Method: http.MethodGet, Path: RoutePrefix + "/logs", Query: url.Values{"id": {id}},
	})
}

func TestLogRouteChecksDurableIdentityAndDiagnostics(t *testing.T) {
	const id = "synthetic-history"
	const canary = "PRIVATE_DIAGNOSTIC_CANARY"
	s := offlineService(t, offlineOptions(t))
	base := historyJob(id, "", 1, time.Now())
	base.LogVersion = 1
	s.mu.Lock()
	s.jobs[id] = &session{record: base}
	s.mu.Unlock()
	write := func(t *testing.T, record storedJob) {
		t.Helper()
		if err := s.journal.db.Update(func(tx *bolt.Tx) error { return putGob(tx, "jobs", id, record) }); err != nil {
			t.Fatal(err)
		}
	}
	rowDigest := func(t *testing.T) (digest [sha256.Size]byte) {
		t.Helper()
		if err := s.journal.db.View(func(tx *bolt.Tx) error {
			if bucket := tx.Bucket([]byte("jobs")); bucket != nil {
				digest = sha256.Sum256(bucket.Get([]byte(id)))
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return digest
	}
	wantFault := func(t *testing.T) {
		t.Helper()
		before := rowDigest(t)
		r := getStoredLogs(s, id)
		v := ojson.ParseBytes(r.Body)
		if r.StatusCode != 503 || v.Get("code").String() != "storage_unavailable" || v.Get("source").String() != "plugin" ||
			v.Get("outcome_unknown").Type != ojson.False || strings.Contains(string(r.Body), canary) || v.Get("entries").Exists() {
			t.Fatalf("unsafe or misleading result returned from a damaged diagnostic row (HTTP %d)", r.StatusCode)
		}
		if before != rowDigest(t) {
			t.Fatal("the diagnostic read modified the original journal evidence")
		}
	}
	t.Run("other job in same slot", func(t *testing.T) {
		record := base
		record.View.ID = "different-job"
		record.LogEntries = []LogEntry{{ID: 1, Event: "request", RequestKind: "oauth_authorize", HTTPStatus: 503}}
		write(t, record)
		wantFault(t)
	})
	t.Run("tombstone wins over earlier session", func(t *testing.T) {
		record := base
		record.HistoryDeleted = true
		write(t, record)
		r := getStoredLogs(s, id)
		if r.StatusCode != 404 || ojson.GetBytes(r.Body, "code").String() != "not_found" {
			t.Fatalf("deleted diagnostics appeared as a successful empty page (HTTP %d)", r.StatusCode)
		}
	})
	for name, damage := range map[string]func(*LogEntry){
		"time":            func(e *LogEntry) { e.Time = canary },
		"oversized time":  func(e *LogEntry) { e.Time = "2026-11-21T12:00:00." + strings.Repeat("9", 500) + "Z" },
		"event":           func(e *LogEntry) { e.Event = canary },
		"level":           func(e *LogEntry) { e.Level = canary },
		"status":          func(e *LogEntry) { e.Status = canary },
		"phase":           func(e *LogEntry) { e.Phase = canary },
		"method":          func(e *LogEntry) { e.Method = canary },
		"request kind":    func(e *LogEntry) { e.RequestKind = canary },
		"security status": func(e *LogEntry) { e.SecurityStatus = canary },
		"error code":      func(e *LogEntry) { e.ErrorCode = canary },
		"error stage":     func(e *LogEntry) { e.ErrorStage = canary },
		"runtime code":    func(e *LogEntry) { e.RuntimeCode = canary },
		"error reason":    func(e *LogEntry) { e.ErrorReason = canary },
		"HTTP status":     func(e *LogEntry) { e.HTTPStatus = 600 },
		"duration":        func(e *LogEntry) { e.DurationMS = 900001 },
		"ID gap":          func(e *LogEntry) { e.ID++ },
	} {
		t.Run(name, func(t *testing.T) {
			record := base
			record.LogEntries = []LogEntry{{ID: 1, Level: "info"}, {ID: 2, Event: "state", Level: "info", Status: "queued"}}
			damage(&record.LogEntries[1])
			write(t, record)
			wantFault(t)
		})
	}
	t.Run("retention limit", func(t *testing.T) {
		record := base
		record.LogEntries = make([]LogEntry, maxJobLogEntries+1)
		for i := range record.LogEntries {
			record.LogEntries[i] = LogEntry{ID: uint64(i + 1), Level: "info"}
		}
		write(t, record)
		wantFault(t)
	})
	t.Run("unsupported format", func(t *testing.T) {
		record := base
		record.LogVersion = 2
		write(t, record)
		wantFault(t)
		other := base
		other.View.ID = "unrelated-healthy"
		if err := s.journal.save(other); err != nil {
			t.Fatal(err)
		}
		r := getStoredLogs(s, other.View.ID)
		if r.StatusCode != 200 || ojson.GetBytes(r.Body, "entries.0.id").Int() != 1 {
			t.Fatal("one incompatible job made the other job's accepted logs unreadable")
		}
	})
	t.Run("bucket missing", func(t *testing.T) {
		if err := s.journal.db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte("jobs")) }); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if recover() != nil {
				t.Error("damaged journal panicked in a management read")
			}
		}()
		wantFault(t)
	})
}

func TestLogRouteReadsEarlyEventShapeWithoutSessionIndex(t *testing.T) {
	const id = "early-log"
	s := offlineService(t, offlineOptions(t))
	// Gob omitted all modern runtime and event fields in the older schema.
	type earlyEntry struct {
		ID                                                  uint64
		Time, Level, Status, Phase, ErrorStage, ErrorReason string
	}
	early := struct {
		View       Job
		LogEntries []earlyEntry
	}{
		View: Job{ID: id},
		LogEntries: []earlyEntry{
			{ID: 7, Time: "2026-11-17T01:02:03Z", Level: "info", Status: "running", Phase: "password"},
			{ID: 8, Level: "info", Status: "interrupted", ErrorStage: "password", ErrorReason: "upstream_rejected"},
		},
	}
	if err := s.journal.db.Update(func(tx *bolt.Tx) error { return putGob(tx, "jobs", id, early) }); err != nil {
		t.Fatal(err)
	}
	result := s.Handle(context.Background(), pluginapi.ManagementRequest{
		Method: http.MethodGet, Path: RoutePrefix + "/logs", Query: url.Values{"id": {id}, "cursor": {"7"}, "limit": {"1"}},
	})
	page := ojson.ParseBytes(result.Body)
	entry := page.Get("entries.0")
	if result.StatusCode != 200 || page.Get("job_id").String() != id || !page.Get("legacy").Bool() || !page.Get("truncated").Bool() ||
		page.Get("retained_count").Int() != 2 || page.Get("first_id").Int() != 7 || page.Get("last_id").Int() != 8 ||
		page.Get("next_cursor").Int() != 8 || page.Get("has_more").Bool() || len(page.Get("entries").Array()) != 1 ||
		entry.Get("event").Exists() || entry.Get("id").Int() != 8 || entry.Get("level").String() != "info" ||
		entry.Get("status").String() != "interrupted" || entry.Get("error_reason").String() != "upstream_rejected" {
		t.Fatalf("legacy diagnostics or cursor metadata changed (HTTP %d)", result.StatusCode)
	}
}

func TestLogRouteConcurrentRemovalReturnsOnlyCompleteSnapshots(t *testing.T) {
	s := offlineService(t, offlineOptions(t))
	record := historyJob("removed-log", "", 1, time.Now())
	id := record.View.ID
	if err := s.journal.save(record); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.jobs[id] = &session{record: record}
	s.mu.Unlock()
	valid := func(r pluginapi.ManagementResponse) bool {
		v := ojson.ParseBytes(r.Body)
		if r.StatusCode == 404 {
			return v.Get("code").String() == "not_found" && !v.Get("entries").Exists()
		}
		return r.StatusCode == 200 && v.Get("job_id").String() == id && v.Get("retained_count").Int() == 1 &&
			len(v.Get("entries").Array()) == 1 && v.Get("entries.0.id").Int() == 1 && v.Get("entries.0.status").String() == record.View.Status
	}
	if r := getStoredLogs(s, id); !valid(r) || r.StatusCode != 200 {
		t.Fatal("accepted durable event was not visible before removal")
	}
	const readers = 32
	start := make(chan struct{})
	done := make(chan bool, readers)
	for range readers {
		go func() {
			<-start
			done <- valid(getStoredLogs(s, id))
		}()
	}
	close(start)
	err := s.DeleteJob(id, record.View.Revision)
	for range readers {
		if !<-done {
			t.Error("concurrent diagnostics returned a partial or foreign job")
		}
	}
	if r := getStoredLogs(s, id); err != nil || !valid(r) || r.StatusCode != 404 {
		t.Fatal("a completed removal still exposed a diagnostic page")
	}
}

func TestLogRouteOnReadOnlyStoreIsIndependentOfAdmission(t *testing.T) {
	s, j, _ := readOnlyJobFixture(t)
	id := j.record.View.ID
	done := make(chan pluginapi.ManagementResponse, 1)
	s.mu.Lock()
	go func() { done <- getStoredLogs(s, id) }()
	select {
	case result := <-done:
		s.mu.Unlock()
		if result.StatusCode != 200 || ojson.GetBytes(result.Body, "job_id").String() != id {
			t.Fatal("an accepted job in a read-only journal was not readable")
		}
	case <-time.After(350 * time.Millisecond):
		s.mu.Unlock()
		<-done
		t.Fatal("read-only diagnostics waited for an unrelated admission lock")
	}
	if err := s.journal.db.Close(); err != nil {
		t.Fatal(err)
	}
	r := getStoredLogs(s, "not-in-session-index")
	if r.StatusCode != 503 || ojson.GetBytes(r.Body, "source").String() != "plugin" {
		t.Fatal("the process index established a false absence while the store was closed")
	}
}
