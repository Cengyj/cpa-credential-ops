package quality

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	bolt "go.etcd.io/bbolt"
)

const dispositionPrefix = "credential-disposition:"
const disableCredential = "disable_credential"
const deleteCredential = "delete_credential"

// The native independent-pause action is intentionally kept separate from CPA's
// disabled boolean. Historical pause ownership never becomes a new API action.
func managedRule(r *Rule) bool {
	if r == nil {
		return false
	}
	for _, action := range r.UpstreamErrorActions {
		if credentialMutation(action) {
			return true
		}
	}
	return credentialMutation(r.Action)
}

func credentialMutation(action string) bool {
	return action == disableCredential || action == deleteCredential
}

// Omitted settings from older editors preserve the saved policy. An explicit
// empty object or observe-only entries clear it.
func prepareUpstreamActions(in, old *Rule) error {
	settings := in.UpstreamErrorActions
	if settings == nil && old != nil {
		settings = old.UpstreamErrorActions
	}
	in.UpstreamErrorActions = nil
	for condition, action := range settings {
		switch condition {
		case "unauthorized", "payment_required", "quota_exhausted", "model_unsupported":
		default:
			return fmt.Errorf("invalid_request: unknown upstream error condition")
		}
		if action == "observe_only" {
			continue
		}
		if !credentialMutation(action) {
			return fmt.Errorf("invalid_request: invalid upstream error action")
		}
		if in.UpstreamErrorActions == nil {
			in.UpstreamErrorActions = make(map[string]string)
		}
		in.UpstreamErrorActions[condition] = action
	}
	return nil
}

func upstreamAction(f *Frozen, s *Sample) string {
	if f.ManagedProbe == nil || s.UpstreamCondition == "" || s.ExecutionStatus != "failed" || s.QualityVerdict != "unknown" {
		return ""
	}
	e := s.Evidence
	if !managedEvidencePath(f.ManagedProbe, e.Path) || e.ConditionsChanged || e.AuthID != f.AuthID || e.RequestedModel != s.Model || e.PayloadFingerprint == "" ||
		s.Index < 0 || s.Index >= f.Samples || !contains(f.Models, s.Model) {
		return ""
	}
	if action := f.UpstreamErrorActions[s.UpstreamCondition]; credentialMutation(action) {
		return action
	}
	return ""
}

type CredentialDisposition struct {
	ErrorSource         *CredentialErrorSource `json:"error_source,omitempty"`
	AnswerOwnerRuleID   string                 `json:"answer_owner_rule_id,omitempty"`
	TriggerAuthID       string                 `json:"trigger_auth_id,omitempty"`
	TriggerRunID        string                 `json:"trigger_run_id,omitempty"`
	CatalogMissingModel string                 `json:"catalog_missing_model,omitempty"`
	UpstreamCondition   string                 `json:"upstream_condition,omitempty"`
	Email               string                 `json:"email,omitempty"`
	AuthID              string                 `json:"auth_id"`
	RuleID              string                 `json:"rule_id"`
	RuleRevision        uint64                 `json:"rule_revision"`
	ActionID            string                 `json:"action_id"`
	RunID               string                 `json:"run_id"`
	Operation           string                 `json:"operation"`
	State               string                 `json:"state"`
	Snapshot            CredentialSnapshot     `json:"snapshot"`
	Models              []string               `json:"models"`
	CreatedAt           time.Time              `json:"created_at"`
	UpdatedAt           time.Time              `json:"updated_at"`
}

// Error provenance never grants recovery ownership. Keep it independently of
// the answer disposition and the trigger run's configurable history retention.
type CredentialErrorSource struct {
	RuleID         string    `json:"rule_id"`
	RuleRevision   uint64    `json:"rule_revision"`
	RunID          string    `json:"run_id"`
	AuthID         string    `json:"auth_id"`
	Condition      string    `json:"condition"`
	Model          string    `json:"model,omitempty"`
	CatalogMissing bool      `json:"catalog_missing,omitempty"`
	Evidence       Evidence  `json:"evidence"`
	CreatedAt      time.Time `json:"created_at"`
}

