package quality

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestOpenStorePreservesExistingZeroByteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quality.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir, 0)
	if err == nil {
		_ = s.Close()
		t.Fatal("truncated database was accepted as a new store")
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != 0 {
		t.Fatalf("truncated file changed during failed startup: %v, %v", info, err)
	}
}

func TestOpenStoreRejectsIncompleteInitializedDatabase(t *testing.T) {
	for _, missing := range append(append([]string{}, buckets...), "schema") {
		t.Run(missing, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir, 0)
			if err != nil {
				t.Fatal(err)
			}
			err = s.Write(func(tx *bolt.Tx) error {
				other, err := tx.CreateBucket([]byte("retained-extension"))
				if err != nil {
					return err
				}
				if err := other.Put([]byte("receipt"), []byte("preserve")); err != nil {
					return err
				}
				if missing == "schema" {
					return tx.Bucket([]byte("meta")).Delete([]byte("schema"))
				}
				return tx.DeleteBucket([]byte(missing))
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(dir, 0)
			if err == nil {
				reopened.Close()
				t.Fatal("incomplete initialized database accepted as healthy")
			}
			db, err := bolt.Open(filepath.Join(dir, "quality.db"), 0600, &bolt.Options{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.View(func(tx *bolt.Tx) error {
				if !bytes.Equal(tx.Bucket([]byte("retained-extension")).Get([]byte("receipt")), []byte("preserve")) {
					t.Fatal("remaining data changed during failed startup")
				}
				if missing == "schema" {
					if tx.Bucket([]byte("meta")).Get([]byte("schema")) != nil {
						t.Fatal("missing schema marker was rewritten")
					}
				} else if tx.Bucket([]byte(missing)) != nil {
					t.Fatal("missing bucket was recreated")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenStoreDistinguishesNewAndPreviouslyCommittedEmptyRoot(t *testing.T) {
	for _, name := range []string{"unused", "emptied after use"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "quality.db")
			db, err := bolt.Open(path, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			if name == "emptied after use" {
				if err := db.Update(prepareQualityStore); err != nil {
					t.Fatal(err)
				}
				if err := db.Update(func(tx *bolt.Tx) error {
					for _, bucket := range buckets {
						if err := tx.DeleteBucket([]byte(bucket)); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := OpenStore(dir, 0)
			if s != nil {
				defer s.Close()
			}
			if name == "unused" && err != nil {
				t.Fatalf("fresh empty bbolt database rejected: %v", err)
			}
			if name == "emptied after use" && err == nil {
				t.Fatal("previously initialized and emptied database was reinitialized")
			}
		})
	}
}
