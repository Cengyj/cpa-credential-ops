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

func TestJobDiagnosticsDisplayAndOriginalCommand(t *testing.T) {
	const marker = "PRIVATE-DIAGNOSTIC-CANARY"
	const key, id = "synthetic-display-command", "synthetic-display-job"
	s := offlineService(t, offlineOptions(t))
	account := importOfflineAccount(t, s)
	body := []byte(`{}`)
	canonical, err := canonicalRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := s.journal.fingerprint("POST/accounts/start", canonical)
	base := historyJob(id, account.ID, 1, time.Now())
	base.View.Status, base.View.ErrorCode = "failed", "exchange_failed"
	base.LogVersion = 1
	base.LogEntries = []LogEntry{{ID: 1, Level: "info", Status: "running", Phase: "oauth"}}
	seed := func(t *testing.T, record storedJob, cached Job, code string) {
		t.Helper()
		raw, err := Encode(cached)
		if err != nil {
			t.Fatal(err)
		}
		link := idempotency{Fingerprint: fingerprint, JobPath: "/accounts/start", JobID: id, Response: raw, Code: code}
		if err := s.journal.db.Update(func(tx *bolt.Tx) error {
			if err := putGob(tx, "jobs", id, record); err != nil {
				return err
			}
			return putGob(tx, "idempotency", s.journal.fingerprint(key), link)
		}); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		s.jobs[id] = &session{record: record}
		s.mu.Unlock()
	}
	digest := func(t *testing.T) (sum [2][32]byte) {
		t.Helper()
		if err := s.journal.db.View(func(tx *bolt.Tx) error {
			sum[0] = sha256.Sum256(tx.Bucket([]byte("jobs")).Get([]byte(id)))
			sum[1] = sha256.Sum256(tx.Bucket([]byte("idempotency")).Get([]byte(s.journal.fingerprint(key))))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return sum
	}
	get := func(route string, query url.Values) pluginapi.ManagementResponse {
		return s.Handle(context.Background(), pluginapi.ManagementRequest{Method: http.MethodGet, Path: RoutePrefix + route, Query: query})
	}
	replay := func() pluginapi.ManagementResponse {
		return s.Handle(context.Background(), pluginapi.ManagementRequest{Method: http.MethodPost, Path: RoutePrefix + "/accounts/start",
			Headers: http.Header{"Idempotency-Key": {key}}, Body: body})
	}
	fault := func(t *testing.T, path string, reply pluginapi.ManagementResponse) {
		t.Helper()
		v := ojson.ParseBytes(reply.Body)
		if reply.StatusCode != 503 || v.Get("code").String() != "storage_unavailable" || v.Get("source").String() != "plugin" ||
			strings.Contains(string(reply.Body), marker) || v.Get("job").Exists() || v.Get("items").Exists() {
			t.Errorf("%s returned HTTP %d, echoed private marker %v", path, reply.StatusCode, strings.Contains(string(reply.Body), marker))
		}
	}
	for name, damage := range map[string]func(*Job){
		"method": func(j *Job) { j.Method = marker }, "intent": func(j *Job) { j.Intent = marker },
		"action": func(j *Job) { j.Action = marker }, "status": func(j *Job) { j.Status = marker },
		"phase": func(j *Job) { j.Phase = marker }, "error code": func(j *Job) { j.ErrorCode = marker },
		"error stage": func(j *Job) { j.ErrorStage = marker }, "error reason": func(j *Job) { j.ErrorReason = marker },
		"runtime code": func(j *Job) { j.RuntimeCode = marker }, "vault code": func(j *Job) { j.VaultError = marker },
		"security status": func(j *Job) { j.SecurityStatus = marker }, "created": func(j *Job) { j.CreatedAt = marker },
		"expiry": func(j *Job) { j.ExpiresAt = marker }, "HTTP status": func(j *Job) { j.HTTPStatus = 999 },
	} {
		t.Run(name, func(t *testing.T) {
			record := base
			damage(&record.View)
			seed(t, record, record.View, "")
			before := digest(t)
			fault(t, "detail", getJobDetail(s, id))
			fault(t, "history", get("/jobs", nil))
			fault(t, "account history", get("/jobs", url.Values{"account_id": {account.ID}}))
			fault(t, "command receipt", queryJobCommand(s, key))
			fault(t, "same-key response", replay())
			if logs := getStoredLogs(s, id); logs.StatusCode != 200 || strings.Contains(string(logs.Body), marker) {
				t.Error("clean event stream unavailable")
			}
			if digest(t) != before {
				t.Error("reading or replaying rewrote the job or command")
			}
		})
	}
	t.Run("cached response diverges from clean current job", func(t *testing.T) {
		old := base.View
		old.ErrorReason = marker
		seed(t, base, old, "")
		before := digest(t)
		if r := queryJobCommand(s, key); r.StatusCode != 200 || strings.Contains(string(r.Body), marker) {
			t.Error("clean current receipt disappeared")
		}
		fault(t, "cached result", replay())
		if digest(t) != before {
			t.Error("reading a damaged cached response ran or modified the command")
		}
	})
	t.Run("account remains available with a damaged summary", func(t *testing.T) {
		row := base
		row.View.ErrorReason = marker
		seed(t, row, row.View, "")
		s.accountMu.Lock()
		a := s.accounts[account.ID]
		a.View.LastJobID = id
		s.accounts[account.ID] = a
		s.accountMu.Unlock()
		got := get("/accounts", nil)
		entry := ojson.GetBytes(got.Body, "items.0")
		if got.StatusCode != 200 || entry.Get("id").String() != account.ID || entry.Get("last_job_id").String() != id ||
			entry.Get("last_job_error_code").String() != "storage_unavailable" || strings.Contains(string(got.Body), marker) {
			t.Error("account listing lost the account or included its damaged task diagnostics")
		}
	})
	t.Run("corrupt failure code on a command without a job", func(t *testing.T) {
		link := idempotency{Fingerprint: fingerprint, JobPath: "/accounts/start", Code: marker}
		if err := s.journal.db.Update(func(tx *bolt.Tx) error {
			return putGob(tx, "idempotency", s.journal.fingerprint(key), link)
		}); err != nil {
			t.Fatal(err)
		}
		before := digest(t)
		fault(t, "failed command", queryJobCommand(s, key))
		fault(t, "failed same-key response", replay())
		if digest(t) != before {
			t.Error("failed command was replaced")
		}
	})
	t.Run("historical presentation remains unchanged", func(t *testing.T) {
		row := base
		row.View.Phase, row.View.ErrorStage, row.View.ErrorReason = "complete", "mfa", "proxy_connection_failed"
		row.View.ErrorCode = "network_error"
		seed(t, row, row.View, "")
		for name, reply := range map[string]pluginapi.ManagementResponse{"detail": getJobDetail(s, id), "history": get("/jobs", nil), "command": queryJobCommand(s, key), "replay": replay()} {
			if reply.StatusCode != 200 || !strings.Contains(string(reply.Body), `"phase":"complete"`) || !strings.Contains(string(reply.Body), `"error_reason":"proxy_connection_failed"`) {
				t.Errorf("%s lost fixed historical labels (HTTP %d)", name, reply.StatusCode)
			}
		}
	})
}
