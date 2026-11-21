package quality

import (
	"context"
	"errors"
	"slices"
)

// New routes in a retained group cannot escape an unresolved group action by
// receiving a new ID. Existing members also carry their own exact snapshot.
func groupGuard(records map[string]*CredentialDisposition, t Target) *CredentialDisposition {
	if t.ProviderGroupID == "" {
		return nil
	}
	var confirmed *CredentialDisposition
	for _, d := range records {
		if activeDisposition(d) && d.Snapshot.ProviderMechanism == "group_disabled" && d.Snapshot.ProviderGroupID == t.ProviderGroupID {
			if d.State != "disabled" {
				return d
			}
			confirmed = d
		}
	}
	return confirmed
}

// A group enablement is observed as one local transition. Check the complete
// current group independently of the old recovery plan. Unmatched historical
// routes keep their records; they never grant new members recovery authority.
func (en *Engine) observeEnabledProviderGroupLocked(ctx context.Context, targets []Target, old *CredentialDisposition, read snapshotReader) error {
	if read == nil || !en.Capabilities().ManagementSession {
		return errors.New("management_session_required: background management connection is not ready")
	}
	byID := make(map[string]Target, len(targets))
	for _, t := range targets {
		if _, duplicate := byID[t.AuthID]; duplicate {
			return errors.New("blocked: provider catalog contains duplicate identities")
		}
		byID[t.AuthID] = t
	}
	anchor, present := byID[old.AuthID]
	if !present || !anchor.identityVerified || anchor.identityConflict || !ruleSubjectEnabled(anchor) {
		return errors.New("blocked: current provider group identity cannot be verified")
	}
	current, err := read(ctx, anchor.AuthID, anchor.AuthIndex)
	if err != nil {
		return err
	}
	if current.Disabled || len(current.ProviderMembers) == 0 || !sameCredentialEntry(current, old.Snapshot) || !contains(current.ProviderMembers, anchor.AuthID) {
		return errors.New("blocked: current provider group identity or enablement cannot be verified")
	}
	for _, d := range en.managed {
		if activeDisposition(d) && d.Snapshot.ProviderGroupID == current.ProviderGroupID && d.State != "disabled" {
			return errors.New("blocked: provider group contains an unconfirmed credential action")
		}
	}
	update := changes()
	for _, id := range current.ProviderMembers {
		t, present := byID[id]
		if !present || !t.identityVerified || t.identityConflict || !ruleSubjectEnabled(t) ||
			t.QualityPaused || t.RetainedPause || en.localPause(RunConfig{AuthID: id}) != "" || t.ProviderGroupID != current.ProviderGroupID {
			return errors.New("blocked: current provider group enablement cannot be verified for every current member")
		}
		s, err := read(ctx, id, t.AuthIndex)
		if err != nil {
			return err
		}
		if s.Disabled || !sameProviderGroupPlan(s, current) {
			return errors.New("blocked: provider group identity, membership or enablement changed during the read")
		}
		d := en.managed[id]
		if !activeDisposition(d) {
			continue
		}
		if d.State != "disabled" || !sameCredentialEntry(s, d.Snapshot) {
			return errors.New("blocked: current provider entry has an unresolved association")
		}
		next := *d
		next.State, next.UpdatedAt = "externally_enabled", en.now()
		update.dispositions = append(update.dispositions, &next)
		en.cancelManagedRuns(update, id, "")
		for ruleID, r := range en.rules {
			if ruleID == d.RuleID || usesCredential(r.RunConfig, id) {
				copy := cloneRule(r)
				copy.FailCount, copy.EvidenceSignature = 0, ""
				update.rules[ruleID] = copy
			}
		}
	}
	update.audits = append(update.audits, en.audit("credential_external_enable", old.RuleID, old.RunID, old.ActionID,
		"CPA enablement confirmed for the complete current provider group; matching ordinary sources retired without a CPA write"))
	return en.commitLocked(update)
}

