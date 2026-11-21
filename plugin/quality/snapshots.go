package quality

import (
	"maps"
	"slices"
)

// Clones copy mutable slices and pointers without serializing large model outputs.
func cloneValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneConfig(config RunConfig) RunConfig {
	config.Models = slices.Clone(config.Models)
	config.Parameters.Temperature = cloneValue(config.Parameters.Temperature)
	return config
}

func cloneFrozen(frozen Frozen) Frozen {
	frozen.RunConfig = cloneConfig(frozen.RunConfig)
	frozen.UpstreamErrorActions = maps.Clone(frozen.UpstreamErrorActions)
	frozen.ManagedProbe = cloneValue(frozen.ManagedProbe)
	frozen.ProviderDependencies = slices.Clone(frozen.ProviderDependencies)
	return frozen
}

func cloneSamples(samples []Sample) []Sample {
	out := slices.Clone(samples)
	for i := range out {
		out[i].TTFTMS = cloneValue(out[i].TTFTMS)
		out[i].Usage = cloneValue(out[i].Usage)
		out[i].JudgeUsage = cloneValue(out[i].JudgeUsage)
	}
	return out
}

func cloneRun(run *Run) *Run {
	if run == nil {
		return nil
	}
	out := *run
	out.AnswerPreview = cloneValue(run.AnswerPreview)
	out.SkipDetail = cloneValue(run.SkipDetail)
	out.PreflightDetail = cloneValue(run.PreflightDetail)
	out.Config = cloneFrozen(run.Config)
	out.CompletedAt = cloneValue(run.CompletedAt)
	out.PrunedSlots = slices.Clone(run.PrunedSlots)
	out.Samples = cloneSamples(run.Samples)
	out.Controls = cloneSamples(run.Controls)
	return &out
}

func cloneRule(rule *Rule) *Rule {
	if rule == nil {
		return nil
	}
	out := *rule
	out.ObjectTypes = slices.Clone(rule.ObjectTypes)
	out.AuthorizedGroups = slices.Clone(rule.AuthorizedGroups)
	out.RunConfig = cloneConfig(rule.RunConfig)
	out.UpstreamErrorActions = maps.Clone(rule.UpstreamErrorActions)
	out.LastManualSkips = slices.Clone(rule.LastManualSkips)
	out.LastScopeDispatch = cloneScopeDispatch(rule.LastScopeDispatch)
	if rule.PendingSchedule != nil {
		pending := *rule.PendingSchedule
		pending.Accepted = slices.Clone(pending.Accepted)
		out.PendingSchedule = &pending
	}
	return &out
}

func cloneScopeDispatch(summary *ScopeDispatchSummary) *ScopeDispatchSummary {
	copy := cloneValue(summary)
	if copy != nil {
		copy.Details = slices.Clone(summary.Details)
		copy.Candidates = maps.Clone(summary.Candidates)
	}
	return copy
}