func (en *Engine) setErrorSource(d *CredentialDisposition, run *Run, condition string) {
	d.UpstreamCondition, d.CatalogMissingModel = condition, run.Config.CatalogMissingModel
	if d.CatalogMissingModel != "" {
		d.UpstreamCondition = ""
	}
	d.TriggerAuthID, d.TriggerRunID = run.Config.AuthID, run.ID
	d.UpdatedAt = en.now()
	source := &CredentialErrorSource{RuleID: run.Config.RuleID, RuleRevision: run.Config.RuleRevision,
		RunID: run.ID, AuthID: run.Config.AuthID, Condition: condition, CreatedAt: en.now(),
		Model: run.Config.CatalogMissingModel, CatalogMissing: run.Config.CatalogMissingModel != ""}
	if len(run.Samples) > 0 && !source.CatalogMissing {
		last := run.Samples[len(run.Samples)-1]
		source.Model, source.Evidence = last.Model, last.Evidence
	}
	d.ErrorSource = source
}

func answerOwner(d *CredentialDisposition) string {
	if d == nil {
		return ""
	}
	if d.AnswerOwnerRuleID != "" {
		return d.AnswerOwnerRuleID
	}
	// Older answer dispositions predate the explicit owner field.
	if d.State == "disabled" && d.UpstreamCondition == "" && d.CatalogMissingModel == "" && d.ErrorSource == nil {
		return d.RuleID
	}
	return ""
}

func (en *Engine) loadDispositions() error {
	en.managed = map[string]*CredentialDisposition{}
	return en.store.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte("meta")).Cursor()
		for key, raw := c.Seek([]byte(dispositionPrefix)); key != nil && strings.HasPrefix(string(key), dispositionPrefix); key, raw = c.Next() {
			var d CredentialDisposition
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			if d.AuthID == "" || string(key) != dispositionPrefix+d.AuthID {
				return fmt.Errorf("invalid credential disposition identity")
			}
			en.managed[d.AuthID] = &d
		}
		return nil
	})
}

func activeDisposition(d *CredentialDisposition) bool {
	return d != nil && d.State != "restored" && d.State != "externally_enabled" && d.State != "entry_missing" && d.State != "acknowledged" && d.State != "precondition_failed"
}

func pendingCredentialAction(state string) string {
	switch state {
	case "disable_pending":
		return disableCredential
	case "delete_pending":
		return deleteCredential
	case "restore_pending":
		return "restore_credential"
	}
	return ""
}

// Startup has exclusive access. A pending intent cannot establish whether CPA
// received the write. Preserve its evidence without consulting or changing CPA.
func (en *Engine) interruptCredentialWrites() error {
	for _, d := range en.managed {
		operation := pendingCredentialAction(d.State)
		if operation == "" {
			continue
		}
		c := changes()
		next := *d
		next.State, next.UpdatedAt = "attention", en.now()
		c.disposition = &next
		if r := en.runs[d.RunID]; r != nil && r.ActionStatus == d.State && r.Config.RuleID == d.RuleID && r.Config.AuthID == d.AuthID {
			updated := cloneRun(r)
			updated.CredentialAction, updated.ActionStatus = operation, "credential_action_uncertain"
			c.runs[r.ID] = updated
		}
		c.audits = append(c.audits, en.audit("credential_action_uncertain", d.RuleID, d.RunID, d.ActionID, "Plugin restarted before confirmation of "+operation))
		if err := en.commitLocked(c); err != nil {
			return err
		}
	}
	// Old history may outlive a handling marker that was explicitly acknowledged.
	for id, r := range en.runs {
		if op := pendingCredentialAction(r.ActionStatus); op != "" {
			c := changes()
			updated := cloneRun(r)
			updated.CredentialAction, updated.ActionStatus = op, "credential_action_uncertain"
			c.runs[id] = updated
			if err := en.commitLocked(c); err != nil {
				return err
			}
		}
	}
	return nil
}

type HandlingSummary struct {
	AnswerOwnerRuleID   string                 `json:"answer_owner_rule_id,omitempty"`
	ErrorSource         *CredentialErrorSource `json:"error_source,omitempty"`
	CatalogMissingModel string                 `json:"catalog_missing_model,omitempty"`
	UpstreamCondition   string                 `json:"upstream_condition,omitempty"`
	Email               string                 `json:"email,omitempty"`
	AuthID              string                 `json:"auth_id"`
	ActionID            string                 `json:"action_id"`
	State               string                 `json:"state"`
}

func handlingSummary(d *CredentialDisposition) HandlingSummary {
	return HandlingSummary{Email: d.Email, AuthID: d.AuthID, ActionID: d.ActionID, State: d.State,
		UpstreamCondition: d.UpstreamCondition, CatalogMissingModel: d.CatalogMissingModel,
		AnswerOwnerRuleID: answerOwner(d), ErrorSource: cloneValue(d.ErrorSource)}
}

