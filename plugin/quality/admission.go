package quality

import (
	"context"
	"errors"
	"fmt"
	"os"

	bolt "go.etcd.io/bbolt"
)

type runReceipt struct{ ID, Hash string }

// The receipt is committed with the run. A surviving receipt also prevents
// replay after history retention or an administrator removes the run.
func (en *Engine) submittedRunLocked(key, requestHash string) (*Run, error) {
	if key == "" {
		return nil, nil
	}
	var receipt runReceipt
	if err := en.store.Get("idempotency", key, &receipt); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("unavailable: read submission receipt: %w", err)
	}
	if requestHash != "" && requestHash != receipt.Hash {
		return nil, fmt.Errorf("conflict: idempotency key reused with different input")
	}
	if r := en.runs[receipt.ID]; r != nil {
		return r, nil
	}
	return nil, fmt.Errorf("gone: idempotent run was removed")
}

func (en *Engine) runByReceipt(key string) (*Run, error) {
	if len(key) > 128 {
		return nil, fmt.Errorf("invalid_request: idempotency key too long")
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	r, err := en.submittedRunLocked(key, "")
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("command_not_accepted: manual test has not been accepted")
	}
	return runForDisplay(r), nil
}

func (en *Engine) validateManual(c *RunConfig) error {
	if c.ExpectedObjectType != "" && c.ExpectedObjectType != objectOAuth && c.ExpectedObjectType != objectProvider || len(c.ExpectedAuthIndex) > 512 {
		return fmt.Errorf("invalid_request: invalid manual target binding")
	}
	if len(c.Models) > maxManualModels {
		return fmt.Errorf("models out of bounds")
	}
	return en.validateConfig(c, false)
}

func (c RunConfig) verifyManualSubject(targets []Target, kind, index string) error {
	if kind == "" && index == "" {
		return nil
	}
	found := false
	for _, t := range targets {
		if t.AuthID != c.AuthID {
			continue
		}
		if found || t.identityConflict || t.Provider != c.Provider || kind != "" && objectType(t) != kind || index != "" && t.AuthIndex != index {
			found = false
			break
		}
		found = true
	}
	if !found {
		return fmt.Errorf("blocked: 原绑定的测试对象类型、提供方或索引已变化，请重新选择测试对象。")
	}
	return nil
}

func (en *Engine) validateConfig(c *RunConfig, scoped bool) error {
	if scoped {
		if c.AuthID != "" || c.Provider != "" || c.ControlAuthID != "" || c.ControlProvider != "" || c.Judge.Mode == "llm" || c.Judge.AuthID != "" {
			return fmt.Errorf("invalid_request: all-enabled observation cannot bind a subject, judge or control credential")
		}
	} else if c.AuthID == "" || c.Provider == "" || len(c.AuthID) > 512 || len(c.Provider) > 128 {
		return fmt.Errorf("auth_id and provider are required")
	}
	if len(c.Models) < 1 || len(c.Models) > en.cfg.MaxModels {
		return fmt.Errorf("models out of bounds")
	}
	seen := map[string]bool{}
	for _, m := range c.Models {
		if m == "" || len(m) > 256 || seen[m] {
			return fmt.Errorf("invalid or duplicate model")
		}
		seen[m] = true
	}
	if c.Samples == 0 {
		c.Samples = 1
	}
	if c.Samples < 1 || c.Samples > 8 || c.Samples*len(c.Models) > en.cfg.MaxSamples {
		return fmt.Errorf("samples must be 1..8 and within total budget")
	}
	if c.Parameters.MaxTokens == 0 {
		c.Parameters.MaxTokens = min(1024, en.cfg.MaxOutputTokens)
	}
	if c.Parameters.MaxTokens < 16 || c.Parameters.MaxTokens > en.cfg.MaxOutputTokens {
		return fmt.Errorf("max_tokens out of bounds")
	}
	if c.Parameters.Temperature != nil && (*c.Parameters.Temperature < 0 || *c.Parameters.Temperature > 2) {
		return fmt.Errorf("temperature out of bounds")
	}
	switch c.Parameters.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "auto":
	default:
		return fmt.Errorf("invalid reasoning_effort")
	}
	if c.Judge.Mode == "" {
		c.Judge.Mode = "deterministic"
	}
	c.Judge.TemplateVersion = "1"
	switch c.Judge.Mode {
	case "deterministic", "manual":
	case "llm":
		if c.Judge.AuthID == "" || c.Judge.AuthID == c.AuthID || c.Judge.Model == "" || c.Judge.Provider == "" {
			return fmt.Errorf("judge requires an independent credential and model")
		}
	default:
		return fmt.Errorf("invalid judge mode")
	}
	if c.ControlAuthID != "" && (c.ControlAuthID == c.AuthID || c.ControlAuthID == c.Judge.AuthID || c.ControlProvider == "") {
		return fmt.Errorf("control requires a distinct credential and provider")
	}
	if c.ControlAuthID != "" && c.Samples*len(c.Models)*2 > en.cfg.MaxSamples {
		return fmt.Errorf("control and subject exceed total sample limit")
	}
	if len(c.IdempotencyKey) > 128 {
		return fmt.Errorf("idempotency key too long")
	}
	return nil
}
func (en *Engine) Submit(ctx context.Context, c RunConfig) (*Run, error) {
	if c.Judge.Mode == "" {
		c.Judge.Mode = "manual"
	}
	return en.submit(ctx, c, "", 0)
}
func (en *Engine) submit(ctx context.Context, c RunConfig, ruleID string, scheduledRevision uint64) (*Run, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	return en.submitLocked(ctx, c, ruleID, scheduledRevision, en.host.Targets, nil, nil)
}

