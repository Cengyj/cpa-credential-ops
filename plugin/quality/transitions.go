package quality

import (
	"context"
	"fmt"
	"sort"
	"time"

	log "github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
)

// changeSet publishes a state transition only after all of its records are durable.
type changeSet struct {
	hostInput    *Config
	settings     *Config
	disposition  *CredentialDisposition
	dispositions []*CredentialDisposition
	timers       map[string]bool
	rules        map[string]*Rule
	runs         map[string]*Run
	audits       []Audit
	intent       *Action
	loginHold    *credentialLoginHold
}

func changes() *changeSet { return &changeSet{rules: map[string]*Rule{}, runs: map[string]*Run{}} }
func (en *Engine) commitLocked(c *changeSet) error {
	en.cancelRevokedSchedulesLocked(c)
	if len(c.rules) == 0 && len(c.runs) == 0 && len(c.audits) == 0 && c.settings == nil && c.timers == nil && c.intent == nil && c.loginHold == nil && c.disposition == nil && len(c.dispositions) == 0 {
		return nil
	}
	if c.disposition != nil {
		c.dispositions = append(c.dispositions, c.disposition)
	}
	err := en.store.Write(func(tx *bolt.Tx) error {
		if c.hostInput != nil {
			if err := putQualityHostInput(tx, *c.hostInput); err != nil {
				return err
			}
		}
		if c.settings != nil {
			if err := put(tx, "meta", "settings", c.settings); err != nil {
				return err
			}
		}
		for _, d := range c.dispositions {
			if err := put(tx, "meta", dispositionPrefix+d.AuthID, d); err != nil {
				return err
			}
		}
		if c.timers != nil {
			if err := put(tx, "meta", ruleTimersKey, c.timers); err != nil {
				return err
			}
		}
		if c.loginHold != nil {
			if err := put(tx, "meta", loginHoldPrefix+c.loginHold.AuthID, c.loginHold); err != nil {
				return err
			}
		}
		for id, r := range c.rules {
			if err := put(tx, "rules", id, r); err != nil {
				return err
			}
		}
		for id, r := range c.runs {
			if err := put(tx, "runs", id, r); err != nil {
				return err
			}
		}
		for _, a := range c.audits {
			if err := put(tx, "audit", a.ID, a); err != nil {
				return err
			}
		}
		if c.intent != nil {
			return put(tx, "meta", "intent:"+c.intent.ActionID, c.intent)
		}
		return nil
	})
	if err != nil {
		en.fault = fmt.Errorf("persist quality transition: %w", err)
		for _, cancel := range en.active {
			cancel()
		}
		return en.fault
	}
	if en.schedulesRevokedLocked(c) {
		en.scheduleGeneration++
	}
	if c.timers != nil {
		en.timers = c.timers
	}
	if hold := c.loginHold; hold != nil {
		if en.loginHolds == nil {
			en.loginHolds = make(map[string]*credentialLoginHold)
		}
		en.loginGeneration++
		hold.generation = en.loginGeneration
		en.loginHolds[hold.AuthID] = hold
	}
	for _, d := range c.dispositions {
		if en.managed == nil {
			en.managed = map[string]*CredentialDisposition{}
		}
		en.managed[d.AuthID] = d
	}
	for id, r := range c.rules {
		if old := en.rules[id]; old != nil {
			*old = *r
		} else {
			en.rules[id] = r
		}
	}
	for id, r := range c.runs {
		if old := en.runs[id]; old != nil {
			if old.Status == "queued" && r.CompletedAt != nil {
				en.finishRuleChildLocked(r.Config)
			}
			*old = *r
		} else {
			en.runs[id] = r
		}
		if r.CancelRequested {
			if cancel := en.active[id]; cancel != nil {
				cancel()
			}
		}
	}
	if len(c.runs) != 0 {
		en.notifyWorkersLocked()
	}
	return nil
}

func (en *Engine) schedulesRevokedLocked(c *changeSet) bool {
	if c.settings == nil || c.settings.SchedulingEnabled {
		return false
	}
	if en.cfg.SchedulingEnabled {
		return true
	}
	if c.timers != nil {
		for id, allowed := range en.timers {
			if allowed && !c.timers[id] {
				return true
			}
		}
	}
	return false
}