func (en *Engine) orphanedHandlingLocked(d *CredentialDisposition) bool {
	return d != nil && d.State == "attention" && en.rules[d.RuleID] == nil
}

// Acknowledgment is an explicit local release of bookkeeping after an operator
// has enabled/replaced/removed the credential in CPA. It never changes CPA state.
func (en *Engine) acknowledgeHandling(ctx context.Context, ruleID, authID, actionID string) error {
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	d := en.managed[authID]
	r := en.rules[ruleID]
	orphaned := ruleID == "" && en.orphanedHandlingLocked(d)
	if authID == "" || actionID == "" || d == nil || d.ActionID != actionID || !activeDisposition(d) || !orphaned && (r == nil || d.RuleID != ruleID) {
		return fmt.Errorf("conflict: credential handling record changed")
	}
	if _, busy := en.busyAuth[authID]; busy {
		return fmt.Errorf("conflict: wait for the active credential task to finish")
	}
	directory := &providerInventoryStatus{Verified: true}
	if d.Snapshot.ProviderMechanism != "" {
		directory.Verified = false
		ctx = context.WithValue(ctx, providerInventoryStatusKey{}, directory)
	}
	targets, err := en.host.Targets(ctx)
	if err != nil {
		return err
	}
	if !directory.Verified {
		return fmt.Errorf("blocked: the current provider configuration could not be verified")
	}
	found := false
	for _, t := range targets {
		if t.AuthID != authID {
			continue
		}
		if found {
			return fmt.Errorf("blocked: credential catalog contains duplicate identities")
		}
		found = true
		if _, err := en.acknowledgedTargetSnapshot(ctx, t); err != nil {
			return err
		}
	}
	if d.Snapshot.ProviderMechanism == "group_disabled" {
		return en.acknowledgeProviderGroupLocked(ctx, r, d, targets)
	}
	if !found && d.Snapshot.ProviderMechanism == "" {
		h, ok := en.host.(interface {
			removedCredentials(context.Context, []*CredentialDisposition, int) ([]*CredentialDisposition, error)
		})
		if !ok {
			return fmt.Errorf("blocked: current credential removal cannot be verified")
		}
		removed, err := h.removedCredentials(ctx, []*CredentialDisposition{d}, 1)
		if err != nil || len(removed) != 1 {
			return fmt.Errorf("blocked: current credential removal could not be confirmed in CPA")
		}
	}
	next := *d
	next.State = "acknowledged"
	next.UpdatedAt = en.now()
	c := changes()
	c.disposition = &next
	if r != nil {
		en.cancelRuleChangesLocked(c, r.ID, "credential_handling_acknowledged")
	} else {
		en.cancelManagedRuns(c, authID, "")
	}
	c.audits = append(c.audits, en.audit("credential_handling_acknowledged", d.RuleID, "", d.ActionID, "Operator acknowledged external resolution; rule settings preserved; no CPA credential write"))
	return en.commitLocked(c)
}

// A stale enabled list row is insufficient to clear an uncertain write. Read
// the current identified credential without probing or changing its CPA state.
func (en *Engine) acknowledgedTargetSnapshot(ctx context.Context, t Target) (CredentialSnapshot, error) {
	var empty CredentialSnapshot
	if !t.identityVerified || !ruleSubjectEnabled(t) || t.RuntimeStatus != "active" && t.RuntimeStatus != "registered" || en.localPause(RunConfig{AuthID: t.AuthID}) != "" {
		return empty, fmt.Errorf("blocked: first resolve this credential's state and identity in CPA")
	}
	h, ok := en.host.(managedHost)
	if !ok {
		return empty, fmt.Errorf("unsupported: current credential snapshots are unavailable")
	}
	s, err := h.CredentialSnapshot(ctx, t.AuthID, t.AuthIndex)
	if err != nil {
		return empty, err
	}
	if s.Disabled || s.AuthID != t.AuthID || s.AuthIndex != t.AuthIndex || s.Provider != t.Provider || s.Name == "" || s.Fingerprint == "" || s.ProviderMechanism == "" && s.UpdatedAt.IsZero() {
		return empty, fmt.Errorf("blocked: current credential enablement could not be confirmed in CPA")
	}
	return s, nil
}

