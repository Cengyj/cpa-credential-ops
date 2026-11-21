package quality

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
)

type scopeMatch struct {
	target         Target
	config         RunConfig
	snapshot       *CredentialSnapshot
	missing        string
	recovery       bool
	judge, control *Target
}

var errScopeDirectoryUnavailable = errors.New("unavailable: cannot read CPA targets")

func needsScopeSnapshot(rule *Rule, t Target) bool {
	if disabledObject(t) {
		return true
	}
	if t.IsProvider {
		if rule.LegacyProviderObservation || !managedRule(rule) {
			return false
		}
		// Neither a selected channel nor the directory scope authorizes group writes.
		if t.ProviderDisableUnit == "group" && !rule.holdsGroup(t) {
			return false
		}
		if rule.Action == disableCredential {
			return true
		}
		for _, action := range rule.UpstreamErrorActions {
			if action == disableCredential {
				return true
			}
		}
		return false
	}
	return managedRule(rule)
}

func recoveryMember(rule *Rule, t Target, d *CredentialDisposition) bool {
	return disabledObject(t) && activeDisposition(d) && d.State == "disabled" && d.RuleID == rule.ID &&
		errorPolicyDisabled(d).Code == "" && rule.AutoRestore && rule.Action == disableCredential
}

func currentErrorSuspension(t Target, d *CredentialDisposition) bool {
	return disabledObject(t) && !t.identityConflict && objectType(t) != objectUnknown && errorPolicyDisabled(d).Code != "" &&
		(!t.IsProvider || providerBindingProblem(t) == nil)
}

func scopeMember(rule *Rule, t Target, d *CredentialDisposition, group string) bool {
	if !selectedIdentity(rule, t, group) || currentErrorSuspension(t, d) {
		return false
	}
	if !rule.acceptsSubject(t) && objectType(t) != objectUnknown && dynamicScope(rule.TargetScope) {
		return false
	}
	if rule.TargetScope == scopeAll && disabledObject(t) {
		return recoveryMember(rule, t, d)
	}
	return true
}

