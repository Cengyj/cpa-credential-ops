package quality

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/json"
	bolt "go.etcd.io/bbolt"
)

type CredentialLoginPrepareRequest struct {
	AuthID           string `json:"auth_id"`
	ExpectedIdentity string `json:"expected_identity"`
	PreparationID    string `json:"preparation_id"`
}

type CredentialLoginPrepareResponse struct {
	PreparationID string `json:"preparation_id"`
	Ready         bool   `json:"ready"`
}

const loginHoldPrefix = "credential-login:"

type loginPreparationError struct {
	code string
	error
}

func (e loginPreparationError) LoginErrorCode() string { return e.code }

type credentialLoginHold struct {
	CredentialLoginPrepareRequest
	State             string            `json:"state"`
	MissingEntryRules map[string]uint64 `json:"missing_entry_rules,omitempty"`
	// Only the current process can own a live writer. After restart the
	// login service reads its separate commit journal before releasing a hold.
	live       bool
	generation uint64
}

func (en *Engine) loadLoginHolds() error {
	en.loginHolds = make(map[string]*credentialLoginHold)
	return en.store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket([]byte("meta")).Cursor()
		for key, raw := cursor.Seek([]byte(loginHoldPrefix)); key != nil && strings.HasPrefix(string(key), loginHoldPrefix); key, raw = cursor.Next() {
			var hold credentialLoginHold
			if err := json.Unmarshal(raw, &hold); err != nil {
				return err
			}
			if string(key) != loginHoldPrefix+hold.AuthID || hold.AuthID == "" || hold.PreparationID == "" {
				return errors.New("invalid credential login hold")
			}
			switch hold.State {
			case "preparing", "verifying", "failed", "unknown", "identity_conflict", "completed":
				en.loginHolds[hold.AuthID] = &hold
			default:
				return errors.New("unknown credential login hold state")
			}
		}
		return nil
	})
}

func (en *Engine) loginWaitLocked(c RunConfig) *credentialLoginHold {
	for _, id := range dependencies(c) {
		if hold := en.loginHolds[id]; hold != nil && hold.State != "completed" {
			return hold
		}
	}
	return nil
}

func (h *credentialLoginHold) reason() (string, string) {
	switch h.State {
	case "failed":
		return "credential_update_failed", "此次凭证更新未完成，等待新的已确认授权。"
	case "unknown":
		return "credential_write_unknown", "凭证写入结果待核对，检测仍在等待。"
	case "identity_conflict":
		return "credential_identity_conflict", "授权回执与原 CPA 条目不一致，等待核对身份。"
	default:
		return "credential_update_pending", "等待凭证更新完成并核实 CPA 提交回执。"
	}
}

