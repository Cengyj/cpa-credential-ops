package account

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	bolt "go.etcd.io/bbolt"
)

type Callback func(string, any, any) error
type Auth struct {
	ID, Index, Provider, FileName, ProxyURL string
	Metadata                                map[string]any
	Disabled                                bool
	raw                                     []byte
	entry                                   Entry
}

func (a *Auth) Clone() *Auth {
	if a == nil {
		return nil
	}
	out := *a
	out.Metadata = map[string]any{}
	for k, v := range a.Metadata {
		out.Metadata[k] = v
	}
	out.raw = append([]byte(nil), a.raw...)
	return &out
}

type Entry struct {
	ID             string    `json:"id"`
	Index          string    `json:"auth_index"`
	Name           string    `json:"name"`
	Provider       string    `json:"provider"`
	Type           string    `json:"type"`
	Email          string    `json:"email"`
	Account        string    `json:"account"`
	Status         string    `json:"status"`
	StatusMessage  string    `json:"status_message"`
	Disabled       bool      `json:"disabled"`
	Unavailable    bool      `json:"unavailable"`
	RuntimeOnly    bool      `json:"runtime_only"`
	NextRetryAfter time.Time `json:"next_retry_after"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type Manager struct {
	call     Callback
	commitMu sync.Mutex
	db       *bolt.DB
	closed   atomic.Bool
}

func New(dir string, call Callback) (*Manager, error) {
	if call == nil {
		return nil, errors.New("plugin_unavailable")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, errors.New("storage_unavailable")
	}
	if fi, e := os.Lstat(dir); e != nil || !fi.IsDir() || fi.Mode().Perm()&0077 != 0 {
		return nil, errors.New("storage_unavailable")
	}
	p := filepath.Join(dir, "login-receipts.db")
	if fi, e := os.Lstat(p); e == nil && (!fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0) {
		return nil, errors.New("storage_unavailable")
	}
	db, e := bolt.Open(p, 0600, &bolt.Options{Timeout: time.Second})
	if e != nil {
		return nil, errors.New("storage_unavailable")
	}
	if e = db.Update(func(tx *bolt.Tx) error { _, e := tx.CreateBucketIfNotExists([]byte("receipts")); return e }); e != nil {
		db.Close()
		return nil, e
	}
	return &Manager{call: call, db: db}, nil
}
func (m *Manager) Close() error {
	m.closed.Store(true)
	m.commitMu.Lock()
	defer m.commitMu.Unlock()
	return m.db.Close()
}
func (m *Manager) Valid() bool { return m != nil && !m.closed.Load() }
func (m *Manager) LoginCapability() error {
	if !m.Valid() {
		return errors.New("plugin_unavailable")
	}
	_, e := m.entries()
	return e
}
func (m *Manager) invoke(method string, in, out any) error {
	if !m.Valid() {
		return errors.New("plugin_unavailable")
	}
	if e := m.call(method, in, out); e != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}
func (m *Manager) entries() ([]Entry, error) {
	var r struct {
		Files []Entry `json:"files"`
	}
	e := m.invoke("host.auth.list", struct{}{}, &r)
	if e == nil && r.Files == nil {
		e = errors.New("storage_unavailable")
	}
	return r.Files, e
}
func encode(v any) ([]byte, error) {
	d := ojson.NewDocString(`{}`)
	if e := d.Set("value", v); e != nil {
		return nil, e
	}
	return []byte(ojson.ParseBytes(d.Bytes()).Get("value").Raw), nil
}
func (m *Manager) read(entry Entry) (*Auth, error) {
	if entry.Index == "" || entry.RuntimeOnly {
		return nil, errors.New("unsupported_login_target")
	}
	var r struct {
		Name string          `json:"name"`
		JSON json.RawMessage `json:"json"`
	}
	if e := m.invoke("host.auth.get", map[string]string{"auth_index": entry.Index}, &r); e != nil {
		return nil, e
	}
	root := ojson.ParseBytes(r.JSON)
	if !ojson.ValidBytes(r.JSON) || !root.IsObject() {
		return nil, errors.New("identity_unverifiable")
	}
	metadata, _ := root.Value().(map[string]any)
	canonical, err := canonicalJSON(r.JSON)
	if err != nil {
		return nil, err
	}
	b := []byte(canonical)
	provider := entry.Provider
	if provider == "" {
		provider = entry.Type
	}
	if provider == "" {
		provider, _ = metadata["type"].(string)
	}
	p, _ := metadata["proxy_url"].(string)
	if p == "" {
		p, _ = metadata["proxy-url"].(string)
	}
	name := r.Name
	if name == "" {
		name = entry.Name
	}
	id := entry.ID
	if id == "" {
		id = entry.Name
	}
	a := &Auth{ID: id, Index: entry.Index, Provider: provider, FileName: name, ProxyURL: p, Metadata: metadata, Disabled: entry.Disabled || entry.Status == "disabled", raw: b, entry: entry}
	if v, ok := metadata["disabled"].(bool); ok && v {
		a.Disabled = true
	}
	return a, nil
}
func (m *Manager) List() []*Auth {
	entries, e := m.entries()
	if e != nil {
		return nil
	}
	out := []*Auth{}
	for _, v := range entries {
		if a, e := m.read(v); e == nil {
			out = append(out, a)
		}
	}
	return out
}

// ListChecked is used by identity-sensitive writes. A known Codex record that
// cannot be read is unknown evidence, never evidence that the identity is absent.
func (m *Manager) ListChecked() ([]*Auth, error) {
	entries, err := m.entries()
	if err != nil {
		return nil, err
	}
	out := []*Auth{}
	for _, entry := range entries {
		if entry.Provider != "codex" && entry.Type != "codex" {
			continue
		}
		a, err := m.read(entry)
		if err != nil {
			return nil, err
		}
		if _, err := LoginPrincipalFromAuth(a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}
func (m *Manager) GetByID(id string) (*Auth, bool) { a, e := m.get(id); return a, e == nil }
func (m *Manager) get(id string) (*Auth, error) {
	entries, e := m.entries()
	if e != nil {
		return nil, e
	}
	var matched *Entry
	for i, v := range entries {
		if v.ID == id || v.Name == id || v.Index == id {
			if matched != nil {
				return nil, errors.New("identity_conflict")
			}
			matched = &entries[i]
		}
	}
	if matched != nil {
		return m.read(*matched)
	}
	return nil, errors.New("not_found")
}
func (m *Manager) LoginSnapshot(id string) (LoginSnapshot, error) {
	a, err := m.get(id)
	if err != nil {
		return LoginSnapshot{}, err
	}
	return snapshot(a)
}
func (m *Manager) LoginBinding(id string) (LoginSnapshot, LoginPrincipal, []string, error) {
	a, e := m.get(id)
	if e != nil {
		return LoginSnapshot{}, LoginPrincipal{}, nil, e
	}
	s, p, e := snapshotAndPrincipal(a)
	return s, p, LoginPlanTypes(a), e
}
func snapshot(a *Auth) (LoginSnapshot, error) {
	s, _, err := snapshotAndPrincipal(a)
	return s, err
}
func snapshotAndPrincipal(a *Auth) (LoginSnapshot, LoginPrincipal, error) {
	if a == nil || a.Provider != "codex" || a.FileName == "" || filepath.Base(a.FileName) != a.FileName || !strings.HasSuffix(a.FileName, ".json") {
		return LoginSnapshot{}, LoginPrincipal{}, errors.New("unsupported_login_target")
	}
	p, e := LoginPrincipalFromAuth(a)
	if e != nil {
		return LoginSnapshot{}, LoginPrincipal{}, e
	}
	return LoginSnapshot{AuthID: a.ID, AuthIndex: a.Index, Name: a.FileName, IdentityVersion: loginHash("identity", a.ID, p.Key()), Principal: p.Key(), PolicyFingerprint: loginHash(string(a.raw), strconv.FormatBool(a.Disabled))}, p, nil
}

type Target struct {
	QualityPaused, OperatorDisabled bool
	UnavailableReasons              []string
}

func (m *Manager) QualityTarget(id string) (Target, error) {
	a, e := m.get(id)
	if e != nil {
		return Target{}, e
	}
	t := Target{OperatorDisabled: a.Disabled, UnavailableReasons: []string{}}
	if a.Disabled {
		t.UnavailableReasons = append(t.UnavailableReasons, "manual_disabled")
	}
	if a.entry.NextRetryAfter.After(time.Now()) {
		t.UnavailableReasons = append(t.UnavailableReasons, "native_cooldown")
	}
	return t, nil
}

type LoginHealth struct {
	Exists, AutoEligible                                    bool
	Status, Reason, Fingerprint, IdentityVersion, Principal string
}

func (m *Manager) LoginHealth(id string) (LoginHealth, error) {
	a, e := m.get(id)
	if e != nil {
		return LoginHealth{Status: "missing", Reason: "credential_deleted"}, e
	}
	s, e := snapshot(a)
	if e != nil {
		return LoginHealth{}, e
	}
	h := LoginHealth{Exists: true, Status: "unknown", Reason: "no_auth_failure_observed", IdentityVersion: s.IdentityVersion, Principal: s.Principal}
	if a.Disabled {
		h.Status = "disabled"
		h.Reason = "manual_disabled"
		return h, nil
	}
	if a.entry.NextRetryAfter.After(time.Now()) {
		h.Status = "cooldown"
		h.Reason = "native_cooldown"
		return h, nil
	}
	// Public callbacks omit the host's internal renewal state. Recognize only
	// explicit machine error identifiers and the official host's fixed invalid
	// refresh message. Quota, expiry and generic failures never justify a login.
	code := strings.TrimSpace(a.entry.StatusMessage)
	if ojson.Valid(code) {
		code = ojson.Get(code, "code").String()
		if code == "" {
			code = ojson.Get(a.entry.StatusMessage, "error.code").String()
		}
	}
	if a.entry.Status == "error" {
		switch code {
		case "invalid_grant", "requires_sign_in", "unauthorized", "unauthorized (refresh token invalid)":
			h.Status = "needs_reauth"
			h.Reason = code
			if code == "unauthorized (refresh token invalid)" {
				h.Reason = "unauthorized"
			}
			h.AutoEligible = true
			h.Fingerprint = loginHash("auth-failure", id, grant(a), code)
		}
	}
	if !h.AutoEligible && a.entry.Unavailable {
		h.Status = "unavailable"
		h.Reason = "native_unavailable"
	}
	return h, nil
}
func grant(a *Auth) string {
	return loginHash(authMetadataString(a, "id_token"), authMetadataString(a, "access_token"), authMetadataString(a, "refresh_token"))
}
func (m *Manager) receipt(r LoginCommitResult) error {
	b, e := encode(r)
	if e != nil {
		return e
	}
	return m.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("receipts")).Put([]byte(r.CommitID), b) })
}
func (m *Manager) LoginCommitStatus(id string) (LoginCommitResult, error) {
	var r LoginCommitResult
	e := m.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("receipts")).Get([]byte(id))
		if b == nil {
			return errors.New("not_found")
		}
		v := ojson.ParseBytes(b)
		r = LoginCommitResult{CommitID: v.Get("commit_id").String(), AuthID: v.Get("auth_id").String(), AuthIndex: v.Get("auth_index").String(), Name: v.Get("name").String(), IdentityVersion: v.Get("identity_version").String(), Principal: v.Get("principal").String(), State: v.Get("state").String(), CredentialEnabled: v.Get("credential_enabled").Bool()}
		if !ojson.ValidBytes(b) || r.CommitID != id || r.State == "" {
			return errors.New("storage_unavailable")
		}
		return nil
	})
	return r, e
}

// VerifyLoginReceipt corroborates a durable successful write after a restart.
// It never submits a save or exposes credential material to the quality engine.
func (m *Manager) VerifyLoginReceipt(r LoginCommitResult) error {
	actual, err := m.get(r.AuthID)
	if err != nil {
		return err
	}
	current, err := snapshot(actual)
	if err != nil || current.AuthID != r.AuthID || current.Name != r.Name || r.AuthIndex != current.AuthIndex || current.IdentityVersion != r.IdentityVersion {
		return errors.New("identity_conflict")
	}
	var live struct {
		Auth Entry `json:"auth"`
	}
	if err := m.invoke("host.auth.get_runtime", map[string]string{"auth_index": actual.Index}, &live); err != nil {
		return err
	}
	provider := live.Auth.Provider
	if provider == "" {
		provider = live.Auth.Type
	}
	if live.Auth.ID == "" || live.Auth.Index == "" || live.Auth.Status == "" {
		return errors.New("storage_unavailable")
	}
	if live.Auth.ID != actual.ID || live.Auth.Index != actual.Index || provider != actual.Provider {
		return errors.New("identity_conflict")
	}
	if runtimeDisabled := live.Auth.Disabled || live.Auth.Status == "disabled"; runtimeDisabled != actual.Disabled {
		return errors.New("storage_unavailable")
	}
	return nil
}

// Public host.auth.save has no CAS. Re-read immediately before the one write,
// preserve all non-token fields, and never report a failed/uncertain write as
// committed. The local mutex only serializes this plugin's own writers.
func (m *Manager) CommitLoginCandidate(ctx context.Context, r LoginCommitRequest) (LoginCommitResult, error) {
	m.commitMu.Lock()
	defer m.commitMu.Unlock()
	result := LoginCommitResult{CommitID: r.CommitID, State: "aborted"}
	if r.Candidate == nil || r.CommitID == "" {
		return result, errors.New("invalid_request")
	}
	result.AuthID = r.Candidate.ID
	if old, readErr := m.LoginCommitStatus(r.CommitID); readErr == nil {
		if old.State != "upload_excluded" {
			return old, errors.New("already_consumed")
		}
	} else if readErr.Error() != "not_found" {
		return result, errors.New("storage_unavailable")
	}
	if e := ctx.Err(); e != nil {
		return result, errors.New("cancelled")
	}
	if r.Guard != nil {
		if e := r.Guard(); e != nil {
			return result, e
		}
	}
	principal, e := LoginPrincipalFromAuth(r.Candidate)
	if e != nil {
		return result, e
	}
	result.Principal = principal.Key()
	name := r.Candidate.FileName
	data := ojson.NewDocString(`{}`)
	if r.Expected.AuthID != "" {
		before, e := m.get(r.Expected.AuthID)
		if e != nil {
			return result, e
		}
		s, e := snapshot(before)
		if e != nil || s != r.Expected || principal.Key() != s.Principal {
			return result, errors.New("policy_changed")
		}
		name = s.Name
		result.AuthID = s.AuthID
		data = ojson.NewDoc(before.raw)
		_ = data.Set("disabled", before.Disabled)
	} else {
		entries, e := m.entries()
		if e != nil {
			return result, e
		}
		for _, entry := range entries {
			if entry.Name == name {
				return result, errors.New("duplicate_identity")
			}
			if entry.Provider != "codex" && entry.Type != "codex" {
				continue
			}
			a, e := m.read(entry)
			if e != nil {
				return result, e
			}
			{
				p, e := LoginPrincipalFromAuth(a)
				if e != nil {
					return result, e
				}
				if e == nil && p.Key() == principal.Key() {
					return result, errors.New("duplicate_identity")
				}
			}
		}
		_ = data.Set("disabled", true)
	}
	if r.Prepare != nil {
		if e := r.Prepare(ctx, result.AuthID, r.Expected.IdentityVersion); e != nil {
			return result, e
		}
	}
	if e := ctx.Err(); e != nil {
		return result, errors.New("cancelled")
	}
	if r.Expected.AuthID != "" {
		fresh, e := m.LoginSnapshot(r.Expected.AuthID)
		if e != nil || fresh != r.Expected {
			return result, errors.New("policy_changed")
		}
	}
	for _, key := range []string{"type", "id_token", "access_token", "refresh_token", "expired", "last_refresh", "account_id", "email", "plan_type"} {
		if v, ok := r.Candidate.Metadata[key]; ok {
			if err := data.Set(key, v); err != nil {
				return result, errors.New("storage_unavailable")
			}
		}
	}
	if r.Expected.AuthID == "" && r.Candidate.ProxyURL != "" {
		_ = data.Set("proxy_url", r.Candidate.ProxyURL)
	}
	if r.EnableCredential {
		_ = data.Set("disabled", false)
	}
	if r.Guard != nil {
		if e := r.Guard(); e != nil {
			return result, e
		}
	}
	result.State = "commit_unknown"
	if e := m.receipt(result); e != nil {
		return result, errors.New("storage_unavailable")
	}
	var saved struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	abort := func(cause error) (LoginCommitResult, error) {
		result.State = "aborted"
		// This receipt proves that no save began. Only an explicit retry of the
		// retained candidate may replace it; uncertain writes remain consumed.
		if cause.Error() == "cpa_plan_excluded" {
			result.State = "upload_excluded"
		}
		if e := m.receipt(result); e != nil {
			result.State = "commit_unknown"
			return result, errors.New("commit_unknown")
		}
		return result, cause
	}
	if r.Guard != nil {
		if e := r.Guard(); e != nil {
			return abort(e)
		}
	}
	if r.Expected.AuthID != "" {
		fresh, e := m.LoginSnapshot(r.Expected.AuthID)
		if e != nil || fresh != r.Expected {
			return abort(errors.New("policy_changed"))
		}
	}
	if e := ctx.Err(); e != nil {
		return abort(errors.New("cancelled"))
	}
	if r.Guard != nil {
		if e := r.Guard(); e != nil {
			return abort(e)
		}
	}
	if e := ctx.Err(); e != nil {
		return abort(errors.New("cancelled"))
	}
	saveErr := m.invoke("host.auth.save", map[string]any{"name": name, "json": json.RawMessage(data.Bytes())}, &saved)
	if saveErr != nil {
		return result, errors.New("commit_unknown")
	}
	// Successful save means the official callback completed runtime upsert.
	// Confirm physical material and public runtime presence as separate checks.
	actual, e := m.get(name)
	if e != nil || actual.FileName != name || grant(actual) != grant(r.Candidate) || (r.Expected.AuthID != "" && (actual.ID != r.Expected.AuthID || actual.Index != r.Expected.AuthIndex)) {
		return result, errors.New("commit_unknown")
	}
	wanted, e := canonicalJSON(data.Bytes())
	if e != nil || loginHash(wanted) != loginHash(string(actual.raw)) {
		return result, errors.New("commit_unknown")
	}

	var live struct {
		Auth Entry `json:"auth"`
	}
	e = m.invoke("host.auth.get_runtime", map[string]string{"auth_index": actual.Index}, &live)
	provider := live.Auth.Provider
	if provider == "" {
		provider = live.Auth.Type
	}
	if e != nil || live.Auth.Index != actual.Index || live.Auth.ID != actual.ID || provider != "codex" || live.Auth.Status == "" {
		result.State = "saved_not_adopted"
		_ = m.receipt(result)
		return result, errors.New("saved_not_adopted")
	}
	runtimeDisabled := live.Auth.Disabled || live.Auth.Status == "disabled"
	if runtimeDisabled != ojson.GetBytes(data.Bytes(), "disabled").Bool() {
		result.State = "saved_not_adopted"
		_ = m.receipt(result)
		if r.EnableCredential && runtimeDisabled {
			return result, errors.New("credential_enable_conflict")
		}
		return result, errors.New("saved_not_adopted")
	}
	s, e := snapshot(actual)
	if e != nil {
		return result, errors.New("commit_unknown")
	}
	result.AuthID = actual.ID
	result.AuthIndex = actual.Index
	result.Name = actual.FileName
	result.IdentityVersion = s.IdentityVersion
	result.State = "committed"
	result.CredentialEnabled = !runtimeDisabled
	if e = m.receipt(result); e != nil {
		result.State = "commit_unknown"
		result.CredentialEnabled = false
		return result, errors.New("commit_unknown")
	}
	return result, nil
}
