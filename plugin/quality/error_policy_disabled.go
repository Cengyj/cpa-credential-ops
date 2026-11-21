package quality

import (
	"context"
	"errors"
	"fmt"
)

type excludedCredentialIDs struct{}

// Reconcile confirmed ordinary suspensions against current CPA state before
// applying scope or recovery policy. Unknown writes and independent pauses stay
// protected; an observation never writes a credential or starts a model call.
func (en *Engine) reconcileCurrentTargetsLocked(ctx context.Context, targets []Target, ids []string) map[string]error {
	var read snapshotReader
	if h, ok := en.host.(managedHost); ok {
		read = h.CredentialSnapshot
	}
	return en.reconcileUsingLocked(ctx, targets, ids, read)
}

func (en *Engine) reconcileUsingLocked(ctx context.Context, targets []Target, ids []string, read snapshotReader) map[string]error {
	failures := make(map[string]error)
	for _, t := range targets {
		if ids != nil && !contains(ids, t.AuthID) {
			continue
		}
		d := en.managed[t.AuthID]
		if d == nil || d.State != "disabled" || disabledObject(t) {
			continue
		}
		if !ruleSubjectEnabled(t) || !t.identityVerified && !t.catalogVerified {
			failures[t.AuthID] = rejectScope("blocked: current CPA identity cannot be verified", "identity_conflict", "CPA 当前对象身份或运行状态无法核实，请检查目录连接。")
			continue
		}
		if read == nil || t.IsProvider && !en.Capabilities().ManagementSession {
			failures[t.AuthID] = &scopeRejection{"management_session_required: background management connection is not ready", sessionSkip()}
			continue
		}
		if d.Snapshot.ProviderMechanism == "group_disabled" {
			if err := en.observeEnabledProviderGroupLocked(ctx, targets, d, read); err != nil {
				failures[t.AuthID] = err
			}
			continue
		}
		snapshot, err := read(ctx, t.AuthID, t.AuthIndex)
		if err == nil && snapshot.Disabled {
			err = &scopeRejection{"blocked: credential runtime and snapshot disagree", snapshotRuntimeConflict(t, true)}
		}
		if err == nil {
			err = en.observeEnabledTargetLocked(t, snapshot, d, false)
		}
		if err != nil {
			failures[t.AuthID] = err
		}
	}
	return failures
}

// Missing runtime entries can reflect a reload. Only a file-specific CPA 404
// proves file removal. Preserve the old record and authority on ambiguous or
// failed reads, so a returning disabled entry cannot silently regain probes.
func (en *Engine) observeMissingEntriesLocked(ctx context.Context, targets []Target, providersVerified bool) error {
	present := make(map[string]bool, len(targets))
	unresolvedGroups := make(map[string]bool)
	for _, t := range targets {
		present[t.AuthID] = true
		if t.ProviderGroupID != "" && (disabledObject(t) || t.identityConflict || !t.identityVerified) {
			unresolvedGroups[t.ProviderGroupID] = true
		}
	}
	var files []*CredentialDisposition
	confirmed := make(map[string]*CredentialDisposition)
	for id, d := range en.managed {
		if !en.currentDirectoryAuthority(ctx, id) || d.State != "disabled" || present[id] || d.Snapshot.AuthID == "" || d.Snapshot.AuthIndex == "" {
			continue
		}
		if d.Snapshot.ProviderMechanism != "" {
			// A rotated key also disappears from CPA's content-derived IDs.
			// Retain the old source while the configured group remains disabled
			// or unverified; absence alone must not turn it into a recoverable
			// manual disable. This does not transfer ownership to a new ID.
			if providersVerified && !unresolvedGroups[d.Snapshot.ProviderGroupID] {
				confirmed[id] = d
			}
		} else {
			files = append(files, d)
		}
	}
	if h, ok := en.host.(interface {
		removedCredentials(context.Context, []*CredentialDisposition, int) ([]*CredentialDisposition, error)
	}); ok && en.Capabilities().ManagementSession && len(files) != 0 {
		workers := max(1, en.cfg.Workers)
		en.mu.Unlock()
		removed, err := h.removedCredentials(ctx, files, workers)
		en.mu.Lock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := en.mutableLocked(); err != nil {
			return err
		}
		if err == nil {
			for _, d := range removed {
				confirmed[d.AuthID] = d
			}
		}
	}
	update := changes()
	for id, expected := range confirmed {
		if en.managed[id] != expected || !en.currentDirectoryAuthority(ctx, id) {
			continue
		}
		d := en.managed[id]
		next := *d
		next.State, next.UpdatedAt = "entry_missing", en.now()
		update.dispositions = append(update.dispositions, &next)
		en.cancelManagedRuns(update, id, "")
		update.audits = append(update.audits, en.audit("credential_entry_missing", d.RuleID, d.RunID, d.ActionID,
			"CPA confirmed the previous entry locator is absent; history retained without granting a new entry recovery authority"))
	}
	if len(update.dispositions) == 0 {
		return nil
	}
	return en.commitLocked(update)
}

