package quality

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestRuleCommandBucketsRejectLostCompletedRunReceipts(t *testing.T) {
	en, host, rule := scopeFixture(t, scopedTarget("one", "a", "b"))
	const key = "issued-before-partial-restore"
	if _, err := en.acceptRuleRun(context.Background(), key, rule.ID, rule.Revision); err != nil {
		t.Fatal(err)
	}
	en.wg.Wait()
	receipt, err := en.ruleCommandStatus(key)
	if err != nil || receipt.State != "completed" || receipt.Result == nil || receipt.Result.Count != 1 || host.executeCalls != 0 {
		t.Fatal("fixture did not durably dispatch its fake scope", receipt, err)
	}
	if err := en.store.Write(func(tx *bolt.Tx) error {
		if first, _ := tx.Bucket([]byte(pendingRuleCommandsBucket)).Cursor().First(); first != nil {
			t.Fatal("completed run still in pending index")
		}
		// Reproduce a partial restore of a pre-marker database.
		if err := tx.Bucket([]byte("meta")).Delete([]byte(ruleCommandBucketsKey)); err != nil {
			return err
		}
		return tx.DeleteBucket([]byte(ruleCommandsBucket))
	}); err != nil {
		t.Fatal(err)
	}
	if err := en.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(en.cfg.DataDir, 0)
	if err == nil {
		defer reopened.Close()
		en.store = reopened
		_, lookupErr := en.ruleCommandStatus(key)
		t.Fatalf("partial restore accepted; previously completed command lookup: %v", lookupErr)
	}
	if host.executeCalls != 0 {
		t.Fatal("recovery replayed model work")
	}
}

