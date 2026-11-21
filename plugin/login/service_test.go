package login

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"encoding/json"
	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/cliproxyapi-plugins/model-quality/login/account"
	codex "github.com/cliproxyapi-plugins/model-quality/login/oauth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type fakeHost struct {
	mu                    sync.Mutex
	files                 map[string]map[string]any
	entries               map[string]account.Entry
	saves                 int
	saveError, adoptError bool
	runtimeDisabled       bool
}

func newFakeHost() *fakeHost {
	return &fakeHost{files: map[string]map[string]any{}, entries: map[string]account.Entry{}}
}
func (h *fakeHost) call(method string, in, out any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, _ := Encode(in)
	r := ojson.ParseBytes(b)
	var v any
	switch method {
	case "host.auth.list":
		items := []account.Entry{}
		for _, e := range h.entries {
			items = append(items, e)
		}
		v = map[string]any{"files": items}
	case "host.auth.get":
		name := r.Get("auth_index").String()
		f, ok := h.files[name]
		if !ok {
			return errors.New("not found")
		}
		v = map[string]any{"name": name, "json": f}
	case "host.auth.get_runtime":
		if h.adoptError {
			return errors.New("not adopted")
		}
		e, ok := h.entries[r.Get("auth_index").String()]
		if !ok {
			return errors.New("not found")
		}
		if h.runtimeDisabled {
			e.Disabled = true
			e.Status = "disabled"
		}
		v = map[string]any{"auth": e}
	case "host.auth.save":
		h.saves++
		name := r.Get("name").String()
		data := r.Get("json").Value().(map[string]any)
		h.files[name] = data
		email, _ := data["email"].(string)
		disabled, _ := data["disabled"].(bool)
		h.entries[name] = account.Entry{ID: name, Index: name, Name: name, Provider: "codex", Email: email, Disabled: disabled, Status: "active", UpdatedAt: time.Now()}
		if h.saveError {
			return errors.New("lost acknowledgement")
		}
		v = map[string]any{"name": name, "path": "/fixture/" + name}
	default:
		return errors.New("unapproved callback")
	}
	b, e := Encode(v)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}

type offlineRuntime struct {
	run func(context.Context, RuntimeRequest) (string, error)
}

func (offlineRuntime) Capabilities() error { return nil }
func (r offlineRuntime) Run(ctx context.Context, in RuntimeRequest) (string, error) {
	return r.run(ctx, in)
}
func offlineCallback(state string) string {
	return codex.RedirectURI + "?state=" + url.QueryEscape(state) + "&code=OFFLINE_CODE_SECRET"
}
func offlineBundle(t *testing.T, now time.Time, id string) *codex.CodexAuthBundle {
	t.Helper()
	d := ojson.NewDocString(`{}`)
	for k, v := range map[string]any{"iss": "https://auth.openai.com", "aud": codex.ClientID, "exp": now.Add(time.Hour).Unix(), "sub": "offline-user", "email": "offline@example.test", "https://api\\.openai\\.com/auth": map[string]string{"chatgpt_account_id": id, "chatgpt_user_id": "offline-user"}} {
		if e := d.Set(k, v); e != nil {
			t.Fatal(e)
		}
	}
	token := "e30." + base64.RawURLEncoding.EncodeToString(d.Bytes()) + ".offline-signature"
	return &codex.CodexAuthBundle{TokenData: codex.CodexTokenData{IDToken: token, AccessToken: token, RefreshToken: "OFFLINE_REFRESH_SECRET", Expire: now.Add(time.Hour).Format(time.RFC3339)}, LastRefresh: now.Format(time.RFC3339)}
}
func offlineOptions(t *testing.T) Options {
	t.Helper()
	h := newFakeHost()
	return Options{Dir: filepath.Join(t.TempDir(), "login"), Call: h.call, Runtime: offlineRuntime{run: func(_ context.Context, in RuntimeRequest) (string, error) { return offlineCallback(in.State), nil }}, Exchange: func(context.Context, string, *codex.PKCECodes, string) (*codex.CodexAuthBundle, error) {
		return offlineBundle(t, time.Now(), "offline-account"), nil
	}}
}
func offlineService(t *testing.T, opts Options) *Service {
	t.Helper()
	s, e := New(opts)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	})
	return s
}
func offlineStart() StartRequest {
	return StartRequest{ConsumerPlugin: "model-quality", Method: "password_totp", Intent: "import", Credentials: Credentials{Email: "offline@example.test", Password: "  OFFLINE_PASSWORD_SECRET  ", TOTPSecret: "JBSWY3DPEHPK3PXP"}}
}
func waitLogin(t *testing.T, s *Service, id, want string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, e := s.Detail(id)
		if e != nil {
			t.Fatal(e)
		}
		if j.Status == want {
			return j
		}
		if terminal(j.Status) && want != "failed" {
			t.Fatalf("unexpected terminal job: %+v", j)
		}
		time.Sleep(time.Millisecond)
	}
	j, _ := s.Detail(id)
	t.Fatalf("job did not reach %s: %+v", want, j)
	return Job{}
}
func startCandidate(t *testing.T, s *Service, r StartRequest) Job {
	t.Helper()
	j, e := s.Start(r)
	if e != nil {
		t.Fatal(e)
	}
	return waitLogin(t, s, j.ID, "awaiting_commit")
}
func commitCandidate(t *testing.T, s *Service, j Job) Job {
	t.Helper()
	if _, e := s.Commit(context.Background(), j.ID, j.CandidateID, j.Revision); e != nil {
		t.Fatal(e)
	}
	return waitLogin(t, s, j.ID, "committed")
}

