package quality

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type executionTask struct {
	id      string
	ctx     context.Context
	cancel  context.CancelFunc
	frozen  Frozen
	source  string
	authIDs []string
}

// notifyWorkersLocked wakes idle workers after admission or dependency release.
func (en *Engine) notifyWorkersLocked() {
	if en.queueChanged != nil {
		close(en.queueChanged)
	}
	en.queueChanged = make(chan struct{})
}

// claimRunLocked reserves every dependency together; a blocked job never owns a worker.
func (en *Engine) claimRunLocked(id string) *executionTask {
	r := en.runs[id]
	if r == nil || r.Status != "queued" || en.closed || en.fault != nil {
		return nil
	}
	authIDs := dependencies(r.Config.RunConfig)
	for _, authID := range authIDs {
		if _, busy := en.busyAuth[authID]; busy {
			return nil
		}
	}
	next := cloneRun(r)
	next.Status = "running"
	c := changes()
	c.runs[id] = next
	if err := en.commitLocked(c); err != nil {
		return nil
	}
	if en.busyAuth == nil {
		en.busyAuth = map[string]struct{}{}
	}
	for _, authID := range authIDs {
		en.busyAuth[authID] = struct{}{}
	}
	ctx, cancel := context.WithCancel(en.ctx)
	en.active[id] = cancel
	return &executionTask{id: id, ctx: ctx, cancel: cancel, frozen: cloneFrozen(r.Config), source: r.Source, authIDs: authIDs}
}

// nextRunnableLocked rotates unavailable jobs without polling or exceeding queue capacity.
func (en *Engine) nextRunnableLocked() *executionTask {
	for pending := len(en.queue); pending > 0; pending-- {
		id := <-en.queue
		if ids, ok := en.scopeBatches[id]; ok {
			if task := en.rotateBatchLocked(id, ids, true); task != nil {
				return task
			}
			continue
		}
		r := en.runs[id]
		if r == nil || r.Status != "queued" {
			continue
		}
		if task := en.claimRunLocked(id); task != nil {
			return task
		}
		en.queue <- id
	}
	return nil
}

// compactQueueLocked frees terminal entries while preserving the remaining FIFO order.
func (en *Engine) compactQueueLocked() {
	for pending := len(en.queue); pending > 0; pending-- {
		id := <-en.queue
		if ids, ok := en.scopeBatches[id]; ok {
			en.rotateBatchLocked(id, ids, false)
			continue
		}
		if r := en.runs[id]; r != nil && r.Status == "queued" {
			en.queue <- id
		}
	}
}

