package quality

import (
	"context"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

func (en *Engine) Rules() []Rule {
	en.mu.Lock()
	defer en.mu.Unlock()
	items := make([]Rule, 0, len(en.rules))
	for _, id := range en.sortedRuleIDsLocked() {
		items = append(items, en.ruleForDisplayLocked(en.rules[id]))
	}
	return items
}

func (en *Engine) sortedRuleIDsLocked() []string {
	ids := make([]string, 0, len(en.rules))
	for id := range en.rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (en *Engine) ruleForDisplayLocked(r *Rule) Rule {
	next := cloneRule(r)
	if next.PendingSchedule != nil {
		next.PendingSchedule.Accepted = nil
	}
	if next.LastScopeDispatch != nil {
		for i, detail := range next.LastScopeDispatch.Details {
			if en.runs[detail.RunID] == nil {
				next.LastScopeDispatch.Details[i].RunID = ""
			}
		}
	}
	if en.unsupportedRule(next) {
		next.Enabled = false
		next.Status = "unsupported"
		next.NextRun = time.Time{}
	}
	next.timerActive = ruleScheduled(next) && en.timerAllowed(next)
	if next.timerActive {
		if retry := en.scheduleRetry[r.ID]; retry.revision == r.Revision && (retry.due.Equal(r.NextRun) || r.PendingSchedule != nil && retry.due.Equal(r.PendingSchedule.Due)) {
			next.timerWaitReason = retry.reason
		}
		if next.timerWaitReason == "" && r.PendingSchedule != nil {
			next.timerWaitReason = "部分对象已受理，其余对象等待后台管理目录就绪。"
		}
	}
	if next.Status == "detecting" {
		if hold := en.loginWaitLocked(next.RunConfig); hold != nil {
			next.Status, _ = hold.reason()
			next.timerActive = false
		}
	}
	return *next
}
func (en *Engine) SaveRule(ctx context.Context, in Rule, update bool) (*Rule, error) {
	return en.saveRule(ctx, in, update, "")
}

func (en *Engine) saveRule(ctx context.Context, in Rule, update bool, activation string) (*Rule, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	return en.saveRuleLocked(ctx, in, update, activation)
}

func (en *Engine) saveRuleLocked(ctx context.Context, in Rule, update bool, activation string) (*Rule, error) {
	if en.closed || en.fault != nil {
		return nil, fmt.Errorf("unavailable")
	}
	if en.unsupportedRule(&in) {
		return nil, unsupportedQuality()
	}
	old := en.rules[in.ID]
	if err := selectRuleObjects(&in, old); err != nil {
		return nil, err
	}
	if err := prepareUpstreamActions(&in, old); err != nil {
		return nil, err
	}
	if activation != "" {
		if en.Capabilities().QualityPause || (in.Action != "" && in.Action != "observe_only" && !credentialMutation(in.Action)) || in.AutomaticPause || (update && old != nil && en.unsupportedRule(old)) {
			return nil, unsupportedQuality()
		}
		in.Enabled, in.SchedulingEnabled = true, false
		if update && old != nil {
			in.Enabled, in.SchedulingEnabled = old.Enabled, old.SchedulingEnabled
		}
		switch activation {
		case "keep":
		case "start":
			if old != nil && (old.BlockedAuthID != "" || old.ActionID != "") {
				return nil, unsupportedQuality()
			}
			in.Enabled, in.SchedulingEnabled = true, true
		case "stop":
			in.SchedulingEnabled = false
		default:
			return nil, fmt.Errorf("invalid_request: unknown timer activation")
		}
	}
	if !en.Capabilities().QualityPause && in.TargetScope == "" && in.AuthID == "" && in.Provider == "" && (!update || old != nil && dynamicScope(old.TargetScope)) {
		in.TargetScope = scopeAll
		if old != nil {
			in.TargetScope = old.TargetScope
		}
	}
	scoped := dynamicScope(in.TargetScope) || in.TargetGroup != ""
	if in.TargetScope != "" && in.TargetScope != "single" && !scoped {
		return nil, fmt.Errorf("invalid_request: unknown target scope")
	}
	if scoped && (in.AutomaticPause || (in.Action != "" && in.Action != "observe_only" && !credentialMutation(in.Action)) || en.Capabilities().QualityPause) {
		return nil, fmt.Errorf("unsupported: dynamic scope is for official-host observation only")
	}
	modelDefault := in.Parameters.ReasoningEffort == "default"
	if modelDefault {
		in.Parameters.ReasoningEffort = ""
	}
	if update && old != nil {
		if in.Parameters.MaxTokens == 0 {
			in.Parameters.MaxTokens = old.Parameters.MaxTokens
		}
		if in.Parameters.Temperature == nil {
			in.Parameters.Temperature = old.Parameters.Temperature
		}
		if in.Parameters.ReasoningEffort == "" && !modelDefault {
			in.Parameters.ReasoningEffort = old.Parameters.ReasoningEffort
		}
	} else if in.Parameters.ReasoningEffort == "" && !modelDefault {
		in.Parameters.ReasoningEffort = "high"
	}
	switch in.Parameters.ReasoningEffort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		// An edit may preserve a retired effort but cannot newly select it.
		if !modelDefault && (!update || old == nil || old.Parameters.ReasoningEffort != in.Parameters.ReasoningEffort) {
			return nil, fmt.Errorf("invalid_request: unsupported rule reasoning_effort")
		}
	}
	if e := en.validateConfig(&in.RunConfig, scoped); e != nil {
		return nil, e
	}
	if _, e := FreezeQuestion(in.RunConfig); e != nil {
		return nil, e
	}
	if in.Action == "" {
		in.Action = "observe_only"
	}
	if in.Action != "observe_only" && in.Action != "pause_credential" && !credentialMutation(in.Action) {
		return nil, fmt.Errorf("unsupported action")
	}
	if old != nil {
		needsCurrentState := false
		for _, d := range en.managed {
			needsCurrentState = needsCurrentState || d.RuleID == old.ID && d.State == "disabled"
		}
		if needsCurrentState {
			targets, readErr := en.host.Targets(ctx)
			if readErr != nil {
				return nil, fmt.Errorf("unavailable: cannot read current CPA targets: %w", readErr)
			}
			failures := en.reconcileCurrentTargetsLocked(ctx, targets, nil)
			for id, failure := range failures {
				if d := en.managed[id]; d != nil && d.RuleID == old.ID {
					return nil, failure
				}
			}
		}
	}
	if err := en.validateManagedRule(&in, old); err != nil {
		return nil, err
	}
	if len(in.Name) > 128 {
		return nil, fmt.Errorf("name too long")
	}
	if in.FailThreshold == 0 {
		in.FailThreshold = 3
	}
	if in.FailThreshold < 1 || in.FailThreshold > 100 {
		return nil, fmt.Errorf("threshold must be 1..100")
	}
	if err := prepareResultLimit(&in, old); err != nil {
		return nil, err
	}
	if err := en.storageAdmission(); err != nil {
		return nil, err
	}
	if update {
		if old == nil {
			return nil, fmt.Errorf("not_found: rule")
		}
		if !en.Capabilities().QualityPause && (old.ActionID != "" || old.BlockedAuthID != "") {
			return nil, unsupportedQuality()
		}
		if in.Revision != old.Revision {
			return nil, fmt.Errorf("conflict: stale rule revision")
		}
		if old.ActionID != "" && in.AuthID != old.AuthID {
			return nil, fmt.Errorf("conflict: release owned pause before changing target")
		}
		in.Revision++
		in.BlockedAuthID = old.BlockedAuthID
		in.IntentRevision = old.IntentRevision
		in.ActionID = old.ActionID
		in.ActionRevision = old.ActionRevision
		in.ActionRuleRevision = old.ActionRuleRevision
		in.IdentityVersion = old.IdentityVersion
	} else {
		if in.ID != "" {
			return nil, fmt.Errorf("new rule ID must be empty")
		}
		in.ID = ID()
		in.BlockedAuthID = ""
		in.IntentRevision = 0
		in.Revision = 1
		in.ActionID = ""
		in.ActionRevision = 0
		in.ActionRuleRevision = 0
		in.IdentityVersion = ""
	}
	in.EvidenceSignature = ""
	in.FailCount = 0
	in.PendingSchedule = nil
	in.LastRun = ""
	in.LastScopeBatch = ""
	in.LastManualSkips = nil
	in.LastScopeDispatch = nil
	if old != nil {
		in.LastRun, in.LastScopeBatch = old.LastRun, old.LastScopeBatch
		in.LastScopeDispatch = cloneScopeDispatch(old.LastScopeDispatch)
		if in.TargetScope == old.TargetScope {
			in.LastManualSkips = old.LastManualSkips
		}
	}
	in.IdempotencyKey = ""
	if err := prepareRuleSchedule(&in, old); err != nil {
		return nil, err
	}
	planned, err := ruleNextInstant(&in, en.now(), time.Local)
	if err != nil {
		return nil, err
	}
	for _, r := range en.rules {
		if r.ID != in.ID && r.AuthID == in.AuthID && r.Action == "pause_credential" && in.Action == "pause_credential" {
			return nil, fmt.Errorf("conflict: one credential permits one pause rule")
		}
	}
	var targets []Target
	retired := !scoped && en.terminalDependencyLocked(in.RunConfig)
	newGroup := in.TargetGroup != "" && (old == nil || in.TargetGroup != old.TargetGroup)
	for _, group := range in.AuthorizedGroups {
		newGroup = newGroup || old == nil || !contains(old.AuthorizedGroups, group)
	}
	checkSelection := in.expectedTargetObjectType != ""
	if !scoped && !retired || newGroup || checkSelection {
		targets, err = en.host.Targets(en.inventoryContextLocked(ctx))
		if err != nil {
			return nil, err
		}
		if err = validateRuleGroups(&in, old, targets); err != nil {
			return nil, err
		}
		if checkSelection {
			if err := in.verifySelectedRuleTarget(targets); err != nil {
				return nil, err
			}
			in.expectedTargetObjectType = ""
		}
	}
	if !scoped && !retired {
		if problem := en.checkRuleProvidersLocked(&in, in.RunConfig, targets); problem != nil {
			keepBindings := old != nil && in.AuthID == old.AuthID && in.ControlAuthID == old.ControlAuthID && in.Judge.AuthID == old.Judge.AuthID
			startingTimer := activation == "start" || in.SchedulingEnabled && (old == nil || !old.SchedulingEnabled)
			if problem.detail.Code != "provider_targets_excluded" || !keepBindings || startingTimer {
				return nil, problem
			}
		}
		if !checkSelection {
			for _, t := range targets {
				in.recordBoundTarget(t)
			}
		}
		if managedRule(&in) {
			validationRule := in
			if update {
				// Validate the saved target even when this rule already disabled
				// it. The permission is local to validation; no probe is queued.
				validationRule.Action = disableCredential
				validationRule.AutoRestore = true
			}
			targets, _, err = en.managedTargetsLocked(ctx, &validationRule, targets, true)
			if err != nil {
				rejection, ok := err.(*scopeRejection)
				if !ok || preflightStatus(rejection.detail.Code) != "failed" {
					return nil, err
				}
			}
		}
	}
	if in.Action == "pause_credential" {
		supported := false
		for _, t := range targets {
			if t.AuthID == in.AuthID && t.CanPause {
				supported = true
			}
		}
		if !supported {
			return nil, fmt.Errorf("unsupported: target cannot be paused precisely")
		}
	}
	if in.Enabled {
		if in.ActionID != "" {
			return nil, fmt.Errorf("blocked: owned pause requires manual release")
		}
		if !retired && !scoped {
			if err = en.checkTargetPoliciesLocked(in.RunConfig, targets, !en.Capabilities().QualityPause); err != nil {
				return nil, err
			}
		}
		in.BlockedAuthID = ""
	} else if blocked := pausedTarget(in.RunConfig, targets); blocked != "" {
		in.BlockedAuthID = blocked
	}
	in.Status = ruleStatus(&in)
	switch {
	case !ruleScheduled(&in):
		in.NextRun = time.Time{}
	case old != nil && ruleScheduled(old) && (activation != "start" || en.timerAllowed(old)) && sameRuleSchedule(&in, old) && !old.NextRun.IsZero():
		in.NextRun = old.NextRun.UTC()
	default:
		in.NextRun = planned
	}
	c := changes()
	if activation == "start" || !in.SchedulingEnabled || !in.Enabled {
		c.timers = en.changedTimer(in.ID, activation == "start")
	}
	c.rules[in.ID] = cloneRule(&in)
	kind := "rule_created"
	if update {
		kind = "rule_updated"
		en.cancelRuleChangesLocked(c, in.ID, "rule_changed")
	}
	c.audits = append(c.audits, en.audit(kind, in.ID, "", "", fmt.Sprintf("revision=%d", in.Revision)))
	if err = en.commitLocked(c); err != nil {
		return nil, err
	}
	saved := cloneRule(&in)
	saved.timerActive = ruleScheduled(saved) && en.timerAllowed(saved)
	return saved, nil
}

func (en *Engine) setRuleTimer(ctx context.Context, id string, revision uint64, enabled bool) (*Rule, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return nil, err
	}
	old := en.rules[id]
	if old == nil {
		return nil, fmt.Errorf("not_found: rule")
	}
	if old.Revision != revision {
		return nil, fmt.Errorf("conflict: stale rule revision")
	}
	if en.Capabilities().QualityPause || en.unsupportedRule(old) || old.ActionID != "" || old.BlockedAuthID != "" {
		return nil, unsupportedQuality()
	}
	next := cloneRule(old)
	next.timerActive = ruleScheduled(old) && en.timerAllowed(old)
	// Inactive rules can retain permission behind a closed global gate or pause.
	if enabled && next.timerActive || !enabled && !old.SchedulingEnabled && !en.timers[id] {
		return next, nil
	}
	if enabled {
		return en.saveRuleLocked(ctx, *next, true, "start")
	}
	// Stopping a timer must also work while the credential is offline.
	next.SchedulingEnabled, next.timerActive = false, false
	next.Revision++
	resetRule(next)
	c := changes()
	c.rules[id] = next
	c.timers = en.changedTimer(id, false)
	en.cancelRuleChangesLocked(c, id, "rule_changed")
	c.audits = append(c.audits, en.audit("rule_schedule_state", id, "", "", "timer stopped"))
	if err := en.commitLocked(c); err != nil {
		return nil, err
	}
	return cloneRule(next), nil
}

