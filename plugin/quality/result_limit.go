package quality

import (
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/MoeForge/moego/json"
	bolt "go.etcd.io/bbolt"
)

const defaultMaxResults = 100

type historyClearResult struct {
	Deleted         int `json:"deleted"`
	Kept            int `json:"kept"`
	Archived        int `json:"archived,omitempty"`
	KeptForHandling int `json:"kept_for_handling,omitempty"`
}

type providerEvidenceRound struct{ rule, batch, group string }

func (r *Run) providerRound() providerEvidenceRound {
	s := r.Config.ManagedProbe
	if s == nil || s.ProviderMechanism != "group_disabled" || len(s.ProviderMembers) < 2 || r.Config.ScopeBatchID == "" || s.ProviderGroupID == "" {
		return providerEvidenceRound{}
	}
	return providerEvidenceRound{r.Config.RuleID, r.Config.ScopeBatchID, s.ProviderGroupID}
}

// A sibling may still need every answer after this worker has finished.
// Pending group intents point to the triggering run across restart and rule deletion.
func (en *Engine) protectProviderRoundLocked(held map[providerEvidenceRound]bool, r *Run) {
	key := r.providerRound()
	if key.batch == "" {
		return
	}
	d := en.managed[r.Config.AuthID]
	if r.CompletedAt == nil || en.active[r.ID] != nil ||
		d != nil && (d.RunID == r.ID || d.TriggerRunID == r.ID) && (d.State == "attention" || pendingCredentialAction(d.State) != "") {
		held[key] = true
	}
}

func (en *Engine) ruleHistoryEvidenceLocked() (confirmed, unresolved map[string]bool) {
	confirmed, unresolved = make(map[string]bool), make(map[string]bool)
	for _, d := range en.managed {
		var runs map[string]bool
		switch {
		case errorPolicyDisabled(d).Code != "":
			runs = confirmed
		case activeDisposition(d) && d.State != "disabled" && d.State != "deleted":
			runs = unresolved
		default:
			continue
		}
		for _, id := range []string{d.RunID, d.TriggerRunID} {
			if id != "" {
				runs[id] = true
			}
		}
	}
	held := make(map[providerEvidenceRound]bool)
	for _, r := range en.runs {
		en.protectProviderRoundLocked(held, r)
	}
	if len(held) != 0 {
		for id, r := range en.runs {
			if held[r.providerRound()] {
				unresolved[id] = true
			}
		}
	}
	return confirmed, unresolved
}

// Confirmed actions keep their recovery snapshot independently of run history.
// An outstanding write still needs its complete run and audit for reconciliation.
func (en *Engine) handlingEvidenceLocked(run *Run) bool {
	d := en.managed[run.Config.AuthID]
	if (errorPolicyDisabled(d).Code != "" || activeDisposition(d) && d.State == "attention") &&
		(d.RunID == run.ID || d.TriggerRunID == run.ID) {
		return true
	}
	return en.active[run.ID] != nil || (activeDisposition(d) && d.State != "disabled" && d.State != "deleted" && d.RunID == run.ID)
}