func (en *Engine) validateManagedRule(in, old *Rule) error {
	if len(in.UpstreamErrorActions) > 0 && (in.Action == "pause_credential" || in.AutomaticPause) {
		return fmt.Errorf("invalid_request: upstream error actions cannot be combined with independent quality pause")
	}
	if in.AutoRestore && in.Action != disableCredential {
		return fmt.Errorf("invalid_request: automatic restore requires credential suspension")
	}
	if managedRule(in) {
		if !en.Capabilities().CredentialManagement {
			return fmt.Errorf("unsupported: CPA management adapter is unavailable")
		}
		if in.ControlAuthID != "" || in.Judge.Mode == "llm" {
			return fmt.Errorf("unsupported: management probes do not support independent control or LLM judge credentials")
		}
		if credentialMutation(in.Action) {
			q, err := FreezeQuestion(in.RunConfig)
			if err != nil || in.Judge.Mode != "" && in.Judge.Mode != "deterministic" || q.Answer == "" || q.Kind != "logic" && q.Kind != "random" {
				return fmt.Errorf("invalid_request: answer handling requires a supported standard-answer question and deterministic judging")
			}
		}
		if _, ok := en.host.(managedHost); !ok {
			return fmt.Errorf("unsupported: CPA credential actions unavailable")
		}
	}
	if old != nil {
		for _, d := range en.managed {
			if d.RuleID != old.ID || !activeDisposition(d) || d.State == "deleted" {
				continue
			}
			scopeChanged := in.TargetScope != old.TargetScope && !(dynamicScope(in.TargetScope) && dynamicScope(old.TargetScope))
			if in.Action != old.Action || fingerprint(in.RunConfig) != fingerprint(old.RunConfig) || scopeChanged {
				return fmt.Errorf("conflict: enable or resolve the suspended credential in CPA before changing the test or handling policy")
			}
		}
	}
	return nil
}

func canRecoverDisabled(r *Rule, d *CredentialDisposition) bool {
	return recoveryDenied(r, d).Code == ""
}

func recoveryDenied(r *Rule, d *CredentialDisposition) ScopeSkipDetail {
	if detail := errorPolicyDisabled(d); detail.Code != "" {
		return detail
	}
	if !r.AutoRestore {
		return ScopeSkipDetail{Code: "recovery_disabled", Reason: "规则未开启整轮通过后的自动恢复，不能复检停用凭证。"}
	}
	if r.Action != disableCredential {
		return ScopeSkipDetail{Code: "recovery_not_permitted", Reason: "当前处置策略没有停用凭证复检权限。"}
	}
	if activeDisposition(d) {
		if d.State != "disabled" {
			return ScopeSkipDetail{Code: "handling_blocked", Reason: "这份处置记录没有已确认的停用状态，不能复检。"}
		}
		if answerOwner(d) != r.ID {
			return ScopeSkipDetail{Code: "other_rule_owner", Reason: "停用记录属于另一条规则。"}
		}
	} else if r.TargetScope != scopeAllCredentials {
		return ScopeSkipDetail{Code: "recovery_not_permitted", Reason: "当前范围没有复检人工停用凭证的权限。"}
	}
	return ScopeSkipDetail{}
}

// The returned projection is local to the probe. CPA stays disabled throughout.
func (en *Engine) admitManagedLocked(ctx context.Context, r *Rule, t Target, validateOnly bool) (Target, *CredentialSnapshot, error) {
	h, ok := en.host.(managedHost)
	if !ok || !en.Capabilities().ManagementSession {
		return t, nil, fmt.Errorf("management_session_required: background management connection is not ready")
	}
	if en.localPause(RunConfig{AuthID: t.AuthID}) != "" || t.QualityPaused || t.RetainedPause {
		return t, nil, fmt.Errorf("blocked: retained quality pause")
	}
	snapshot, err := h.CredentialSnapshot(ctx, t.AuthID, t.AuthIndex)
	if err != nil {
		return t, nil, err
	}
	return en.admitSnapshotLocked(r, t, snapshot, validateOnly)
}

func (en *Engine) admitSnapshotLocked(r *Rule, t Target, snapshot CredentialSnapshot, validateOnly bool) (Target, *CredentialSnapshot, error) {
	d := en.managed[t.AuthID]
	if activeDisposition(d) {
		if d.State != "disabled" {
			return t, nil, rejectScope("blocked: credential handling requires attention", "handling_blocked", "对象的上次处置尚待核对。")
		}
		if !snapshot.Disabled {
			if err := en.observeEnabledTargetLocked(t, snapshot, d, validateOnly); err != nil {
				return t, nil, err
			}
		} else {
			if detail := errorPolicyDisabled(d); detail.Code != "" {
				return t, nil, &scopeRejection{"blocked: credential already disabled by error policy", detail}
			}
			if !canInspectManualDisabled(r, d) {
				if detail := recoveryDenied(r, d); detail.Code != "" {
					return t, nil, &scopeRejection{"blocked: disabled credential is outside detection scope", detail}
				}
			}
			return projectOwnedTarget(r, t, snapshot, d)
		}
	}
	if snapshot.Disabled && !canInspectManualDisabled(r, d) {
		if detail := recoveryDenied(r, d); detail.Code != "" {
			return t, nil, &scopeRejection{"blocked: disabled credential is outside detection scope", detail}
		}
	}
	return projectManagedTarget(r, t, snapshot)
}