func (en *Engine) PauseRule(id string, rev uint64, paused bool) error {
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	r := en.rules[id]
	if r == nil {
		return fmt.Errorf("not_found: rule")
	}
	if r.Revision != rev {
		return fmt.Errorf("conflict: stale rule revision")
	}
	next := cloneRule(r)
	next.Enabled = !paused
	next.Revision++
	resetRule(next)
	if !paused {
		if en.unsupportedRule(r) {
			return unsupportedQuality()
		}
		if r.ActionID != "" || !en.Capabilities().QualityPause && r.BlockedAuthID != "" {
			return fmt.Errorf("blocked: owned pause requires manual release")
		}
		if !en.terminalDependencyLocked(r.RunConfig) {
			targets, err := en.host.Targets(en.inventoryContextLocked(en.ctx))
			if err != nil {
				return err
			}
			if problem := en.checkRuleProvidersLocked(r, r.RunConfig, targets); problem != nil {
				return problem
			}
			if !en.Capabilities().QualityPause {
				for i, target := range targets {
					if target.AuthID != r.AuthID || !disabledObject(target) || !needsScopeSnapshot(r, target) {
						continue
					}
					// Resuming the rule uses the same owned recovery admission as
					// dispatch. This projection does not enable or probe CPA.
					projected, _, snapshotErr := en.admitManagedLocked(en.ctx, r, target, false)
					if snapshotErr != nil {
						return snapshotErr
					}
					targets = append([]Target(nil), targets...)
					targets[i] = projected
					break
				}
			}
			if err = en.checkTargetPoliciesLocked(r.RunConfig, targets, !en.Capabilities().QualityPause); err != nil {
				return err
			}
		}
		next.BlockedAuthID = ""
	}
	next.Status = ruleStatus(next)
	next.NextRun = nextRun(*next, en.now())
	c := changes()
	c.rules[id] = next
	en.cancelRuleChangesLocked(c, id, "rule_changed")
	c.audits = append(c.audits, en.audit("rule_schedule_state", id, "", r.ActionID, fmt.Sprintf("paused=%t revision=%d", paused, next.Revision)))
	return en.commitLocked(c)
}
func (en *Engine) DeleteRule(ctx context.Context, id string, rev uint64) error {
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	r := en.rules[id]
	if r == nil {
		return fmt.Errorf("not_found: 此规则已不存在，请刷新规则列表。")
	}
	if r.Revision != rev {
		return fmt.Errorf("conflict: 规则已更新，请刷新后再删除。")
	}
	if en.Capabilities().QualityPause {
		actions, e := en.host.Actions(ctx)
		if e != nil {
			return e
		}
		for _, a := range actions {
			if a.RuleID == id {
				return fmt.Errorf("conflict: 此规则持有独立质量暂停，解除暂停后才能删除。")
			}
		}
	} else if r.ActionID != "" || r.BlockedAuthID != "" {
		return unsupportedQuality()
	}
	if r.ActionID != "" {
		return fmt.Errorf("conflict: 此规则的独立质量暂停尚待核对。")
	}
	retainedRuns, retainedActions := map[string]bool{}, map[string]bool{}
	var clearHandling []string
	var unresolved []*CredentialDisposition
	for _, d := range en.managed {
		// Removing a rule or its history must not turn an error suspension
		// into an operator disable that another rule may probe or restore.
		if errorPolicyDisabled(d).Code != "" && (d.RuleID == id || d.ErrorSource != nil && d.ErrorSource.RuleID == id) {
			for _, runID := range []string{d.RunID, d.TriggerRunID} {
				if runID != "" {
					retainedRuns[runID] = true
				}
			}
			if d.ErrorSource != nil && d.ErrorSource.RunID != "" {
				retainedRuns[d.ErrorSource.RunID] = true
			}
			retainedActions[d.ActionID] = true
			continue
		}
		if d.RuleID != id {
			continue
		}
		if pendingCredentialAction(d.State) != "" {
			return fmt.Errorf("conflict: CPA 凭证写入尚在处理中，请等待结果后再删除。")
		}
		switch d.State {
		case "disabled", "deleted", "restored", "externally_enabled", "entry_missing", "acknowledged", "precondition_failed":
			clearHandling = append(clearHandling, d.AuthID)
			continue
		}
		// Unknown write outcomes remain quarantined by credential identity.
		// Dropping the owner must not make an uncertain write eligible again.
		next := *d
		next.RuleID, next.RuleRevision, next.State = "", 0, "attention"
		unresolved = append(unresolved, &next)
		if d.RunID != "" {
			retainedRuns[d.RunID] = true
		}
		if d.TriggerRunID != "" {
			retainedRuns[d.TriggerRunID] = true
		}
		if d.ActionID != "" {
			retainedActions[d.ActionID] = true
		}
	}
	drop := make(map[string]bool)
	for _, run := range en.runs {
		if run.Config.RuleID != id {
			continue
		}
		if run.CompletedAt == nil || en.active[run.ID] != nil {
			return fmt.Errorf("conflict: 规则还有未结束的检测，请等待或取消后再删除。")
		}
		if !retainedRuns[run.ID] {
			drop[run.ID] = true
		}
	}
	audit := en.audit("rule_deleted", id, "", "", fmt.Sprintf("Rule deleted; cleared %d credential records and kept %d uncertain writes quarantined", len(clearHandling), len(unresolved)))
	timers := en.changedTimer(id, false)
	if err := en.store.Write(func(tx *bolt.Tx) error {
		if timers != nil {
			if err := put(tx, "meta", ruleTimersKey, timers); err != nil {
				return err
			}
		}
		if err := tx.Bucket([]byte("rules")).Delete([]byte(id)); err != nil {
			return err
		}
		for _, authID := range clearHandling {
			if err := tx.Bucket([]byte("meta")).Delete([]byte(dispositionPrefix + authID)); err != nil {
				return err
			}
		}
		for _, d := range unresolved {
			if err := put(tx, "meta", dispositionPrefix+d.AuthID, d); err != nil {
				return err
			}
		}
		if err := deleteRuleRecords(tx, drop, id, retainedRuns, retainedActions); err != nil {
			return err
		}
		return put(tx, "audit", audit.ID, audit)
	}); err != nil {
		en.persistFailure(err)
		return err
	}
	delete(en.rules, id)
	delete(en.scheduleRetry, id)
	delete(en.finishedRules, id)
	for _, authID := range clearHandling {
		delete(en.managed, authID)
	}
	for _, d := range unresolved {
		en.managed[d.AuthID] = d
	}
	for runID := range drop {
		delete(en.runs, runID)
	}
	if timers != nil {
		en.timers = timers
	}
	return nil
}
func (en *Engine) RunRule(ctx context.Context, id string) (*Run, error) {
	if en.Capabilities().QualityPause {
		return en.submit(ctx, RunConfig{}, id, 0)
	}
	en.mu.Lock()
	r := en.rules[id]
	if r == nil {
		en.mu.Unlock()
		return nil, fmt.Errorf("not_found: rule")
	}
	revision := r.Revision
	en.mu.Unlock()
	receipt, err := en.dispatchRule(ctx, id, revision, false)
	if err != nil {
		return nil, err
	}
	if receipt.RunID == "" {
		return nil, fmt.Errorf("blocked: scope has no single runnable subject")
	}
	return en.Run(receipt.RunID)
}

