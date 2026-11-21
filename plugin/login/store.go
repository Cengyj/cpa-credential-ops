package login

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	bolt "go.etcd.io/bbolt"
)

type journal struct {
	db       *bolt.DB
	key      []byte
	idemMu   sync.Mutex
	idemKeys map[string]*commandLock
	now      func() time.Time
}

const journalMaxBytes int64 = 256 << 20
const journalReserveBytes int64 = 8 << 20
const terminalRetention = 30 * 24 * time.Hour

type storedJob struct {
	AuthorizationSavedAt              string
	HistoryDeleted                    bool
	PlanObserved                      bool
	SecurityAccountID                 string
	SecurityStarted                   bool
	SecurityResolved                  bool
	AdoptedIdentity, AdoptedPrincipal string
	AccountEvidence                   string
	AccountSequence                   uint64
	View                              Job
	Created, Updated                  time.Time
	RateKey                           string
	LogVersion                        uint8
	LogEntries                        []LogEntry
}
type idempotency struct {
	JobPath      string
	BatchVersion uint8
	Retired      bool
	Fingerprint  string
	Pending      bool
	Response     []byte
	Code         string
	Created      time.Time
	JobID        string
}

func openJournal(dir string) (*journal, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("storage_unavailable")
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() || fi.Mode()&0077 != 0 {
		return nil, errors.New("storage_unavailable")
	}
	path := filepath.Join(dir, "credential-login.db")
	if fi, err := os.Lstat(path); err == nil && (!fi.Mode().IsRegular() || fi.Mode()&0077 != 0) {
		return nil, errors.New("storage_unavailable")
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	j := &journal{db: db, now: func() time.Time { return time.Now().UTC() }}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"jobs", "idempotency", "meta", "vault", "accounts"} {
			if _, e := tx.CreateBucketIfNotExists([]byte(name)); e != nil {
				return e
			}
		}
		b := tx.Bucket([]byte("meta"))
		schema := string(b.Get([]byte("schema")))
		if schema != "" && schema != "2" {
			return errors.New("storage_unavailable")
		}
		if schema == "" {
			if errEmpty := tx.ForEach(func(_ []byte, bucket *bolt.Bucket) error {
				if bucket.Stats().KeyN != 0 {
					return errors.New("storage_unavailable")
				}
				return nil
			}); errEmpty != nil {
				return errEmpty
			}
		}
		j.key = bytes.Clone(b.Get([]byte("hmac")))
		if len(j.key) != 32 {
			if schema != "" || len(j.key) != 0 || tx.Bucket([]byte("jobs")).Stats().KeyN != 0 || tx.Bucket([]byte("idempotency")).Stats().KeyN != 0 || tx.Bucket([]byte("accounts")).Stats().KeyN != 0 || tx.Bucket([]byte("vault")).Stats().KeyN != 0 {
				return errors.New("storage_unavailable")
			}
			j.key = make([]byte, 32)
			if _, e := rand.Read(j.key); e != nil {
				return e
			}
			if errKey := b.Put([]byte("hmac"), j.key); errKey != nil {
				return errKey
			}
		}
		if schema == "" {
			return b.Put([]byte("schema"), []byte("2"))
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, errors.New("storage_unavailable")
	}
	return j, nil
}
func (j *journal) admit() error { return j.capacity(false) }
func (j *journal) capacity(maintenance bool) error {
	info, err := os.Stat(j.db.Path())
	limit := journalMaxBytes - journalReserveBytes
	if maintenance {
		limit = journalMaxBytes - (1 << 20)
	}
	if err != nil || info.Size() >= limit {
		return errors.New("storage_capacity")
	}
	return nil
}
func (j *journal) prune(now time.Time) ([]string, error) {
	removed := []string{}
	err := j.db.Update(func(tx *bolt.Tx) error {
		protected := map[string]bool{}
		jobs := tx.Bucket([]byte("jobs"))
		cursor := jobs.Cursor()
		for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
			var record storedJob
			if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&record); err != nil {
				return err
			}
			status := record.View.Status
			if !terminal(status) || status == "saved_not_adopted" || status == "commit_unknown" || securityBlocks(record) {
				protected[record.View.ID] = true
				continue
			}
			if !record.Updated.IsZero() && !record.Updated.Add(terminalRetention).After(now) {
				removed = append(removed, record.View.ID)
				if err := cursor.Delete(); err != nil {
					return err
				}
			}
		}
		var retired map[string]idempotency
		cursor = tx.Bucket([]byte("idempotency")).Cursor()
		for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
			var record idempotency
			if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&record); err != nil {
				return err
			}
			jobID := record.JobID
			if jobID == "" {
				jobID = ojson.ParseBytes(record.Response).Get("id").String()
			}
			if record.BatchVersion != 0 || record.Retired || record.Pending || protected[jobID] || record.Created.IsZero() || record.Created.Add(terminalRetention).After(now) {
				continue
			}
			// Retain a compact reservation for commands whose result has aged out.
			// A cleanup or restart must never turn a consumed key into new authority.
			if retired == nil {
				retired = make(map[string]idempotency)
			}
			retired[string(key)] = idempotency{Fingerprint: record.Fingerprint, JobPath: record.JobPath, Retired: true}
		}
		for id, record := range retired {
			if err := putGob(tx, "idempotency", id, record); err != nil {
				return err
			}
		}
		return nil
	})
	return removed, err
}
func putGob(tx *bolt.Tx, bucket, key string, v any) error {
	var b bytes.Buffer
	if err := gob.NewEncoder(&b).Encode(v); err != nil {
		return err
	}
	return tx.Bucket([]byte(bucket)).Put([]byte(key), b.Bytes())
}
func (j *journal) fingerprint(parts ...string) string {
	h := hmac.New(sha256.New, j.key)
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalRequest keeps insignificant object ordering and whitespace out of the
// HMAC while preserving every string byte, including passwords and callbacks.
func canonicalRequest(raw []byte) (string, error) {
	if !ojson.ValidBytes(raw) {
		return "", errors.New("invalid_request")
	}
	var encode func(ojson.Result) (string, error)
	encode = func(value ojson.Result) (string, error) {
		if value.IsObject() {
			fields := map[string]ojson.Result{}
			keys := []string{}
			duplicate := false
			value.ForEach(func(key, child ojson.Result) bool {
				name := key.String()
				if _, found := fields[name]; found {
					duplicate = true
					return false
				}
				keys = append(keys, name)
				fields[name] = child
				return true
			})
			if duplicate {
				return "", errors.New("invalid_request")
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, key := range keys {
				child, err := encode(fields[key])
				if err != nil {
					return "", err
				}
				parts = append(parts, strconv.Quote(key)+":"+child)
			}
			return "{" + strings.Join(parts, ",") + "}", nil
		}
		if value.IsArray() {
			parts := []string{}
			for _, child := range value.Array() {
				encoded, err := encode(child)
				if err != nil {
					return "", err
				}
				parts = append(parts, encoded)
			}
			return "[" + strings.Join(parts, ",") + "]", nil
		}
		if value.Type == ojson.String {
			return strconv.Quote(value.String()), nil
		}
		return value.Raw, nil
	}
	return encode(ojson.ParseBytes(raw))
}
func (j *journal) save(v storedJob, diagnostic ...LogEntry) error {
	return j.db.Update(func(tx *bolt.Tx) error { return j.saveTx(tx, v, diagnostic...) })
}

// saveTx preserves the diagnostic stream when job state shares a transaction
// with account credentials or admission metadata.
func (j *journal) saveTx(tx *bolt.Tx, v storedJob, diagnostic ...LogEntry) error {
	var previous storedJob
	raw := tx.Bucket([]byte("jobs")).Get([]byte(v.View.ID))
	if raw != nil {
		if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&previous); err != nil {
			return err
		}
	}
	// Keep the event and the state it describes in the same transaction.
	v.LogVersion, v.LogEntries = previous.LogVersion, previous.LogEntries
	if raw == nil {
		v.LogVersion = 1
	}
	events := []LogEntry{}
	event := safeLogEntry(v.View)
	if raw == nil || event != safeLogEntry(previous.View) {
		events = append(events, event)
	}
	events = append(events, diagnostic...)
	for _, event := range events {
		event.Revision = v.View.Revision
		event.ID = 1
		if len(v.LogEntries) > 0 {
			event.ID = v.LogEntries[len(v.LogEntries)-1].ID + 1
		}
		event.Time = j.now().UTC().Format(time.RFC3339Nano)
		v.LogEntries = append(v.LogEntries, event)
		if len(v.LogEntries) > maxJobLogEntries {
			v.LogEntries = v.LogEntries[len(v.LogEntries)-maxJobLogEntries:]
		}
	}
	v.View.AuthorizationURL, v.View.Email = "", ""
	return putGob(tx, "jobs", v.View.ID, v)
}

func (j *journal) load() ([]storedJob, error) {
	out := []storedJob{}
	err := j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("jobs")).ForEach(func(k, v []byte) error {
			var s storedJob
			if err := gob.NewDecoder(bytes.NewReader(v)).Decode(&s); err != nil {
				return err
			}
			out = append(out, s)
			return nil
		})
	})
	return out, err
}