// Retire only a confirmed suspension when CPA identifies its current live
// target independently. This is local reconciliation, with no credential write.
func (en *Engine) observeEnabledTargetLocked(t Target, s CredentialSnapshot, d *CredentialDisposition, validateOnly bool) error {
	if t.QualityPaused || t.RetainedPause || en.localPause(RunConfig{AuthID: t.AuthID}) != "" {
		return rejectScope("blocked: retained quality pause", "quality_paused", "凭证仍受质量暂停保护，禁止检测。")
	}
	if !ruleSubjectEnabled(t) {
		if t.ManualDisabled || t.RuntimeStatus == "disabled" {
			return &scopeRejection{"blocked: credential runtime and snapshot disagree", snapshotRuntimeConflict(t, false)}
		}
		return &scopeRejection{"blocked: credential unavailable", runtimeSkip(t)}
	}
	if s.AuthID != t.AuthID || s.AuthIndex != t.AuthIndex || s.Provider != t.Provider || s.Name == "" || s.Fingerprint == "" || (s.ProviderMechanism == "" && s.UpdatedAt.IsZero()) {
		return rejectScope("blocked: current credential identity cannot be verified", "identity_conflict", "CPA 当前文件快照与本轮目录凭证标识不一致，无法核实当前凭证。")
	}
	changed := !sameCredential(s, d.Snapshot)
	if changed && !t.catalogVerified && !t.identityVerified {
		return rejectScope("blocked: current credential catalog identity cannot be verified", "identity_conflict", "CPA 未能从当前目录唯一核实已启用的凭证，原停用记录已保留。")
	}
	if validateOnly {
		return nil
	}
	next := *d
	next.State, next.UpdatedAt = "externally_enabled", en.now()
	kind, detail := "credential_external_enable", "CPA enablement observed. Previous automatic restore ownership relinquished."
	if changed {
		kind, detail = "credential_external_change", "CPA reports an enabled credential with a changed file or identity. The previous suspension was retired locally without a credential write."
	}
	c := changes()
	c.disposition = &next
	en.cancelManagedRuns(c, t.AuthID, "")
	for id, old := range en.rules {
		if id == d.RuleID || usesCredential(old.RunConfig, t.AuthID) {
			updated := cloneRule(old)
			updated.FailCount, updated.EvidenceSignature = 0, ""
			c.rules[id] = updated
		}
	}
	c.audits = append(c.audits, en.audit(kind, d.RuleID, d.RunID, d.ActionID, detail))
	return en.commitLocked(c)
}

func projectOwnedTarget(r *Rule, t Target, snapshot CredentialSnapshot, d *CredentialDisposition) (Target, *CredentialSnapshot, error) {
	if !sameCredentialEntry(snapshot, d.Snapshot) {
		return t, nil, rejectScope("blocked: suspended entry identity cannot be matched", "identity_conflict", "CPA 当前条目标识无法与原答错停用记录唯一关联；原记录保留，不猜测恢复归属。")
	}
	if d.RuleID == r.ID && r.AutoRestore && len(d.Models) != len(r.Models) {
		return t, nil, rejectScope("blocked: recovery must test the original complete model selection", "recovery_models_changed", "当前所选模型数量与停用记录中的数量不同。")
	}
	for _, model := range r.Models {
		if d.RuleID == r.ID && r.AutoRestore && !contains(d.Models, model) {
			return t, nil, &scopeRejection{"blocked: recovery must test the original complete model selection", ScopeSkipDetail{Code: "recovery_models_changed", Model: model, Reason: "当前所选模型不在停用时的模型集合中。"}}
		}
	}
	return projectManagedTarget(r, t, snapshot)
}