// Revocation and task cancellation share the same durable transaction. A later
// restart of scheduling cannot revive work admitted under the old permission.
func (en *Engine) cancelRevokedSchedulesLocked(c *changeSet) {
	if c.settings == nil && c.timers == nil {
		return
	}
	global, timers := en.cfg.SchedulingEnabled, en.timers
	if c.settings != nil {
		global = c.settings.SchedulingEnabled
	}
	if c.timers != nil {
		timers = c.timers
	}
	for id, old := range en.runs {
		if next := c.runs[id]; next != nil {
			old = next
		}
		if !old.Config.Scheduled || old.Config.RuleID == "" || old.CancelRequested || old.Status != "queued" && old.Status != "running" {
			continue
		}
		rule := en.rules[old.Config.RuleID]
		if next := c.rules[old.Config.RuleID]; next != nil {
			rule = next
		}
		if rule != nil && rule.SchedulingEnabled && (global || timers[rule.ID]) {
			continue
		}
		next := en.cancelledRunLocked(old, "blocked")
		next.BlockReason = "scheduling_disabled"
		c.runs[id] = next
	}
}

func (en *Engine) mutableLocked() error {
	if en.closed || en.fault != nil {
		return fmt.Errorf("unavailable: plugin is stopping or persistence failed")
	}
	return nil
}
func (en *Engine) audit(kind, ruleID, runID, actionID, detail string) Audit {
	return Audit{ID: ID(), Time: en.now(), Kind: kind, RuleID: ruleID, RunID: runID, ActionID: actionID, Detail: detail}
}
func ruleStatus(r *Rule) string {
	if r.ActionID != "" || r.BlockedAuthID != "" {
		return "awaiting_release"
	}
	if r.Enabled {
		return "detecting"
	}
	return "paused"
}
func resetRule(r *Rule) {
	r.FailCount = 0
	r.EvidenceSignature = ""
	r.NextRun = time.Time{}
	r.PendingSchedule = nil
}
func dependencies(c RunConfig) []string {
	ids := []string{c.AuthID}
	if c.ControlAuthID != "" {
		ids = append(ids, c.ControlAuthID)
	}
	if c.Judge.Mode == "llm" && c.Judge.AuthID != "" {
		ids = append(ids, c.Judge.AuthID)
	}
	sort.Strings(ids)
	unique := ids[:0]
	for _, id := range ids {
		if id != "" && (len(unique) == 0 || unique[len(unique)-1] != id) {
			unique = append(unique, id)
		}
	}
	return unique
}
func usesCredential(c RunConfig, id string) bool { return contains(dependencies(c), id) }
func pausedTarget(c RunConfig, targets []Target) string {
	for _, id := range dependencies(c) {
		for _, t := range targets {
			if t.AuthID == id && t.QualityPaused {
				return id
			}
		}
	}
	return ""
}
func (en *Engine) localPause(c RunConfig) string {
	for _, r := range en.rules {
		if r.ActionID != "" && usesCredential(c, r.AuthID) {
			return r.AuthID
		}
		if !en.Capabilities().QualityPause && r.BlockedAuthID != "" && usesCredential(c, r.BlockedAuthID) {
			return r.BlockedAuthID
		}
	}
	return ""
}
func (en *Engine) checkTargetsLocked(c RunConfig, targets []Target) error {
	return en.checkTargetsForRunLocked(c, targets, false)
}
func (en *Engine) checkTargetsForRunLocked(c RunConfig, targets []Target, ruleProbe bool) error {
	if hold := en.loginWaitLocked(c); hold != nil {
		code, reason := hold.reason()
		return rejectScope("blocked: "+reason, code, reason)
	}
	return en.checkTargetPoliciesLocked(c, targets, ruleProbe)
}
func (en *Engine) checkTargetPoliciesLocked(c RunConfig, targets []Target, ruleProbe bool) error {
	if en.fault != nil {
		return fmt.Errorf("unavailable: persistent state requires operator attention")
	}
	if id := en.localPause(c); id != "" {
		return fmt.Errorf("blocked: credential quality pause awaits manual release")
	}
	for _, id := range dependencies(c) {
		var found *Target
		for i := range targets {
			if targets[i].AuthID == id {
				found = &targets[i]
				break
			}
		}
		if found == nil {
			return fmt.Errorf("blocked: credential missing")
		}
		if found.QualityPaused || found.RetainedPause {
			return fmt.Errorf("blocked: credential quality pause awaits manual release")
		}
		if groupTargetBlocked(en.managed, *found) {
			return fmt.Errorf("blocked: provider group has unresolved credential handling")
		}
		if ruleProbe && id == c.AuthID {
			if !ruleSubjectEnabled(*found) {
				return fmt.Errorf("blocked: credential unavailable")
			}
		} else if found.ManualDisabled || !found.EffectiveAvailable {
			return fmt.Errorf("blocked: credential unavailable")
		}
	}
	return nil
}
func (en *Engine) haltChangesLocked(c *changeSet, authID, exceptRun, reason string) {
	for id, old := range en.rules {
		if !usesCredential(old.RunConfig, authID) {
			continue
		}
		r := c.rules[id]
		if r == nil {
			r = cloneRule(old)
		}
		if !r.Enabled && r.NextRun.IsZero() && r.BlockedAuthID != "" && r.Status == "awaiting_release" && r.FailCount == 0 && r.EvidenceSignature == "" {
			continue
		}
		r.Revision++
		r.Enabled = false
		if r.BlockedAuthID == "" {
			r.BlockedAuthID = authID
		}
		resetRule(r)
		r.Status = "awaiting_release"
		c.rules[id] = r
	}
	for id, old := range en.runs {
		if id == exceptRun || !usesCredential(old.Config.RunConfig, authID) || (old.Status != "queued" && old.Status != "running") {
			continue
		}
		if old.CancelRequested && old.BlockReason == reason {
			continue
		}
		r := en.cancelledRunLocked(old, "blocked")
		r.BlockReason = reason
		r.ActionStatus = "awaiting_release"
		c.runs[id] = r
	}
}
func (en *Engine) haltCredentialLocked(authID, exceptRun, reason string) error {
	c := changes()
	en.haltChangesLocked(c, authID, exceptRun, reason)
	return en.commitLocked(c)
}
func (en *Engine) cancelRuleChangesLocked(c *changeSet, id, reason string) {
	for runID, old := range en.runs {
		if old.Config.RuleID != id || (old.Status != "queued" && old.Status != "running") {
			continue
		}
		r := en.cancelledRunLocked(old, "blocked")
		r.BlockReason = reason
		c.runs[runID] = r
	}
}

