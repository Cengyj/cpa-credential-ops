package quality

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
)

const scopeAll = "all_enabled"
const scopeAllCredentials = "all_credentials"
const maxScopePending = 1024

func dynamicScope(scope string) bool {
	return scope == scopeAll || scope == scopeAllCredentials
}

type ruleDispatch struct {
	PreflightDetails []ScopeSkipDetail `json:"preflight_details,omitempty"`
	Verified         bool              `json:"verified"`
	Reason           string            `json:"reason,omitempty"`
	Candidates       map[string]int    `json:"candidates"`
	RecoveryCount    int               `json:"recovery_count"`
	SkipReasons      map[string]int    `json:"skip_reasons,omitempty"`
	SkipDetails      []ScopeSkipDetail `json:"skip_details,omitempty"`
	RunID            string            `json:"run_id,omitempty"`
	BatchID          string            `json:"scope_batch_id,omitempty"`
	Count            int               `json:"count"`
	Skipped          int               `json:"skipped"`
}

// A batch is one fair queue entry, not an unbounded burst of queue reservations.
// Its durable child runs are pinned to concrete credentials and never replayed on restart.
func (en *Engine) dispatchRule(ctx context.Context, id string, revision uint64, scheduled bool) (ruleDispatch, error) {
	return en.dispatchWithInventory(ctx, id, revision, scheduled, en.host.Targets)
}

func (en *Engine) dispatchWithInventory(ctx context.Context, id string, revision uint64, scheduled bool, inventory func(context.Context) ([]Target, error)) (ruleDispatch, error) {
	return en.dispatchOccurrence(ctx, id, revision, scheduled, inventory, nil)
}

func (en *Engine) dispatchOccurrence(ctx context.Context, id string, revision uint64, scheduled bool, inventory func(context.Context) ([]Target, error), occurrence *scheduledRule) (ruleDispatch, error) {
	return en.dispatchRulePlan(ctx, id, revision, scheduled, inventory, occurrence, nil)
}