func TestRuleCommandBucketLossWithSurvivingEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, kind, state string
		legacy            bool
		lost              []string
	}{
		{"marked create lost receipts", "create", "completed", false, []string{ruleCommandsBucket}},
		{"marked run lost both", "run", "completed", false, []string{pendingRuleCommandsBucket, ruleCommandsBucket}},
		{"marked run lost pending", "run", "completed", false, []string{pendingRuleCommandsBucket}},
		{"old terminal run proves missing pending", "run", "completed", true, []string{pendingRuleCommandsBucket}},
		{"old running receipt lost pending entry", "run", "running", true, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Write(func(tx *bolt.Tx) error {
				cmd := ruleCommandRecord{ruleCommandReceipt: ruleCommandReceipt{Key: "once", Kind: tt.kind, RuleID: "rule", Revision: 1}, RequestHash: "original"}
				if tt.kind == "run" {
					cmd.State = "accepted"
					if err := putRuleCommand(tx, &cmd); err != nil {
						return err
					}
				}
				cmd.State = tt.state
				if err := putRuleCommand(tx, &cmd); err != nil {
					return err
				}
				if tt.legacy {
					if err := tx.Bucket([]byte("meta")).Delete([]byte(ruleCommandBucketsKey)); err != nil {
						return err
					}
				}
				if len(tt.lost) == 0 {
					return tx.Bucket([]byte(pendingRuleCommandsBucket)).Delete([]byte(cmd.Key))
				}
				for _, name := range tt.lost {
					if err := tx.DeleteBucket([]byte(name)); err != nil {
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
			if reopened, err := OpenStore(dir, 0); err == nil {
				reopened.Close()
				t.Fatal("startup accepted incomplete command history")
			}
			raw, err := bolt.Open(filepath.Join(dir, "quality.db"), 0600, &bolt.Options{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if err := raw.View(func(tx *bolt.Tx) error {
				for _, name := range tt.lost {
					if tx.Bucket([]byte(name)) != nil {
						return fmt.Errorf("recreated lost bucket %s", name)
					}
				}
				if tt.legacy && tx.Bucket([]byte("meta")).Get([]byte(ruleCommandBucketsKey)) != nil {
					return fmt.Errorf("failed compatibility check marked incomplete history as verified")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuleCommandBucketUpgradeFromLegacyLayouts(t *testing.T) {
	for _, layout := range []string{"unused", "create only", "running and accepted", "old writer added pending"} {
		t.Run(layout, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir, 0)
			if err != nil {
				t.Fatal(err)
			}
			hasPending := layout == "running and accepted" || layout == "old writer added pending"
			err = s.Write(func(tx *bolt.Tx) error {
				if layout == "unused" {
					return nil
				}
				create := ruleCommandRecord{ruleCommandReceipt: ruleCommandReceipt{Key: "original-create", Kind: "create", RuleID: "original", State: "completed"}, RequestHash: "create-hash"}
				if err := putRuleCommand(tx, &create); err != nil {
					return err
				}
				if hasPending {
					for _, state := range []string{"running", "accepted"} {
						run := ruleCommandRecord{ruleCommandReceipt: ruleCommandReceipt{Key: state, Kind: "run", RuleID: "original", State: state}, RequestHash: "run-hash"}
						if err := putRuleCommand(tx, &run); err != nil {
							return err
						}
					}
				}
				meta := tx.Bucket([]byte("meta"))
				if layout == "old writer added pending" {
					return meta.Put([]byte(ruleCommandBucketsKey), []byte(ruleCommandsBucket))
				}
				return meta.Delete([]byte(ruleCommandBucketsKey))
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				s, err = OpenStore(dir, 0)
				if err != nil {
					t.Fatal(err)
				}
				en := &Engine{store: s}
				if err := en.interruptRuleCommands(); err != nil {
					t.Fatal(err)
				}
				if layout != "unused" {
					r, err := en.findRuleCommand("original-create", "create-hash")
					if err != nil || r == nil || r.RuleID != "original" || r.State != "completed" {
						t.Fatal("lost original creation receipt", r, err)
					}
				}
				if hasPending {
					for _, key := range []string{"accepted", "running"} {
						r, err := en.findRuleCommand(key, "run-hash")
						if err != nil || r == nil || r.State != "interrupted" || r.Error == nil || r.Error.Code != "interrupted" {
							t.Fatal("old active command did not stop at its original receipt", r, err)
						}
					}
				}
				err = s.db.View(func(tx *bolt.Tx) error {
					marker := string(tx.Bucket([]byte("meta")).Get([]byte(ruleCommandBucketsKey)))
					expected := ""
					if layout != "unused" {
						expected = ruleCommandsBucket
					}
					if hasPending {
						expected = pendingRuleCommandsBucket
					}
					if marker != expected || (tx.Bucket([]byte(ruleCommandsBucket)) != nil) != (layout != "unused") ||
						(tx.Bucket([]byte(pendingRuleCommandsBucket)) != nil) != hasPending {
						return fmt.Errorf("incorrect bucket layout or marker %q", marker)
					}
					if key, _ := tx.Bucket([]byte("runs")).Cursor().First(); key != nil {
						return fmt.Errorf("startup replayed an old command")
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRuleCommandBucketValidation(b *testing.B) {
	for _, count := range []int{16, 4096} {
		for _, layout := range []string{"unmarked", "marked"} {
			b.Run(fmt.Sprintf("%s/%d", layout, count), func(b *testing.B) {
				s, err := OpenStore(b.TempDir(), 0)
				if err != nil {
					b.Fatal(err)
				}
				defer s.Close()
				err = s.Write(func(tx *bolt.Tx) error {
					for i := range count {
						key := fmt.Sprintf("fixture-create-%05d", i)
						r := ruleCommandRecord{ruleCommandReceipt: ruleCommandReceipt{Key: key, Kind: "create", RuleID: key, State: "completed"}, RequestHash: "fake"}
						if err := putRuleCommand(tx, &r); err != nil {
							return err
						}
					}
					if layout == "unmarked" {
						return tx.Bucket([]byte("meta")).Delete([]byte(ruleCommandBucketsKey))
					}
					return nil
				})
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					tx, err := s.db.Begin(true)
					if err != nil {
						b.Fatal(err)
					}
					err = prepareRuleCommandBuckets(tx)
					rollbackErr := tx.Rollback()
					if err != nil || rollbackErr != nil {
						b.Fatal(err, rollbackErr)
					}
				}
			})
		}
	}
}