func (en *Engine) clearRuleHistory() (historyClearResult, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	var result historyClearResult
	if err := en.mutableLocked(); err != nil {
		return result, err
	}
	drop, cleared, liveBatches := map[string]bool{}, map[string]bool{}, map[string]bool{}
	archive := map[string]*Run{}
	confirmed, unresolved := en.ruleHistoryEvidenceLocked()
	for id, run := range en.runs {
		if run.Source != "scheduled_probe" {
			continue
		}
		r := en.rules[run.Config.RuleID]
		active := run.CompletedAt == nil || en.active[id] != nil
		handling := unresolved[id] || (r != nil && (r.ActionID != "" || r.BlockedAuthID != ""))
		if run.HistoryCleared {
			cleared[id] = true
			if !active && !handling && !confirmed[id] {
				drop[id] = true
			}
		} else if active || handling {
			liveBatches[run.Config.ScopeBatchID] = true
			result.Kept++
			if !active {
				result.KeptForHandling++
			}
		} else {
			cleared[id] = true
			if confirmed[id] {
				next := cloneRun(run)
				next.HistoryCleared = true
				archive[id] = next
				result.Archived++
			} else {
				drop[id] = true
				result.Deleted++
			}
		}
	}
	updates := map[string]*Rule{}
	for id, r := range en.rules {
		clearBatch := r.LastScopeBatch != "" && !liveBatches[r.LastScopeBatch]
		clearLinks := r.LastScopeDispatch != nil && slices.ContainsFunc(r.LastScopeDispatch.Details, func(d ScopeSkipDetail) bool { return cleared[d.RunID] })
		if !cleared[r.LastRun] && !clearBatch && !clearLinks {
			continue
		}
		n := cloneRule(r)
		if cleared[r.LastRun] {
			n.LastRun = ""
		}
		if clearBatch {
			n.LastScopeBatch = ""
		}
		if clearLinks {
			for i, detail := range n.LastScopeDispatch.Details {
				if cleared[detail.RunID] {
					n.LastScopeDispatch.Details[i].RunID = ""
				}
			}
		}
		updates[id] = n
	}
	if len(drop) == 0 && len(archive) == 0 && len(updates) == 0 {
		return result, nil
	}
	err := en.store.Write(func(tx *bolt.Tx) error {
		for id, run := range archive {
			if err := put(tx, "runs", id, run); err != nil {
				return err
			}
		}
		for id, r := range updates {
			if err := put(tx, "rules", id, r); err != nil {
				return err
			}
		}
		return deleteRuleRecords(tx, drop, "", nil, nil)
	})
	if err != nil {
		return historyClearResult{}, err
	}
	for id := range drop {
		delete(en.runs, id)
	}
	for id, run := range archive {
		en.runs[id] = run
	}
	for id, r := range updates {
		*en.rules[id] = *r
	}
	return result, nil
}

func deleteRuleRecords(tx *bolt.Tx, drop map[string]bool, deletedRule string, keepRuns, keepActions map[string]bool) error {
	for id := range drop {
		if err := tx.Bucket([]byte("runs")).Delete([]byte(id)); err != nil {
			return err
		}
	}
	b := tx.Bucket([]byte("audit"))
	var keys []string
	if err := b.ForEach(func(k, v []byte) error {
		var a Audit
		if err := json.Unmarshal(v, &a); err != nil {
			return err
		}
		if !keepRuns[a.RunID] && !keepActions[a.ActionID] && (drop[a.RunID] || deletedRule != "" && a.RuleID == deletedRule) {
			keys = append(keys, string(k))
		}
		return nil
	}); err != nil {
		return err
	}
	for _, k := range keys {
		if err := b.Delete([]byte(k)); err != nil {
			return err
		}
	}
	return nil
}

func prepareResultLimit(in, old *Rule) error {
	if in.RetentionDays != 0 && (old == nil || old.RetentionDays != in.RetentionDays) {
		return fmt.Errorf("invalid_request: use max_results instead of retention_days")
	}
	if in.RetentionMode == "" && in.MaxResults == 0 && old != nil {
		in.RetentionMode = old.RetentionMode
	}
	switch in.RetentionMode {
	case "manual":
		if in.MaxResults != 0 {
			return fmt.Errorf("invalid_request: manual retention cannot specify max_results")
		}
		in.RetentionDays = 0
		return nil
	case "", "count":
	default:
		return fmt.Errorf("invalid_request: unknown retention_mode")
	}
	if in.MaxResults == 0 {
		switch {
		case old != nil && old.MaxResults > 0:
			in.MaxResults = old.MaxResults
		case old != nil && old.RetentionDays > 0 && in.RetentionMode == "":
			in.RetentionDays = old.RetentionDays
			return nil
		default:
			in.MaxResults = defaultMaxResults
		}
	}
	if in.MaxResults < 1 || in.MaxResults > 200 {
		return fmt.Errorf("invalid_request: max_results must be 1..200")
	}
	in.RetentionDays = 0
	return nil
}

