package quality

import (
	"errors"
	"maps"
	"strings"
	"time"
)

type ScopeSkipDetail struct {
	NotExecuted bool   `json:"not_executed,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	AuthID      string `json:"auth_id"`
	ObjectType  string `json:"object_type,omitempty"`
	AuthIndex   string `json:"auth_index,omitempty"`
	Email       string `json:"email,omitempty"`
	Code        string `json:"code"`
	Model       string `json:"model,omitempty"`
	Reason      string `json:"reason"`
}

func newScopedRun(rule *Rule, t Target, config RunConfig, question Question, batch string, scheduled bool, now time.Time, snapshot *CredentialSnapshot) *Run {
	config = cloneConfig(config)
	config.AuthID = t.AuthID
	if config.Provider == "" {
		config.Provider = t.Provider
	}
	f := Frozen{RunConfig: config, Question: question, RuleID: rule.ID, RuleRevision: rule.Revision, ScopeBatchID: batch,
		SubjectIndex: t.AuthIndex, Scheduled: scheduled, IdentityVersion: t.IdentityVersion, ComparisonIdentity: t.ComparisonIdentity,
		PayloadConfigVersion: t.PayloadConfigVersion, ManagedProbe: snapshot, UpstreamErrorActions: maps.Clone(rule.UpstreamErrorActions)}
	if isProviderTarget(t) {
		f.ProviderObservation = true
		f.ProviderName = t.ProviderName
		if snapshot == nil || rule.LegacyProviderObservation {
			f.UpstreamErrorActions = nil
		}
	}
	f.Fingerprint = fingerprint(f)
	return &Run{ID: ID(), CredentialEmail: t.Email, Status: "queued", Source: "scheduled_probe", CreatedAt: now, Config: f, Samples: []Sample{}, ActionStatus: "observe_only"}
}

func markDispatchSkip(run *Run, detail *ScopeSkipDetail, now time.Time) {
	detail.AuthID, detail.Email, detail.RunID = run.Config.AuthID, run.CredentialEmail, run.ID
	if status := preflightStatus(detail.Code); !detail.NotExecuted && status != "" {
		run.PreflightDetail = cloneValue(detail)
		run.Status, run.CompletedAt, run.Error = status, &now, detail.Reason
		return
	}
	run.SkipDetail = cloneValue(detail)
	run.Status, run.CompletedAt = "skipped", &now
}

func preflightStatus(code string) string {
	switch code {
	case "unsupported_provider", "unsupported_file", "native_routing_required", "snapshot_unavailable":
		return "failed"
	case "identity_conflict", "identity_missing", "bound_origin_changed", "provider_mismatch", "handling_blocked", "ownership_changed", "quality_paused", "snapshot_runtime_conflict", "management_session_required", "runtime_unavailable", "credential_missing":
		return "blocked"
	}
	return ""
}

func queueCatalogRun(run *Run, model string) {
	detail := &ScopeSkipDetail{Code: "missing_model", Model: model, Reason: "CPA 已核实该凭证没有注册所选模型。", ObjectType: objectOAuth, AuthID: run.Config.AuthID, Email: run.CredentialEmail, RunID: run.ID}
	if run.Config.ProviderObservation {
		detail.ObjectType, detail.AuthIndex = objectProvider, run.Config.SubjectIndex
		detail.Reason = "CPA 已核实该供应商路由没有注册所选模型。"
	}
	run.PreflightDetail = detail
	run.Config.CatalogMissingModel = model
	run.Config.Fingerprint = ""
	run.Config.Fingerprint = fingerprint(run.Config)
}

func ruleSubjectEnabled(t Target) bool {
	if objectType(t) == objectUnknown || isProviderTarget(t) && (contains(t.Reasons, "provider_route_unverified") || contains(t.Reasons, "provider_runtime_unconfirmed")) {
		return false
	}
	return !t.identityConflict && t.AuthID != "" && t.AuthIndex != "" && t.Provider != "" && !t.ManualDisabled && t.RuntimeStatus != "disabled" && !t.QualityPaused && !t.RetainedPause
}

func providerRouteProblem(t Target, models []string) *scopeRejection {
	if problem := providerBindingProblem(t); problem != nil {
		return problem
	}
	if !ruleSubjectEnabled(t) || !t.EffectiveAvailable || t.RuntimeStatus != "active" && t.RuntimeStatus != "registered" || len(t.Models) == 0 {
		return rejectScope("blocked: provider target is unavailable", "provider_unavailable", "CPA 未能核实可用的供应商凭证与模型目录。")
	}
	for _, model := range models {
		if !contains(t.Models, model) {
			return &scopeRejection{"blocked: provider model is not registered", ScopeSkipDetail{Code: "provider_model_unavailable", Model: model, Reason: "供应商未注册所选模型。"}}
		}
	}
	return nil
}

func firstMissingModel(t Target, models []string) string {
	if t.catalogVerified && len(t.Models) > 0 && !t.identityConflict {
		for _, model := range models {
			if !contains(t.Models, model) {
				return model
			}
		}
	}
	return ""
}

func (en *Engine) recordFixedAdmissionLocked(rule *Rule, run *Run) (*Run, error) {
	next := cloneRule(rule)
	next.LastRun, next.LastScopeBatch = run.ID, run.Config.ScopeBatchID
	summary := &ScopeDispatchSummary{At: run.CreatedAt, Matched: 1}
	ch := changes()
	if detail := run.SkipDetail; detail != nil {
		summary.Matched, summary.Skipped = 0, 1
		summary.Details = []ScopeSkipDetail{*detail}
		ch.audits = append(ch.audits, en.audit("credential_skipped", rule.ID, run.ID, "", detail.Reason))
	}
	next.LastScopeDispatch = summary
	ch.rules[rule.ID], ch.runs[run.ID] = next, run
	if err := en.commitLocked(ch); err != nil {
		return nil, err
	}
	if run.Status == "queued" {
		en.queue <- run.ID
	} else {
		en.noteRoundEndLocked(rule.ID)
		if rule.MaxResults > 0 {
			if err := en.pruneResultCountsLocked(rule.ID, run.Config.AuthID); err != nil {
				en.persistFailure(err)
			}
		}
	}
	return cloneRun(run), nil
}

func sessionSkip() ScopeSkipDetail {
	return ScopeSkipDetail{Code: "management_session_required", Reason: "后台管理会话尚未就绪，请查看后台连接状态。"}
}

func queueSkip() ScopeSkipDetail {
	return ScopeSkipDetail{Code: "queue_full", Reason: "队列容量不足，本轮未入队。"}
}

func missingCredentialSkip() ScopeSkipDetail {
	return ScopeSkipDetail{Code: "credential_missing", Reason: "当前目录没有这份规则绑定的凭证。"}
}

type ScopeDispatchSummary struct {
	DirectoryUnavailable bool              `json:"directory_unavailable,omitempty"`
	Reason               string            `json:"reason,omitempty"`
	RecoveryCount        int               `json:"recovery_count,omitempty"`
	Candidates           map[string]int    `json:"candidates,omitempty"`
	At                   time.Time         `json:"at"`
	Matched              int               `json:"matched"`
	Skipped              int               `json:"skipped"`
	Details              []ScopeSkipDetail `json:"details,omitempty"`
}

// Keep the established management error and attach a display-safe explanation
// at the guard that actually rejected this credential.
type scopeRejection struct {
	legacy string
	detail ScopeSkipDetail
}

func (e *scopeRejection) Error() string { return e.legacy }

func rejectScope(legacy, code, reason string) *scopeRejection {
	return &scopeRejection{legacy: legacy, detail: ScopeSkipDetail{Code: code, Reason: reason}}
}

func (d *ruleDispatch) skip(t Target, detail ScopeSkipDetail) {
	detail.AuthID, detail.Email = t.AuthID, t.Email
	if detail.ObjectType == "" {
		detail.ObjectType = objectType(t)
	}
	if detail.ObjectType == objectProvider {
		detail.AuthIndex = t.AuthIndex
	}
	d.SkipDetails = append(d.SkipDetails, detail)
	d.Skipped++
}

func (d *ruleDispatch) skipManaged(t Target, err error) {
	var rejected *scopeRejection
	if !errors.As(err, &rejected) {
		rejected = rejectScope("blocked: credential snapshot is unavailable", "snapshot_unavailable", "无法从 CPA 核对本轮凭证快照。")
	}
	if d.SkipReasons == nil {
		d.SkipReasons = make(map[string]int)
	}
	d.SkipReasons[rejected.legacy]++
	d.skip(t, rejected.detail)
}

func missingModel(model, reason string) ScopeSkipDetail {
	return ScopeSkipDetail{Code: "missing_model", Model: model, Reason: reason}
}

func modelCatalogSkip(t Target, model, reason string) ScopeSkipDetail {
	if !t.catalogVerified || len(t.Models) == 0 {
		return ScopeSkipDetail{Code: "catalog_unavailable", Model: model, Reason: "CPA 未返回可核对的非空凭证模型目录，无法确认该模型是否已注册。"}
	}
	return missingModel(model, reason)
}

// Report the first difference in the same comparisons used by sameSnapshot.
func snapshotDifference(current, retained CredentialSnapshot) string {
	switch {
	case !sameCredential(current, retained):
		return "凭证当前的路由标识或内容指纹与停用记录不一致。"
	case current.Disabled != retained.Disabled:
		return "凭证当前的停用状态与停用记录不一致。"
	case current.UpdatedAt.IsZero() || !current.UpdatedAt.Equal(retained.UpdatedAt):
		return "凭证当前的运行更新时间与停用记录不一致。"
	default:
		return "凭证当前的文件修改时间与停用记录不一致。"
	}
}

func scopeRuntimeLabel(status string) string {
	label := map[string]string{
		"active": "活动（active）", "disabled": "停用（disabled）", "unknown": "未知（unknown）",
		"pending": "等待中（pending）", "refreshing": "刷新中（refreshing）", "error": "错误（error）",
		"": "未提供",
	}[status]
	if label == "" {
		return "未识别"
	}
	return label
}

func snapshotRuntimeConflict(t Target, snapshotDisabled bool) ScopeSkipDetail {
	state, flag := "启用", "关闭"
	if snapshotDisabled {
		state = "停用"
	}
	if t.ManualDisabled {
		flag = "开启"
	}
	return ScopeSkipDetail{Code: "snapshot_runtime_conflict", Reason: "凭证快照显示" + state + "，目录运行状态为" + scopeRuntimeLabel(t.RuntimeStatus) + "、停用标记为" + flag + "，两次读取不一致。"}
}

func runtimeSkip(t Target) ScopeSkipDetail {
	var reasons []string
	for _, r := range t.Reasons {
		switch r {
		case "operator_disabled", "manual_disabled":
			reasons = append(reasons, "凭证已停用")
		case "native_unavailable":
			reasons = append(reasons, "宿主标记为不可用")
		case "native_cooldown":
			reasons = append(reasons, "宿主冷却中")
		case "runtime_status_unavailable":
			reasons = append(reasons, "无法核对运行状态")
		default:
			reasons = append(reasons, "宿主报告了其他运行限制")
		}
	}
	if len(reasons) == 0 {
		switch {
		case t.AuthID == "" || t.AuthIndex == "" || t.Provider == "":
			reasons = append(reasons, "凭证标识、索引或提供方缺失")
		case t.ManualDisabled:
			reasons = append(reasons, "凭证已停用")
		case !t.EffectiveAvailable:
			reasons = append(reasons, "宿主未标记为可用")
		}
	}
	text := "CPA 运行状态为" + scopeRuntimeLabel(t.RuntimeStatus)
	if len(reasons) > 0 {
		text += "，" + strings.Join(reasons, "、")
	}
	return ScopeSkipDetail{Code: "runtime_unavailable", Reason: text + "。"}
}