func TestPortableLoginManualCommitAndPolicyPreservation(t *testing.T) {
	h := newFakeHost()
	opts := offlineOptions(t)
	opts.Call = h.call
	prepared := 0
	opts.Prepare = func(context.Context, string, string, string) error { prepared++; return nil }
	s := offlineService(t, opts)
	j := startCandidate(t, s, offlineStart())
	if h.saves != 0 {
		t.Fatal("candidate wrote credential before confirmation")
	}
	c, e := s.Candidate(j.CandidateID)
	if e != nil || c.Email != "offline@example.test" || c.AccountID != "offline-account" {
		t.Fatal("candidate identity missing")
	}
	j = commitCandidate(t, s, j)
	if h.saves != 1 || prepared != 1 {
		t.Fatal("commit not exactly once")
	}
	h.mu.Lock()
	h.files[j.TargetAuthID]["note"] = "keep note"
	h.files[j.TargetAuthID]["nested_policy"] = map[string]any{"weight": 3}
	h.files[j.TargetAuthID]["proxy_url"] = "http://proxy.fixture:8080"
	delete(h.files[j.TargetAuthID], "disabled")
	h.mu.Unlock()
	snapshot, e := s.opts.Manager.LoginSnapshot(j.TargetAuthID)
	if e != nil {
		t.Fatal(e)
	}
	r := offlineStart()
	r.Intent = "reauthorize"
	r.TargetAuthID = j.TargetAuthID
	r.ExpectedIdentityVersion = snapshot.IdentityVersion
	next := startCandidate(t, s, r)
	next = commitCandidate(t, s, next)
	h.mu.Lock()
	defer h.mu.Unlock()
	saved := h.files[next.TargetAuthID]
	if saved["disabled"] != true || saved["note"] != "keep note" || saved["proxy_url"] != "http://proxy.fixture:8080" || saved["nested_policy"] == nil {
		t.Fatal("reauthorization overwrote operator policy")
	}
}