func (en *Engine) worker() {
	defer en.wg.Done()
	for {
		en.mu.Lock()
		if en.closed || en.fault != nil {
			en.mu.Unlock()
			return
		}
		task := en.nextRunnableLocked()
		if en.fault != nil {
			en.mu.Unlock()
			return
		}
		if en.queueChanged == nil {
			en.queueChanged = make(chan struct{})
		}
		changed := en.queueChanged
		en.mu.Unlock()
		if task != nil {
			en.executeTask(task)
			continue
		}
		select {
		case <-en.ctx.Done():
			return
		case <-changed:
		}
	}
}
func (en *Engine) background() {
	defer en.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastReconcile, lastRetention time.Time
	for {
		select {
		case <-en.ctx.Done():
			return
		case event := <-en.events:
			if e := en.store.Put("usage", event.ID, event); e != nil {
				en.dropped.Add(1)
			}
		case now := <-ticker.C:
			if en.Capabilities().QualityPause && now.Sub(lastReconcile) >= 30*time.Second {
				if err := en.Reconcile(en.ctx); err != nil {
					log.Debug("model-quality: reconciliation remains pending")
				}
				lastReconcile = now
			}
			if now.Sub(lastRetention) >= time.Hour {
				if err := en.Prune(now.UTC()); err != nil {
					log.WithError(err).Error("model-quality: retention failed")
				}
				lastRetention = now
			}
			en.Tick(now.UTC())
		}
	}
}
func (en *Engine) Tick(now time.Time) {
	en.mu.Lock()
	if en.closed || en.fault != nil || (!en.cfg.SchedulingEnabled && len(en.timers) == 0) {
		en.mu.Unlock()
		return
	}
	if en.Capabilities().QualityPause {
		targets, err := en.host.Targets(en.ctx)
		if err != nil {
			en.mu.Unlock()
			return
		}
		for _, t := range targets {
			if t.QualityPaused {
				if err = en.haltCredentialLocked(t.AuthID, "", "quality_paused"); err != nil {
					en.mu.Unlock()
					return
				}
			}
		}
	}
	due, err := en.takeDueRulesLocked(now)
	inventoryCtx := context.WithValue(en.inventoryContextLocked(en.ctx), scheduledInventoryGeneration{}, en.loginGeneration)
	en.mu.Unlock()
	if err != nil {
		return
	}
	if len(due) == 0 {
		return
	}
	status := &providerInventoryStatus{Verified: true}
	read := sync.OnceValues(func() ([]Target, error) {
		return en.host.Targets(context.WithValue(inventoryCtx, providerInventoryStatusKey{}, status))
	})
	inventory := func(ctx context.Context) ([]Target, error) {
		targets, err := read()
		if coverage, ok := ctx.Value(providerInventoryStatusKey{}).(*providerInventoryStatus); ok {
			coverage.Verified = status.Verified
			coverage.Reason = status.Reason
		}
		return targets, err
	}
	for _, scheduled := range due {
		if _, err = en.dispatchOccurrence(inventoryCtx, scheduled.id, scheduled.revision, true, inventory, &scheduled); err != nil {
			en.mu.Lock()
			r := en.rules[scheduled.id]
			if !en.closed && en.fault == nil && r != nil && r.Revision == scheduled.revision && en.scheduleGeneration == scheduled.generation &&
				ruleScheduled(r) && en.timerAllowed(r) && scheduled.current(r) && en.loginWaitLocked(r.RunConfig) == nil {
				// No worker owns this occurrence. Delay the next read/admission
				// attempt, including for SDK errors outside the directory request.
				en.deferOccurrenceLocked(r, &scheduled, "后台暂未能受理本轮，正在等待重新核实。", false)
			}
			en.mu.Unlock()
			log.WithField("rule_id", scheduled.id).Debug("model-quality: scheduled run not admitted")
		}
	}
}

type scheduledInventoryGeneration struct{}

type scheduledRule struct {
	id         string
	revision   uint64
	generation uint64
	due        time.Time
	checked    time.Time
	catchup    bool
	remainder  bool
}

// The receipt and each child's admission share a transaction. Both survive
// restart independently of result history and never authorize external writes.
type pendingRuleSchedule struct {
	Due              time.Time         `json:"due"`
	Accepted         []string          `json:"accepted_auth_ids,omitempty"`
	WaitingLogins    map[string]string `json:"waiting_logins,omitempty"` // subject -> blocking credential
	DirectoryPending bool              `json:"directory_pending,omitempty"`
}

func (p *pendingRuleSchedule) waitsForDirectory() bool {
	// Older receipts contained only Due and Accepted and waited for inventory.
	return p.DirectoryPending || len(p.WaitingLogins) == 0
}

func (en *Engine) remainderReadyLocked(p *pendingRuleSchedule, capability HostCapabilities) bool {
	if p == nil {
		return false
	}
	if p.waitsForDirectory() && (!capability.CredentialManagement || capability.ManagementSession) {
		return true
	}
	for _, credential := range p.WaitingLogins {
		if hold := en.loginHolds[credential]; hold != nil && hold.State == "completed" {
			return true
		}
	}
	return false
}

func canRunWithoutManagement(r *Rule) bool {
	return dynamicScope(r.TargetScope) && !managedRule(r) && contains(ruleTypes(r), objectOAuth)
}

func (s *scheduledRule) current(r *Rule) bool {
	if s.remainder {
		return r.PendingSchedule != nil && r.PendingSchedule.Due.Equal(s.due)
	}
	return r.NextRun.Equal(s.due)
}

type scheduleRetry struct {
	revision       uint64
	due            time.Time
	after          time.Time
	delay          time.Duration
	waitManagement bool
	reason         string
}

func (en *Engine) deferOccurrenceLocked(r *Rule, occurrence *scheduledRule, reason string, waitManagement bool) {
	if en.scheduleRetry == nil {
		en.scheduleRetry = make(map[string]scheduleRetry)
	}
	retry := en.scheduleRetry[r.ID]
	delay := 2 * time.Second
	if retry.revision == r.Revision && retry.due.Equal(occurrence.due) {
		delay = max(delay, min(time.Minute, retry.delay*2))
	}
	en.scheduleRetry[r.ID] = scheduleRetry{revision: r.Revision, due: occurrence.due, delay: delay,
		after: occurrence.checked.Add(delay + rand.N(delay/4)), waitManagement: waitManagement, reason: reason}
}