func (en *Engine) collectScopeLocked(ctx context.Context, draft *Rule, inventory func(context.Context) ([]Target, error), preview bool) (ruleDispatch, []scopeMatch, map[string]Target, error) {
	r := cloneRule(draft)
	original := en.rules[r.ID]
	loginGeneration := en.loginGeneration
	if captured, shared := ctx.Value(scheduledInventoryGeneration{}).(uint64); shared {
		loginGeneration = captured
	}
	ctx = en.inventoryContextLocked(ctx)
	coverage := &providerInventoryStatus{Verified: true}
	ctx = context.WithValue(ctx, providerInventoryStatusKey{}, coverage)
	if en.snapshotSlots == nil {
		en.snapshotSlots = make(chan struct{}, max(1, en.cfg.Workers))
	}
	en.mu.Unlock()
	targets, err := inventory(ctx)
	if err == nil && !coverage.Verified {
		// The SDK still knows file credentials when the separate provider
		// config directory is down. Its runtime provider rows are partial.
		targets = slices.Clone(targets)
		for i := range targets {
			if targets[i].IsProvider {
				t := &targets[i]
				t.Reasons = slices.Clone(t.Reasons)
				markProviderDirectoryUnavailable(t, coverage.Reason)
			}
		}
	}
	en.mu.Lock()
	if err == nil && r.expectedTargetObjectType != "" {
		if selectionErr := r.verifySelectedRuleTarget(targets); selectionErr != nil {
			return ruleDispatch{}, nil, nil, selectionErr
		}
	}
	var profiles []scopeProfile
	var reconciliation map[string]error
	if err == nil {
		err = en.observeMissingEntriesLocked(ctx, targets, coverage.Verified)
		if err == nil {
			reconciliation = en.reconcileCurrentTargetsLocked(ctx, targets, nil)
		}
	}
	owned := maps.Clone(en.managed)
	if err == nil {
		en.mu.Unlock()
		profiles = en.readScopeProfiles(ctx, r, targets, owned)
		en.mu.Lock()
		for i, target := range targets {
			if failure := reconciliation[target.AuthID]; failure != nil {
				profiles[i].err = failure
			}
		}
	}
	result := ruleDispatch{Candidates: map[string]int{objectOAuth: 0, objectProvider: 0}, Verified: err == nil}
	if ctx.Err() != nil {
		return result, nil, nil, ctx.Err()
	}
	if original != nil && (en.rules[r.ID] != original || original.Revision != r.Revision) {
		return result, nil, nil, fmt.Errorf("conflict: stale rule revision")
	}
	if stateErr := en.mutableLocked(); stateErr != nil {
		return result, nil, nil, stateErr
	}
	if err != nil {
		result.Reason = "CPA 对象目录尚未读到，范围待核实。"
		if preview {
			return result, nil, nil, nil
		}
		return result, nil, nil, fmt.Errorf("%w: %w", errScopeDirectoryUnavailable, err)
	}
	if !preview && !r.LegacyScope && !dynamicScope(r.TargetScope) && en.Capabilities().QualityPause {
		return result, nil, nil, unsupportedQuality()
	}
	seen := make(map[string]Target, len(targets))
	for _, t := range targets {
		if t.AuthID != "" {
			seen[t.AuthID] = t
		}
	}
	var providerDirectoryReason string
	if !coverage.Verified && scopeNeedsProviderDirectory(r, seen) {
		result.Verified = false
		result.Reason = "CPA 提供商配置目录未读到，AI 提供商范围待核实。"
		if coverage.Reason != "" {
			result.Reason = coverage.Reason
		}
		providerDirectoryReason = result.Reason
	}
	group := selectedGroup(r, targets)
	var matches []scopeMatch
	var encounters int
	if !dynamicScope(r.TargetScope) {
		for _, id := range dependencies(r.RunConfig) {
			t := seen[id]
			if failure := reconciliation[id]; failure != nil && (!t.IsProvider || providerBindingProblem(t) == nil) {
				return result, nil, seen, failure
			}
			if currentErrorSuspension(t, en.managed[id]) {
				result.Reason = "指定对象或其依赖已按上游错误策略暂停，退出后续运维检测。"
				return result, nil, seen, nil
			}
		}
	}
	if r.LegacyScope && !dynamicScope(r.TargetScope) && !contains(r.ObjectTypes, objectProvider) {
		for _, id := range dependencies(r.RunConfig) {
			if seen[id].IsProvider {
				result.Reason = "旧规则中的供应商绑定未获原工作台授权。"
				result.skip(seen[id], ScopeSkipDetail{Code: "provider_targets_excluded", Reason: result.Reason, NotExecuted: true})
				if !preview {
					return result, nil, seen, fmt.Errorf("blocked: legacy provider binding was not enabled")
				}
				return result, nil, seen, nil
			}
		}
	}
	for i, t := range targets {
		if ctx.Err() != nil {
			return result, nil, nil, ctx.Err()
		}
		d := en.managed[t.AuthID]
		held := groupGuard(en.managed, t)
		if currentErrorSuspension(t, held) {
			continue
		}
		if !scopeMember(r, t, d, group) {
			continue
		}
		encounters++
		kind := objectType(t)
		result.Candidates[kind]++
		if held != nil && groupTargetBlocked(en.managed, t) && !activeDisposition(d) {
			result.skip(t, ScopeSkipDetail{Code: "group_members_changed", Reason: "本组已有处置归属，当前路由的身份与停用快照无法对应。"})
			continue
		}
		cfg := r.RunConfig
		cfg.AuthID, cfg.Provider = t.AuthID, t.Provider
		m := scopeMatch{target: t, config: cfg, recovery: disabledObject(t) && canRecoverDisabled(r, d)}
		var detail *ScopeSkipDetail
		for _, id := range dependencies(m.config) {
			if hold := en.loginHolds[id]; hold != nil && (hold.State != "completed" || hold.generation > loginGeneration) {
				code, reason := hold.reason()
				detail = &ScopeSkipDetail{Code: code, Reason: reason, NotExecuted: true}
				break
			}
		}
		if detail == nil {
			detail = en.scopeAdmissionLocked(r, &m, t, seen, profiles[i], owned[t.AuthID], preview, providerDirectoryReason)
		}
		if detail != nil {
			if detail.Code == "bound_origin_changed" {
				t = r.boundHistoryTarget()
			} else if disabledObject(t) {
				detail.NotExecuted = true
			}
			result.skip(t, *detail)
			continue
		}
		if m.snapshot == nil || !m.snapshot.Disabled {
			m.missing = firstMissingModel(t, cfg.Models)
		}
		matches = append(matches, m)
		if m.recovery {
			result.RecoveryCount++
		}
	}
	if encounters == 0 && dynamicScope(r.TargetScope) && result.Verified {
		result.Reason = "当前目录中没有可纳入本轮的对象。下次执行时会重新匹配。"
	}
	if encounters == 0 && !dynamicScope(r.TargetScope) {
		missing := r.boundHistoryTarget()
		kind := missing.ObjectType
		detail := missingCredentialSkip()
		if kind == objectProvider {
			detail.Reason = "当前目录中找不到规则绑定的供应商路由。"
		} else if kind == objectUnknown {
			detail.Reason = "当前目录中找不到规则绑定的对象。"
		}
		if !result.Verified && kind != objectOAuth {
			detail = ScopeSkipDetail{Code: "provider_directory_unavailable", NotExecuted: true, Reason: result.Reason}
		}
		if r.TargetGroup != "" {
			if result.Verified {
				detail = ScopeSkipDetail{Code: "group_missing", Reason: "指定的提供商配置组不在当前可核实目录中。"}
			}
		}
		for _, t := range seen {
			if usesCredential(r.RunConfig, t.AuthID) && (t.QualityPaused || t.RetainedPause) {
				detail = ScopeSkipDetail{Code: "quality_paused", Reason: "规则依赖的对象仍受独立质量暂停保护。"}
			}
		}
		// A terminal group or subject is retired, with no repeated records.
		excluded := false
		for _, t := range targets {
			if selectedIdentity(r, t, group) && (currentErrorSuspension(t, en.managed[t.AuthID]) || currentErrorSuspension(t, groupGuard(en.managed, t))) {
				excluded = true
				break
			}
		}
		if excluded {
			result.Reason = "指定的提供商组已按上游错误策略暂停，退出后续运维检测。"
		} else {
			detail.ObjectType = kind
			seen[missing.AuthID] = missing
			result.skip(missing, detail)
		}
	}
	result.Count = len(matches)
	if preview {
		result.classifyPreflight()
	}
	return result, matches, seen, nil
}