func TestPortableLoginRefusesChangedSnapshotAndDoesNotReplayUnknownSave(t *testing.T) {
	for _, scenario := range []string{"changed", "unknown", "adoption"} {
		t.Run(scenario, func(t *testing.T) {
			h := newFakeHost()
			opts := offlineOptions(t)
			opts.Call = h.call
			s := offlineService(t, opts)
			j := commitCandidate(t, s, startCandidate(t, s, offlineStart()))
			snapshot, e := s.opts.Manager.LoginSnapshot(j.TargetAuthID)
			if e != nil {
				t.Fatal(e)
			}
			r := offlineStart()
			r.Intent = "reauthorize"
			r.TargetAuthID = j.TargetAuthID
			r.ExpectedIdentityVersion = snapshot.IdentityVersion
			j = startCandidate(t, s, r)
			h.mu.Lock()
			switch scenario {
			case "changed":
				h.files[j.TargetAuthID]["note"] = "external change"
			case "unknown":
				h.saveError = true
			case "adoption":
				h.adoptError = true
			}
			h.mu.Unlock()
			if _, e = s.Commit(context.Background(), j.ID, j.CandidateID, j.Revision); e != nil {
				t.Fatal(e)
			}
			want := "failed"
			if scenario == "unknown" {
				want = "commit_unknown"
			}
			if scenario == "adoption" {
				want = "saved_not_adopted"
			}
			finished := waitLogin(t, s, j.ID, want)
			if scenario == "changed" && h.saves != 1 {
				t.Fatal("changed snapshot was overwritten")
			}
			if scenario != "changed" && h.saves != 2 {
				t.Fatal("save was replayed")
			}
			if finished.Status == "committed" {
				t.Fatal("uncertain save reported as committed")
			}
			if _, e = s.Commit(context.Background(), j.ID, j.CandidateID, j.Revision); e == nil {
				t.Fatal("uncertain write was resubmitted")
			}
		})
	}
}

func TestPortableAccountsPersistPlaintextAndAutomaticallyEnableCredential(t *testing.T) {
	h := newFakeHost()
	opts := offlineOptions(t)
	opts.Call = h.call
	s := offlineService(t, opts)
	items, e := s.ImportAccounts([]Credentials{offlineStart().Credentials})
	if e != nil {
		t.Fatal(e)
	}
	a := items[0]
	j, e := s.StartAccount(a.ID, a.Revision, "")
	if e != nil {
		t.Fatal(e)
	}
	j = waitLogin(t, s, j.ID, "committed")
	if !j.AutoCommit || !j.EnableCredential || !j.CredentialEnabled || j.CandidateID != "" {
		t.Fatal("account login did not finish enabled without a confirmation step")
	}
	items, e = s.Accounts()
	if e != nil {
		t.Fatal(e)
	}
	a = items[0]
	if a.AuthID != j.TargetAuthID || !a.LastJobAutoCommit || !a.LastJobCredentialEnabled {
		t.Fatal("committed account not linked")
	}
	h.mu.Lock()
	h.files[a.AuthID]["disabled"] = false
	entry := h.entries[a.AuthID]
	entry.Disabled = false
	entry.Status = "error"
	entry.StatusMessage = "invalid_grant"
	h.entries[a.AuthID] = entry
	h.mu.Unlock()
	a, e = s.UpdateAccount(AccountUpdate{ID: a.ID, Revision: a.Revision, AutoRelogin: true})
	if e != nil {
		t.Fatal(e)
	}
	settings, _ := s.AccountSettings()
	settings.InspectionEnabled = true
	settings.AutoReloginEnabled = true
	if _, e = s.UpdateAccountSettings(settings); e != nil {
		t.Fatal(e)
	}
	s.accountMu.Lock()
	stored := s.accounts[a.ID]
	e = s.inspectAccountLocked(stored, true)
	s.accountMu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	items, _ = s.Accounts()
	automatic := waitLogin(t, s, items[0].LastJobID, "committed")
	if !automatic.Automatic || !automatic.CredentialEnabled {
		t.Fatal("inspection did not start login")
	}
	s.maintainAccounts()
	if h.saves != 2 {
		t.Fatal("automatic inspection did not submit exactly one replacement")
	}
	capRaw, _ := Encode(s.Capabilities())
	if !ojson.GetBytes(capRaw, "automatic_commit").Bool() || ojson.GetBytes(capRaw, "commit_mode").String() != "account_auto_commit" || !ojson.GetBytes(capRaw, "manual_oauth_commit").Bool() {
		t.Fatal("capability did not explain account automatic submission")
	}
	detail, e := s.AccountDetail(a.ID)
	if e != nil || detail.Credentials != offlineStart().Credentials {
		t.Fatal("plaintext account material changed")
	}
	logs, e := s.Logs(automatic.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := Encode(logs)
	for _, secret := range []string{"OFFLINE_PASSWORD_SECRET", "OFFLINE_REFRESH_SECRET", "OFFLINE_CODE_SECRET", "JBSWY3DPEHPK3PXP"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("job log retained secret")
		}
	}
	for _, entry := range logs.Entries {
		if entry.Status == "awaiting_commit" {
			t.Fatal("account login introduced a manual confirmation stage")
		}
	}
	snapshot, e := s.opts.Manager.LoginSnapshot(automatic.TargetAuthID)
	if e != nil {
		t.Fatal(e)
	}
	manualRequest := offlineStart()
	manualRequest.Intent, manualRequest.TargetAuthID, manualRequest.ExpectedIdentityVersion = "reauthorize", automatic.TargetAuthID, snapshot.IdentityVersion
	manual := startCandidate(t, s, manualRequest)
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s2 := offlineService(t, opts)
	detail, e = s2.AccountDetail(a.ID)
	if e != nil || detail.Credentials != offlineStart().Credentials {
		t.Fatal("plaintext account did not survive restart")
	}
	old, e := s2.Detail(manual.ID)
	if e != nil || old.Status != "interrupted" {
		t.Fatal("restart resumed stale OAuth candidate")
	}
	completed, e := s2.Detail(automatic.ID)
	if e != nil || completed.Status != "committed" || !completed.CredentialEnabled || h.saves != 2 {
		t.Fatal("restart lost the automatic commit result or repeated a save")
	}
}

