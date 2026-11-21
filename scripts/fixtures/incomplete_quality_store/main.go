// Fixture for an incomplete schema-2 restore. Only creates a new scratch file.
package main

import (
	"errors"
	"log"
	"os"

	bolt "go.etcd.io/bbolt"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("provide a new scratch database path")
	}
	if err := seed(os.Args[1]); err != nil {
		log.Fatal(err)
	}
}

func seed(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"meta", "runs", "rules", "audit", "usage", "presets"} {
			if _, err := tx.CreateBucket([]byte(name)); err != nil {
				return err
			}
		}
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), []byte("2"))
	})
	return errors.Join(err, db.Close())
}