func projectManagedTarget(r *Rule, t Target, snapshot CredentialSnapshot) (Target, *CredentialSnapshot, error) {
	if snapshot.Disabled {
		if t.RuntimeStatus != "disabled" || !t.ManualDisabled {
			return t, nil, &scopeRejection{"blocked: disabled credential is outside recovery scope", snapshotRuntimeConflict(t, true)}
		}
		if t.RetainedPause || t.QualityPaused {
			return t, nil, rejectScope("blocked: disabled credential is outside recovery scope", "quality_paused", "凭证仍受质量暂停保护，禁止检测。")
		}
		for _, reason := range t.Reasons {
			if reason != "operator_disabled" && reason != "manual_disabled" {
				return t, nil, &scopeRejection{"blocked: credential has additional runtime restrictions", runtimeSkip(t)}
			}
		}
		t.ManualDisabled, t.EffectiveAvailable, t.RuntimeStatus = false, true, "active"
		// CPA unregisters models on disable. A recovery run still has to
		// receive a complete, judged answer for every configured model.
		if len(t.Models) == 0 {
			t.Models = append([]string(nil), r.Models...)
		}
	}
	if !ruleSubjectEnabled(t) {
		detail := runtimeSkip(t)
		if !snapshot.Disabled && (t.RuntimeStatus == "disabled" || t.ManualDisabled) {
			detail = snapshotRuntimeConflict(t, false)
		}
		return t, nil, &scopeRejection{"blocked: credential is disabled or unavailable", detail}
	}
	return t, &snapshot, nil
}

func (en *Engine) managedTargetsLocked(ctx context.Context, r *Rule, targets []Target, validateOnly bool) ([]Target, *CredentialSnapshot, error) {
	if !managedRule(r) {
		return targets, nil, nil
	}
	out := append([]Target(nil), targets...)
	for i, t := range out {
		if t.AuthID == r.AuthID {
			if isProviderTarget(t) {
				return targets, nil, nil
			}
			adjusted, snapshot, err := en.admitManagedLocked(ctx, r, t, validateOnly)
			out[i] = adjusted
			return out, snapshot, err
		}
	}
	return out, nil, fmt.Errorf("blocked: credential is missing")
}

func (en *Engine) checkManagedRunLocked(run *Run, targets []Target) ([]Target, error) {
	if run.Config.ManagedProbe == nil {
		return targets, nil
	}
	r := en.rules[run.Config.RuleID]
	if r == nil || !managedRule(r) && !(run.Config.ManagedProbe.Disabled && canInspectManualDisabled(r, en.managed[run.Config.AuthID])) {
		return targets, fmt.Errorf("blocked: credential handling rule changed")
	}
	out := append([]Target(nil), targets...)
	for i, t := range out {
		if t.AuthID == run.Config.AuthID {
			expected := run.Config.ManagedProbe
			if t.AuthIndex != expected.AuthIndex || t.QualityPaused || t.RetainedPause || !expected.Disabled && activeDisposition(en.managed[t.AuthID]) {
				return nil, fmt.Errorf("blocked: credential handling changed after dispatch")
			}
			adjusted, _, err := en.admitSnapshotLocked(r, t, *expected, false)
			if err != nil {
				return nil, err
			}
			out[i] = adjusted
			return out, nil
		}
	}
	return nil, fmt.Errorf("blocked: credential is missing")
}

func managedRoundVerdict(run *Run) string {
	if run.Config.ManagedProbe == nil || run.Config.Judge.Mode != "deterministic" {
		return "unknown"
	}
	verdict := roundVerdict(run, func(s *Sample) bool {
		e := &s.Evidence
		return managedEvidencePath(run.Config.ManagedProbe, e.Path) && !e.ConditionsChanged && e.AuthID == run.Config.AuthID && e.RequestedModel == s.Model && e.PayloadFingerprint != "" && e.ResolvedModel != ""
	})
	if verdict == "mixed" {
		return "incorrect"
	}
	return verdict
}

func (en *Engine) cancelManagedRuns(c *changeSet, authID, exceptRun string) {
	for id, old := range en.runs {
		if id == exceptRun || !usesCredential(old.Config.RunConfig, authID) || (old.Status != "queued" && old.Status != "running") {
			continue
		}
		r := en.cancelledRunLocked(old, "blocked")
		r.BlockReason = "credential_handling"
		r.ActionStatus = "credential_handling"
		c.runs[id] = r
	}
}

