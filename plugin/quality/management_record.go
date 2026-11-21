package quality

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/json"
)

// connectionFiles owns the private atomic record beneath the locked data directory.
type connectionFiles string

// JSON escaping can expand each byte of an accepted management key sixfold.
const maxConnectionRecordBytes = 64 << 10

// The file contains a plaintext secret or a durable clear/invalidation marker.
// Revisions are opaque JSON-safe integers used for browser compare-and-swap.
type connectionRecord struct {
	Version  int    `json:"version"`
	Revision uint64 `json:"revision"`
	Base     string `json:"base"`
	State    string `json:"state"`
	Key      string `json:"key,omitempty"`
}

func (files connectionFiles) readRecord() (*connectionRecord, error) {
	info, err := os.Lstat(string(files))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("unsafe connection directory")
	}
	// A stopped process may leave a staged key before the rename. The
	// exclusive quality store lock prevents a second instance from staging here.
	if err := files.removeStagedRecords(); err != nil {
		return nil, err
	}
	return files.readInstalled()
}

func (files connectionFiles) readInstalled() (*connectionRecord, error) {
	name := filepath.Join(string(files), "connection.json")
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxConnectionRecordBytes {
		return nil, errors.New("unsafe connection file")
	}
	raw, err := os.ReadFile(name)
	var r connectionRecord
	if err != nil || DecodeStrict(raw, &r) != nil || r.Version != 1 || r.Revision == 0 || r.Revision > 1<<52 || r.Base == "" ||
		!(r.State == "saved" && validManagementKey(r.Key) || (r.State == "invalid" || r.State == "cleared") && r.Key == "") {
		return nil, errors.New("invalid connection record")
	}
	return &r, nil
}

func (files connectionFiles) removeStagedRecords() error {
	entries, err := os.ReadDir(string(files))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		suffix, match := strings.CutPrefix(entry.Name(), ".connection-")
		if !match || !entry.Type().IsRegular() {
			continue
		}
		if _, err := strconv.ParseUint(suffix, 10, 64); err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(string(files), entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

var errConnectionDurability = errors.New("connection_save_unknown: could not confirm durable configuration replacement")

func (files connectionFiles) saveRecord(record connectionRecord) error {
	if err := os.Mkdir(string(files), 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(string(files))
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe connection directory")
	}
	if err := syncDirectory(filepath.Dir(string(files))); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > maxConnectionRecordBytes {
		return errors.New("connection record exceeds storage limit")
	}
	file, err := os.CreateTemp(string(files), ".connection-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(string(files), "connection.json")); err != nil {
		return err
	}
	if err := syncDirectory(string(files)); err != nil {
		return errConnectionDurability
	}
	installed, err := files.readInstalled()
	if err != nil || installed == nil || *installed != record {
		return errConnectionDurability
	}
	return nil
}
