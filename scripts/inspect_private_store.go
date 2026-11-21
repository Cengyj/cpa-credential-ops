//go:build ignore

// Read a private stable copy and print only a canonical logical database digest.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
	"gopkg.in/yaml.v3"
)

type result struct {
	Format  string          `json:"format"`
	SHA256  string          `json:"sha256"`
	Buckets uint64          `json:"buckets"`
	Keys    uint64          `json:"keys"`
	Startup *startupSummary `json:"quality_startup,omitempty"`
}

type startupSummary struct {
	SchemaUpgrade      bool   `json:"schema_upgrade"`
	UnfinishedRuns     uint64 `json:"queued_or_running_runs"`
	LegacyScopeRules   uint64 `json:"legacy_scope_rules"`
	PendingWrites      uint64 `json:"pending_credential_writes"`
	InvalidJSONRecords uint64 `json:"invalid_json_records"`
}

func qualityStartup(tx *bolt.Tx) *startupSummary {
	meta, rules, runs := tx.Bucket([]byte("meta")), tx.Bucket([]byte("rules")), tx.Bucket([]byte("runs"))
	if meta == nil || rules == nil || runs == nil || tx.Bucket([]byte("audit")) == nil {
		return nil
	}
	out := &startupSummary{SchemaUpgrade: string(meta.Get([]byte("schema"))) != "2"}
	_ = rules.ForEach(func(_, raw []byte) error {
		var rule struct {
			ObjectTypes []string `json:"object_types"`
		}
		if json.Unmarshal(raw, &rule) != nil {
			out.InvalidJSONRecords++
		} else if rule.ObjectTypes == nil {
			out.LegacyScopeRules++
		}
		return nil
	})
	_ = runs.ForEach(func(_, raw []byte) error {
		var run struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(raw, &run) != nil {
			out.InvalidJSONRecords++
		} else if run.Status == "queued" || run.Status == "running" {
			out.UnfinishedRuns++
		}
		return nil
	})
	_ = meta.ForEach(func(key, raw []byte) error {
		if !strings.HasPrefix(string(key), "credential-disposition:") {
			return nil
		}
		var disposition struct {
			State string `json:"state"`
		}
		if json.Unmarshal(raw, &disposition) != nil {
			out.InvalidJSONRecords++
		} else if strings.HasSuffix(disposition.State, "_pending") {
			out.PendingWrites++
		}
		return nil
	})
	return out
}

func frame(h hash.Hash, tag byte, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write([]byte{tag})
	_, _ = h.Write(size[:])
	_, _ = h.Write(value)
}

func bucketDigest(h hash.Hash, name []byte, bucket *bolt.Bucket, out *result) error {
	frame(h, 'B', name)
	var sequence [8]byte
	binary.BigEndian.PutUint64(sequence[:], bucket.Sequence())
	frame(h, 'S', sequence[:])
	out.Buckets++
	err := bucket.ForEach(func(key, value []byte) error {
		if child := bucket.Bucket(key); child != nil {
			return bucketDigest(h, key, child, out)
		}
		frame(h, 'K', key)
		frame(h, 'V', value)
		out.Keys++
		return nil
	})
	frame(h, 'E', nil)
	return err
}

func inspect(path string) (result, error) {
	out := result{Format: "bbolt-logical-v1"}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return out, errors.New("private regular snapshot required")
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return out, errors.New("cannot open private snapshot")
	}
	h := sha256.New()
	frame(h, 'F', []byte(out.Format))
	err = db.View(func(tx *bolt.Tx) error {
		valid := true
		for range tx.Check() {
			valid = false
		}
		if !valid {
			return errors.New("snapshot integrity failed")
		}
		out.Startup = qualityStartup(tx)
		return tx.ForEach(func(name []byte, bucket *bolt.Bucket) error {
			return bucketDigest(h, name, bucket, &out)
		})
	})
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		return out, errors.New("snapshot inspection failed")
	}
	out.SHA256 = hex.EncodeToString(h.Sum(nil))
	return out, nil
}

type configurationVerification struct {
	Verified       bool `json:"verified"`
	AuthKVVerified bool `json:"auth_kv_verified"`
}