// PrepareCredentialLogin isolates every use of the old grant through the
// write window. User permissions and the schedule remain independently editable.
func (en *Engine) PrepareCredentialLogin(ctx context.Context, request CredentialLoginPrepareRequest) (CredentialLoginPrepareResponse, error) {
	reply := CredentialLoginPrepareResponse{PreparationID: request.PreparationID}
	if request.AuthID == "" || len(request.AuthID) > 512 || len(request.ExpectedIdentity) > 128 || request.PreparationID == "" || len(request.PreparationID) > 128 {
		return reply, errors.New("invalid login preparation")
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return reply, err
	}
	probe := RunConfig{AuthID: request.AuthID}
	if !en.Capabilities().QualityPause && en.localPause(probe) != "" {
		return reply, loginPreparationError{"retained_quality_pause", unsupportedQuality()}
	}
	// Only a confirmed ordinary disable can become stale after a CPA operator
	// enables the entry. Pending writes and independent pauses need no query.
	if record := en.managed[request.AuthID]; record != nil && record.State == "disabled" {
		targets, err := en.readAdmissionTargetsLocked(ctx, en.host.Targets, probe, nil)
		if err != nil {
			return reply, loginPreparationError{"quality_authority_unavailable", err}
		}
		if err := currentTargetError(en.reconcileDirectoryLocked(ctx, targets, []string{request.AuthID}), []string{request.AuthID}); err != nil {
			return reply, loginPreparationError{"quality_authority_unavailable", err}
		}
	}
	if err := en.mutableLocked(); err != nil {
		return reply, err
	}
	if !en.Capabilities().QualityPause && en.localPause(probe) != "" {
		return reply, loginPreparationError{"retained_quality_pause", unsupportedQuality()}
	}
	if record := en.managed[request.AuthID]; activeDisposition(record) {
		code := "credential_handling_pending"
		if record.State == "disabled" || record.State == "deleted" {
			code = "quality_credential_restricted"
		}
		return reply, loginPreparationError{code, errors.New("blocked: credential handling must be resolved before account authorization")}
	}
	prev := en.loginHolds[request.AuthID]
	for id, other := range en.loginHolds {
		if id != request.AuthID && other.PreparationID == request.PreparationID {
			return reply, loginPreparationError{"identity_conflict", errors.New("conflict: preparation ID belongs to another credential")}
		}
	}
	if prev != nil && prev.PreparationID == request.PreparationID && prev.ExpectedIdentity != request.ExpectedIdentity {
		return reply, loginPreparationError{"identity_conflict", errors.New("conflict: credential login binding changed")}
	}
	if prev == nil || prev.PreparationID != request.PreparationID || prev.State == "failed" {
		if prev != nil && prev.State != "failed" && prev.State != "completed" && !(prev.State == "identity_conflict" && prev.ExpectedIdentity != "" && prev.ExpectedIdentity == request.ExpectedIdentity) {
			return reply, loginPreparationError{"login_commit_pending", errors.New("blocked: previous credential write requires verification")}
		}
		c := changes()
		c.loginHold = &credentialLoginHold{CredentialLoginPrepareRequest: request, State: "preparing", live: true}
		for id, old := range en.rules {
			if !usesCredential(old.RunConfig, request.AuthID) || old.ActionID != "" || old.BlockedAuthID != "" {
				continue
			}
			r := cloneRule(old)
			r.FailCount, r.EvidenceSignature = 0, ""
			c.rules[id] = r
			if request.ExpectedIdentity == "" && r.Enabled {
				if c.loginHold.MissingEntryRules == nil {
					c.loginHold.MissingEntryRules = make(map[string]uint64)
				}
				c.loginHold.MissingEntryRules[id] = r.Revision
			}
		}
		for id, old := range en.runs {
			if !usesCredential(old.Config.RunConfig, request.AuthID) || old.Status != "running" && old.Status != "queued" {
				continue
			}
			r := en.cancelledRunLocked(old, "cancelled")
			r.BlockReason = "credential_authorization_changed"
			c.runs[id] = r
		}
		c.audits = append(c.audits, en.audit("credential_login_prepare", "", "", request.PreparationID, "Dependent executions isolated before credential write"))
		if err := en.commitLocked(c); err != nil {
			return reply, err
		}
	} else if prev.State != "preparing" || !prev.live {
		return reply, loginPreparationError{"already_consumed", errors.New("conflict: preparation already consumed")}
	}
	for {
		if err := ctx.Err(); err != nil {
			return reply, err
		}
		if err := en.mutableLocked(); err != nil {
			return reply, err
		}
		if en.ctx.Err() != nil {
			return reply, errors.New("plugin stopping")
		}
		if _, busy := en.busyAuth[request.AuthID]; !busy {
			reply.Ready = true
			return reply, nil
		}
		if en.queueChanged == nil {
			en.notifyWorkersLocked()
		}
		changed := en.queueChanged
		en.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-en.ctx.Done():
		case <-changed:
		}
		en.mu.Lock()
	}
}

// PendingCredentialLogins returns only read-only reconciliation work. A live
// prepare must keep its reservation until the account writer reports its result.
func (en *Engine) PendingCredentialLogins() map[string]string {
	en.mu.Lock()
	defer en.mu.Unlock()
	var pending map[string]string
	for id, hold := range en.loginHolds {
		if !hold.live && (hold.State == "preparing" || hold.State == "verifying") {
			if pending == nil {
				pending = make(map[string]string)
			}
			pending[id] = hold.PreparationID
		}
	}
	return pending
}

// FinishCredentialLogin consumes only the matching receipt. It never writes
// CPA or enables a rule. The next normal admission reads current permissions,
// runtime availability, and a new snapshot for every subject and dependency.
func (en *Engine) FinishCredentialLogin(authID, commitID, identity, outcome string) error {
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	old := en.loginHolds[authID]
	if old == nil || old.PreparationID != commitID || old.State != "preparing" && old.State != "verifying" {
		return nil
	}
	state := "unknown"
	switch outcome {
	case "aborted", "upload_excluded":
		state = "failed"
	case "awaiting_verification":
		state = "verifying"
	case "identity_conflict":
		state = "identity_conflict"
	case "committed":
		if identity == "" || old.ExpectedIdentity != "" && identity != old.ExpectedIdentity {
			state = "identity_conflict"
		} else {
			state = "completed"
		}
	}
	if old.State == state && !old.live {
		return nil
	}
	c := changes()
	updated := *old
	updated.State, updated.live = state, false
	c.loginHold = &updated
	if state == "completed" {
		// Creating a new file at an old locator does not carry the operator's
		// authorization for its previous occupant. Only unchanged direct
		// bindings from before that create need an explicit rule resume.
		for id, revision := range old.MissingEntryRules {
			r := en.rules[id]
			if r == nil || !r.Enabled || r.Revision != revision || !usesCredential(r.RunConfig, authID) || r.ActionID != "" || r.BlockedAuthID != "" {
				continue
			}
			next := cloneRule(r)
			next.Enabled = false
			next.Revision++
			resetRule(next)
			next.Status = ruleStatus(next)
			c.rules[id] = next
			c.audits = append(c.audits, en.audit("credential_binding_changed", id, "", commitID, "A new CPA entry occupied the previous locator; review the rule binding before resuming"))
		}
		updated.MissingEntryRules = nil
	}
	c.audits = append(c.audits, en.audit("credential_login_result", "", "", commitID, fmt.Sprintf("Credential write coordination: %s", state)))
	return en.commitLocked(c)
}