func scopeNeedsProviderDirectory(rule *Rule, targets map[string]Target) bool {
	if rule.TargetGroup != "" {
		return true
	}
	if dynamicScope(rule.TargetScope) {
		return contains(ruleTypes(rule), objectProvider)
	}
	for _, id := range dependencies(rule.RunConfig) {
		if t, ok := targets[id]; ok {
			if t.IsProvider {
				return true
			}
			continue
		}
		if couldBeBoundProvider(rule, id) {
			return true
		}
	}
	return false
}

// A missing row can still belong to an unreadable provider directory. This
// describes incomplete coverage only and never establishes identity or access.
func couldBeBoundProvider(rule *Rule, id string) bool {
	switch id {
	case rule.AuthID:
		if rule.TargetObjectType == objectOAuth || rule.TargetObjectType == objectProvider {
			return rule.TargetObjectType == objectProvider
		}
		return contains(ruleTypes(rule), objectProvider) && (len(ruleTypes(rule)) == 1 || providerFamily(id, rule.Provider) != "")
	case rule.ControlAuthID:
		return providerFamily(id, rule.ControlProvider) != ""
	case rule.Judge.AuthID:
		return rule.Judge.Mode == "llm" && providerFamily(id, rule.Judge.Provider) != ""
	}
	return false
}

func (receipt *ruleDispatch) classifyPreflight() {
	skips := receipt.SkipDetails[:0]
	for _, detail := range receipt.SkipDetails {
		if !detail.NotExecuted && preflightStatus(detail.Code) != "" {
			receipt.PreflightDetails = append(receipt.PreflightDetails, detail)
			receipt.Count++
			receipt.Skipped--
		} else {
			skips = append(skips, detail)
		}
	}
	receipt.SkipDetails = skips
}