func (en *Engine) dispatchRulePlan(ctx context.Context, id string, revision uint64, scheduled bool, inventory func(context.Context) ([]Target, error), occurrence *scheduledRule, command *ruleCommandRecord) (ruleDispatch, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	var result ruleDispatch
	if err := en.mutableLocked(); err != nil {
		return result, err
	}
	if reserved := en.commandAdmissions[id]; reserved != "" && (command == nil || reserved != command.Key) {
		return result, fmt.Errorf("conflict: rule admission already accepted")
	}
	generation := en.scheduleGeneration
	if occurrence != nil && occurrence.generation != generation {
		return result, fmt.Errorf("blocked: scheduled permission was revoked after this occurrence was captured")
	}
	rule := en.rules[id]
	if rule == nil {
		return result, fmt.Errorf("not_found: rule")
	}
	if (revision != 0 || scheduled || dynamicScope(rule.TargetScope)) && revision != rule.Revision {
		return result, fmt.Errorf("conflict: stale rule revision")
	}
	var manualOccurrence *scheduledRule
	if !scheduled && occurrence == nil && ruleScheduled(rule) && en.timerAllowed(rule) && !rule.NextRun.IsZero() {
		now := en.now()
		due := !rule.NextRun.After(now)
		if due || rule.PendingSchedule != nil {
			manualOccurrence = &scheduledRule{id: rule.ID, revision: rule.Revision, generation: generation, due: rule.NextRun, checked: now, catchup: !rule.NextRun.After(en.loadedAt), remainder: !due}
			if !due {
				manualOccurrence.due = rule.PendingSchedule.Due
				manualOccurrence.catchup = true
			}
		}
	}
	if occurrence != nil {
		if !occurrence.current(rule) || !ruleScheduled(rule) || !en.timerAllowed(rule) {
			return result, fmt.Errorf("conflict: scheduled occurrence changed")
		}
		if en.hasCurrentRoundLocked(rule) {
			if occurrence.remainder {
				return result, nil
			}
			next := cloneRule(rule)
			next.NextRun = nextRun(*next, occurrence.checked)
			c := changes()
			c.rules[id] = next
			return result, en.commitLocked(c)
		}
	}
	if hold := en.loginWaitLocked(rule.RunConfig); hold != nil {
		code, reason := hold.reason()
		return result, rejectScope("blocked: "+reason, code, reason)
	}
	if !dynamicScope(rule.TargetScope) && en.Capabilities().QualityPause {
		if en.terminalDependencyLocked(rule.RunConfig) {
			ch := changes()
			ch.ruleCommand = completedDispatch(command, result)
			return result, en.commitLocked(ch)
		}
		var schedRev uint64
		if scheduled {
			schedRev = revision
		}
		r, err := en.submitLocked(ctx, RunConfig{}, id, schedRev, inventory, occurrence, command)
		if err == nil {
			result.RunID = r.ID
			if r.SkipDetail != nil {
				result.BatchID, result.Skipped, result.SkipDetails = r.Config.ScopeBatchID, 1, []ScopeSkipDetail{*r.SkipDetail}
			} else {
				result.Count = 1
			}
		}
		return result, err
	}
	if err := en.checkScopeLocked(rule, scheduled); err != nil {
		return result, err
	}
	if en.scanning[id] {
		return result, fmt.Errorf("conflict: rule already scanning")
	}
	if en.scanning == nil {
		en.scanning = make(map[string]bool)
	}
	en.scanning[id] = true
	defer delete(en.scanning, id)
	plan := cloneRule(rule)
	result, matches, seen, err := en.collectScopeLocked(ctx, plan, inventory, false)
	if scheduled && generation != en.scheduleGeneration {
		return ruleDispatch{}, fmt.Errorf("blocked: scheduled permission was revoked during directory verification")
	}
	rule = en.rules[id]
	if rule == nil || rule.Revision != plan.Revision {
		return ruleDispatch{}, fmt.Errorf("conflict: stale rule revision")
	}
	covered := occurrence
	if covered == nil && manualOccurrence != nil && en.scheduleGeneration == generation && manualOccurrence.current(rule) && ruleScheduled(rule) && en.timerAllowed(rule) {
		covered = manualOccurrence
	}
	if occurrence != nil && (!occurrence.current(rule) || !en.timerAllowed(rule) || !ruleScheduled(rule)) {
		return ruleDispatch{}, fmt.Errorf("conflict: scheduled occurrence changed during directory verification")
	}
	if err != nil {
		var rejection *scopeRejection
		missingManagement := errors.As(err, &rejection) && rejection.detail.Code == "management_session_required"
		if occurrence != nil && (errors.Is(err, errScopeDirectoryUnavailable) || missingManagement) {
			en.deferOccurrenceLocked(rule, occurrence, result.Reason, missingManagement)
			return result, nil
		}
		return result, err
	}
	if err := en.checkScopeLocked(rule, scheduled); err != nil {
		return result, err
	}
	var step scopeScheduleStep
	if covered != nil {
		matches, step = en.scopeScheduleLocked(rule, covered, &result, matches, seen)
		if step.deferScan {
			en.deferOccurrenceLocked(rule, covered, result.Reason, false)
			result.Count = 0
			ch := changes()
			ch.ruleCommand = completedDispatch(command, result)
			return result, en.commitLocked(ch)
		}
	}
	base := cloneConfig(rule.RunConfig)
	if err := en.validateConfig(&base, dynamicScope(rule.TargetScope) || rule.TargetGroup != ""); err != nil {
		return result, err
	}
	if err := en.storageAdmission(); err != nil {
		return result, err
	}
	pending := 0
	for _, r := range en.runs {
		if r.Status != "queued" && r.Status != "running" {
			continue
		}
		if r.Config.ScopeBatchID != "" {
			pending++
		}
	}
	q, err := freezeDispatchQuestion(&base)
	if err != nil {
		return result, err
	}
	batch, now, ch := ID(), en.now(), changes()
	workers := len(matches)
	if workers > 0 {
		en.compactQueueLocked()
	}
	full := workers > 0 && (len(en.queue) >= cap(en.queue) || pending+workers > maxScopePending)
	if full && step.pending != nil {
		en.deferOccurrenceLocked(rule, covered, queueSkip().Reason, false)
		if !scheduled {
			return result, fmt.Errorf("queue_full: rule dispatch capacity exhausted")
		}
		result.Count = 0
		return result, nil
	}
	var ids []string
	next := cloneRule(rule)
	for _, m := range matches {
		if full {
			if m.recovery {
				result.RecoveryCount--
			}
			result.skip(m.target, queueSkip())
			continue
		}
		// Older fixed rules recorded the index but not the directory origin.
		if next.TargetObjectType == "" && next.TargetAuthIndex != "" {
			next.recordBoundTarget(m.target)
		}
		m.config.Seed = base.Seed
		run := newScopedRun(rule, m.target, m.config, q, batch, scheduled, now, m.snapshot)
		run.Config.RecoveryCheck = m.recovery
		if m.judge != nil {
			run.Config.JudgeIdentity = m.judge.IdentityVersion
			if m.judge.IsProvider {
				run.Config.ProviderDependencies = append(run.Config.ProviderDependencies, m.judge.AuthID)
			}
		}
		if m.control != nil {
			run.Config.ControlIdentity = m.control.IdentityVersion
			run.Config.BatchID = ID()
			if m.control.IsProvider {
				run.Config.ProviderDependencies = append(run.Config.ProviderDependencies, m.control.AuthID)
			}
		}
		run.Config.Fingerprint = ""
		run.Config.Fingerprint = fingerprint(run.Config)
		if m.missing != "" {
			queueCatalogRun(run, m.missing)
		}
		ch.runs[run.ID] = run
		ids = append(ids, run.ID)
		if step.pending != nil {
			step.pending.Accepted = append(step.pending.Accepted, run.Config.AuthID)
		}
	}
	var manualSkips []string
	for i := range result.SkipDetails {
		detail := &result.SkipDetails[i]
		t := seen[detail.AuthID]
		if covered != nil && step.waitsFor(*detail) {
			detail.NotExecuted = true
			continue
		}
		if detail.Code == "bound_origin_changed" {
			t = plan.boundHistoryTarget()
		}
		config := base
		config.AuthID, config.Provider = t.AuthID, t.Provider
		run := newScopedRun(plan, t, config, q, batch, scheduled, now, nil)
		markDispatchSkip(run, detail, now)
		ch.runs[run.ID] = run
		if step.pending != nil {
			step.pending.Accepted = append(step.pending.Accepted, run.Config.AuthID)
		}
		if detail.Code == "manual_disabled" {
			manualSkips = append(manualSkips, t.AuthID)
		}
	}
	result.Count = len(ids)
	result.classifyPreflight()
	matched := result.Count
	next.LastScopeDispatch = cloneScopeDispatch(&ScopeDispatchSummary{At: now, Reason: result.Reason, Matched: matched, Skipped: result.Skipped, Details: result.SkipDetails, Candidates: result.Candidates, RecoveryCount: result.RecoveryCount})
	sort.Strings(manualSkips)
	if len(manualSkips) > 0 {
		if result.SkipReasons == nil {
			result.SkipReasons = map[string]int{}
		}
		result.SkipReasons["manual_disabled"] = len(manualSkips)
	}
	next.LastManualSkips = manualSkips
	if !dynamicScope(rule.TargetScope) && rule.TargetGroup == "" && len(ch.runs) == 1 {
		for runID := range ch.runs {
			result.RunID = runID
			next.LastRun = runID
		}
	}
	next.LastScopeBatch = ""
	if len(ch.runs) != 0 {
		next.LastScopeBatch, result.BatchID = batch, batch
	}
	ch.rules[id] = next
	if covered != nil && (scheduled || !full) {
		// An explicit run and a timer can cover the same occurrence. Persist
		// admitted members, outstanding prerequisites and its cursor together.
		next.PendingSchedule = step.pending
		en.consumeOccurrence(ch, next, covered)
	}
	if !dynamicScope(plan.TargetScope) {
		for _, target := range seen {
			if (target.QualityPaused || target.RetainedPause) && usesCredential(plan.RunConfig, target.AuthID) {
				en.haltChangesLocked(ch, target.AuthID, "", "quality_paused")
			}
		}
	}
	ch.audits = append(ch.audits, en.audit("scope_dispatched", id, "", "", fmt.Sprintf("匹配 %d 个凭证，跳过 %d 个", matched, result.Skipped)))
	if err := ctx.Err(); err != nil {
		return ruleDispatch{}, err
	}
	if result.RunID != "" && rule.TargetScope == "" && len(ids) == 1 {
		result.BatchID = ""
	}
	ch.ruleCommand = completedDispatch(command, result)
	if err = en.commitLocked(ch); err != nil {
		return ruleDispatch{}, err
	}
	if covered != nil {
		delete(en.scheduleRetry, id)
	}
	if len(ids) > 0 {
		if en.scopeBatches == nil {
			en.scopeBatches = make(map[string][]string)
		}
		en.scopeBatches[batch] = ids
		if en.roundRemaining == nil {
			en.roundRemaining = make(map[string]int)
		}
		en.roundRemaining[batch] = len(ids)
		en.queue <- batch
		en.notifyWorkersLocked()
	} else {
		en.noteRoundEndLocked(id)
	}
	if len(ch.runs) > len(ids) && rule.MaxResults > 0 {
		if err := en.pruneResultCountsLocked(id, ""); err != nil {
			en.persistFailure(err)
		}
	}
	return result, nil
}

