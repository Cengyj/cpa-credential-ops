package quality

import (
	"context"
	"errors"
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"
)

func (en *Engine) DeleteManualRun(id string) error {
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("invalid_request: missing run ID")
	}
	r := en.runs[id]
	if r == nil {
		return nil
	}
	if err := en.manualRunRemovalErrorLocked(r); err != nil {
		return err
	}
	return en.deleteManualRunsLocked([]string{id})
}

func (en *Engine) manualRunRemovalErrorLocked(r *Run) error {
	if r.Source != "manual_test" || r.Config.RuleID != "" {
		return fmt.Errorf("conflict: only manual tests can be removed here")
	}
	if r.CompletedAt == nil || en.handlingEvidenceLocked(r) || r.Status == "queued" || r.Status == "running" || r.Status == "cancelling" {
		return fmt.Errorf("conflict: test is unfinished or retained for handling")
	}
	return nil
}

func (en *Engine) clearManualHistory() (historyClearResult, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	var result historyClearResult
	if err := en.mutableLocked(); err != nil {
		return result, err
	}
	var ids []string
	for id, run := range en.runs {
		if run.Source != "manual_test" {
			continue
		}
		if en.manualRunRemovalErrorLocked(run) != nil {
			result.Kept++
			continue
		}
		ids = append(ids, id)
	}
	if err := en.deleteManualRunsLocked(ids); err != nil {
		return historyClearResult{}, err
	}
	result.Deleted = len(ids)
	return result, nil
}