type commandLock struct {
	mu    sync.Mutex
	users int
}

func (j *journal) lockCommand(id string) func() {
	j.idemMu.Lock()
	if j.idemKeys == nil {
		j.idemKeys = make(map[string]*commandLock)
	}
	lock := j.idemKeys[id]
	if lock == nil {
		lock = &commandLock{}
		j.idemKeys[id] = lock
	}
	lock.users++
	j.idemMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		j.idemMu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(j.idemKeys, id)
		}
		j.idemMu.Unlock()
	}
}

func validCommandKey(key string) bool {
	return len(key) >= 8 && len(key) <= 128 && !strings.ContainsAny(key, "\r\n\x00")
}

func (j *journal) commandRecord(id string) (*idempotency, error) {
	var record *idempotency
	err := j.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte("idempotency")).Get([]byte(id))
		if raw == nil {
			return nil
		}
		record = new(idempotency)
		return gob.NewDecoder(bytes.NewReader(raw)).Decode(record)
	})
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	return record, nil
}

func (record *idempotency) check(fingerprint string) error {
	switch {
	case record.Fingerprint != fingerprint:
		return errors.New("idempotency_conflict")
	case record.Retired:
		return errors.New("command_result_expired")
	case record.Pending:
		return errors.New("idempotency_unknown")
	case record.Code != "":
		if !publicStoredCode(record.Code) {
			return errors.New("storage_unavailable")
		}
		return errors.New(record.Code)
	}
	return nil
}