func currentTargetError(failures map[string]error, ids []string) error {
	for _, id := range ids {
		if err := failures[id]; err != nil {
			return err
		}
	}
	return nil
}

// Call only after reconciling current CPA state. Remaining error sources
// describe a still-disabled object and cannot authorize a model probe.
func (en *Engine) runDispositionLocked(c RunConfig, manual bool) error {
	if hold := en.loginWaitLocked(c); hold != nil {
		code, reason := hold.reason()
		return rejectScope("blocked: "+reason, code, reason)
	}
	for _, id := range dependencies(c) {
		d := en.managed[id]
		if !activeDisposition(d) {
			continue
		}
		if cause := errorPolicyCause(d); cause != "" {
			return fmt.Errorf("blocked: %s，凭证已按错误策略停用。", cause)
		}
		if manual && d.State != "disabled" {
			return errors.New("blocked: 凭证存在未解除的处置记录，请在质量运维中核对。")
		}
	}
	return nil
}

func (en *Engine) frozenDispositionLocked(f Frozen) error {
	if err := en.runDispositionLocked(f.RunConfig, f.RuleID == ""); err != nil {
		return err
	}
	if f.ManualPolicyActionID != "" {
		return errors.New("blocked: 旧测试依赖已经失效的处置记录，请重新提交当前凭证测试。")
	}
	return nil
}

func (en *Engine) currentManualAccount(ctx context.Context, c RunConfig, targets []Target) (string, error) {
	for _, t := range targets {
		if t.AuthID != c.AuthID {
			continue
		}
		if t.IsProvider {
			h, ok := en.host.(managedHost)
			if !ok {
				return "", errors.New("blocked: current provider configuration cannot be verified")
			}
			snapshot, err := h.CredentialSnapshot(ctx, t.AuthID, t.AuthIndex)
			if err != nil {
				return "", err
			}
			if snapshot.Disabled {
				return "", errors.New("blocked: CPA current provider is disabled")
			}
			return snapshot.Fingerprint, nil
		}
		if h, ok := en.host.(interface {
			verifyEnabledManualFile(context.Context, Target, *CredentialDisposition) (string, error)
		}); ok {
			return h.verifyEnabledManualFile(ctx, t, nil)
		}
	}
	return "", errors.New("blocked: current CPA credential cannot be verified")
}

func groupTargetBlocked(records map[string]*CredentialDisposition, t Target) bool {
	d := groupGuard(records, t)
	return d != nil && (d.State != "disabled" || disabledObject(t))
}

func (en *Engine) terminalDependencyLocked(c RunConfig) bool {
	for _, id := range dependencies(c) {
		if errorPolicyDisabled(en.managed[id]).Code != "" {
			return true
		}
	}
	return false
}

// The confirmed cause describes the original suspension, independently of CPA's current state.
func errorPolicyCause(d *CredentialDisposition) string {
	if d == nil || d.State != "disabled" {
		return ""
	}
	if d.CatalogMissingModel != "" {
		return "CPA 注册目录缺少所选模型"
	}
	switch d.UpstreamCondition {
	case "unauthorized":
		return "上游返回 HTTP 401 未授权"
	case "payment_required":
		return "上游返回 HTTP 402 需要付款"
	case "quota_exhausted":
		return "上游返回 HTTP 429，确认账户额度耗尽"
	case "model_unsupported":
		return "上游明确不支持所选模型（HTTP 400 或 404）"
	default:
		return ""
	}
}

