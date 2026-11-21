package quality

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

type Store struct {
	db      *bolt.DB
	history auditIndex
}

var buckets = []string{"meta", "runs", "rules", "audit", "usage", "idempotency", "presets"}

func OpenStore(dir string, _ int64) (*Store, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	db, e := bolt.Open(filepath.Join(dir, "quality.db"), 0600, &bolt.Options{Timeout: time.Nanosecond, OpenFile: openQualityFile})
	if e != nil {
		return nil, fmt.Errorf("open plugin store (single instance required): %w", e)
	}
	s := &Store{db: db}
	e = db.Update(prepareQualityStore)
	if e != nil {
		return nil, errors.Join(e, db.Close())
	}
	return s, nil
}

// bbolt initializes any zero-length file before a transaction can inspect it.
// Only the process that created the file may take that initialization path.
func openQualityFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	if flags&os.O_CREATE == 0 || flags&os.O_TRUNC != 0 {
		// bbolt also calls this hook for snapshot reads and copies.
		return os.OpenFile(path, flags, mode)
	}
	file, err := os.OpenFile(path, flags|os.O_EXCL, mode)
	if !errors.Is(err, os.ErrExist) {
		return file, err
	}
	file, err = os.OpenFile(path, flags&^os.O_CREATE, mode)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && info.Size() > 0 {
		return file, nil
	}
	if err == nil {
		err = errors.New("incomplete quality store: existing file is empty")
	}
	return nil, errors.Join(err, file.Close())
}

func prepareQualityStore(tx *bolt.Tx) error {
	first, _ := tx.Cursor().First()
	newStore := first == nil
	// bbolt initializes its two metadata pages at IDs 0 and 1. A later
	// empty root has a committed history, so its former data is unknown.
	if newStore && tx.ID() != 2 {
		return fmt.Errorf("incomplete quality store: empty database after prior transactions")
	}
	if !newStore {
		meta := tx.Bucket([]byte("meta"))
		if meta == nil || meta.Get([]byte("schema")) == nil {
			return fmt.Errorf("incomplete quality store: missing schema in a populated database")
		}
		version := string(meta.Get([]byte("schema")))
		if version == "2" {
			// Recreating an empty bucket here would silently discard evidence
			// of a partial restore, including command and ownership history.
			for _, name := range buckets {
				if tx.Bucket([]byte(name)) == nil {
					return fmt.Errorf("incomplete quality store: missing required bucket %s", name)
				}
			}
			return prepareRuleCommandBuckets(tx)
		}
		if version != "1" {
			return fmt.Errorf("unsupported plugin store schema")
		}
	}
	for _, name := range buckets {
		if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
			return err
		}
	}
	if !newStore {
		if err := migrateManualOnly(tx); err != nil {
			return err
		}
	}
	if err := tx.Bucket([]byte("meta")).Put([]byte("schema"), []byte("2")); err != nil {
		return err
	}
	return prepareRuleCommandBuckets(tx)
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Put(bucket, key string, v any) error {
	return s.Write(func(tx *bolt.Tx) error { return put(tx, bucket, key, v) })
}
func (s *Store) Write(fn func(*bolt.Tx) error) error {
	if err := s.db.Update(fn); err != nil {
		return fmt.Errorf("unavailable: write plugin state: %w", err)
	}
	return nil
}
func put(tx *bolt.Tx, bucket, key string, v any) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return tx.Bucket([]byte(bucket)).Put([]byte(key), raw)
}
func (s *Store) Get(bucket, key string, v any) error {
	return s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket)).Get([]byte(key))
		if b == nil {
			return os.ErrNotExist
		}
		return json.Unmarshal(b, v)
	})
}
func (s *Store) All(bucket string, fn func([]byte) error) error {
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucket)).ForEach(func(k, v []byte) error { return fn(v) })
	})
	if err != nil {
		return fmt.Errorf("unavailable: read plugin state: %w", err)
	}
	return nil
}

// Prune only terminal history; actions and rule ownership are never expired.
func (s *Store) Prune(before time.Time, keep map[string]bool, cutoffs map[string]time.Time) error {
	_, err := s.prune(historyRetention{before: before, keepRuns: keep, cutoffs: cutoffs}, nil)
	return err
}

type historyRetention struct {
	before      time.Time
	keepRuns    map[string]bool
	keepActions map[string]bool
	cutoffs     map[string]time.Time
}

// The engine holds its mutex for this transaction. Its committed runs carry
// the same completion timestamps as the stored rows, including after reload.
func (s *Store) prune(policy historyRetention, loaded map[string]*Run) ([]string, error) {
	var deletedRuns []string
	err := s.db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range []string{"runs", "usage", "audit"} {
			b := tx.Bucket([]byte(bucket))
			c := b.Cursor()
			var expired []string
			for k, v := c.First(); k != nil; k, v = c.Next() {
				if bucket == "runs" && policy.keepRuns[string(k)] {
					continue
				}
				cutoff := policy.before
				if bucket == "runs" {
					if custom, ok := policy.cutoffs[string(k)]; ok {
						cutoff = custom
					}
				}
				var ts time.Time
				switch bucket {
				case "runs":
					r := loaded[string(k)]
					if r == nil {
						r = new(Run)
						if err := decode(v, r); err != nil {
							return err
						}
					}
					if r.CompletedAt != nil {
						ts = *r.CompletedAt
					}
				case "usage":
					var r UsageEvent
					if e := json.Unmarshal(v, &r); e != nil {
						return e
					}
					ts = r.Time
				case "audit":
					var r Audit
					if e := json.Unmarshal(v, &r); e != nil {
						return e
					}
					if policy.keepActions[r.ActionID] {
						continue
					}
					ts = r.Time
					if custom, ok := policy.cutoffs[r.RunID]; ok {
						cutoff = custom
					}
				}
				if !ts.IsZero() && ts.Before(cutoff) {
					expired = append(expired, string(k))
				}
			}
			for _, key := range expired {
				if err := b.Delete([]byte(key)); err != nil {
					return err
				}
			}
			if bucket == "runs" {
				deletedRuns = expired
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return deletedRuns, nil
}