// Idempotent records intent before executing the operation. An interrupted response
// remains unknown instead of silently repeating an authorization or mutation.
func (j *journal) idempotent(key, path string, raw []byte, fn func() (any, error)) ([]byte, error) {
	if !validCommandKey(key) {
		return nil, errors.New("invalid_request")
	}
	canonical, err := canonicalRequest(raw)
	if err != nil {
		return nil, err
	}
	id := j.fingerprint(key)
	fingerprint := j.fingerprint(path, canonical)
	release := j.lockCommand(id)
	defer release()
	old, err := j.commandRecord(id)
	if err != nil {
		return nil, err
	}
	if old != nil {
		if err := old.check(fingerprint); err != nil {
			return nil, err
		}
		if jobReplyPath(path) {
			return cachedJobReply(old.Response, old.JobID)
		}
		return old.Response, nil
	}
	maintenance := strings.HasSuffix(path, "/cancel") || strings.HasSuffix(path, "/discard") || strings.HasSuffix(path, "/commit") || path == "POST/accounts/security/complete" || strings.HasPrefix(path, "DELETE")
	if err = j.capacity(maintenance); err != nil {
		return nil, err
	}
	record := idempotency{Fingerprint: fingerprint, Pending: true, Created: j.now(), JobPath: jobCommandPath(path)}
	if err = j.db.Update(func(tx *bolt.Tx) error { return putGob(tx, "idempotency", id, record) }); err != nil {
		return nil, errors.New("storage_unavailable")
	}
	value, opErr := fn()
	// Once the operation has run, a lost result cannot establish non-execution.
	// Leave the durable pending reservation in place for a same-key lookup.
	response, e := Encode(value)
	if e != nil {
		return nil, errors.New("idempotency_unknown")
	}
	if record.JobPath != "" && opErr != nil {
		if inserted, readErr := j.commandRecord(id); readErr != nil || inserted != nil && inserted.JobID != "" {
			// The operation may have failed after atomically admitting a job.
			// Keep its identity attached for a read-only outcome lookup.
			return nil, errors.New("idempotency_unknown")
		}
	}
	record.Pending = false
	record.Code = SafeCode(opErr)
	// Authorization links are transient; detail reconstructs them in memory.
	d := ojsonDocumentWithoutAuthorization(response)
	record.Response = d
	record.JobID = ojson.ParseBytes(d).Get("id").String()
	if err = j.db.Update(func(tx *bolt.Tx) error { return putGob(tx, "idempotency", id, record) }); err != nil {
		return nil, errors.New("idempotency_unknown")
	}
	if opErr != nil {
		return nil, errors.New(record.Code)
	}
	return response, nil
}