func verifyConfiguration(path string) configurationVerification {
	var out configurationVerification
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var config struct {
		Server struct {
			KVStore struct {
				Disabled bool   `yaml:"disabled"`
				Path     string `yaml:"path"`
			} `yaml:"kv-store"`
		} `yaml:"server"`
		OAuth struct {
			AuthDir string `yaml:"auth-dir"`
		} `yaml:"oauth"`
		Plugins struct {
			Dir     string `yaml:"dir"`
			Configs map[string]struct {
				DataDir string `yaml:"data_dir"`
			} `yaml:"configs"`
		} `yaml:"plugins"`
	}
	var fields map[string]yaml.Node
	if yaml.Unmarshal(raw, &config) != nil || yaml.Unmarshal(raw, &fields) != nil {
		return out
	}
	out.Verified = config.OAuth.AuthDir == "/root/.cli-proxy-api" &&
		config.Plugins.Dir == "/CLIProxyAPI/plugins" &&
		config.Plugins.Configs["model-quality"].DataDir == "/CLIProxyAPI/plugin-data/model-quality"
	_, legacyKV := fields["kv-store"]
	kvPath := strings.TrimSpace(config.Server.KVStore.Path)
	// Only the exact, enabled v8 auth-dir KV location gets a logical comparison.
	out.AuthKVVerified = out.Verified && !legacyKV && !config.Server.KVStore.Disabled &&
		(kvPath == "" || kvPath == "/root/.cli-proxy-api/kv.db")
	return out
}

type qualityComparison struct {
	Format             string `json:"format"`
	Preserved          bool   `json:"preserved"`
	EmptyAuditsAdded   uint64 `json:"scheduled_empty_audits_added"`
	RuleNextRunUpdated uint64 `json:"rule_next_run_updated"`
	HostInputMigrated  bool   `json:"quality_host_input_migrated"`
	UnexpectedChanges  uint64 `json:"unexpected_changes"`
}

// Reject duplicate fields so the narrow exceptions cannot conceal extra writes.
func objectFields(raw []byte) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, false
	}
	return fields, true
}

