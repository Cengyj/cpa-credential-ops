package quality

import (
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

// migrateManualOnly atomically removes legacy recovery settings and retires old work.
func migrateManualOnly(tx *bolt.Tx) error {
	rules := map[string]*Rule{}
	held := map[string]bool{}
	if err := tx.Bucket([]byte("rules")).ForEach(func(k, v []byte) error {
		var r Rule
		if err := json.Unmarshal(v, &r); err != nil {
			return err
		}
		rules[string(k)] = &r
		if r.ActionID != "" {
			held[r.AuthID] = true
		}
		return nil
	}); err != nil {
		return err
	}
	for id, r := range rules {
		r.Revision++
		resetRule(r)
		for authID := range held {
			if usesCredential(r.RunConfig, authID) {
				r.Enabled = false
				r.BlockedAuthID = authID
			}
		}
		r.Status = ruleStatus(r)
		if r.Enabled {
			r.NextRun = nextRun(*r, time.Now().UTC())
		}
		if err := put(tx, "rules", id, r); err != nil {
			return err
		}
	}
	if raw := tx.Bucket([]byte("meta")).Get([]byte("settings")); raw != nil {
		var c Config
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		if err := putQualityConfig(tx, "settings", c); err != nil {
			return err
		}
	}
	return tx.Bucket([]byte("meta")).Put([]byte("policy"), []byte("manual-release-only-v2"))
}