func (en *Engine) checkScopeLocked(rule *Rule, scheduled bool) error {
	if err := en.mutableLocked(); err != nil {
		return err
	}
	if (rule.Action != "observe_only" && !managedRule(rule)) || rule.AutomaticPause || en.Capabilities().QualityPause {
		return unsupportedQuality()
	}
	if !rule.Enabled || ruleStatus(rule) != "detecting" || scheduled && !en.timerAllowed(rule) {
		return fmt.Errorf("blocked: rule or scheduling is stopped")
	}
	if hold := en.loginWaitLocked(rule.RunConfig); hold != nil {
		code, reason := hold.reason()
		return rejectScope("blocked: "+reason, code, reason)
	}
	for _, run := range en.runs {
		if run.Config.RuleID == rule.ID && (run.Status == "queued" || run.Status == "running" || en.active[run.ID] != nil) {
			return fmt.Errorf("conflict: rule already running")
		}
	}
	return nil
}

type scopeProfile struct {
	snapshot CredentialSnapshot
	err      error
}

// Read independent files outside the engine lock. All scans share the configured
// worker limit; each slot writes only its own indexed result.
func (en *Engine) readScopeProfiles(ctx context.Context, rule *Rule, targets []Target, owned map[string]*CredentialDisposition) []scopeProfile {
	profiles := make([]scopeProfile, len(targets))
	host, supported := en.host.(managedHost)
	var wg sync.WaitGroup
	group := selectedGroup(rule, targets)
	for i, t := range targets {
		d := owned[t.AuthID]
		if rule.boundOriginChanged(t.AuthID, objectType(t)) {
			continue
		}
		if !scopeMember(rule, t, d, group) || !needsScopeSnapshot(rule, t) || !rule.acceptsSubject(t) || t.identityConflict || t.QualityPaused || t.RetainedPause || t.ProviderProbeReason != "" && (disabledObject(t) || firstMissingModel(t, rule.Models) == "") || groupTargetBlocked(owned, t) && !activeDisposition(d) {
			continue
		}
		if activeDisposition(d) && d.State != "disabled" {
			continue
		}
		if disabledObject(t) && !canRecoverDisabled(rule, d) && (rule.LegacyScope || rule.TargetScope != scopeAllCredentials) {
			continue
		}
		if !supported || !en.Capabilities().ManagementSession {
			detail := sessionSkip()
			if disabledObject(t) && !supported {
				detail = ScopeSkipDetail{Code: "manual_disabled", Reason: "对象保持停用，当前宿主没有可核实的定向探测接口。"}
			}
			profiles[i].err = &scopeRejection{"management_session_required", detail}
			continue
		}
		select {
		case en.snapshotSlots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return profiles
		}
		wg.Go(func() {
			defer func() { <-en.snapshotSlots }()
			// A different worker may finish a credential action while this
			// profile is waiting for its shared read slot.
			en.mu.Lock()
			live := en.managed[t.AuthID]
			changed := live != d || disabledObject(t) && errorPolicyDisabled(live).Code != "" || en.localPause(RunConfig{AuthID: t.AuthID}) != "" || groupTargetBlocked(en.managed, t) && !activeDisposition(live)
			bindings := rule.RunConfig
			bindings.AuthID = t.AuthID
			hold := en.loginWaitLocked(bindings)
			en.mu.Unlock()
			if hold != nil {
				code, reason := hold.reason()
				profiles[i].err = rejectScope("blocked: credential authorization is changing", code, reason)
				return
			}
			if changed {
				profiles[i].err = rejectScope("blocked: credential ownership changed during scan", "ownership_changed", "等待读取期间凭证处置归属已变更。")
				return
			}
			profiles[i].snapshot, profiles[i].err = host.CredentialSnapshot(ctx, t.AuthID, t.AuthIndex)
		})
	}
	wg.Wait()
	return profiles
}

