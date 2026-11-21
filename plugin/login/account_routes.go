package login

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/MoeForge/moego/ojson"
)

// Account writes share the login service's authenticated idempotency journal.
func credentialAccountWrite(s *Service, method, path string, raw []byte) (any, error) {
	return s.writeAccountCommand(method, path, raw, "")
}

func (s *Service) writeAccountCommand(method, path string, raw []byte, commandID string) (any, error) {
	invalid := errors.New("invalid_request")
	if path == "/accounts/import" && method == http.MethodPost {
		r, err := Object(raw, "entries", "proxy_url")
		if err != nil || !r.Get("entries").IsArray() {
			return nil, invalid
		}
		if proxy := r.Get("proxy_url"); proxy.Exists() && proxy.Type != ojson.String {
			return nil, invalid
		}
		values := r.Get("entries").Array()
		if len(values) < 1 || len(values) > 100 {
			return nil, invalid
		}
		entries := make([]Credentials, 0, len(values))
		defer func() {
			for i := range entries {
				entries[i] = Credentials{}
			}
		}()
		for _, value := range values {
			entry, err := DecodeCredentials(value)
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
		}
		accounts, err := s.ImportAccountsWithProxy(entries, r.Get("proxy_url").String())
		return map[string]any{"items": accounts}, err
	}
	if path == "/accounts/settings" && method == http.MethodPut {
		r, err := Object(raw, "revision", "inspection_enabled", "auto_relogin_enabled", "interval_minutes", "login_proxy_url", "cpa_upload_excluded_plans")
		if err != nil {
			return nil, err
		}
		if proxy := r.Get("login_proxy_url"); proxy.Exists() && proxy.Type != ojson.String {
			return nil, invalid
		}
		revision, err := accountUint(r.Get("revision"))
		if err != nil {
			return nil, err
		}
		interval, err := accountUint(r.Get("interval_minutes"))
		if err != nil || interval < 5 || interval > 1440 {
			return nil, invalid
		}
		for _, key := range []string{"inspection_enabled", "auto_relogin_enabled"} {
			v := r.Get(key)
			if v.Type != ojson.True && v.Type != ojson.False {
				return nil, invalid
			}
		}
		current, err := s.AccountSettings()
		if err != nil {
			return nil, err
		}
		if r.Get("login_proxy_url").Exists() {
			current.LoginProxyURL = r.Get("login_proxy_url").String()
		}
		if values := r.Get("cpa_upload_excluded_plans"); values.Exists() {
			if !values.IsArray() || len(values.Array()) > 32 {
				return nil, invalid
			}
			current.CPAUploadExcludedPlans = []string{}
			for _, value := range values.Array() {
				if value.Type != ojson.String {
					return nil, invalid
				}
				current.CPAUploadExcludedPlans = append(current.CPAUploadExcludedPlans, value.String())
			}
		}
		return s.UpdateAccountSettings(AccountSettings{Revision: revision, InspectionEnabled: r.Get("inspection_enabled").Bool(), AutoReloginEnabled: r.Get("auto_relogin_enabled").Bool(), IntervalMinutes: int(interval), LoginProxyURL: current.LoginProxyURL, CPAUploadExcludedPlans: current.CPAUploadExcludedPlans})
	}
	if path == "/accounts/security" || path == "/accounts/security/complete" {
		allowed := []string{"id", "revision", "job_id"}
		if path == "/accounts/security" {
			allowed = []string{"id", "revision", "action", "logout_all_devices", "reauthorize", "upload_to_cpa"}
		}
		r, err := Object(raw, allowed...)
		if err != nil || method != http.MethodPost {
			return nil, invalid
		}
		for _, k := range []string{"id", "action", "job_id"} {
			v := r.Get(k)
			if v.Exists() && (v.Type != ojson.String || len(v.String()) > 512) {
				return nil, invalid
			}
		}
		revision, err := accountUint(r.Get("revision"))
		if err != nil || r.Get("id").String() == "" {
			return nil, invalid
		}
		if path == "/accounts/security/complete" {
			return s.CompleteAccountSecurity(r.Get("id").String(), revision, r.Get("job_id").String())
		}
		for _, k := range []string{"logout_all_devices", "reauthorize"} {
			v := r.Get(k)
			if v.Type != ojson.True && v.Type != ojson.False {
				return nil, invalid
			}
		}
		reauthorize := r.Get("reauthorize").Bool()
		upload, err := optionalCPAUpload(r, reauthorize)
		if err != nil {
			return nil, err
		}
		return s.startManualSecurity(r.Get("id").String(), revision, r.Get("action").String(), r.Get("logout_all_devices").Bool(), reauthorize, upload, commandID)
	}
	allowed := []string{"id", "revision"}
	if path == "/accounts/start" {
		allowed = append(allowed, "target_auth_id", "upload_to_cpa")
	}
	if path == "/accounts/entry" && method == http.MethodPut {
		allowed = append(allowed, "label", "auto_relogin", "credentials", "proxy_url")
	}
	r, err := Object(raw, allowed...)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"id", "label", "target_auth_id"} {
		v := r.Get(key)
		if v.Exists() && (v.Type != ojson.String || len(v.String()) > 512) {
			return nil, invalid
		}
	}
	id := r.Get("id").String()
	if id == "" {
		return nil, invalid
	}
	revision, err := accountUint(r.Get("revision"))
	if err != nil {
		return nil, err
	}
	switch {
	case path == "/accounts/start" && method == http.MethodPost:
		upload, err := optionalCPAUpload(r, true)
		if err != nil {
			return nil, err
		}
		return s.startManualAccountCommand(id, revision, r.Get("target_auth_id").String(), upload, commandID)
	case path == "/accounts/entry" && method == http.MethodDelete:
		return map[string]bool{"deleted": true}, s.DeleteAccount(id, revision)
	case path == "/accounts/entry" && method == http.MethodPut:
		v := r.Get("auto_relogin")
		if v.Type != ojson.True && v.Type != ojson.False {
			return nil, invalid
		}
		update := AccountUpdate{ID: id, Revision: revision, Label: r.Get("label").String(), AutoRelogin: v.Bool()}
		if proxy := r.Get("proxy_url"); proxy.Exists() {
			if proxy.Type != ojson.String {
				return nil, invalid
			}
			value := proxy.String()
			update.ProxyURL = &value
		}
		if r.Get("credentials").Exists() {
			credentials, err := DecodeCredentials(r.Get("credentials"))
			if err != nil {
				return nil, err
			}
			update.Credentials = &credentials
			defer func() { credentials = Credentials{} }()
		}
		return s.UpdateAccount(update)
	default:
		return nil, errors.New("not_found")
	}
}

func optionalCPAUpload(r ojson.Result, fallback bool) (bool, error) {
	v := r.Get("upload_to_cpa")
	if !v.Exists() {
		return fallback, nil
	}
	if v.Type != ojson.True && v.Type != ojson.False {
		return false, errors.New("invalid_request")
	}
	return v.Bool(), nil
}

func accountUint(v ojson.Result) (uint64, error) {
	value, err := strconv.ParseUint(v.Raw, 10, 64)
	if v.Type != ojson.Number || err != nil {
		return 0, errors.New("invalid_request")
	}
	return value, nil
}