func (en *Engine) decideManagedLocked(run *Run, r *Rule, c *changeSet) {
	out := c.runs[run.ID]
	r.FailCount = 0
	r.EvidenceSignature = ""
	c.rules[r.ID] = r
	verdict := managedRoundVerdict(run)
	operation := r.Action
	condition := ""
	if errorPolicyDisabled(en.managed[run.Config.AuthID]).Code != "" {
		out.ActionStatus = "credential_kept_disabled"
		_ = en.commitLocked(c)
		return
	}
	catalogAction := run.Config.CatalogMissingModel != ""
	if catalogAction {
		if !credentialMutation(r.UpstreamErrorActions["model_unsupported"]) {
			out.ActionStatus = "observe_only"
			_ = en.commitLocked(c)
			return
		}
		if run.Config.ManagedProbe == nil {
			out.ActionStatus = "catalog_action_unconfirmed"
			_ = en.commitLocked(c)
			return
		}
		operation, condition = r.UpstreamErrorActions["model_unsupported"], "model_unsupported"
	}
	if run.Status == "completed" && !run.CancelRequested && run.BlockReason == "" && len(run.Samples) > 0 {
		last := &run.Samples[len(run.Samples)-1]
		if action := upstreamAction(&run.Config, last); action != "" && action == r.UpstreamErrorActions[last.UpstreamCondition] {
			operation, condition = action, last.UpstreamCondition
			out.UpstreamCondition = condition
		}
	}
	if snap := run.Config.ManagedProbe; snap != nil && snap.ProviderMechanism != "" {
		en.decideProviderLocked(run, r, c, verdict, operation, condition)
		return
	}
	if verdict == "unknown" && condition == "" {
		out.ActionStatus = "insufficient_evidence"
		_ = en.commitLocked(c)
		return
	}
	h, ok := en.host.(managedHost)
	if !ok || !en.Capabilities().ManagementSession {
		out.ActionStatus = "management_session_required"
		_ = en.commitLocked(c)
		return
	}
	current, err := h.CredentialSnapshot(en.ctx, run.Config.AuthID, run.Config.ManagedProbe.AuthIndex)
	// A file write needs a current file snapshot, even for the same CPA ID and index.
	if err != nil || current.ProviderMechanism != "" || r.boundOriginChanged(current.AuthID, objectOAuth) || !sameSnapshot(current, *run.Config.ManagedProbe) {
		out.ActionStatus = "credential_changed"
		_ = en.commitLocked(c)
		return
	}
	if catalogAction && !en.confirmCatalogActionLocked(run, r) {
		out.ActionStatus = "catalog_action_unconfirmed"
		_ = en.commitLocked(c)
		return
	}
	prior := en.managed[current.AuthID]
	restore := false
	if activeDisposition(prior) {
		if prior.State != "disabled" || !sameCredentialEntry(current, prior.Snapshot) {
			out.ActionStatus = "credential_action_attention"
			_ = en.commitLocked(c)
			return
		}
		if current.Disabled && condition != "" {
			if operation == disableCredential {
				next := *prior
				next.AnswerOwnerRuleID = answerOwner(prior)
				en.setErrorSource(&next, run, condition)
				c.disposition = &next
				en.cancelManagedRuns(c, current.AuthID, run.ID)
				c.audits = append(c.audits, en.audit("error_policy_disabled", r.ID, run.ID, prior.ActionID, "Confirmed upstream condition on a disabled entry; original answer owner retained; no CPA write"))
			}
			out.ActionStatus = "credential_kept_disabled"
			_ = en.commitLocked(c)
			return
		}
		if verdict != "correct" || !canRecoverDisabled(r, prior) {
			out.ActionStatus = "credential_kept_disabled"
			_ = en.commitLocked(c)
			return
		}
		restore = true
	} else if current.Disabled {
		if condition != "" && operation == disableCredential {
			d := &CredentialDisposition{AuthID: current.AuthID, RuleID: r.ID, RuleRevision: r.Revision, ActionID: ID(), RunID: run.ID,
				Operation: disableCredential, State: "disabled", Snapshot: current, CreatedAt: en.now(), UpdatedAt: en.now()}
			en.setErrorSource(d, run, condition)
			c.disposition = d
			en.cancelManagedRuns(c, current.AuthID, run.ID)
			c.audits = append(c.audits, en.audit("error_policy_disabled", r.ID, run.ID, d.ActionID, "Confirmed upstream condition on an already disabled entry; no answer recovery ownership and no CPA write"))
		}
		if condition != "" || !canRecoverDisabled(r, prior) || verdict != "correct" {
			out.ActionStatus = "credential_kept_disabled"
			_ = en.commitLocked(c)
			return
		}
		restore = true
	} else if condition == "" && (verdict != "incorrect" || !credentialMutation(operation)) {
		out.ActionStatus = "observe_only"
		_ = en.commitLocked(c)
		return
	}
	state := "disable_pending"
	if operation == deleteCredential {
		state = "delete_pending"
	}
	if restore {
		state = "restore_pending"
		operation = "restore_credential"
	}
	d := &CredentialDisposition{AuthID: current.AuthID, RuleID: r.ID, RuleRevision: r.Revision, ActionID: ID(), RunID: run.ID, Operation: operation, State: state, Snapshot: current, Models: append([]string(nil), run.Config.Models...), CreatedAt: en.now(), UpdatedAt: en.now()}
	d.Email = run.CredentialEmail
	if condition != "" {
		en.setErrorSource(d, run, condition)
	} else if operation == disableCredential {
		d.AnswerOwnerRuleID = r.ID
	}
	if restore && activeDisposition(prior) {
		d.ActionID = prior.ActionID
		d.CreatedAt = prior.CreatedAt
		d.AnswerOwnerRuleID = answerOwner(prior)
	}
	c.disposition = d
	out.CredentialAction = operation
	out.ActionStatus = state
	if !restore {
		en.cancelManagedRuns(c, current.AuthID, run.ID)
	}
	detail := "Complete management probe round; exact credential snapshot checked before requesting CPA action"
	if condition != "" {
		detail = "Confirmed upstream condition " + condition + "; exact credential snapshot checked before requesting CPA action"
	}
	if catalogAction {
		detail = "CPA credential catalog confirmed the selected model was not registered; no upstream request; exact credential snapshot checked before requesting CPA action"
	}
	if restore && !activeDisposition(prior) {
		detail = "Manually disabled credential passed every sample in the all-credentials rule; requesting enablement"
	}
	c.audits = append(c.audits, en.audit(state, r.ID, run.ID, d.ActionID, detail))
	// The durable intent is written before the API call; pending or uncertain
	// actions are never replayed by restart, timers, GETs, or retries.
	if err = en.commitLocked(c); err != nil {
		return
	}
	// The worker still owns this credential and the pending intent blocks
	// conflicting operations. An unrelated worker need not wait for CPA's write.
	en.mu.Unlock()
	next, err := applyCredentialIntent(en.ctx, h, *d)
	en.mu.Lock()
	result := changes()
	out = cloneRun(en.runs[run.ID])
	result.runs[run.ID] = out
	if err != nil {
		next.State = "attention"
		next.Snapshot = current
		out.ActionStatus = "credential_action_uncertain"
		if errors.Is(err, errCredentialPrecondition) {
			next.State, out.ActionStatus = "precondition_failed", "credential_changed"
			if prior != nil {
				next = *prior
			}
		}
	} else {
		switch next.State {
		case "disabled":
			out.ActionStatus = "credential_suspended"
		case "deleted":
			out.ActionStatus = "credential_removed"
		case "restored":
			out.ActionStatus = "credential_restored"
		}
	}
	next.UpdatedAt = en.now()
	result.disposition = &next
	detail = "CPA action confirmed; ownership and credential snapshot recorded"
	if err != nil {
		detail = "CPA action could not be confirmed; no automatic retry: " + scrubError(err)
		if errors.Is(err, errCredentialPrecondition) {
			detail = "Credential changed during the final local check; no CPA write was attempted and prior ownership was preserved"
		}
	}
	result.audits = append(result.audits, en.audit(out.ActionStatus, r.ID, run.ID, next.ActionID, detail))
	_ = en.commitLocked(result)
}

func applyCredentialIntent(ctx context.Context, h managedHost, d CredentialDisposition) (CredentialDisposition, error) {
	switch d.Operation {
	case deleteCredential:
		err := h.DeleteCredential(ctx, d.Snapshot)
		d.State = "deleted"
		return d, err
	case disableCredential, "restore_credential":
	default:
		return d, fmt.Errorf("invalid credential operation")
	}
	disable := d.Operation == disableCredential
	after, err := h.SetCredentialDisabled(ctx, d.Snapshot, disable)
	d.Snapshot = after
	d.State = "restored"
	if disable {
		d.State = "disabled"
	}
	return d, err
}

func canInspectManualDisabled(rule *Rule, d *CredentialDisposition) bool {
	return rule != nil && !rule.LegacyScope && rule.TargetScope == scopeAllCredentials &&
		(!activeDisposition(d) || d.State == "disabled" && errorPolicyDisabled(d).Code == "")
}

func managedEvidencePath(snapshot *CredentialSnapshot, path string) bool {
	if snapshot == nil {
		return false
	}
	if snapshot.ProviderMechanism != "" {
		return path == "provider_config_probe"
	}
	return path == "cpa_management_probe"
}
