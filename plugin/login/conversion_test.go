package login

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/cliproxyapi-plugins/model-quality/login/account"
	"github.com/cliproxyapi-plugins/model-quality/login/conversion"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func conversionFixture(t *testing.T) string {
	t.Helper()
	token := offlineBundle(t, time.Now(), "offline-account").TokenData.AccessToken
	d := ojson.NewDocString(`{"type":"sub2api-data","version":1,"accounts":[{"platform":"openai","type":"oauth","name":"Offline conversion","credentials":{"refresh_token":"CONVERSION_PRIVATE_REFRESH","plan_type":"plus"}}]}`)
	if err := d.Set("accounts.0.credentials.access_token", token); err != nil {
		t.Fatal(err)
	}
	return d.String()
}

func TestConversionRefusesExistingFilesAndChangedPolicy(t *testing.T) {
	for _, mode := range []string{"filename", "identity", "prepare", "policy-changed", "runtime-missing", "runtime-disabled"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			opts := offlineOptions(t)
			opts.Call = h.call
			var s *Service
			opts.Prepare = func(context.Context, string, string, string) error {
				if mode == "prepare" {
					return errors.New("policy_changed")
				}
				if mode == "policy-changed" {
					s.uploadExcludedPlans.Store([]string{"plus"})
				}
				return nil
			}
			s = offlineService(t, opts)
			source := conversionFixture(t)
			converted, err := conversion.Convert([]byte(source), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			item := converted.Items[0]
			want, saves := "duplicate_identity", 0
			switch mode {
			case "filename", "identity":
				name := item.Filename
				if mode == "identity" {
					name = "existing.json"
				}
				item.Credential["refresh_token"] = "different-existing-grant"
				h.files[name] = item.Credential
				h.entries[name] = account.Entry{ID: name, Index: name, Name: name, Provider: "codex", Status: "active"}
			case "prepare":
				want = "policy_changed"
			case "policy-changed":
				want = "cpa_plan_excluded"
			case "runtime-missing":
				h.adoptError = true
				want, saves = "saved_not_adopted", 1
			case "runtime-disabled":
				h.runtimeDisabled = true
				want, saves = "saved_not_adopted", 1
			}
			r := conversionRequest(t, s, "upload", "conversion-guard", source)
			if ojson.GetBytes(r.Body, "items.0.code").String() != want || h.saves != saves {
				t.Fatalf("guard failed %s %d", r.Body, h.saves)
			}
			if saves == 1 {
				h.adoptError, h.runtimeDisabled = false, false
				r = conversionRequest(t, s, "upload", "conversion-guard-retry", source)
				if h.saves != 1 || ojson.GetBytes(r.Body, "items.0.status").String() != "uncertain" {
					t.Fatal("unadopted save replayed")
				}
			}
		})
	}
}

func TestConversionBlocksPendingIdentityWithoutEmail(t *testing.T) {
	for _, mode := range []string{"account", "job"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			opts := offlineOptions(t)
			opts.Call = h.call
			s := offlineService(t, opts)
			if mode == "account" {
				a := importOfflineAccount(t, s)
				s.accountMu.Lock()
				stored := s.accounts[a.ID]
				stored.Principal = (account.LoginPrincipal{AccountID: "offline-account", UserID: "offline-user"}).Key()
				stored.View.SecurityPending = true
				err := s.saveAccountLocked(stored)
				s.accountMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			} else {
				s.mu.Lock()
				s.jobs["pending"] = &session{record: storedJob{SecurityAccountID: "offline-account", View: Job{ID: "pending", Action: "reset_totp", Status: "security_uncertain"}}}
				s.mu.Unlock()
			}
			d := ojson.NewDocString(conversionFixture(t))
			claims := `{"https://api.openai.com/auth":{"chatgpt_account_id":"offline-account","chatgpt_user_id":"offline-user"}}`
			_ = d.Set("accounts.0.credentials.access_token", "e30."+base64.RawURLEncoding.EncodeToString([]byte(claims))+".offline")
			r := conversionRequest(t, s, "upload", "conversion-no-email", d.String())
			if h.saves != 0 || ojson.GetBytes(r.Body, "items.0.code").String() != "security_pending" {
				t.Fatalf("security identity bypass: %s", r.Body)
			}
		})
	}
}