func TestPortableManagementRoutesAndIdempotency(t *testing.T) {
	s := offlineService(t, offlineOptions(t))
	raw, _ := Encode(map[string]any{"entries": []Credentials{offlineStart().Credentials}})
	req := pluginapi.ManagementRequest{Method: "POST", Path: RoutePrefix + "/accounts/import", Headers: http.Header{"Idempotency-Key": {"fixture-import"}}, Body: raw}
	first := s.Handle(context.Background(), req)
	second := s.Handle(context.Background(), req)
	if first.StatusCode != 200 || second.StatusCode != 200 || ojson.GetBytes(first.Body, "items.0.id").String() != ojson.GetBytes(second.Body, "items.0.id").String() {
		t.Fatalf("import idempotency failed: %d %s", first.StatusCode, first.Body)
	}
	accounts, _ := s.Accounts()
	if len(accounts) != 1 {
		t.Fatal("repeated import wrote twice")
	}
	req.Path = "/v8/management/credential-logins/accounts/import"
	if got := s.Handle(context.Background(), req); got.StatusCode != 404 {
		t.Fatal("standalone service accepted patched-host route")
	}
	for _, r := range ManagementRoutes() {
		if !strings.HasPrefix(r["path"], RoutePrefix+"/") {
			t.Fatal("business route escaped authenticated plugin namespace")
		}
	}
	if fi, e := os.Stat(filepath.Join(s.opts.Dir, "credential-login.db")); e != nil || fi.Mode().Perm() != 0600 {
		t.Fatal("plaintext store permissions changed")
	}
	for _, query := range []url.Values{{"limit": {"200"}}, {"limit": {"1"}}} {
		r := s.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: RoutePrefix + "/jobs", Query: query})
		if r.StatusCode != 200 {
			t.Fatal("UI jobs pagination rejected")
		}
	}
	for _, query := range []url.Values{{"limit": {"201"}}, {"limit": {"-1"}}, {"limit": {"1", "2"}}, {"cursor": {"0"}}} {
		r := s.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: RoutePrefix + "/jobs", Query: query})
		if r.StatusCode != 400 || ojson.GetBytes(r.Body, "code").String() != "invalid_request" {
			t.Fatal("invalid pagination did not return UI error code")
		}
	}
}

func TestCandidateFailsClosedWhenExistingCodexCannotBeRead(t *testing.T) {
	h := newFakeHost()
	h.entries["unreadable.json"] = account.Entry{ID: "unreadable.json", Index: "unreadable.json", Name: "unreadable.json", Provider: "codex"}
	opts := offlineOptions(t)
	opts.Call = h.call
	s := offlineService(t, opts)
	j, e := s.Start(offlineStart())
	if e != nil {
		t.Fatal(e)
	}
	finished := waitLogin(t, s, j.ID, "failed")
	if finished.ErrorCode != "storage_unavailable" || h.saves != 0 {
		t.Fatal("unreadable credential became absence evidence")
	}
}