func providerWriteAllowed(r *Rule, s CredentialSnapshot) bool {
	if r == nil || r.LegacyProviderObservation || !contains(r.ObjectTypes, "provider") {
		return false
	}
	switch s.ProviderMechanism {
	case "excluded_models":
		return true
	case "group_disabled":
		return s.ProviderGroupID != "" && contains(r.AuthorizedGroups, s.ProviderGroupID)
	}
	return false
}

// For ordinary answer decisions the entire group must have received the same
// plan in this dispatch. A skipped or missing route prevents both operations.
func (en *Engine) providerRoundVerdict(run *Run, snap CredentialSnapshot) string {
	if snap.ProviderMechanism != "group_disabled" || len(snap.ProviderMembers) == 1 {
		return managedRoundVerdict(run)
	}
	if run.Config.ScopeBatchID == "" || len(snap.ProviderMembers) == 0 {
		return "unknown"
	}
	found := make(map[string]bool, len(snap.ProviderMembers))
	verdict := "correct"
	for _, child := range en.runs {
		cfg := &child.Config
		if cfg.RuleID != run.Config.RuleID || cfg.RuleRevision != run.Config.RuleRevision || cfg.ScopeBatchID != run.Config.ScopeBatchID || !contains(snap.ProviderMembers, cfg.AuthID) {
			continue
		}
		s := cfg.ManagedProbe
		if found[cfg.AuthID] || s == nil || !sameProviderGroupPlan(*s, snap) || cfg.Samples != run.Config.Samples ||
			!slices.Equal(cfg.Models, run.Config.Models) || cfg.Question != run.Config.Question || child.CancelRequested || child.BlockReason != "" {
			return "unknown"
		}
		found[cfg.AuthID] = true
		switch managedRoundVerdict(child) {
		case "incorrect":
			verdict = "incorrect"
		case "correct":
		default:
			return "unknown"
		}
	}
	if len(found) != len(snap.ProviderMembers) {
		return "unknown"
	}
	return verdict
}

func sameProviderGroupPlan(a, b CredentialSnapshot) bool {
	return a.ProviderGroupID != "" && a.ProviderGroupID == b.ProviderGroupID && a.ProviderMechanism == b.ProviderMechanism &&
		a.Fingerprint != "" && a.Fingerprint == b.Fingerprint && a.Disabled == b.Disabled &&
		slices.Equal(a.ProviderMembers, b.ProviderMembers)
}