// Keep submission receipts so clearing history cannot replay a deleted test.
func (en *Engine) deleteManualRunsLocked(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := en.store.Write(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("runs"))
		for _, id := range ids {
			if err := b.Delete([]byte(id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for _, id := range ids {
		delete(en.runs, id)
	}
	return nil
}

func (en *Engine) Cancel(id string) error {
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	r := en.runs[id]
	if r == nil {
		return fmt.Errorf("not_found: run")
	}
	if r.Status != "queued" && r.Status != "running" {
		return nil
	}
	c := changes()
	c.runs[id] = en.cancelledRunLocked(r, "cancelled")
	return en.commitLocked(c)
}
func (en *Engine) Review(id string, index int, verdict, note string) error {
	if verdict != "correct" && verdict != "incorrect" && verdict != "unknown" {
		return fmt.Errorf("invalid verdict")
	}
	if len(note) > 2048 {
		return fmt.Errorf("review too long")
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	r := en.runs[id]
	if r == nil || r.HistoryCleared || index < r.PrunedSamples || index-r.PrunedSamples >= len(r.Samples) {
		return fmt.Errorf("not_found: sample")
	}
	index -= r.PrunedSamples
	next := cloneRun(r)
	next.Samples[index].Review = verdict
	next.Samples[index].ReviewNote = note
	c := changes()
	c.runs[id] = next
	return en.commitLocked(c)
}
func (en *Engine) executeTask(task *executionTask) {
	id, ctx, f, source := task.id, task.ctx, task.frozen, task.source
	defer func() {
		task.cancel()
		en.mu.Lock()
		for _, authID := range task.authIDs {
			delete(en.busyAuth, authID)
		}
		en.finishRuleChildLocked(f)
		en.notifyWorkersLocked()
		en.mu.Unlock()
	}()
	if f.CatalogMissingModel != "" {
		en.finish(id, en.checkExecution(ctx, id, f))
		return
	}
	phases := []bool{false}
	if f.ControlAuthID != "" {
		phases = []bool{true, false}
	}
	for _, control := range phases {
		for _, model := range f.Models {
			for i := 0; i < f.Samples; i++ {
				authID, provider := f.AuthID, f.Provider
				if control {
					authID, provider = f.ControlAuthID, f.ControlProvider
				}
				sample, err := en.evaluate(ctx, id, f, authID, provider, model, source)
				sample.Index = i
				if sample.ExecutionStatus == "skipped" && f.RuleID != "" {
					en.finish(id, &scopeRejection{"blocked: " + sample.Error, ScopeSkipDetail{
						AuthID: authID, Model: model, Code: "provider_probe_unavailable", Reason: sample.Error, NotExecuted: true,
					}})
					return
				}
				if sample.ExecutionStatus != "" {
					en.mu.Lock()
					current := cloneRun(en.runs[id])
					if control {
						current.Controls = append(current.Controls, sample)
					} else {
						current.Samples = append(current.Samples, sample)
					}
					change := changes()
					change.runs[id] = current
					saveErr := en.commitLocked(change)
					en.mu.Unlock()
					if saveErr != nil {
						en.finish(id, saveErr)
						return
					}
				}
				if err != nil {
					en.finish(id, err)
					return
				}
				if !control && upstreamAction(&f, &sample) != "" {
					en.finish(id, ctx.Err())
					return
				}
			}
		}
	}
	en.finish(id, ctx.Err())
}
func (en *Engine) evaluate(ctx context.Context, runID string, f Frozen, authID, provider, model, source string) (Sample, error) {
	if err := en.checkExecution(ctx, runID, f); err != nil {
		return Sample{}, err
	}
	day, err := en.reserve(f.Parameters.MaxTokens)
	if err != nil {
		return Sample{}, err
	}
	en.mu.Lock()
	responseLimit := en.cfg.MaxResponseBytes
	en.mu.Unlock()
	probe := f.ManagedProbe
	if authID != f.AuthID {
		probe = nil
	}
	s := en.host.Execute(ctx, Execution{ManagedProbe: probe, ResponseLimit: responseLimit, AuthID: authID, Provider: provider, Model: model, Prompt: f.Question.Prompt, Source: source, Parameters: f.Parameters})
	s.Model = model
	s.QualityVerdict = "unknown"
	if !en.Capabilities().VerifiedEvidence && probe == nil {
		s.Evidence.Verified = false
		s.Evidence.Reason = "official_host_evidence_unavailable"
	}
	s.Comparable = s.ExecutionStatus == "completed" && s.Evidence.Verified && !s.Evidence.ConditionsChanged && s.Evidence.AuthID == authID && s.Evidence.RequestedModel == model && s.Evidence.ResolvedModel != "" && s.Evidence.PayloadFingerprint != "" && f.PayloadConfigVersion != "" && s.Evidence.PayloadConfigVersion == f.PayloadConfigVersion
	if err := en.account(day, s.Usage); err != nil {
		return s, err
	}
	if s.ExecutionStatus == "completed" {
		switch f.Judge.Mode {
		case "deterministic":
			s.QualityVerdict, s.Reason = DeterministicJudge(f.Question, s.Output)
		case "manual":
			s.Reason = "manual assessment required"
		case "llm":
			if err := en.checkExecution(ctx, runID, f); err != nil {
				return s, err
			}
			if judgeDay, err := en.reserve(512); err != nil {
				s.Reason = "judge budget exhausted"
			} else {
				j := en.host.Execute(ctx, Execution{ResponseLimit: min(responseLimit, 8192), AuthID: f.Judge.AuthID, Provider: f.Judge.Provider, Model: f.Judge.Model, Prompt: JudgePrompt(f.Question, s.Output), System: JudgeInstructions, Source: "judge", Parameters: Parameters{MaxTokens: 512}, Judge: true})
				s.JudgeUsage = j.Usage
				if err := en.account(judgeDay, j.Usage); err != nil {
					return s, err
				}
				if j.ExecutionStatus == "completed" {
					s.QualityVerdict, s.Reason = StrictJudge(j.Output)
					s.Comparable = s.Comparable && j.Evidence.Verified && !j.Evidence.ConditionsChanged
				} else {
					s.Reason = "judge execution failed"
				}
			}
		}
	}
	return s, nil
}

func (en *Engine) finish(id string, err error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	r := cloneRun(en.runs[id])
	defer delete(en.active, id)
	if r == nil {
		return
	}
	now := en.now()
	r.CompletedAt = &now
	r.Status = "completed"
	if err != nil {
		r.Status = "failed"
		r.Error = scrubError(err)
		if errors.Is(err, context.Canceled) {
			r.Status = "cancelled"
		}
		if strings.Contains(err.Error(), "blocked:") {
			r.Status = "blocked"
		}
		var rejection *scopeRejection
		if errors.As(err, &rejection) && rejection.detail.NotExecuted && len(r.Samples) == 0 && len(r.Controls) == 0 {
			detail := rejection.detail
			markDispatchSkip(r, &detail, now)
		}
	}
	if r.CancelRequested {
		r.Status = "cancelled"
	}
	if r.BlockReason != "" {
		r.Status = "blocked"
		r.Error = r.BlockReason
	}
	c := changes()
	c.runs[id] = r
	if e := en.commitLocked(c); e != nil {
		return
	}
	en.decideLocked(en.runs[id])
	if rule := en.rules[r.Config.RuleID]; rule != nil && rule.MaxResults > 0 {
		if err := en.pruneResultCountsLocked(rule.ID, r.Config.AuthID); err != nil {
			en.persistFailure(err)
		}
	}
}
func scrubError(e error) string {
	if e == nil {
		return ""
	}
	s := e.Error()
	for _, word := range []string{"token", "api_key", "Authorization", "Bearer ", "cookie"} {
		if strings.Contains(s, word) {
			return "host operation failed (sensitive details omitted)"
		}
	}
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}