// RoundVerdict requires every planned sample and explicit verdicts for every model.
func RoundVerdict(r *Run) string {
	return roundVerdict(r, func(s *Sample) bool { return s.Comparable })
}

func roundVerdict(r *Run, hasEvidence func(*Sample) bool) string {
	if r.Status != "completed" || r.Error != "" || r.BlockReason != "" || r.CancelRequested || r.UpstreamCondition != "" ||
		len(r.Config.Models) == 0 || r.Config.Samples < 1 || len(r.Samples) != len(r.Config.Models)*r.Config.Samples {
		return "unknown"
	}
	seen := make(map[string][]bool, len(r.Config.Models))
	for _, model := range r.Config.Models {
		if seen[model] != nil {
			return "unknown"
		}
		seen[model] = make([]bool, r.Config.Samples)
	}
	failed := make(map[string]bool)
	for i := range r.Samples {
		s := &r.Samples[i]
		indices := seen[s.Model]
		if s.Index < 0 || s.Index >= len(indices) || indices[s.Index] || s.ExecutionStatus != "completed" || s.Error != "" || s.UpstreamCondition != "" || !hasEvidence(s) {
			return "unknown"
		}
		indices[s.Index] = true
		switch s.QualityVerdict {
		case "incorrect":
			failed[s.Model] = true
		case "correct":
		default:
			return "unknown"
		}
	}
	if len(failed) == len(seen) {
		return "incorrect"
	}
	if len(failed) == 0 {
		return "correct"
	}
	return "mixed"
}
func (en *Engine) decideLocked(run *Run) {
	if run.Source != "scheduled_probe" || run.Config.RuleID == "" || en.closed || en.fault != nil {
		return
	}
	r := cloneRule(en.rules[run.Config.RuleID])
	out := cloneRun(run)
	c := changes()
	c.runs[run.ID] = out
	if en.loginWaitLocked(run.Config.RunConfig) != nil || run.BlockReason == "credential_authorization_changed" {
		out.ActionStatus = "credential_authorization_changed"
		_ = en.commitLocked(c)
		return
	}
	if r == nil || !r.Enabled || r.Revision != run.Config.RuleRevision || ruleStatus(r) != "detecting" {
		out.ActionStatus = "stale_rule"
		if r != nil && ruleStatus(r) == "awaiting_release" {
			out.ActionStatus = "awaiting_release"
		}
		_ = en.commitLocked(c)
		return
	}
	r.LastRun = run.ID
	if run.Config.ProviderObservation && (run.Config.ManagedProbe == nil || r.LegacyProviderObservation) {
		r.FailCount, r.EvidenceSignature = 0, ""
		out.ActionStatus = "observe_only"
		c.rules[r.ID] = r
		_ = en.commitLocked(c)
		return
	}
	if managedRule(r) {
		en.decideManagedLocked(run, r, c)
		return
	}
	if !en.Capabilities().VerifiedEvidence || dynamicScope(r.TargetScope) {
		r.FailCount = 0
		r.EvidenceSignature = ""
		out.ActionStatus = "observe_only"
		c.rules[r.ID] = r
		_ = en.commitLocked(c)
		return
	}
	verdict := RoundVerdict(run)
	var targets []Target
	if verdict != "unknown" {
		var err error
		targets, err = en.host.Targets(en.ctx)
		if err == nil {
			if paused := pausedTarget(run.Config.RunConfig, targets); paused != "" {
				c.rules[r.ID] = r
				en.haltChangesLocked(c, paused, run.ID, "quality_paused")
				out.ActionStatus = "awaiting_release"
				_ = en.commitLocked(c)
				return
			}
			err = en.checkTargetsLocked(run.Config.RunConfig, targets)
		}
		if err == nil {
			err = frozenIdentityError(run.Config, targets)
		}
		if err != nil {
			verdict = "unknown"
			out.ActionStatus = "evidence_unavailable"
		}
	}
	signature := roundSignature(run)
	if r.EvidenceSignature != signature {
		r.FailCount = 0
		r.EvidenceSignature = signature
	}
	if run.Config.Question.Kind == "random" && !en.hasControlLocked(run, targets) {
		verdict = "unknown"
		out.ActionStatus = "missing_valid_control"
	}
	if verdict == "incorrect" {
		r.FailCount++
	} else {
		r.FailCount = 0
	}
	c.rules[r.ID] = r
	if r.Action != "pause_credential" || verdict != "incorrect" || r.FailCount < r.FailThreshold || !r.AutomaticPause || !en.cfg.AutomaticPause {
		_ = en.commitLocked(c)
		return
	}
	a := Action{AuthID: r.AuthID, OwnerPlugin: "model-quality", RuleID: r.ID, RuleRevision: r.Revision, ActionID: ID(), ActionRevision: 1, EvidenceRunID: run.ID, IdentityVersion: run.Config.IdentityVersion, Reason: "all selected models explicitly failed complete comparable rounds; manual release required", CreatedAt: en.now()}
	r.ActionID = a.ActionID
	r.ActionRevision = a.ActionRevision
	r.ActionRuleRevision = a.RuleRevision
	r.IdentityVersion = a.IdentityVersion
	en.haltChangesLocked(c, r.AuthID, run.ID, "quality_paused")
	r = c.rules[r.ID]
	r.IntentRevision = r.Revision
	out.ActionStatus = "pending_reconciliation"
	c.intent = &a
	c.audits = append(c.audits, en.audit("pause_requested", r.ID, run.ID, a.ActionID, "all dependent rules and tasks stopped before host action"))
	if err := en.commitLocked(c); err != nil {
		return
	}
	result, err := en.host.Pause(en.ctx, a)
	update := changes()
	out = cloneRun(en.runs[run.ID])
	update.runs[run.ID] = out
	if err != nil {
		out.ActionStatus = "pending_reconciliation"
		update.audits = append(update.audits, en.audit("pause_uncertain", r.ID, run.ID, a.ActionID, scrubError(err)))
	} else {
		held := cloneRule(en.rules[r.ID])
		held.ActionRevision = result.ActionRevision
		update.rules[r.ID] = held
		out.ActionStatus = "paused"
		update.audits = append(update.audits, en.audit("credential_paused", r.ID, run.ID, a.ActionID, "credential paused; all evaluation stopped; manual release required"))
	}
	_ = en.commitLocked(update)
}
func (en *Engine) hasControlLocked(r *Run, targets []Target) bool {
	if r.Config.BatchID == "" || r.Config.ControlAuthID == "" || r.CompletedAt == nil || en.now().Sub(*r.CompletedAt) > 5*time.Minute || r.CompletedAt.Sub(r.CreatedAt) > 5*time.Minute {
		return false
	}
	var subject, peer *Target
	for i := range targets {
		if targets[i].AuthID == r.Config.AuthID {
			subject = &targets[i]
		}
		if targets[i].AuthID == r.Config.ControlAuthID {
			peer = &targets[i]
		}
	}
	if subject == nil || peer == nil || subject.IdentityVersion != r.Config.IdentityVersion || peer.IdentityVersion != r.Config.ControlIdentity || subject.ComparisonIdentity == "" || peer.ComparisonIdentity == "" || subject.ComparisonIdentity == peer.ComparisonIdentity {
		return false
	}
	control := *r
	control.Samples = r.Controls
	if RoundVerdict(&control) != "correct" || len(r.Controls) != len(r.Samples) {
		return false
	}
	for i, s := range r.Samples {
		o := r.Controls[i]
		if s.Model != o.Model || s.Index != o.Index || s.Evidence.Path != o.Evidence.Path || s.Evidence.ResolvedModel != o.Evidence.ResolvedModel || s.Evidence.PayloadFingerprint != o.Evidence.PayloadFingerprint {
			return false
		}
	}
	return true
}