// Take at most one runnable member per queue rotation; unavailable siblings do not own workers.
func (en *Engine) rotateBatchLocked(batch string, ids []string, claim bool) *executionTask {
	live := ids[:0]
	var task *executionTask
	for i, id := range ids {
		if r := en.runs[id]; r == nil || r.Status != "queued" {
			continue
		}
		if claim && task == nil {
			task = en.claimRunLocked(id)
			if task != nil {
				// Usually the first child is free. Avoid scanning all remaining siblings per claim.
				if len(live) == 0 {
					live = ids[i+1:]
				} else {
					live = append(live, ids[i+1:]...)
				}
				break
			}
		}
		live = append(live, id)
	}
	if len(live) == 0 {
		delete(en.scopeBatches, batch)
	} else {
		en.scopeBatches[batch] = live
		en.queue <- batch
	}
	return task
}

func (en *Engine) visibleTargets(ctx context.Context) ([]Target, error) {
	coverage, ok := ctx.Value(providerInventoryStatusKey{}).(*providerInventoryStatus)
	if !ok {
		coverage = &providerInventoryStatus{Verified: true}
		ctx = context.WithValue(ctx, providerInventoryStatusKey{}, coverage)
	}
	en.mu.Lock()
	ctx = en.inventoryContextLocked(ctx)
	en.mu.Unlock()
	targets, err := en.host.Targets(ctx)
	if err != nil {
		return nil, err
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.observeMissingEntriesLocked(ctx, targets, coverage.Verified); err != nil {
		return nil, err
	}
	failures := en.reconcileDirectoryLocked(ctx, targets, nil)
	out := append([]Target(nil), targets...)
	for i := range out {
		if failure := failures[out[i].AuthID]; failure != nil {
			out[i].Reasons = append(append([]string(nil), out[i].Reasons...), "handling_state_unverified")
			out[i].HandlingStateReason = scrubError(failure)
		}
		out[i].ObjectType = objectType(out[i])
		out[i].RetainedPause = en.localPause(RunConfig{AuthID: out[i].AuthID}) != ""
		if d := en.managed[out[i].AuthID]; activeDisposition(d) {
			out[i].CredentialHandling = d.State
			if cause := errorPolicyCause(d); cause != "" && disabledObject(out[i]) {
				out[i].ErrorPolicyBlock = &ScopeSkipDetail{Code: "error_policy_disabled", AuthID: d.AuthID, Model: d.CatalogMissingModel, Reason: cause}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AuthID < out[j].AuthID })
	return out, nil
}

func (en *Engine) setScheduling(expected, enabled bool) error {
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	if en.cfg.SchedulingEnabled != expected {
		return fmt.Errorf("conflict: scheduling permission changed")
	}
	saved := en.cfg
	if err := en.store.Get("meta", "settings", &saved); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	saved.SchedulingEnabled = enabled
	if err := en.writeScheduleSettings(saved, !enabled); err != nil {
		return err
	}
	en.cfg.SchedulingEnabled = enabled
	return nil
}