func conversionRequest(t *testing.T, s *Service, path, key, source string, indexes ...int) pluginapi.ManagementResponse {
	t.Helper()
	body := map[string]any{"source": source}
	if path == "upload" {
		if indexes == nil {
			indexes = []int{0}
		}
		body["indexes"] = indexes
	}
	raw, err := Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	return s.Handle(context.Background(), pluginapi.ManagementRequest{Method: http.MethodPost, Path: RoutePrefix + "/conversion/" + path, Body: raw, Headers: http.Header{"Idempotency-Key": {key}}})
}

func TestConversionPreviewAndUploadAreOfflineAndIdempotent(t *testing.T) {
	h := newFakeHost()
	opts := offlineOptions(t)
	opts.Call = h.call
	opts.Runtime = offlineRuntime{run: func(context.Context, RuntimeRequest) (string, error) {
		t.Error("conversion started login runtime")
		return "", nil
	}}
	s := offlineService(t, opts)
	source := conversionFixture(t)
	preview := conversionRequest(t, s, "preview", "", source)
	if preview.StatusCode != 200 || ojson.GetBytes(preview.Body, "ready").Int() != 1 || h.saves != 0 {
		t.Fatalf("preview failed: %d", preview.StatusCode)
	}
	if ojson.GetBytes(preview.Body, "items.0.credential.id_token").Exists() {
		t.Fatal("fabricated ID token")
	}
	for _, check := range []struct{ key, status, code string }{
		{"conversion-one", "uploaded", ""},
		{"conversion-one", "uploaded", ""},
		{"conversion-new-key", "existing", "conversion_previously_uploaded"},
	} {
		r := conversionRequest(t, s, "upload", check.key, source)
		if r.StatusCode != 200 || ojson.GetBytes(r.Body, "items.0.status").String() != check.status || ojson.GetBytes(r.Body, "items.0.code").String() != check.code {
			t.Fatalf("upload failed: %d %s", r.StatusCode, r.Body)
		}
	}
	if h.saves != 1 {
		t.Fatal("conversion repeated credential write")
	}
	if accounts, err := s.Accounts(); err != nil || len(accounts) != 0 {
		t.Fatal("token import created password/2FA account")
	}
	if all, err := s.opts.Manager.ListChecked(); err != nil || len(all) != 1 {
		t.Fatalf("access-token-only credential broke subsequent account scans: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(opts.Dir + "/credential-login.db")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("CONVERSION_PRIVATE_REFRESH")) {
		t.Fatal("preview or upload journal retained credential tokens")
	}
	s = offlineService(t, opts)
	r := conversionRequest(t, s, "upload", "conversion-reopened", source)
	if r.StatusCode != 200 || h.saves != 1 || ojson.GetBytes(r.Body, "items.0.status").String() != "existing" || ojson.GetBytes(r.Body, "items.0.code").String() != "conversion_previously_uploaded" {
		t.Fatal("restart repeated the import")
	}
	changed := ojson.NewDocString(source)
	_ = changed.Set("accounts.0.credentials.refresh_token", "NEW_OFFLINE_REFRESH")
	r = conversionRequest(t, s, "upload", "conversion-new-token", changed.String())
	if ojson.GetBytes(r.Body, "items.0.code").String() != "duplicate_identity" || h.saves != 1 {
		t.Fatal("changed credential was reported as uploaded or overwrote existing file")
	}
}

func conversionBatchFixture(t *testing.T) string {
	t.Helper()
	token := func(claims map[string]any) string {
		raw, err := Encode(claims)
		if err != nil {
			t.Fatal(err)
		}
		return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".offline"
	}
	var accounts []map[string]any
	for _, name := range []string{"first", "second"} {
		email, accountID, userID := name+"@example.test", "account-"+name, "user-"+name
		authClaims := map[string]string{"chatgpt_account_id": accountID, "chatgpt_user_id": userID, "user_id": userID, "chatgpt_plan_type": "plus"}
		access := token(map[string]any{"exp": int64(4102444800), "sub": "sub-" + name, "https://api.openai.com/auth": authClaims, "https://api.openai.com/profile": map[string]string{"email": email}})
		id := token(map[string]any{"sub": "sub-" + name, "email": email, "https://api.openai.com/auth": authClaims})
		accounts = append(accounts, map[string]any{
			"name": name, "platform": "openai", "type": "oauth", "concurrency": 1, "priority": 1, "rate_multiplier": 1,
			"credentials": map[string]any{
				"type": "codex", "email": email, "account_id": accountID, "chatgpt_account_id": accountID, "chatgpt_user_id": userID,
				"access_token": access, "id_token": id, "refresh_token": "offline-refresh-" + name, "plan_type": "plus",
				"organization_id": "offline-org", "last_refresh": "2026-11-20T00:00:00Z", "max_concurrency": 1, "proxy_url": "",
			},
			"extra": map[string]string{"email": email, "source_type": "fixture"},
		})
	}
	raw, err := Encode(map[string]any{"type": "sub2api-data", "version": 1, "exported_at": "2026-11-20T00:00:00Z", "proxies": []any{}, "accounts": accounts})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestConversionBatchUploadsSelectedRowsIndependently(t *testing.T) {
	source := conversionBatchFixture(t)
	for _, test := range []struct {
		name, status, code string
		saves              int
	}{
		{"both new", "uploaded", "", 2},
		{"already present", "existing", "conversion_already_present", 1},
		{"existing grant differs", "failed", "duplicate_identity", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newFakeHost()
			opts := offlineOptions(t)
			opts.Call = h.call
			s := offlineService(t, opts)
			preview := conversionRequest(t, s, "preview", "", source)
			rows := ojson.GetBytes(preview.Body, "items").Array()
			if preview.StatusCode != 200 || ojson.GetBytes(preview.Body, "ready").Int() != 2 || len(rows) != 2 || h.saves != 0 {
				t.Fatal("two-account export did not preview both credentials")
			}
			first, second := rows[0], rows[1]
			if first.Get("filename").String() == second.Get("filename").String() {
				t.Fatal("different principals generated the same filename")
			}
			if test.name != "both new" {
				current := second.Get("credential").Value().(map[string]any)
				delete(current, "user_id") // Existing CPA files may keep this in claims only.
				if test.name == "existing grant differs" {
					current["refresh_token"] = "different-existing-grant"
				}
				h.files["existing.json"] = current
				h.entries["existing.json"] = account.Entry{ID: "existing.json", Index: "existing.json", Name: "existing.json", Provider: "codex", Status: "active"}
			}
			r := conversionRequest(t, s, "upload", "batch-first-request", source, 1, 0)
			out := ojson.GetBytes(r.Body, "items").Array()
			if r.StatusCode != 200 || len(out) != 2 || h.saves != test.saves {
				t.Fatalf("batch response %d, %d rows, %d writes", r.StatusCode, len(out), h.saves)
			}
			if out[0].Get("index").Int() != 1 || out[0].Get("status").String() != test.status || out[0].Get("code").String() != test.code ||
				out[1].Get("index").Int() != 0 || out[1].Get("status").String() != "uploaded" || h.files[first.Get("filename").String()]["email"] != first.Get("email").String() {
				t.Fatal("batch statuses or credential indexes did not match the selection")
			}
			if test.name == "both new" {
				if h.files[second.Get("filename").String()]["email"] != second.Get("email").String() {
					t.Fatal("the second credential was not saved under its own identity")
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s = offlineService(t, opts)
				retry := conversionRequest(t, s, "upload", "batch-after-restart", source, 0, 1)
				repeated := ojson.GetBytes(retry.Body, "items").Array()
				if h.saves != 2 || len(repeated) != 2 {
					t.Fatal("reopening the batch repeated a write")
				}
				for i, row := range repeated {
					if int(row.Get("index").Int()) != i || row.Get("status").String() != "existing" || row.Get("code").String() != "conversion_previously_uploaded" {
						t.Fatal("reopening the batch did not identify the confirmed receipts")
					}
				}
			}
		})
	}
}

func TestConversionReceiptCannotConfirmChangedOrMissingCredential(t *testing.T) {
	for _, change := range []string{"removed", "rotated", "disabled"} {
		t.Run(change, func(t *testing.T) {
			h := newFakeHost()
			opts := offlineOptions(t)
			opts.Call = h.call
			s := offlineService(t, opts)
			source := conversionFixture(t)
			first := conversionRequest(t, s, "upload", "conversion-initial", source)
			name := ojson.GetBytes(first.Body, "items.0.auth_id").String()
			if h.saves != 1 || name == "" {
				t.Fatal("initial conversion was not saved")
			}
			switch change {
			case "removed":
				delete(h.files, name)
				delete(h.entries, name)
			case "rotated":
				h.files[name]["refresh_token"] = "newer-host-grant"
			case "disabled":
				h.files[name]["disabled"] = true
			}
			r := conversionRequest(t, s, "upload", "conversion-fresh-request", source)
			row := ojson.GetBytes(r.Body, "items.0")
			if h.saves != 1 || row.Get("status").String() != "failed" || row.Get("code").String() != "conversion_previous_upload_changed" {
				t.Fatal("a stale receipt confirmed or overwrote changed credential state")
			}
		})
	}
}

func TestConversionUnknownSaveNeverReplaysAfterRestart(t *testing.T) {
	h := newFakeHost()
	h.saveError = true
	opts := offlineOptions(t)
	opts.Call = h.call
	s := offlineService(t, opts)
	source := conversionFixture(t)
	r := conversionRequest(t, s, "upload", "conversion-lost-ack", source)
	if ojson.GetBytes(r.Body, "items.0.status").String() != "uncertain" || h.saves != 1 {
		t.Fatal("lost save acknowledgment was not quarantined")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	h.saveError = false
	s = offlineService(t, opts)
	r = conversionRequest(t, s, "upload", "conversion-retry-new", source)
	if ojson.GetBytes(r.Body, "items.0.status").String() != "uncertain" || h.saves != 1 {
		t.Fatal("unknown import was replayed after restart")
	}
}

func TestConversionHonorsPolicyAndPendingSecurity(t *testing.T) {
	for _, mode := range []string{"plan", "security", "invalid-selection", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeHost()
			opts := offlineOptions(t)
			opts.Call = h.call
			s := offlineService(t, opts)
			source := conversionFixture(t)
			want := ""
			switch mode {
			case "plan":
				settings, _ := s.AccountSettings()
				settings.CPAUploadExcludedPlans = []string{"plus"}
				if _, err := s.UpdateAccountSettings(settings); err != nil {
					t.Fatal(err)
				}
				want = "cpa_plan_excluded"
			case "security":
				a := importOfflineAccount(t, s)
				s.accountMu.Lock()
				stored := s.accounts[a.ID]
				stored.View.SecurityPending = true
				err := s.saveAccountLocked(stored)
				s.accountMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				want = "security_pending"
			case "invalid-selection":
				source = `{"accounts":[{"platform":"anthropic","type":"oauth","credentials":{"access_token":"offline"}}]}`
			case "disabled":
				d := ojson.NewDocString(source)
				_ = d.Set("accounts.0.disabled", true)
				source = d.String()
			}
			r := conversionRequest(t, s, "upload", "conversion-policy-case", source)
			if mode == "invalid-selection" {
				if r.StatusCode != 400 || h.saves != 0 {
					t.Fatal("invalid selection wrote credentials")
				}
				return
			}
			if mode == "disabled" {
				if h.saves != 1 || ojson.GetBytes(r.Body, "items.0.enabled").Bool() {
					t.Fatal("conversion enabled a disabled credential")
				}
				return
			}
			if ojson.GetBytes(r.Body, "items.0.code").String() != want || h.saves != 0 {
				t.Fatalf("policy guard not honored: %s", r.Body)
			}
		})
	}
}