func (en *Engine) scopeAdmissionLocked(r *Rule, m *scopeMatch, t Target, targets map[string]Target, p scopeProfile, old *CredentialDisposition, preview bool, directoryReason string) *ScopeSkipDetail {
	d := en.managed[t.AuthID]
	problem := func(code, reason string) *ScopeSkipDetail { return &ScopeSkipDetail{Code: code, Reason: reason} }
	if t.QualityPaused || t.RetainedPause || en.localPause(m.config) != "" {
		return problem("quality_paused", "对象仍受独立质量暂停保护，禁止检测。")
	}
	if t.identityConflict {
		return problem("identity_conflict", "CPA 返回的对象或模型目录标识互相冲突。")
	}
	if r.boundOriginChanged(t.AuthID, objectType(t)) {
		return &boundOriginRejection().detail
	}
	if !r.acceptsSubject(t) {
		if objectType(t) == objectUnknown {
			return problem("unverified_inventory_entry", "CPA 未能核实这项目录记录的对象来源。")
		}
		return problem("object_type_excluded", "指定对象的类型未在本规则中勾选。")
	}
	if r.AuthID == t.AuthID && r.Provider != t.Provider {
		return problem("provider_mismatch", "当前对象提供方与规则保存的绑定不一致。")
	}
	if r.AuthID == t.AuthID && r.TargetAuthIndex != "" && r.TargetAuthIndex != t.AuthIndex {
		return problem("identity_conflict", "指定对象的提供方或路由索引已经变化。")
	}
	if e := providerRuleProblem(r, t, r.Models, true); e != nil {
		return &e.detail
	}
	if activeDisposition(d) && d.State != "disabled" {
		return problem("handling_blocked", "对象的上次处置尚待核对。")
	}
	if d != old {
		return problem("ownership_changed", "目录核对期间处置归属已变化。")
	}
	if disabledObject(t) && !canRecoverDisabled(r, d) && (r.LegacyScope || r.TargetScope != scopeAllCredentials) {
		if !activeDisposition(d) {
			return problem("manual_disabled", "对象由管理员停用，本规则范围不允许探测该对象。")
		}
		detail := recoveryDenied(r, d)
		return &detail
	}
	if p.err != nil && activeDisposition(d) && !disabledObject(t) {
		var e *scopeRejection
		if errors.As(p.err, &e) {
			return cloneValue(&e.detail)
		}
		return problem("snapshot_unavailable", "无法读取 CPA 当前凭证状态，旧处置记录已保留。")
	}
	if needsScopeSnapshot(r, t) {
		// A verified missing model on an enabled object is a catalog check,
		// independent of support for sending the requested upstream payload.
		missing := firstMissingModel(t, r.Models)
		if t.IsProvider && disabledObject(t) && missing != "" {
			return &ScopeSkipDetail{Code: "provider_probe_unavailable", Model: missing, Reason: "停用路由的配置映射中没有所选模型，无法定向探测。"}
		}
		if reason := providerPlanProbeReason(t, r.Models, r.Parameters); reason != "" && (disabledObject(t) || missing == "") {
			return problem("provider_probe_unavailable", reason)
		}
		if p.err != nil {
			var e *scopeRejection
			if errors.As(p.err, &e) {
				return cloneValue(&e.detail)
			}
			return problem("snapshot_unavailable", "无法从 CPA 核对本次定向探测需要的快照。")
		}
		projected, snapshot, err := en.admitSnapshotLocked(r, t, p.snapshot, preview)
		if err != nil {
			var e *scopeRejection
			if errors.As(err, &e) {
				return cloneValue(&e.detail)
			}
			return problem("snapshot_unavailable", "对象快照无法核实。")
		}
		t, m.snapshot = projected, snapshot
	}
	if !ruleSubjectEnabled(t) {
		detail := runtimeSkip(t)
		return &detail
	}
	for _, id := range dependencies(m.config) {
		if id == t.AuthID {
			continue
		}
		role, models, desired := "对照", m.config.Models, m.config.ControlProvider
		if id == m.config.Judge.AuthID {
			role, models, desired = "判题", []string{m.config.Judge.Model}, m.config.Judge.Provider
		}
		dep, ok := targets[id]
		if !ok {
			if directoryReason != "" && couldBeBoundProvider(r, id) {
				return problem("provider_directory_unavailable", role+"对象的路由无法核实。"+directoryReason)
			}
			return problem("dependency_unavailable", "规则指定的"+role+"对象不在当前目录中。")
		}
		if dep.QualityPaused || dep.RetainedPause {
			return problem("quality_paused", "规则所需凭证仍受质量暂停保护，禁止检测。")
		}
		if dep.Provider != desired {
			return problem("dependency_unavailable", "判题或对照对象的提供方已变化。")
		}
		if e := providerRuleProblem(r, dep, models, false); e != nil {
			detail := e.detail
			detail.NotExecuted = true
			detail.Reason = role + "对象核对失败。" + detail.Reason
			return &detail
		}
		if activeDisposition(en.managed[id]) || groupTargetBlocked(en.managed, dep) || !dep.EffectiveAvailable || dep.ManualDisabled {
			return problem("dependency_unavailable", "规则指定的"+role+"对象不可用。")
		}
		if dep.ComparisonIdentity == "" || dep.ComparisonIdentity == t.ComparisonIdentity || t.ComparisonIdentity == "" {
			return problem("dependency_identity", "无法确认判题或对照对象独立于被测对象。")
		}
		for _, model := range models {
			if !contains(dep.Models, model) {
				return problem("dependency_unavailable", "判题或对照对象未注册所选模型。")
			}
		}
		if id == m.config.Judge.AuthID {
			m.judge = &dep
		} else {
			m.control = &dep
		}
	}
	return nil
}