// Each account in a dynamic rule has its own result window. Keep sample ordinals
// stable across a partially retained round so a stale review cannot hit another sample.
func (en *Engine) pruneResultCountsLocked(ruleID string, authIDs ...string) error {
	type key struct{ rule, auth string }
	groups := make(map[key][]*Run)
	held := make(map[providerEvidenceRound]bool)
	wanted := make(map[string]bool, len(authIDs))
	for _, id := range authIDs {
		if id != "" {
			wanted[id] = true
		}
	}
	for _, r := range en.runs {
		en.protectProviderRoundLocked(held, r)
		if r.Source != "scheduled_probe" || r.HistoryCleared || r.CompletedAt == nil || r.Status == "running" || r.Status == "queued" || (ruleID != "" && r.Config.RuleID != ruleID) || (len(wanted) != 0 && !wanted[r.Config.AuthID]) {
			continue
		}
		rule := en.rules[r.Config.RuleID]
		if rule == nil || rule.MaxResults < 1 || rule.ActionID != "" || rule.BlockedAuthID != "" {
			continue
		}
		k := key{r.Config.RuleID, r.Config.AuthID}
		groups[k] = append(groups[k], r)
	}
	var drops []string
	var trims []*Run
	for k, list := range groups {
		rule := en.rules[k.rule]
		remaining := rule.MaxResults
		sort.Slice(list, func(i, j int) bool {
			// The committed last-run link establishes completion order even
			// when the clock repeats or moves backwards.
			iLast, jLast := list[i].ID == rule.LastRun, list[j].ID == rule.LastRun
			if iLast != jLast {
				return iLast
			}
			if list[i].CompletedAt.Equal(*list[j].CompletedAt) {
				return list[i].ID > list[j].ID
			}
			return list[i].CompletedAt.After(*list[j].CompletedAt)
		})
		for _, r := range list {
			weight := max(1, len(r.Samples)) // An unsuccessful run without a sample is one diagnostic result.
			switch {
			case remaining >= weight:
				remaining -= weight
			case en.handlingEvidenceLocked(r) || held[r.providerRound()]:
				remaining = 0
			case remaining == 0:
				drops = append(drops, r.ID)
			default:
				n, err := trimResultWindow(r, remaining)
				if err != nil {
					return err
				}
				trims = append(trims, n)
				remaining = 0
			}
		}
	}
	if len(trims) == 0 && len(drops) == 0 {
		return nil
	}
	if err := en.store.Write(func(tx *bolt.Tx) error {
		for _, r := range trims {
			if err := put(tx, "runs", r.ID, r); err != nil {
				return err
			}
		}
		for _, id := range drops {
			if err := tx.Bucket([]byte("runs")).Delete([]byte(id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for _, r := range trims {
		en.runs[r.ID] = r
	}
	for _, id := range drops {
		delete(en.runs, id)
	}
	return nil
}

// Copy the retained answers and their original review positions. Controls run
// first, so controls for unreached subjects and surviving duplicate slots stay.
func trimResultWindow(r *Run, keep int) (*Run, error) {
	removed := len(r.Samples) - keep
	if r.PrunedSamples < 0 || r.PrunedSamples > math.MaxInt-removed {
		return nil, fmt.Errorf("unavailable: stored sample ordinals exceed the supported range")
	}
	next := *r
	next.Samples = slices.Clone(r.Samples[removed:])
	next.Controls = nil
	next.PrunedSamples += removed
	next.PrunedSlots = make([]SampleSlot, len(r.PrunedSlots), len(r.PrunedSlots)+removed)
	copy(next.PrunedSlots, r.PrunedSlots)
	var removedControls map[SampleSlot]struct{}
	if len(r.Controls) != 0 {
		removedControls = make(map[SampleSlot]struct{}, removed)
	}
	for _, s := range r.Samples[:removed] {
		slot := SampleSlot{Model: s.Model, Index: s.Index}
		next.PrunedSlots = append(next.PrunedSlots, slot)
		if removedControls != nil {
			removedControls[slot] = struct{}{}
		}
	}
	if removedControls != nil {
		for _, s := range next.Samples {
			delete(removedControls, SampleSlot{Model: s.Model, Index: s.Index})
		}
		for _, s := range r.Controls {
			if _, removed := removedControls[SampleSlot{Model: s.Model, Index: s.Index}]; !removed {
				next.Controls = append(next.Controls, s)
			}
		}
	}
	return &next, nil
}
