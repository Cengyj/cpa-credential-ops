package quality

import (
	"maps"
	"slices"
	"strings"
)

func (en *Engine) pendingScheduleReasonLocked(p *pendingRuleSchedule) string {
	reasons := make(map[string]bool)
	if p.waitsForDirectory() {
		reasons["本轮仍有对象等待后台管理目录就绪。"] = true
	}
	for _, credential := range p.WaitingLogins {
		hold := en.loginHolds[credential]
		switch {
		case hold == nil:
			reasons["原凭证的更新收据缺失，需要核对。"] = true
		case hold.State != "completed":
			_, reason := hold.reason()
			reasons[reason] = true
		}
	}
	if len(reasons) == 0 {
		return "凭证更新收据已确认，等候调度器重新核实本轮准入。"
	}
	return strings.Join(slices.Sorted(maps.Keys(reasons)), "")
}

// One timer occurrence may have independent file, provider and login
// prerequisites. Only admissions from this occurrence are carried forward.
type scopeScheduleStep struct {
	pending          *pendingRuleSchedule
	waitForDirectory bool
	deferScan        bool
}

func (s scopeScheduleStep) waitsFor(detail ScopeSkipDetail) bool {
	return detail.waitingCredential != "" || s.waitForDirectory && (detail.ObjectType == objectProvider ||
		detail.Code == "provider_directory_unavailable" || detail.Code == "management_session_required")
}

func (en *Engine) scopeScheduleLocked(r *Rule, occurrence *scheduledRule, result *ruleDispatch, matches []scopeMatch, seen map[string]Target) ([]scopeMatch, scopeScheduleStep) {
	capability := en.Capabilities()
	managementDown := capability.CredentialManagement && !capability.ManagementSession
	needsManagement := managedRule(r) || scopeNeedsProviderDirectory(r, seen) || slices.ContainsFunc(result.SkipDetails, func(d ScopeSkipDetail) bool {
		return d.Code == "management_session_required"
	})
	step := scopeScheduleStep{waitForDirectory: !result.Verified || managementDown && needsManagement}
	if step.waitForDirectory && result.Reason == "" {
		result.Reason = sessionSkip().Reason
	}
	if !dynamicScope(r.TargetScope) {
		step.deferScan = step.waitForDirectory
		for _, detail := range result.SkipDetails {
			if detail.waitingCredential != "" {
				step.deferScan, result.Reason = true, detail.Reason
				break
			}
		}
		return matches, step
	}
	next := &pendingRuleSchedule{Due: occurrence.due, WaitingLogins: make(map[string]string), DirectoryPending: step.waitForDirectory}
	if occurrence.remainder {
		prior := r.PendingSchedule
		next.Accepted = slices.Clone(prior.Accepted)
		accepted := make(map[string]bool, len(prior.Accepted))
		for _, id := range prior.Accepted {
			accepted[id] = true
		}
		openDirectory := prior.waitsForDirectory()
		next.DirectoryPending = openDirectory && step.waitForDirectory
		matches = result.filterMembers(matches, func(id string) bool {
			_, waiting := prior.WaitingLogins[id]
			return !accepted[id] && (openDirectory || waiting)
		})
		for subject, credential := range prior.WaitingLogins {
			// An absent or unreadable directory row cannot serve as a login
			// commit receipt. Preserve quarantined subjects across restarts.
			if hold := en.loginHolds[credential]; hold == nil || hold.State != "completed" || step.waitForDirectory {
				next.WaitingLogins[subject] = credential
			}
		}
	}
	if step.waitForDirectory {
		matches = slices.DeleteFunc(matches, func(m scopeMatch) bool {
			wait := m.target.IsProvider || managementDown && m.snapshot != nil
			if wait && m.recovery {
				result.RecoveryCount--
			}
			return wait
		})
	}
	for i := range result.SkipDetails {
		detail := &result.SkipDetails[i]
		if step.waitsFor(*detail) {
			detail.NotExecuted = true
			if detail.waitingCredential != "" {
				next.WaitingLogins[detail.AuthID] = detail.waitingCredential
			}
		} else {
			delete(next.WaitingLogins, detail.AuthID)
		}
	}
	for _, m := range matches {
		delete(next.WaitingLogins, m.target.AuthID)
	}
	if next.DirectoryPending || len(next.WaitingLogins) > 0 {
		step.pending = next
		// Retry an incomplete inventory with backoff. A newly discovered
		// login wait with no ready subjects is checkpointed on its first scan.
		step.deferScan = step.waitForDirectory && len(matches) == 0 && (occurrence.remainder || len(next.WaitingLogins) == 0)
	}
	return matches, step
}

func (d *ruleDispatch) filterMembers(matches []scopeMatch, include func(string) bool) []scopeMatch {
	d.SkipDetails = slices.DeleteFunc(d.SkipDetails, func(detail ScopeSkipDetail) bool { return !include(detail.AuthID) })
	d.Skipped = len(d.SkipDetails)
	return slices.DeleteFunc(matches, func(m scopeMatch) bool {
		if include(m.target.AuthID) {
			return false
		}
		if m.recovery {
			d.RecoveryCount--
		}
		return true
	})
}
