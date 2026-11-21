package login

import (
	"errors"
	"time"

	"github.com/MoeForge/moego/json"
	auth "github.com/cliproxyapi-plugins/model-quality/login/account"
)

// Job summaries are exported independently of their retained event stream.
// Older summaries also used a few fixed labels before runtime events existed.
func publicJobDiagnostics(v Job) bool {
	state := safeLogEntry(v)
	legacyStage := func(value string) bool {
		switch value {
		case "complete", "starting", "mfa", "authorizing", "exchanging", "validating":
			return true
		}
		return false
	}
	return v.Status != "" && v.Status == state.Status &&
		(v.Phase == state.Phase || legacyStage(v.Phase)) &&
		(v.ErrorStage == state.ErrorStage || legacyStage(v.ErrorStage)) &&
		(v.ErrorCode == state.ErrorCode || v.ErrorCode == "network_error" || runtimeErrorCode(v.ErrorCode) != "") &&
		v.ErrorReason == state.ErrorReason && v.RuntimeCode == state.RuntimeCode && v.HTTPStatus == state.HTTPStatus &&
		v.SecurityStatus == state.SecurityStatus && publicStoredCode(v.VaultError) &&
		runtimeDiagnostic(v.Method, []string{"", "browser", "password_totp"}) &&
		runtimeDiagnostic(v.Intent, []string{"", "import", "reauthorize"}) &&
		runtimeDiagnostic(v.Action, []string{"", "reset_totp", "logout_all"}) &&
		publicDiagnosticTime(v.CreatedAt) && publicDiagnosticTime(v.ExpiresAt)
}

func publicStoredCode(code string) bool {
	return code == "" || code == SafeCode(errors.New(code))
}

func publicDiagnosticTime(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > len(time.RFC3339Nano) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func jobReplyPath(path string) bool {
	switch path {
	case "POST/start", "POST/accounts/start", "POST/accounts/security", "POST/accounts/security/complete",
		"POST/callback", "POST/cancel", "POST/discard", "POST/commit":
		return true
	}
	return false
}

// A replay returns the originally accepted response, which can differ from the
// current job. Decode its public shape so unknown properties cannot reappear.
func decodeJobReply(raw []byte, id string) (Job, error) {
	var job Job
	if err := json.Unmarshal(raw, &job); err != nil || job.ID == "" || id != "" && job.ID != id || !publicJobDiagnostics(job) {
		return Job{}, errors.New("storage_unavailable")
	}
	job.PlanType, _ = auth.ParsePlan(job.PlanType)
	return job, nil
}

func cachedJobReply(raw []byte, id string) ([]byte, error) {
	job, err := decodeJobReply(raw, id)
	if err != nil {
		return nil, err
	}
	job.Email, job.AuthorizationURL = "", ""
	return Encode(job)
}