func stringField(fields map[string]json.RawMessage, key string) (string, bool) {
	var value string
	raw, ok := fields[key]
	if !ok || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func emptyField(fields map[string]json.RawMessage, key string) bool {
	if _, exists := fields[key]; !exists {
		return true
	}
	value, ok := stringField(fields, key)
	return ok && value == ""
}

func authorizedScheduledRule(tx *bolt.Tx, key []byte) bool {
	rules, meta := tx.Bucket([]byte("rules")), tx.Bucket([]byte("meta"))
	if rules == nil || meta == nil {
		return false
	}
	fields, ok := objectFields(rules.Get(key))
	id, hasID := stringField(fields, "id")
	if !ok || !hasID || id != string(key) || string(fields["enabled"]) != "true" ||
		string(fields["scheduling_enabled"]) != "true" || !emptyField(fields, "action_id") || !emptyField(fields, "blocked_auth_id") {
		return false
	}
	settings, validSettings := objectFields(meta.Get([]byte("settings")))
	if validSettings && string(settings["scheduling_enabled"]) == "true" {
		return true
	}
	timers, validTimers := objectFields(meta.Get([]byte("explicit_rule_timers")))
	return validTimers && string(timers[id]) == "true"
}

func onlyNextRunChanged(tx *bolt.Tx, key, before, after []byte) bool {
	a, validA := objectFields(before)
	b, validB := objectFields(after)
	if !validA || !validB || len(a) != len(b) || !authorizedScheduledRule(tx, key) {
		return false
	}
	for name, value := range a {
		current, exists := b[name]
		if !exists || (name != "next_run" && !bytes.Equal(value, current)) {
			return false
		}
	}
	previous, okA := stringField(a, "next_run")
	current, okB := stringField(b, "next_run")
	_, errA := time.Parse(time.RFC3339Nano, previous)
	_, errB := time.Parse(time.RFC3339Nano, current)
	return okA && okB && errA == nil && errB == nil && previous != current
}

func allowedEmptyAudit(tx *bolt.Tx, key, raw []byte) bool {
	fields, ok := objectFields(raw)
	if !ok {
		return false
	}
	for name := range fields {
		switch name {
		case "id", "time", "kind", "rule_id", "run_id", "action_id", "detail":
		default:
			return false
		}
	}
	id, validID := stringField(fields, "id")
	rule, validRule := stringField(fields, "rule_id")
	kind, _ := stringField(fields, "kind")
	detail, _ := stringField(fields, "detail")
	stamp, validTime := stringField(fields, "time")
	_, timeErr := time.Parse(time.RFC3339Nano, stamp)
	return validID && id == string(key) && validRule && authorizedScheduledRule(tx, []byte(rule)) &&
		validTime && timeErr == nil && kind == "scope_dispatched" && detail == "匹配 0 个凭证，跳过 0 个" &&
		emptyField(fields, "run_id") && emptyField(fields, "action_id")
}

func compareQualityBucket(tx *bolt.Tx, kind string, before, after *bolt.Bucket, out *qualityComparison) error {
	if before.Sequence() != after.Sequence() {
		out.UnexpectedChanges++
	}
	if err := before.ForEach(func(key, value []byte) error {
		found, current := after.Cursor().Seek(key)
		if !bytes.Equal(found, key) {
			out.UnexpectedChanges++
			return nil
		}
		oldChild, newChild := before.Bucket(key), after.Bucket(key)
		if (oldChild == nil) != (newChild == nil) {
			out.UnexpectedChanges++
		} else if oldChild != nil {
			return compareQualityBucket(tx, "", oldChild, newChild, out)
		} else if !bytes.Equal(value, current) {
			if kind == "rules" && onlyNextRunChanged(tx, key, value, current) {
				out.RuleNextRunUpdated++
			} else {
				out.UnexpectedChanges++
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return after.ForEach(func(key, value []byte) error {
		found, _ := before.Cursor().Seek(key)
		if bytes.Equal(found, key) {
			return nil
		}
		if kind == "meta" && string(key) == "quality_host_input" && after.Bucket(key) == nil {
			// The only new baseline allowed is the exact preimage of the old host config digest.
			var oldDigest string
			computed := sha256.Sum256(value)
			if json.Unmarshal(before.Get([]byte("config_input")), &oldDigest) == nil && oldDigest == hex.EncodeToString(computed[:]) {
				out.HostInputMigrated = true
				return nil
			}
		}
		if kind == "audit" && after.Bucket(key) == nil && allowedEmptyAudit(tx, key, value) {
			out.EmptyAuditsAdded++
		} else {
			out.UnexpectedChanges++
		}
		return nil
	})
}

func compareQuality(beforePath, afterPath string) (qualityComparison, error) {
	out := qualityComparison{Format: "quality-preservation-v1"}
	for _, path := range []string{beforePath, afterPath} {
		if _, err := inspect(path); err != nil {
			return out, err
		}
	}
	before, err := bolt.Open(beforePath, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return out, errors.New("cannot open comparison snapshot")
	}
	defer before.Close()
	after, err := bolt.Open(afterPath, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return out, errors.New("cannot open comparison snapshot")
	}
	defer after.Close()
	err = before.View(func(oldTx *bolt.Tx) error {
		return after.View(func(newTx *bolt.Tx) error {
			if oldTx.Bucket([]byte("rules")) == nil || oldTx.Bucket([]byte("audit")) == nil || oldTx.Bucket([]byte("meta")) == nil {
				return errors.New("quality buckets missing")
			}
			if err := oldTx.ForEach(func(name []byte, old *bolt.Bucket) error {
				current := newTx.Bucket(name)
				if current == nil {
					out.UnexpectedChanges++
					return nil
				}
				return compareQualityBucket(oldTx, string(name), old, current, &out)
			}); err != nil {
				return err
			}
			return newTx.ForEach(func(name []byte, _ *bolt.Bucket) error {
				if oldTx.Bucket(name) == nil {
					out.UnexpectedChanges++
				}
				return nil
			})
		})
	})
	out.Preserved = err == nil && out.UnexpectedChanges == 0
	return out, err
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--verify-config" {
		out := verifyConfiguration(os.Args[2])
		if !out.Verified {
			fmt.Fprintln(os.Stderr, "configured runtime locations could not be verified")
			os.Exit(1)
		}
		if json.NewEncoder(os.Stdout).Encode(out) != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "--compare-quality" {
		out, err := compareQuality(os.Args[2], os.Args[3])
		if err != nil {
			fmt.Fprintln(os.Stderr, "private quality comparison failed")
			os.Exit(1)
		}
		if json.NewEncoder(os.Stdout).Encode(out) != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "one private snapshot path is required")
		os.Exit(2)
	}
	out, err := inspect(os.Args[1])
	if err != nil {
		// Never emit a database path, key, value, bucket name, or raw error.
		fmt.Fprintln(os.Stderr, "private snapshot inspection failed")
		os.Exit(1)
	}
	if json.NewEncoder(os.Stdout).Encode(out) != nil {
		os.Exit(1)
	}
}