// The worker finishes running tasks. A queued task has no worker to finish it.
func (en *Engine) cancelledRunLocked(old *Run, queuedStatus string) *Run {
	r := cloneRun(old)
	r.CancelRequested = true
	if r.Status == "queued" {
		r.Status = queuedStatus
		now := en.now()
		r.CompletedAt = &now
	}
	return r
}

func (en *Engine) executionTargets(ctx context.Context, f Frozen) ([]Target, error) {
	if host, ok := en.host.(interface {
		pinnedTarget(context.Context, string, string) (Target, error)
	}); ok && f.ScopeBatchID != "" && f.Judge.Mode != "llm" && f.ControlAuthID == "" {
		en.mu.Lock()
		peer := en.rules[f.RuleID].expandedProviderPeer(f.AuthID)
		en.mu.Unlock()
		if !peer {
			t, err := host.pinnedTarget(ctx, f.AuthID, f.SubjectIndex)
			return []Target{t}, err
		}
	}
	// The original route must remain in the current directory for an expanded member.
	return en.host.Targets(ctx)
}

// checkExecution repeats admission after queue waits and before each subject, control, or judge call.
func (en *Engine) checkExecution(ctx context.Context, runID string, f Frozen) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	targets, err := en.executionTargets(ctx, f)
	if err != nil {
		return fmt.Errorf("unavailable: credential status cannot be verified")
	}
	if f.RuleID == "" && (f.SubjectIndex != "" || f.ExpectedObjectType != "") {
		kind := f.ExpectedObjectType
		if kind == "" && (f.ProviderName != "" || f.ProviderObservation) {
			kind = objectProvider
		}
		if err := f.verifyManualSubject(targets, kind, f.SubjectIndex); err != nil {
			return err
		}
	}
	en.mu.Lock()
	stateErr := currentTargetError(en.reconcileCurrentTargetsLocked(ctx, targets, dependencies(f.RunConfig)), dependencies(f.RunConfig))
	if stateErr == nil {
		stateErr = en.frozenDispositionLocked(f)
	}
	en.mu.Unlock()
	if stateErr != nil {
		return stateErr
	}
	if f.RuleID != "" {
		// Check the current target kind before reading any file credential state.
		deps := dependencies(f.RunConfig)
		for _, t := range targets {
			if !contains(deps, t.AuthID) {
				continue
			}
			wasProvider := f.ProviderObservation && t.AuthID == f.AuthID || contains(f.ProviderDependencies, t.AuthID)
			if isProviderTarget(t) != wasProvider || contains(t.Reasons, "unverified_inventory_entry") {
				return fmt.Errorf("blocked: credential origin changed or cannot be verified")
			}
			if t.AuthID == f.AuthID && t.IsProvider && f.CatalogMissingModel == "" && f.ManagedProbe == nil && firstMissingModel(t, f.Models) != "" {
				return fmt.Errorf("blocked: 供应商不再注册本轮冻结的模型")
			}
		}
	}
	if expected := f.ManagedProbe; expected != nil {
		en.mu.Lock()
		err = en.frozenDispositionLocked(f)
		en.mu.Unlock()
		if err != nil {
			return err
		}
		h, ok := en.host.(managedHost)
		if !ok || !en.Capabilities().ManagementSession {
			return fmt.Errorf("management_session_required: background management connection is not ready")
		}
		current, err := h.CredentialSnapshot(ctx, expected.AuthID, expected.AuthIndex)
		if err != nil || !sameSnapshot(current, *expected) {
			return fmt.Errorf("blocked: credential changed after dispatch")
		}
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	r := en.runs[runID]
	if en.closed || en.fault != nil {
		return fmt.Errorf("unavailable: engine stopped or persistence failed")
	}
	if r == nil || r.Status != "running" || r.CancelRequested {
		return fmt.Errorf("blocked: task is no longer admitted")
	}
	if err := en.frozenDispositionLocked(f); err != nil {
		return err
	}
	var manualAccount string
	if f.RuleID == "" && f.ManualAccountFingerprint != "" && f.ManualPolicyActionID == "" {
		manualAccount, err = en.currentManualAccountLocked(ctx, f.RunConfig, targets)
		if err != nil {
			return err
		}
	}
	if manualAccount != f.ManualAccountFingerprint {
		return fmt.Errorf("blocked: 测试提交后 CPA 凭证绑定的账号已变化，请重新核对。")
	}
	if id := r.Config.RuleID; id != "" {
		rule := en.rules[id]
		if rule == nil || !rule.Enabled || rule.Revision != r.Config.RuleRevision || ruleStatus(rule) != "detecting" {
			return fmt.Errorf("blocked: stale or stopped rule")
		}
		if problem := en.checkRuleProvidersLocked(rule, f.RunConfig, targets); problem != nil {
			return problem
		}
		for _, t := range targets {
			if f.CatalogMissingModel != "" && t.AuthID == f.AuthID && firstMissingModel(t, []string{f.CatalogMissingModel}) == "" {
				return fmt.Errorf("blocked: CPA 当前目录已不能证实本轮所记录的模型缺项")
			}
			if usesCredential(f.RunConfig, t.AuthID) && groupTargetBlocked(en.managed, t) && !activeDisposition(en.managed[t.AuthID]) {
				return fmt.Errorf("blocked: provider group membership changed")
			}
		}
		if en.unsupportedRule(rule) {
			return unsupportedQuality()
		}
		if r.Config.Scheduled && !en.timerAllowed(rule) {
			return fmt.Errorf("blocked: scheduling disabled")
		}
	}
	targets, err = en.checkManagedRunLocked(r, targets)
	if err != nil {
		return err
	}
	if paused := pausedTarget(r.Config.RunConfig, targets); paused != "" {
		if err = en.haltCredentialLocked(paused, "", "quality_paused"); err != nil {
			return err
		}
	}
	if err = en.checkTargetsForRunLocked(r.Config.RunConfig, targets, r.Source == "scheduled_probe" && !en.Capabilities().QualityPause); err != nil {
		return err
	}
	if err = frozenIdentityError(r.Config, targets); err != nil {
		return err
	}
	if r.Config.ScopeBatchID != "" {
		for _, t := range targets {
			if t.AuthID != r.Config.AuthID {
				continue
			}
			if !ruleSubjectEnabled(t) || t.AuthIndex != r.Config.SubjectIndex || t.Provider != r.Config.Provider {
				return fmt.Errorf("blocked: scoped target changed")
			}
		}
	}
	return ctx.Err()
}

