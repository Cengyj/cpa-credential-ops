package login

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cliproxyapi-plugins/model-quality/login/conversion"
	bolt "go.etcd.io/bbolt"
)

func TestBatchRepairDistinguishesMissingAndDamagedCrossStoreReceipt(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			opts := offlineOptions(t)
			opts.Call = h.call
			s := offlineService(t, opts)
			const key = "restore-original-batch"
			source := conversionFixture(t)
			payload := map[string]any{"source": source, "indexes": []int{0}}
			raw := mustEncodeBatch(t, payload)
			items, err := selectedConversions(raw, s.opts.Now())
			if err != nil {
				t.Fatal(err)
			}
			commitID := "conversion:" + conversion.Identity(items[0])
			name := items[0].Filename
			if r := batchRequest(t, s, context.Background(), "/conversion/upload", key, payload); r.StatusCode != 202 {
				t.Fatal(r.StatusCode)
			}
			waitBatch(t, s, key, "completed")
			canonical, err := canonicalRequest(raw)
			if err != nil {
				t.Fatal(err)
			}
			id := s.journal.fingerprint(key)
			if err := s.journal.acceptAccountBatch(id, s.journal.fingerprint("POST/conversion/upload", canonical), batchReceipt{Kind: "conversion", Status: "running", Total: 1, CommitID: commitID, Intent: &conversionOutcome{Index: 0, Filename: name}}); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			var backup []byte
			editReceipt := func(restore bool) {
				t.Helper()
				db, err := bolt.Open(filepath.Join(opts.Dir, "login-receipts.db"), 0600, &bolt.Options{Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if err := db.Update(func(tx *bolt.Tx) error {
					b := tx.Bucket([]byte("receipts"))
					if restore {
						return b.Put([]byte(commitID), backup)
					}
					backup = bytes.Clone(b.Get([]byte(commitID)))
					if len(backup) == 0 {
						return errors.New("fixture was not durably committed")
					}
					if mode == "missing" {
						return b.Delete([]byte(commitID))
					}
					return b.Put([]byte(commitID), []byte("{"))
				}); err != nil {
					t.Fatal(err)
				}
			}
			editReceipt(false)
			if mode == "corrupt" {
				service, err := New(opts)
				if err == nil {
					service.Close()
					t.Fatal("corrupt SDK receipt consumed the recoverable account-side intent")
				}
				if SafeCode(err) != "storage_unavailable" {
					t.Fatal("damaged storage not identified")
				}
				db, err := bolt.Open(filepath.Join(opts.Dir, "credential-login.db"), 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				readErr := db.View(func(tx *bolt.Tx) error {
					v, err := getBatch(tx, id)
					if err != nil {
						return err
					}
					if v.Status != "running" || v.Intent == nil || v.CommitID != commitID || tx.Bucket([]byte(batchBucket)).Bucket([]byte(activeBatchBucket)).Get([]byte(id)) == nil {
						return errors.New("corrupt receipt erased the recovery checkpoint")
					}
					return nil
				})
				if err := errors.Join(readErr, db.Close()); err != nil {
					t.Fatal(err)
				}
			} else {
				s = offlineService(t, opts)
				v := waitBatch(t, s, key, "reconciliation_required")
				if v.Items[0].Status != "uncertain" {
					t.Fatal("missing receipt was treated as a known write")
				}
				// Even if the CPA file is absent, another request key must be
				// stopped by the account-side evidence for this same identity.
				h.mu.Lock()
				delete(h.files, name)
				delete(h.entries, name)
				h.mu.Unlock()
				const fresh = "fresh-batch-after-missing-receipt"
				if r := batchRequest(t, s, context.Background(), "/conversion/upload", fresh, payload); r.StatusCode != 202 {
					t.Fatal(r.StatusCode)
				}
				waitBatch(t, s, fresh, "reconciliation_required")
				if h.saves != 1 {
					t.Fatal("fresh command repeated an unresolved write")
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			editReceipt(true)
			s = offlineService(t, opts)
			v := waitBatch(t, s, key, "completed")
			if v.Completed != 1 || len(v.Items) != 1 || v.Items[0].Status != "uploaded" || h.saves != 1 {
				t.Fatal("restored matching SDK receipt did not settle the original work without another write")
			}
			if r := batchRequest(t, s, context.Background(), "/conversion/upload", key, payload); r.StatusCode != 202 || h.saves != 1 {
				t.Fatal("replaying the original receipt issued new work")
			}
			t.Log("restored original receipt reconciled without a second SDK write")
		})
	}
}