// Even an early upstream error requires authority over every affected route.
// Read back exact membership and identities immediately before journaling.
func (en *Engine) providerWriteSnapshots(ctx context.Context, h managedHost, expected CredentialSnapshot, r *Rule, errorUpgrade bool) ([]CredentialSnapshot, bool) {
	targets, err := en.host.Targets(ctx)
	if err != nil {
		return nil, false
	}
	ids := []string{expected.AuthID}
	if expected.ProviderMechanism == "group_disabled" {
		ids = expected.ProviderMembers
	}
	if len(ids) == 0 || !contains(ids, expected.AuthID) {
		return nil, false
	}
	current := make([]CredentialSnapshot, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, t := range targets {
		if !contains(ids, t.AuthID) {
			continue
		}
		if seen[t.AuthID] || !t.IsProvider || providerBindingProblem(t) != nil || t.ProviderGroupID != expected.ProviderGroupID ||
			t.QualityPaused || t.RetainedPause || en.localPause(RunConfig{AuthID: t.AuthID}) != "" {
			return nil, false
		}
		if t.AuthID == expected.AuthID && r.subjectBindingProblem(t, targets) != nil {
			return nil, false
		}
		seen[t.AuthID] = true
		s, err := h.CredentialSnapshot(ctx, t.AuthID, t.AuthIndex)
		if err != nil || s.AuthID != t.AuthID || s.AuthIndex != t.AuthIndex || s.Disabled != t.ManualDisabled || !sameProviderGroupPlan(s, expected) {
			return nil, false
		}
		if s.AuthID == expected.AuthID && !sameSnapshot(s, expected) {
			return nil, false
		}
		if d := en.managed[t.AuthID]; activeDisposition(d) {
			if d.State != "disabled" || errorPolicyDisabled(d).Code != "" || !sameCredentialEntry(s, d.Snapshot) || !errorUpgrade && answerOwner(d) != r.ID {
				return nil, false
			}
		} else if groupTargetBlocked(en.managed, t) {
			return nil, false
		}
		current = append(current, s)
	}
	return current, len(current) == len(ids)
}

func (en *Engine) decideProviderLocked(run *Run, r *Rule, c *changeSet, verdict, operation, condition string) {
	out := c.runs[run.ID]
	snapshot := *run.Config.ManagedProbe
	finish := func(status string) {
		out.ActionStatus = status
		_ = en.commitLocked(c)
	}
	if !providerWriteAllowed(r, snapshot) || operation == deleteCredential {
		finish("observe_only")
		return
	}
	if condition == "" {
		verdict = en.providerRoundVerdict(run, snapshot)
	}
	prior := en.managed[snapshot.AuthID]
	restore := snapshot.Disabled && condition == "" && verdict == "correct" && canRecoverDisabled(r, prior)
	errorOnDisabled := snapshot.Disabled && condition != "" && operation == disableCredential
	if !restore && !errorOnDisabled && (snapshot.Disabled || operation != disableCredential || condition == "" && verdict != "incorrect") {
		status := "observe_only"
		if snapshot.Disabled {
			status = "credential_kept_disabled"
		} else if verdict == "unknown" {
			status = "insufficient_evidence"
		}
		finish(status)
		return
	}
	h, ok := en.host.(managedHost)
	if !ok || !en.Capabilities().ManagementSession {
		finish("management_session_required")
		return
	}
	members, ok := en.providerWriteSnapshots(en.ctx, h, snapshot, r, errorOnDisabled)
	if !ok {
		finish("credential_changed")
		return
	}
	if run.Config.CatalogMissingModel != "" && !en.confirmCatalogActionLocked(run, r) {
		finish("catalog_action_unconfirmed")
		return
	}
	if errorOnDisabled {
		actionID := ID()
		for _, s := range members {
			next := CredentialDisposition{AuthID: s.AuthID, RuleID: r.ID, RuleRevision: r.Revision, ActionID: actionID, RunID: run.ID,
				State: "disabled", Operation: disableCredential, Snapshot: s, CreatedAt: en.now(), UpdatedAt: en.now()}
			if old := en.managed[s.AuthID]; activeDisposition(old) {
				next = *old
				next.AnswerOwnerRuleID = answerOwner(old)
			}
			en.setErrorSource(&next, run, condition)
			c.dispositions = append(c.dispositions, &next)
			en.cancelManagedRuns(c, s.AuthID, run.ID)
		}
		c.audits = append(c.audits, en.audit("error_policy_disabled", r.ID, run.ID, actionID, "Confirmed upstream condition on a disabled provider; original answer owners retained; no CPA write"))
		finish("credential_kept_disabled")
		return
	}

	state := "disable_pending"
	if restore {
		state, operation = "restore_pending", "restore_credential"
	}
	now, actionID := en.now(), ID()
	previous := make(map[string]*CredentialDisposition, len(members))
	for _, s := range members {
		previous[s.AuthID] = en.managed[s.AuthID]
		d := &CredentialDisposition{AuthID: s.AuthID, RuleID: r.ID, RuleRevision: r.Revision, ActionID: actionID,
			RunID: run.ID, TriggerAuthID: run.Config.AuthID, TriggerRunID: run.ID, State: state, Operation: operation, Snapshot: s, Models: slices.Clone(r.Models),
			UpstreamCondition: condition, CatalogMissingModel: run.Config.CatalogMissingModel, CreatedAt: now, UpdatedAt: now}
		if condition != "" {
			en.setErrorSource(d, run, condition)
		} else if !restore {
			d.AnswerOwnerRuleID = r.ID
		} else {
			d.AnswerOwnerRuleID = answerOwner(previous[s.AuthID])
		}
		c.dispositions = append(c.dispositions, d)
		if !restore {
			en.cancelManagedRuns(c, s.AuthID, run.ID)
		}
	}
	out.ActionStatus, out.CredentialAction = state, operation
	c.audits = append(c.audits, en.audit(state, r.ID, run.ID, actionID, "Verified provider configuration, members and rule authority before conditional write"))
	if err := en.commitLocked(c); err != nil {
		return
	}
	if restore && activeDisposition(prior) && sameCredentialEntry(snapshot, prior.Snapshot) {
		snapshot.ProviderExclusionSource = prior.Snapshot.ProviderExclusionSource
	}
	en.mu.Unlock()
	after, err := h.SetCredentialDisabled(en.ctx, snapshot, !restore)
	verified := map[string]CredentialSnapshot{after.AuthID: after}
	if err == nil {
		for _, member := range members {
			s := after
			if member.AuthID != after.AuthID {
				s, err = h.CredentialSnapshot(en.ctx, member.AuthID, member.AuthIndex)
			}
			if err != nil || !sameCredential(s, member) || s.Disabled == restore || !sameProviderGroupPlan(s, after) {
				err = errors.New("CPA provider state was not confirmed for all affected routes")
				break
			}
			verified[s.AuthID] = s
		}
	}
	en.mu.Lock()
	update := changes()
	out = cloneRun(en.runs[run.ID])
	update.runs[out.ID] = out
	state, out.ActionStatus = "disabled", "credential_suspended"
	if restore {
		state, out.ActionStatus = "restored", "credential_restored"
	}
	if err != nil {
		state, out.ActionStatus = "attention", "credential_action_uncertain"
		if errors.Is(err, errProviderPrecondition) {
			state, out.ActionStatus = "precondition_failed", "provider_config_conflict"
		}
	}
	for _, intent := range c.dispositions {
		next := *intent
		next.State, next.UpdatedAt = state, en.now()
		if err == nil {
			next.Snapshot = verified[next.AuthID]
		} else if state == "precondition_failed" && previous[next.AuthID] != nil {
			next = *previous[next.AuthID]
		}
		update.dispositions = append(update.dispositions, &next)
	}
	detail := "Verified provider configuration and resulting CPA runtime membership"
	if err != nil {
		detail = scrubError(err)
	}
	update.audits = append(update.audits, en.audit(out.ActionStatus, r.ID, run.ID, actionID, detail))
	_ = en.commitLocked(update)
}

func (en *Engine) acknowledgeProviderGroupLocked(ctx context.Context, rule *Rule, disposition *CredentialDisposition, targets []Target) error {
	members := disposition.Snapshot.ProviderMembers
	seen := make(map[string]bool)
	var current *CredentialSnapshot
	for _, t := range targets {
		if t.ProviderGroupID != disposition.Snapshot.ProviderGroupID {
			continue
		}
		s, err := en.acknowledgedTargetSnapshot(ctx, t)
		if err != nil {
			return err
		}
		if seen[t.AuthID] || s.ProviderMechanism != "group_disabled" || s.ProviderGroupID != t.ProviderGroupID || len(s.ProviderMembers) == 0 || !contains(s.ProviderMembers, s.AuthID) || current != nil && !sameProviderGroupPlan(s, *current) {
			return errors.New("blocked: current provider group identity or membership cannot be verified")
		}
		current = &s
		seen[t.AuthID] = true
	}
	if current != nil && len(seen) != len(current.ProviderMembers) {
		return errors.New("blocked: first verify every current provider group member in CPA")
	}
	update := changes()
	for _, id := range members {
		d := en.managed[id]
		_, busy := en.busyAuth[id]
		if d == nil || busy || d.ActionID != disposition.ActionID || d.RuleID != disposition.RuleID || !activeDisposition(d) || rule == nil && !en.orphanedHandlingLocked(d) {
			return errors.New("conflict: provider group ownership changed")
		}
		next := *d
		next.State, next.UpdatedAt = "acknowledged", en.now()
		update.dispositions = append(update.dispositions, &next)
		if rule == nil {
			en.cancelManagedRuns(update, id, "")
		}
	}
	if rule != nil {
		en.cancelRuleChangesLocked(update, rule.ID, "credential_handling_acknowledged")
	}
	update.audits = append(update.audits, en.audit("credential_handling_acknowledged", disposition.RuleID, "", disposition.ActionID, "Operator released local handling for the complete provider group; rule settings preserved"))
	return en.commitLocked(update)
}