func (en *Engine) submitLocked(ctx context.Context, c RunConfig, ruleID string, scheduledRevision uint64, inventory func(context.Context) ([]Target, error), occurrence *scheduledRule, command *ruleCommandRecord) (*Run, error) {
	if en.closed || en.fault != nil {
		return nil, fmt.Errorf("unavailable: plugin is stopping or persistence failed")
	}
	var rule *Rule
	if ruleID != "" {
		rule = en.rules[ruleID]
		if rule == nil {
			return nil, fmt.Errorf("rule not found")
		}
		if en.unsupportedRule(rule) {
			return nil, unsupportedQuality()
		}
		if dynamicScope(rule.TargetScope) {
			return nil, fmt.Errorf("invalid_request: dynamic scope requires batch admission")
		}
		if scheduledRevision != 0 && (rule.Revision != scheduledRevision || !en.timerAllowed(rule)) {
			return nil, fmt.Errorf("blocked: scheduled rule changed or scheduling disabled")
		}
		if !rule.Enabled || ruleStatus(rule) != "detecting" {
			return nil, fmt.Errorf("blocked: rule paused")
		}
		c = rule.RunConfig
		c.IdempotencyKey = ""
		for _, r := range en.runs {
			if r.Config.RuleID == ruleID && (r.Status == "queued" || r.Status == "running" || en.active[r.ID] != nil) {
				return nil, fmt.Errorf("conflict: rule already running")
			}
		}
	}
	c = cloneConfig(c)
	if rule == nil && c.Judge.Mode == "manual" && c.Parameters.MaxTokens == 0 {
		c.Parameters.MaxTokens = min(2048, en.cfg.MaxOutputTokens)
	}
	var validationErr error
	if rule == nil {
		validationErr = en.validateManual(&c)
	} else {
		validationErr = en.validateConfig(&c, false)
	}
	if validationErr != nil {
		return nil, validationErr
	}
	requestHash := fingerprint(c)
	prior, err := en.submittedRunLocked(c.IdempotencyKey, requestHash)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		return cloneRun(prior), nil
	}
	if err := en.storageAdmission(); err != nil {
		return nil, err
	}
	if len(en.queue) >= cap(en.queue) {
		en.compactQueueLocked()
	}
	queueFull := len(en.queue) >= cap(en.queue)
	if queueFull && (rule == nil || en.Capabilities().QualityPause) {
		return nil, fmt.Errorf("queue_full: retry later")
	}
	fence := en.admissionFenceLocked(rule)
	ctx = en.inventoryContextLocked(ctx)
	targets, e := en.readAdmissionTargetsLocked(ctx, inventory, c, rule)
	if e != nil {
		return nil, e
	}
	if rule == nil {
		if err := c.verifyManualSubject(targets, c.ExpectedObjectType, c.ExpectedAuthIndex); err != nil {
			return nil, err
		}
	}
	if err := currentTargetError(en.reconcileDirectoryLocked(ctx, targets, dependencies(c)), dependencies(c)); err != nil {
		return nil, err
	}
	if err := en.runDispositionLocked(c, rule == nil); err != nil {
		return nil, err
	}
	var manualAccount string
	if d := en.managed[c.AuthID]; rule == nil && d != nil && d.State == "externally_enabled" {
		manualAccount, err = admissionReadLocked(en, ctx, c, rule, func() (string, error) { return en.currentManualAccount(ctx, c, targets) })
		if err != nil {
			return nil, err
		}
	}
	subject := Target{AuthID: c.AuthID, Provider: c.Provider}
	subjectIndex := -1
	for i, t := range targets {
		if t.AuthID == c.AuthID {
			subject, subjectIndex = t, i
			break
		}
	}
	var managed *CredentialSnapshot
	plan := rule
	reject := func(cause error, detail ScopeSkipDetail) (*Run, error) {
		if rule == nil || en.Capabilities().QualityPause {
			return nil, cause
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := en.mutableLocked(); err != nil {
			return nil, err
		}
		q, err := freezeDispatchQuestion(&c)
		if err != nil {
			return nil, err
		}
		run := newScopedRun(plan, subject, c, q, ID(), scheduledRevision != 0, en.now(), nil)
		markDispatchSkip(run, &detail, run.CreatedAt)
		return en.recordFixedAdmissionLocked(rule, run)
	}
	if d := en.managed[c.AuthID]; d != nil && d.RuleID == ruleID && subjectIndex < 0 {
		en.noteRoundEndLocked(ruleID)
	}
	if rule != nil && subject.identityConflict {
		return reject(fmt.Errorf("blocked: credential identity conflict"), ScopeSkipDetail{Code: "identity_conflict", Reason: "CPA 返回的凭证或模型目录标识互相冲突。"})
	}
	if rule != nil && activeDisposition(en.managed[c.AuthID]) && en.managed[c.AuthID].State != "disabled" {
		return reject(fmt.Errorf("blocked: unresolved credential handling"), ScopeSkipDetail{Code: "handling_blocked", Reason: "上次凭证处置尚待核对。"})
	}
	if rule != nil {
		if problem := en.checkRuleProvidersLocked(rule, c, targets); problem != nil {
			if problem.detail.Code == "provider_targets_excluded" {
				return nil, problem
			}
			return reject(problem, problem.detail)
		}
	} else if isProviderTarget(subject) {
		if problem := providerRouteProblem(subject, c.Models); problem != nil {
			return nil, fmt.Errorf("blocked: %s", problem.detail.Reason)
		}
	}
	if managedRule(rule) && subjectIndex >= 0 && !isProviderTarget(subject) && !en.Capabilities().ManagementSession {
		return reject(fmt.Errorf("management_session_required: background management connection is not ready"), sessionSkip())
	}
	targets, managed, e = en.managedTargetsLocked(ctx, rule, targets, false)
	if e != nil {
		var rejected *scopeRejection
		if errors.As(e, &rejected) {
			return reject(e, rejected.detail)
		}
		switch {
		case subjectIndex < 0:
			return reject(e, missingCredentialSkip())
		case subject.QualityPaused || subject.RetainedPause || en.localPause(c) != "":
			return reject(e, ScopeSkipDetail{Code: "quality_paused", Reason: "凭证仍受质量暂停保护，禁止检测。"})
		default:
			return reject(e, ScopeSkipDetail{Code: "snapshot_unavailable", Reason: "无法从 CPA 核对本轮凭证快照。"})
		}
	}
	if paused := pausedTarget(c, targets); paused != "" {
		plan = cloneRule(rule)
		if err := en.haltCredentialLocked(paused, "", "quality_paused"); err != nil {
			return nil, err
		}
	}
	if err := en.checkTargetsForRunLocked(c, targets, rule != nil && !en.Capabilities().QualityPause); err != nil {
		switch {
		case en.localPause(c) != "" || pausedTarget(c, targets) != "":
			return reject(err, ScopeSkipDetail{Code: "quality_paused", Reason: "规则所需凭证仍受质量暂停保护，禁止检测。"})
		case subjectIndex < 0:
			return reject(err, missingCredentialSkip())
		case subject.ManualDisabled || subject.RuntimeStatus == "disabled":
			return reject(err, ScopeSkipDetail{Code: "manual_disabled", Reason: "凭证由管理员停用，当前规则无权复检。"})
		case !subject.EffectiveAvailable:
			return reject(err, runtimeSkip(subject))
		default:
			return reject(err, ScopeSkipDetail{Code: "dependency_unavailable", Reason: "规则所需的对照或判题凭证不可用。"})
		}
	}
	target := &targets[subjectIndex]
	if target.Provider != c.Provider {
		return reject(fmt.Errorf("provider mismatch"), ScopeSkipDetail{Code: "provider_mismatch", Reason: "当前凭证提供方与规则保存的提供方不一致。"})
	}
	if target.ManualDisabled {
		return reject(fmt.Errorf("blocked: operator disabled"), ScopeSkipDetail{Code: "manual_disabled", Reason: "凭证由管理员停用，当前规则无权复检。"})
	}
	for _, m := range c.Models {
		if (rule == nil || en.Capabilities().QualityPause) && !contains(target.Models, m) {
			return reject(fmt.Errorf("model not available for target: %s", m), modelCatalogSkip(*target, m, "凭证没有注册该选定模型。"))
		}
	}
	var judge *Target
	if c.Judge.Mode == "llm" {
		for i := range targets {
			if targets[i].AuthID == c.Judge.AuthID {
				judge = &targets[i]
				break
			}
		}
		if judge == nil || judge.Provider != c.Judge.Provider || !judge.EffectiveAvailable || !contains(judge.Models, c.Judge.Model) {
			return reject(fmt.Errorf("blocked: judge target unavailable"), ScopeSkipDetail{Code: "judge_unavailable", Reason: "判题凭证无法运行规则选定的判题模型。"})
		}
		if target.ComparisonIdentity == "" || judge.ComparisonIdentity == "" || target.ComparisonIdentity == judge.ComparisonIdentity {
			return reject(fmt.Errorf("blocked: independent judge identity cannot be established"), ScopeSkipDetail{Code: "judge_identity", Reason: "无法核实判题凭证与被测凭证相互独立。"})
		}
	}
	var control *Target
	if c.ControlAuthID != "" {
		for i := range targets {
			if targets[i].AuthID == c.ControlAuthID {
				control = &targets[i]
				break
			}
		}
		if control == nil || control.Provider != c.ControlProvider || !control.EffectiveAvailable {
			return reject(fmt.Errorf("blocked: control target unavailable"), ScopeSkipDetail{Code: "control_unavailable", Reason: "规则指定的对照凭证不可用。"})
		}
		if target.ComparisonIdentity == "" || control.ComparisonIdentity == "" || target.ComparisonIdentity == control.ComparisonIdentity {
			return reject(fmt.Errorf("blocked: independent control identity cannot be established"), ScopeSkipDetail{Code: "control_identity", Reason: "无法核实对照凭证与被测凭证相互独立。"})
		}
		for _, m := range c.Models {
			if !contains(control.Models, m) {
				return reject(fmt.Errorf("control model unavailable"), ScopeSkipDetail{Code: "control_model_unavailable", Model: m, Reason: "对照凭证没有注册该选定模型。"})
			}
		}
	}
	if err := en.checkAdmissionFenceLocked(ctx, fence, dependencies(c)); err != nil {
		return nil, err
	}
	// A concurrent submission may have committed the same key, filled a
	// queue slot or admitted this rule while inventory was being read.
	if c.IdempotencyKey != "" {
		prior, err := en.submittedRunLocked(c.IdempotencyKey, requestHash)
		if err != nil || prior != nil {
			return cloneRun(prior), err
		}
	}
	if rule != nil && en.hasCurrentRoundLocked(rule) {
		return nil, fmt.Errorf("conflict: rule already running")
	}
	if len(en.queue) >= cap(en.queue) {
		en.compactQueueLocked()
	}
	queueFull = len(en.queue) >= cap(en.queue)
	if err := en.storageAdmission(); err != nil {
		return nil, err
	}
	if queueFull {
		return reject(fmt.Errorf("queue_full: retry later"), queueSkip())
	}
	q, e := freezeDispatchQuestion(&c)
	if e != nil {
		return nil, e
	}
	now := en.now()
	r := &Run{ID: ID(), Status: "queued", Source: "manual_test", CreatedAt: now, ActionStatus: "not_applicable", Samples: []Sample{}, Config: Frozen{RunConfig: c, Question: q, IdentityVersion: target.IdentityVersion, PayloadConfigVersion: target.PayloadConfigVersion}}
	r.Config.ManualAccountFingerprint = manualAccount
	if rule != nil {
		r.Source = "scheduled_probe"
		r.Config.RuleID = rule.ID
		r.Config.RuleRevision = rule.Revision
		r.Config.Scheduled = scheduledRevision != 0
		r.ActionStatus = "observe_only"
		r.Config.ManagedProbe = managed
		r.Config.UpstreamErrorActions = rule.UpstreamErrorActions
		if isProviderTarget(*target) {
			r.Config.ProviderObservation = true
			r.Config.UpstreamErrorActions = nil
		}
		for _, dependency := range []*Target{control, judge} {
			if dependency != nil && isProviderTarget(*dependency) {
				r.Config.ProviderDependencies = append(r.Config.ProviderDependencies, dependency.AuthID)
			}
		}
	}
	r.CredentialEmail = target.Email
	if rule == nil {
		// Freeze the admitted kind even when an older client only sent the ID.
		r.Config.ExpectedObjectType = objectType(*target)
		r.Config.SubjectIndex = target.AuthIndex
	}
	if rule == nil && isProviderTarget(*target) {
		r.Config.ProviderName = target.ProviderName
		if r.Config.ProviderName == "" {
			r.Config.ProviderName = target.Provider
		}
	}
	if judge != nil {
		r.Config.JudgeIdentity = judge.IdentityVersion
	}
	r.Config.ComparisonIdentity = target.ComparisonIdentity
	if control != nil {
		r.Config.ControlIdentity = control.IdentityVersion
		r.Config.BatchID = ID()
	}
	r.Config.Fingerprint = fingerprint(r.Config)
	// Snapshot verification can outlive the request after the directory read.
	// Check again at the durable admission boundary before workers can own it.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rule != nil && !en.Capabilities().QualityPause {
		if managed == nil || !managed.Disabled {
			if model := firstMissingModel(*target, c.Models); model != "" {
				queueCatalogRun(r, model)
			}
		}
		return en.recordFixedAdmissionLocked(rule, r)
	}
	if occurrence != nil || command != nil {
		c := changes()
		c.ruleCommand = completedDispatch(command, ruleDispatch{RunID: r.ID, Count: 1})
		c.runs[r.ID] = r
		en.consumeOccurrence(c, cloneRule(rule), occurrence)
		if err := en.commitLocked(c); err != nil {
			return nil, err
		}
		en.queue <- r.ID
		en.notifyWorkersLocked()
		return cloneRun(r), nil
	}
	if e = en.store.Write(func(tx *bolt.Tx) error {
		if e := put(tx, "runs", r.ID, r); e != nil {
			return e
		}
		if c.IdempotencyKey != "" {
			return put(tx, "idempotency", c.IdempotencyKey, runReceipt{r.ID, requestHash})
		}
		return nil
	}); e != nil {
		return nil, e
	}
	en.runs[r.ID] = r
	en.queue <- r.ID
	en.notifyWorkersLocked()
	return cloneRun(r), nil
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