// A confirmed error suspension excludes probes until CPA enablement is observed.
func errorPolicyDisabled(d *CredentialDisposition) ScopeSkipDetail {
	cause := errorPolicyCause(d)
	if cause == "" {
		return ScopeSkipDetail{}
	}
	return ScopeSkipDetail{Code: "error_policy_disabled", Model: d.CatalogMissingModel,
		Reason: cause + "，凭证已按错误策略停用，后续检测已停止。"}
}

// Persist a source missing from an older confirmed disposition before
// retention can remove the linked run. Configuration alone is not evidence.
func (en *Engine) recoverDisabledSources() error {
	// A quality suspension could previously run recovery again after an
	// explicitly classified error. Choose its first retained, matching probe.
	recovery := make(map[string]*Run)
	for _, run := range en.runs {
		d := en.managed[run.Config.AuthID]
		if d == nil || d.State != "disabled" || d.Operation != "" && d.Operation != disableCredential || !d.Snapshot.Disabled ||
			d.CatalogMissingModel != "" || d.UpstreamCondition != "" || run.Source != "scheduled_probe" ||
			run.Status != "completed" || run.ActionStatus != "credential_kept_disabled" || run.Config.RuleID != d.RuleID ||
			d.CreatedAt.IsZero() || run.CreatedAt.Before(d.CreatedAt) || run.Config.ManagedProbe == nil ||
			!sameSnapshot(*run.Config.ManagedProbe, d.Snapshot) || confirmedRecoveryCondition(run) == "" {
			continue
		}
		prev := recovery[d.AuthID]
		if prev == nil || run.CreatedAt.Before(prev.CreatedAt) || run.CreatedAt.Equal(prev.CreatedAt) && run.ID < prev.ID {
			recovery[d.AuthID] = run
		}
	}
	for _, old := range en.managed {
		if old.State != "disabled" || old.Operation != "" && old.Operation != disableCredential || old.UpstreamCondition != "" || old.CatalogMissingModel != "" {
			continue
		}
		run := en.runs[old.RunID]
		next := *old
		if run != nil && run.ID == old.RunID && run.Source == "scheduled_probe" && run.ActionStatus == "credential_suspended" &&
			run.Config.AuthID == old.AuthID && run.Config.RuleID == old.RuleID && run.Config.RuleRevision == old.RuleRevision &&
			(run.Config.ManagedProbe == nil || sameCredential(*run.Config.ManagedProbe, old.Snapshot)) &&
			(run.CredentialAction == "" || run.CredentialAction == disableCredential) {
			switch {
			case (run.Status == "skipped" || run.Status == "completed") && run.Config.CatalogMissingModel != "" && run.Config.UpstreamErrorActions["model_unsupported"] == disableCredential:
				next.CatalogMissingModel = run.Config.CatalogMissingModel
			case run.Status == "completed" && run.UpstreamCondition != "":
				next.UpstreamCondition = run.UpstreamCondition
			case run.Status == "completed" && len(run.Samples) > 0:
				last := &run.Samples[len(run.Samples)-1]
				if upstreamAction(&run.Config, last) == disableCredential {
					next.UpstreamCondition = last.UpstreamCondition
				}
			}
		}
		if errorPolicyDisabled(&next).Code == "" && recovery[old.AuthID] != nil {
			next.UpstreamCondition = confirmedRecoveryCondition(recovery[old.AuthID])
		}
		if errorPolicyDisabled(&next).Code != "" {
			updates := changes()
			updates.disposition = &next
			if err := en.commitLocked(updates); err != nil {
				return err
			}
		}
	}
	return nil
}

func confirmedRecoveryCondition(run *Run) string {
	if run.UpstreamCondition != "" && credentialMutation(run.Config.UpstreamErrorActions[run.UpstreamCondition]) {
		return run.UpstreamCondition
	}
	if len(run.Samples) > 0 {
		last := &run.Samples[len(run.Samples)-1]
		if upstreamAction(&run.Config, last) != "" {
			return last.UpstreamCondition
		}
	}
	return ""
}