func (en *Engine) auditLocked(kind, ruleID, runID, actionID, detail string) {
	a := Audit{ID: ID(), Time: en.now(), Kind: kind, RuleID: ruleID, RunID: runID, ActionID: actionID, Detail: detail}
	en.persistFailure(en.store.Put("audit", a.ID, a))
}
func (en *Engine) Actions(ctx context.Context) ([]Action, error) {
	if en.Capabilities().QualityPause {
		return en.host.Actions(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	retained := []Action{}
	for _, rule := range en.rules {
		if rule.ActionID == "" {
			continue
		}
		action := Action{AuthID: rule.AuthID, OwnerPlugin: "model-quality", RuleID: rule.ID, RuleRevision: rule.ActionRuleRevision, ActionID: rule.ActionID, ActionRevision: rule.ActionRevision, IdentityVersion: rule.IdentityVersion}
		var intent Action
		if err := en.store.Get("meta", "intent:"+rule.ActionID, &intent); err == nil {
			action = intent
		}
		action.Status = "retained_unverified"
		retained = append(retained, action)
	}
	sort.Slice(retained, func(i, j int) bool { return retained[i].ActionID < retained[j].ActionID })
	return retained, nil
}
func (en *Engine) Preview(ctx context.Context, ruleID, actionID string, revision uint64) (map[string]any, error) {
	if !en.Capabilities().ManualRelease {
		return nil, unsupportedQuality()
	}
	actions, e := en.host.Actions(ctx)
	if e != nil {
		return nil, e
	}
	for _, a := range actions {
		if a.ActionID == actionID && a.RuleID == ruleID {
			if a.ActionRevision != revision {
				return nil, fmt.Errorf("conflict: stale action revision")
			}
			targets, e := en.host.Targets(ctx)
			if e != nil {
				return nil, e
			}
			return map[string]any{"action": a, "targets": targets, "scope": "entire credential; all models", "manual_release_only": true, "rules_remain_paused": true, "effect": "release only this owned quality pause; all dependent rules remain paused until explicitly resumed; other restrictions remain"}, nil
		}
	}
	return nil, fmt.Errorf("not_found: owned action")
}
func (en *Engine) Release(ctx context.Context, ruleID, actionID string, revision uint64, _ bool) error {
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	if !en.Capabilities().ManualRelease {
		return unsupportedQuality()
	}
	r := en.rules[ruleID]
	if r == nil {
		return fmt.Errorf("not_found: rule")
	}
	actions, err := en.host.Actions(ctx)
	if err != nil {
		return err
	}
	var action *Action
	for i := range actions {
		if actions[i].ActionID == actionID && actions[i].RuleID == ruleID && actions[i].AuthID == r.AuthID && actions[i].OwnerPlugin == "model-quality" {
			action = &actions[i]
			break
		}
	}
	if action == nil || action.ActionRevision != revision || !matchesActionBinding(r, action) {
		return fmt.Errorf("conflict: action missing or revision changed")
	}
	c := changes()
	en.haltChangesLocked(c, action.AuthID, "", "manual_release")
	for _, rule := range c.rules {
		rule.Revision++
		resetRule(rule)
	}
	c.audits = append(c.audits, en.audit("manual_release_requested", ruleID, "", actionID, "all dependent rules and tasks stopped before release"))
	if err = en.commitLocked(c); err != nil {
		return err
	}
	if err = en.host.Release(ctx, *action); err != nil {
		en.auditLocked("manual_release_blocked", ruleID, "", actionID, scrubError(err))
		return err
	}
	remaining := map[string]bool{}
	targets, targetsErr := en.host.Targets(ctx)
	active, actionsErr := en.host.Actions(ctx)
	for _, t := range targets {
		if t.QualityPaused {
			remaining[t.AuthID] = true
		}
	}
	for _, a := range active {
		remaining[a.AuthID] = true
	}
	for id, rule := range en.rules {
		if id != ruleID && rule.ActionID != "" {
			remaining[rule.AuthID] = true
		}
	}
	next := cloneRule(en.rules[ruleID])
	next.ActionID = ""
	next.ActionRevision = 0
	next.ActionRuleRevision = 0
	next.IntentRevision = 0
	update := changes()
	update.rules[ruleID] = next
	for id, rule := range en.rules {
		if usesCredential(rule.RunConfig, action.AuthID) {
			n := cloneRule(rule)
			if id == ruleID {
				n = next
			}
			if targetsErr == nil && actionsErr == nil {
				n.BlockedAuthID = ""
				for _, authID := range dependencies(n.RunConfig) {
					if remaining[authID] {
						n.BlockedAuthID = authID
						break
					}
				}
			}
			n.Enabled = false
			resetRule(n)
			n.Status = ruleStatus(n)
			update.rules[id] = n
		}
	}
	update.audits = append(update.audits, en.audit("manual_release", ruleID, "", actionID, "owned restriction released; dependent rules remain paused; explicit resume required"))
	if err = en.commitLocked(update); err != nil {
		return err
	}
	if targetsErr != nil || actionsErr != nil {
		return fmt.Errorf("unavailable: owned pause released; remaining restrictions await reconciliation")
	}
	return nil
}

// roundSignature prevents consecutive evidence from crossing identity or effective-condition boundaries.
func roundSignature(r *Run) string {
	conditions := make([]any, 0, len(r.Samples))
	question := r.Config.Question.Prompt
	random := r.Config.Question.Kind == "random"
	if random {
		question = r.Config.Question.Family
	}
	for _, s := range r.Samples {
		payload := s.Evidence.PayloadFingerprint
		if random {
			payload = "verified-within-controlled-round"
		}
		conditions = append(conditions, []any{s.Model, s.Index, s.Evidence.ResolvedModel, payload, s.Evidence.Path})
	}
	return fingerprint([]any{r.Config.IdentityVersion, r.Config.JudgeIdentity, r.Config.ControlIdentity, r.Config.PayloadConfigVersion, r.Config.Question.Version, question, r.Config.Parameters, r.Config.Judge, conditions})
}