func frozenIdentityError(f Frozen, targets []Target) error {
	for _, t := range targets {
		if f.RuleID == "" && f.ProviderName != "" && t.AuthID == f.AuthID {
			if !isProviderTarget(t) || t.AuthIndex != f.SubjectIndex || t.Provider != f.Provider {
				return fmt.Errorf("blocked: 所选供应商路由已变化，请重新选择后测试。")
			}
			if problem := providerRouteProblem(t, f.Models); problem != nil {
				return fmt.Errorf("blocked: %s", problem.detail.Reason)
			}
		}
		if t.AuthID == f.AuthID && t.IdentityVersion != f.IdentityVersion {
			return fmt.Errorf("blocked: subject identity changed")
		}
		if f.Judge.Mode == "llm" && t.AuthID == f.Judge.AuthID && t.IdentityVersion != f.JudgeIdentity {
			return fmt.Errorf("blocked: judge identity changed")
		}
		if t.AuthID == f.ControlAuthID && t.IdentityVersion != f.ControlIdentity {
			return fmt.Errorf("blocked: control identity changed")
		}
	}
	return nil
}
func (en *Engine) persistFailure(err error) {
	if err != nil {
		en.fault = err
		log.WithError(err).Error("model-quality: persistence failed; further execution blocked")
		for _, cancel := range en.active {
			cancel()
		}
	}
}
