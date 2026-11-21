package quality

import "context"

// An absent model is meaningful only on a verified catalog row for the same
// enabled target. A disabled target's missing catalog is expected.
func catalogMissingModelAction(rule *Rule, target Target, snapshot CredentialSnapshot, detail ScopeSkipDetail) bool {
	if rule == nil || !credentialMutation(rule.UpstreamErrorActions["model_unsupported"]) {
		return false
	}
	if rule.AuthID != "" && rule.AuthID != target.AuthID {
		// providerWriteSnapshots revalidates this group's anchor and every member
		// before a provider disposition can reach the final catalog confirmation.
		if !rule.expandedProviderPeer(target.AuthID) || !rule.holdsGroup(target) || snapshot.ProviderMechanism != "group_disabled" ||
			target.ProviderGroupID != snapshot.ProviderGroupID || !contains(snapshot.ProviderMembers, rule.AuthID) {
			return false
		}
	}
	return (rule.AuthID == "" || rule.Provider == target.Provider) &&
		detail.Code == "missing_model" && detail.Model != "" && contains(rule.Models, detail.Model) &&
		target.catalogVerified && len(target.Models) > 0 && ruleSubjectEnabled(target) && !contains(target.Models, detail.Model) &&
		!snapshot.Disabled && snapshot.Fingerprint != "" && (!snapshot.UpdatedAt.IsZero() || snapshot.ProviderMechanism != "") &&
		snapshot.AuthID == target.AuthID && snapshot.AuthIndex == target.AuthIndex && snapshot.Provider == target.Provider
}

// The worker owns this credential while the current directory, rule and
// persisted snapshot are rechecked. CPA still has no atomic conditional write.
func (en *Engine) confirmCatalogActionLocked(run *Run, rule *Rule) bool {
	f := &run.Config
	op := f.UpstreamErrorActions["model_unsupported"]
	if run.Status != "completed" || f.ManagedProbe == nil || run.PreflightDetail == nil ||
		run.PreflightDetail.Code != "missing_model" || run.PreflightDetail.Model != f.CatalogMissingModel ||
		run.PreflightDetail.AuthID != f.AuthID || run.CancelRequested || run.BlockReason != "" ||
		len(run.Samples) != 0 || !credentialMutation(op) || rule.UpstreamErrorActions["model_unsupported"] != op ||
		f.AuthID != f.ManagedProbe.AuthID || f.Provider != f.ManagedProbe.Provider || !contains(f.Models, f.CatalogMissingModel) ||
		en.localPause(f.RunConfig) != "" || activeDisposition(en.managed[f.AuthID]) || f.Scheduled && !en.timerAllowed(rule) {
		return false
	}
	host, ok := en.host.(interface {
		pinnedTarget(context.Context, string, string) (Target, error)
	})
	if !ok {
		return false
	}
	target, err := host.pinnedTarget(en.ctx, f.AuthID, f.ManagedProbe.AuthIndex)
	return err == nil && catalogMissingModelAction(rule, target, *f.ManagedProbe, ScopeSkipDetail{Code: "missing_model", Model: f.CatalogMissingModel})
}
