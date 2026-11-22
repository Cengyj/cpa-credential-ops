package quality

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

type ActionState struct {
	Status string  `json:"status"`
	Action *Action `json:"action"`
}

func (h *NativeHost) ActionStatus(ctx context.Context, id string) (ActionState, error) {
	if err := ctx.Err(); err != nil {
		return ActionState{}, err
	}
	return ActionState{}, unsupportedQuality()
}

// Reconcile repairs state uncertainty without ever starting a model call or resuming a rule.
func (en *Engine) Reconcile(ctx context.Context) error {
	if !en.Capabilities().QualityPause {
		return nil
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	if en.closed {
		return nil
	}
	if en.fault != nil {
		return en.fault
	}
	actions, err := en.host.Actions(ctx)
	if err != nil {
		return err
	}
	targets, err := en.host.Targets(ctx)
	if err != nil {
		return err
	}
	active := map[string]Action{}
	paused := map[string]bool{}
	for _, t := range targets {
		if t.QualityPaused {
			paused[t.AuthID] = true
		}
	}
	for _, a := range actions {
		active[a.ActionID] = a
		paused[a.AuthID] = true
	}
	for _, a := range actions {
		r := en.rules[a.RuleID]
		if a.OwnerPlugin == "model-quality" && r != nil && r.AuthID == a.AuthID && r.ActionID == "" {
			c := changes()
			held := cloneRule(r)
			held.ActionID = a.ActionID
			held.ActionRevision = a.ActionRevision
			held.ActionRuleRevision = a.RuleRevision
			held.IdentityVersion = a.IdentityVersion
			held.IntentRevision = 0
			c.rules[r.ID] = held
			en.haltChangesLocked(c, a.AuthID, "", "quality_paused")
			c.audits = append(c.audits, en.audit("reconciled_pause", r.ID, "", a.ActionID, "host-owned pause adopted without changing action binding"))
			if err = en.commitLocked(c); err != nil {
				return err
			}
		}
	}
	var problems []error
	statusHost, hasStatus := en.host.(interface {
		ActionStatus(context.Context, string) (ActionState, error)
	})
	for _, r := range en.rules {
		if r.ActionID == "" {
			continue
		}
		a, exists := active[r.ActionID]
		if exists {
			if !matchesActionBinding(r, &a) {
				problems = append(problems, fmt.Errorf("action binding conflict for rule %s", r.ID))
			}
			continue
		}
		if !hasStatus {
			continue
		}
		state, statusErr := statusHost.ActionStatus(ctx, r.ActionID)
		if statusErr != nil {
			problems = append(problems, statusErr)
			continue
		}
		if (state.Status == "active" || state.Status == "released") && !matchesActionBinding(r, state.Action) {
			problems = append(problems, fmt.Errorf("action status binding conflict for rule %s", r.ID))
			continue
		}
		if state.Status == "active" {
			paused[r.AuthID] = true
			continue
		}
		if state.Status != "released" && state.Status != "absent" {
			problems = append(problems, fmt.Errorf("unknown action reconciliation state"))
			continue
		}
		next := cloneRule(r)
		c := changes()
		if state.Status == "absent" {
			var intent Action
			intentErr := en.store.Get("meta", "intent:"+r.ActionID, &intent)
			run := en.runs[intent.EvidenceRunID]
			valid := intentErr == nil && r.IntentRevision != 0 && r.Revision == r.IntentRevision && r.ActionRuleRevision == intent.RuleRevision && en.cfg.AutomaticPause && r.AutomaticPause && run != nil && RoundVerdict(run) == "incorrect" && en.now().Sub(run.CreatedAt) <= 5*time.Minute
			if valid {
				result, pauseErr := en.host.Pause(ctx, intent)
				if pauseErr != nil {
					problems = append(problems, pauseErr)
					continue
				}
				next.ActionRevision = result.ActionRevision
				paused[next.AuthID] = true
				c.rules[r.ID] = next
				c.audits = append(c.audits, en.audit("reconciled_pause", r.ID, intent.EvidenceRunID, intent.ActionID, "durable halt retained; original pause intent confirmed"))
				if err = en.commitLocked(c); err != nil {
					return err
				}
				continue
			}
		}
		actionID := next.ActionID
		next.ActionID = ""
		next.ActionRevision = 0
		next.ActionRuleRevision = 0
		next.IntentRevision = 0
		next.Enabled = false
		next.Revision++
		resetRule(next)
		next.Status = ruleStatus(next)
		c.rules[r.ID] = next
		c.audits = append(c.audits, en.audit("reconciled_"+state.Status, r.ID, "", actionID, "host confirms action state; rule remains stopped"))
		if err = en.commitLocked(c); err != nil {
			return err
		}
	}
	// Pending intents remain blockers until the host confirms their final disposition.
	for _, r := range en.rules {
		if r.ActionID != "" {
			paused[r.AuthID] = true
		}
	}
	c := changes()
	pausedIDs := make([]string, 0, len(paused))
	for authID := range paused {
		pausedIDs = append(pausedIDs, authID)
	}
	sort.Strings(pausedIDs)
	for _, authID := range pausedIDs {
		en.haltChangesLocked(c, authID, "", "quality_paused")
	}
	for id, r := range en.rules {
		next := c.rules[id]
		if next == nil {
			next = cloneRule(r)
		}
		marker := ""
		for _, authID := range dependencies(next.RunConfig) {
			if paused[authID] {
				marker = authID
				break
			}
		}
		if next.BlockedAuthID != marker {
			next.BlockedAuthID = marker
			next.Enabled = false
			if c.rules[id] == nil {
				next.Revision++
			}
			resetRule(next)
			next.Status = ruleStatus(next)
			c.rules[id] = next
		}
	}
	if err = en.commitLocked(c); err != nil {
		return err
	}
	return errors.Join(problems...)
}

func matchesActionBinding(r *Rule, a *Action) bool {
	return a != nil && a.OwnerPlugin == "model-quality" && a.ActionID == r.ActionID && a.RuleID == r.ID && a.AuthID == r.AuthID && a.RuleRevision == r.ActionRuleRevision && a.ActionRevision == r.ActionRevision && a.IdentityVersion == r.IdentityVersion
}

func (en *Engine) Prune(now time.Time) error {
	en.mu.Lock()
	defer en.mu.Unlock()
	if en.closed {
		return nil
	}
	if en.fault != nil {
		return en.fault
	}
	policy := historyRetention{
		before:   now.Add(-time.Duration(en.cfg.RetentionDays) * 24 * time.Hour),
		keepRuns: map[string]bool{}, keepActions: map[string]bool{}, cutoffs: map[string]time.Time{},
	}
	for _, rule := range en.rules {
		if rule.LastRun != "" {
			policy.keepRuns[rule.LastRun] = true
		}
		if rule.ActionID != "" {
			policy.keepActions[rule.ActionID] = true
			var a Action
			err := en.store.Get("meta", "intent:"+rule.ActionID, &a)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("read retained pause intent: %w", err)
			}
			if a.EvidenceRunID != "" {
				policy.cutoffs[a.EvidenceRunID] = time.Time{}
			}
		}
	}
	for _, d := range en.managed {
		if d.State == "attention" || pendingCredentialAction(d.State) != "" {
			if d.RunID != "" {
				policy.cutoffs[d.RunID] = time.Time{}
			}
			if d.TriggerRunID != "" {
				policy.cutoffs[d.TriggerRunID] = time.Time{}
			}
			if d.ActionID != "" {
				policy.keepActions[d.ActionID] = true
			}
		}
	}
	if err := en.pruneResultCountsLocked("", ""); err != nil {
		return err
	}
	confirmed, unresolved := en.ruleHistoryEvidenceLocked()
	for id, r := range en.runs {
		if _, protected := policy.cutoffs[id]; protected {
			continue
		}
		if r.HistoryCleared {
			if confirmed[id] || unresolved[id] {
				policy.cutoffs[id] = time.Time{}
			} else {
				policy.cutoffs[id] = policy.before
			}
			continue
		}
		if unresolved[id] || en.handlingEvidenceLocked(r) {
			// The zero cutoff protects both the run and its linked audit entries.
			policy.cutoffs[id] = time.Time{}
			continue
		}
		if rule := en.rules[r.Config.RuleID]; rule != nil {
			if rule.ActionID != "" || rule.BlockedAuthID != "" || rule.MaxResults > 0 || rule.RetentionMode == "manual" {
				policy.cutoffs[id] = time.Time{}
			} else if rule.RetentionDays > 0 {
				policy.cutoffs[id] = now.Add(-time.Duration(rule.RetentionDays) * 24 * time.Hour)
			}
		}
	}
	deleted, err := en.store.prune(policy, en.runs)
	if err != nil {
		return err
	}
	for _, id := range deleted {
		delete(en.runs, id)
	}

	return nil
}
func (en *Engine) storageAdmission() error {
	return en.store.db.View(func(tx *bolt.Tx) error {
		// bbolt keeps released pages in the file for subsequent allocations.
		db := tx.DB()
		free := int64(db.Stats().FreePageN) * int64(db.Info().PageSize)
		if tx.Size()-free >= en.cfg.MaxStorageBytes {
			return fmt.Errorf("storage_limit: retention or archival required")
		}
		return nil
	})
}
