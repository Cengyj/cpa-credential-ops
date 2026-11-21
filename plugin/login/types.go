// Package authlogin owns administrator-authorized OpenAI login and account inspection.
package login

import (
	"errors"
	"strings"

	"github.com/MoeForge/moego/ojson"
	auth "github.com/cliproxyapi-plugins/model-quality/login/account"
)

type Credentials struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	TOTPSecret   string `json:"totp_secret"`
	TOTPFactorID string `json:"totp_factor_id,omitempty"`
}
type RuntimeRequest struct {
	commitSecurity      func() error
	factorVerified      func(string) error
	Operation           string      `json:"operation,omitempty"`
	LogoutAfterReset    bool        `json:"logout_after_reset,omitempty"`
	Reauthorize         bool        `json:"reauthorize,omitempty"`
	SecurityJournalPath string      `json:"security_journal_path,omitempty"`
	Version             int         `json:"version"`
	JobID               string      `json:"job_id"`
	Attempt             int         `json:"attempt"`
	AuthorizationURL    string      `json:"authorization_url"`
	RedirectURI         string      `json:"redirect_uri"`
	State               string      `json:"state"`
	Credentials         Credentials `json:"credentials"`
	ProxyURL            string      `json:"proxy_url,omitempty"`
	WorkspaceID         string      `json:"workspace_id,omitempty"`
}
type StartRequest struct {
	knownPlans              []string
	securityBinding         auth.LoginSnapshot
	workspaceID             string
	securityAction          string
	logoutAllDevices        bool
	reauthorize             bool
	accountID               string
	accountSequence         uint64
	automatic               bool
	autoCommit              bool
	enableCredential        bool
	autoEvidence            string
	autoGuard               func() error
	ConsumerPlugin          string
	Method                  string
	Intent                  string
	TargetAuthID            string
	ExpectedIdentityVersion string
	ProxyURL                string
	Credentials             Credentials
	RememberCredentials     bool
	UseSavedCredentials     bool
}
type Job struct {
	CanDelete         bool   `json:"can_delete,omitempty"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	RuntimeCode       string `json:"runtime_code,omitempty"`
	PlanType          string `json:"plan_type"`
	Action            string `json:"action,omitempty"`
	SecurityStatus    string `json:"security_status,omitempty"`
	TOTPReset         bool   `json:"totp_reset"`
	DevicesLoggedOut  bool   `json:"devices_logged_out"`
	Reauthorize       bool   `json:"reauthorize"`
	LogoutAllDevices  bool   `json:"logout_all_devices"`
	AccountID         string `json:"account_id,omitempty"`
	Automatic         bool   `json:"automatic,omitempty"`
	AutoCommit        bool   `json:"auto_commit"`
	EnableCredential  bool   `json:"enable_credential"`
	CredentialEnabled bool   `json:"credential_enabled"`
	ID                string `json:"id"`
	Revision          uint64 `json:"revision"`
	Method            string `json:"method"`
	Intent            string `json:"intent"`
	Status            string `json:"status"`
	Phase             string `json:"phase"`
	MaskedEmail       string `json:"masked_email"`
	Email             string `json:"email,omitempty"`
	ErrorCode         string `json:"error_code,omitempty"`
	ErrorStage        string `json:"error_stage,omitempty"`
	ErrorReason       string `json:"error_reason,omitempty"`
	CandidateID       string `json:"candidate_id,omitempty"`
	AuthorizationURL  string `json:"authorization_url,omitempty"`
	TargetAuthID      string `json:"target_auth_id,omitempty"`
	CreatedAt         string `json:"created_at"`
	ExpiresAt         string `json:"expires_at"`
	VaultError        string `json:"vault_error,omitempty"`
}
type Candidate struct {
	PlanType        string   `json:"plan_type"`
	ID              string   `json:"id"`
	JobID           string   `json:"job_id"`
	Revision        uint64   `json:"revision"`
	MaskedEmail     string   `json:"masked_email"`
	AccountIDMasked string   `json:"account_id_masked"`
	Email           string   `json:"email"`
	AccountID       string   `json:"account_id"`
	UserID          string   `json:"user_id"`
	TargetAuthID    string   `json:"target_auth_id"`
	Restrictions    []string `json:"restrictions"`
	Intent          string   `json:"intent"`
}

// Encode batches serialization through a document, including nested values.
func Encode(value any) ([]byte, error) {
	d := ojson.NewDocString(`{}`)
	if err := d.Set("value", value); err != nil {
		return nil, err
	}
	return []byte(ojson.ParseBytes(d.Bytes()).Get("value").Raw), nil
}
func Object(raw []byte, allowed ...string) (ojson.Result, error) {
	if !ojson.ValidBytes(raw) {
		return ojson.Result{}, errors.New("invalid_request")
	}
	r := ojson.ParseBytes(raw)
	if !r.IsObject() {
		return r, errors.New("invalid_request")
	}
	keys := map[string]bool{}
	for _, k := range allowed {
		keys[k] = true
	}
	seen := map[string]bool{}
	valid := true
	r.ForEach(func(k, v ojson.Result) bool {
		key := k.String()
		if !keys[key] || seen[key] {
			valid = false
			return false
		}
		seen[key] = true
		return true
	})
	if !valid {
		return r, errors.New("invalid_request")
	}
	return r, nil
}
func stringField(r ojson.Result, k string) (string, error) {
	v := r.Get(k)
	if v.Exists() && v.Type != ojson.String {
		return "", errors.New("invalid_request")
	}
	return v.String(), nil
}
func DecodeCredentials(r ojson.Result) (Credentials, error) {
	var c Credentials
	obj, err := Object([]byte(r.Raw), "email", "password", "totp_secret", "totp_factor_id")
	if err != nil {
		return c, err
	}
	if c.Email, err = stringField(obj, "email"); err != nil {
		return c, err
	}
	if c.Password, err = stringField(obj, "password"); err != nil {
		return c, err
	}
	if c.TOTPSecret, err = stringField(obj, "totp_secret"); err != nil {
		return c, err
	}
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	if len(c.Email) > 254 || len(c.Password) > 4096 || len(c.TOTPSecret) > 1024 || !strings.Contains(c.Email, "@") || strings.ContainsAny(c.Email, "\r\n\x00") || c.Password == "" || c.TOTPSecret == "" {
		return Credentials{}, errors.New("invalid_credentials")
	}
	if v := obj.Get("totp_factor_id"); v.Exists() {
		if v.Type != ojson.String || v.String() != "" && (!validSecurityBinding(v.String()) || len(v.String()) > 512) {
			return Credentials{}, errors.New("invalid_credentials")
		}
		c.TOTPFactorID = v.String()
	}
	if !validTOTPSecret(c.TOTPSecret) {
		return Credentials{}, errors.New("invalid_credentials")
	}
	return c, nil
}
func DecodeStart(raw []byte) (StartRequest, error) {
	var s StartRequest
	r, err := Object(raw, "consumer_plugin", "method", "intent", "target_auth_id", "expected_identity_version", "credentials", "remember_credentials", "use_saved_credentials", "proxy_url")
	if err != nil {
		return s, err
	}
	for k, p := range map[string]*string{"consumer_plugin": &s.ConsumerPlugin, "method": &s.Method, "intent": &s.Intent, "target_auth_id": &s.TargetAuthID, "expected_identity_version": &s.ExpectedIdentityVersion, "proxy_url": &s.ProxyURL} {
		*p, err = stringField(r, k)
		if err != nil {
			return s, err
		}
	}
	if s.ProxyURL, err = NormalizeLoginProxy(s.ProxyURL); err != nil {
		return s, err
	}
	for k, p := range map[string]*bool{"remember_credentials": &s.RememberCredentials, "use_saved_credentials": &s.UseSavedCredentials} {
		v := r.Get(k)
		if v.Exists() && v.Type != ojson.True && v.Type != ojson.False {
			return s, errors.New("invalid_request")
		}
		*p = v.Bool()
	}
	if r.Get("credentials").Exists() {
		s.Credentials, err = DecodeCredentials(r.Get("credentials"))
		if err != nil {
			return s, err
		}
	}
	if s.ConsumerPlugin != "model-quality" || (s.Method != "password_totp" && s.Method != "browser") || (s.Intent != "import" && s.Intent != "reauthorize") {
		return s, errors.New("invalid_request")
	}
	if s.Intent == "import" && (s.TargetAuthID != "" || s.ExpectedIdentityVersion != "" || s.UseSavedCredentials) {
		return s, errors.New("invalid_request")
	}
	if s.Intent == "reauthorize" && (s.TargetAuthID == "" || s.ExpectedIdentityVersion == "") {
		return s, errors.New("invalid_request")
	}
	if s.Method == "browser" && (s.Credentials.Password != "" || s.UseSavedCredentials || s.RememberCredentials) {
		return s, errors.New("invalid_request")
	}
	if s.Method == "password_totp" && ((s.UseSavedCredentials && s.Credentials.Password != "") || (!s.UseSavedCredentials && s.Credentials.Password == "")) {
		return s, errors.New("invalid_credentials")
	}
	return s, nil
}
func Mask(s string) string {
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		if i < 1 {
			return "***"
		}
		return s[:1] + "***" + s[i:]
	}
	if len(s) < 8 {
		return "***"
	}
	return s[:3] + "…" + s[len(s)-3:]
}
func SafeCode(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	var preparation interface{ LoginErrorCode() string }
	if errors.As(err, &preparation) {
		s = preparation.LoginErrorCode()
	}
	if strings.HasPrefix(s, "conversion_") && runtimeDiagnostic(s, []string{"conversion_invalid_json", "conversion_unsupported_version", "conversion_unsupported_format", "conversion_account_limit", "conversion_unsupported_provider", "conversion_unsupported_type", "conversion_missing_access_token", "conversion_missing_account_id", "conversion_invalid_token", "conversion_invalid_expiry", "conversion_expired_without_refresh", "conversion_previous_upload_changed"}) {
		return s
	}
	switch s {
	case "cpa_credential_missing", "cpa_credential_unavailable", "quality_reconciliation_pending",
		"retained_quality_pause", "credential_handling_pending", "quality_credential_restricted":
		return s
	case "credential_login_pending":
		return "login_commit_pending"
	case "login_snapshot_conflict", "login_source_conflict":
		return "policy_changed"
	case "login_persist_failed":
		return "storage_unavailable"
	case "login_persist_unknown":
		return "commit_unknown"
	case "login_adoption_failed":
		return "saved_not_adopted"
	}
	if strings.HasPrefix(s, "security_") && runtimeDiagnostic(s, []string{"security_uncertain", "security_pending", "security_failed", "security_journal_invalid", "security_unavailable"}) {
		return s
	}
	if s == "cpa_plan_excluded" || s == "storage_capacity" || s == "quality_authority_unavailable" || s == "credential_enable_conflict" || s == "interrupted" || s == "invalid_proxy" || s == "proxy_auth_unsupported" {
		return s
	}
	for _, v := range []string{"duplicate_account", "account_not_linked", "automation_disabled", "invalid_request", "invalid_credentials", "not_found", "conflict", "duplicate_identity", "identity_conflict", "identity_unverifiable", "identity_changed", "policy_changed", "login_commit_pending", "unsupported_login_target", "unsupported_login_store", "home_controlled", "interaction_required", "credentials_rejected", "totp_rejected", "runtime_unavailable", "runtime_invalid", "runtime_failed", "runtime_timeout", "runtime_protocol_error", "queue_full", "rate_limited", "cancelled", "expired", "plugin_unavailable", "exchange_failed", "storage_unavailable", "vault_unavailable", "vault_not_initialized", "vault_conflict", "vault_corrupt", "vault_identity_changed", "idempotency_conflict", "idempotency_unknown", "invalid_callback", "already_consumed", "commit_unknown", "saved_not_adopted"} {
		if s == v {
			return s
		}
	}
	return "login_failed"
}