// Advance the durable cursor in the very transaction which admits the round.
// A process interruption on either side leaves a due occurrence or its receipt.
func (en *Engine) consumeOccurrence(c *changeSet, next *Rule, occurrence *scheduledRule) {
	if occurrence == nil {
		return
	}
	now := en.now()
	if now.Before(occurrence.checked) {
		now = occurrence.checked
	}
	if !occurrence.remainder {
		next.NextRun = nextRun(*next, now)
	}
	c.rules[next.ID] = next
	if occurrence.catchup && next.PendingSchedule == nil {
		c.audits = append(c.audits, en.audit("rule_timer_catchup", next.ID, "", "",
			"Fresh round for missed schedule "+occurrence.due.UTC().Format(time.RFC3339)))
	}
}

func (en *Engine) takeDueRulesLocked(now time.Time) ([]scheduledRule, error) {
	due := []scheduledRule{}
	c := changes()
	capability := en.Capabilities()
	for _, r := range en.rules {
		if en.unsupportedRule(r) || !en.timerAllowed(r) || !ruleScheduled(r) || r.NextRun.IsZero() || en.loginWaitLocked(r.RunConfig) != nil {
			continue
		}
		normalDue := !r.NextRun.After(now)
		remainderReady := en.remainderReadyLocked(r.PendingSchedule, capability)
		if !normalDue && !remainderReady {
			continue
		}
		if nextRun(*r, now).IsZero() {
			n := cloneRule(r)
			n.NextRun = time.Time{}
			c.rules[r.ID] = n
			c.audits = append(c.audits, en.audit("rule_schedule_invalid", r.ID, "", "", "invalid stored schedule; no run admitted"))
			continue
		}
		occurrence := scheduledRule{id: r.ID, revision: r.Revision, generation: en.scheduleGeneration, due: r.NextRun, checked: now,
			catchup: !r.NextRun.After(en.loadedAt) || r.PendingSchedule != nil, remainder: !normalDue}
		if occurrence.remainder {
			occurrence.due = r.PendingSchedule.Due
		}
		// A current, accepted manual or scheduled round already covers this
		// occurrence. Never queue a second round as soon as that one finishes.
		if en.hasCurrentRoundLocked(r) {
			if normalDue {
				n := cloneRule(r)
				n.NextRun = nextRun(*r, now)
				c.rules[r.ID] = n
			}
			continue
		}
		retry := en.scheduleRetry[r.ID]
		retrying := retry.revision == r.Revision && retry.due.Equal(occurrence.due)
		occurrence.catchup = occurrence.catchup || retrying
		if capability.CredentialManagement && !capability.ManagementSession && !canRunWithoutManagement(r) && (managedRule(r) || scopeNeedsProviderDirectory(r, nil) || retrying && retry.waitManagement) {
			if !retrying || !retry.waitManagement {
				en.deferOccurrenceLocked(r, &occurrence, sessionSkip().Reason, true)
			}
			continue
		}
		if retrying && !retry.waitManagement && now.Before(retry.after) || en.scanning[r.ID] {
			continue
		}
		due = append(due, occurrence)
	}
	if err := en.commitLocked(c); err != nil {
		return nil, err
	}
	return due, nil
}

func (en *Engine) hasCurrentRoundLocked(rule *Rule) bool {
	if len(en.queue) == 0 && len(en.active) == 0 {
		return false
	}
	for _, r := range en.runs {
		if r.Config.RuleID == rule.ID && r.Config.RuleRevision == rule.Revision && !r.CancelRequested && (r.Status == "queued" || r.Status == "running") {
			return true
		}
	}
	return false
}
func (en *Engine) Usage(event UsageEvent) {
	if en.ctx != nil && en.ctx.Err() != nil {
		en.dropped.Add(1)
		return
	}
	if len(event.ID) > 128 || len(event.Model) > 256 || len(event.Provider) > 128 {
		en.dropped.Add(1)
		return
	}
	if event.ID == "" {
		return
	}
	event.Source = trafficSource(event.Source)
	select {
	case en.events <- event:
	default:
		en.dropped.Add(1)
	}
}
