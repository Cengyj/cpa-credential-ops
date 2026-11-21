package quality

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// QueryRuns sorts lightweight references and clones only the requested page.
func (en *Engine) QueryRuns(query url.Values) (map[string]any, error) {
	source := query.Get("source")
	if source != "" && source != "manual_test" && source != "scheduled_probe" {
		return nil, fmt.Errorf("invalid_request: source must be manual_test or scheduled_probe")
	}
	offset, limit := pageBounds(query)
	ruleID, batchID := query.Get("rule_id"), query.Get("scope_batch_id")
	en.mu.Lock()
	defer en.mu.Unlock()
	refs := make([]*Run, 0, len(en.runs))
	for _, run := range en.runs {
		if !run.HistoryCleared && (source == "" || run.Source == source) &&
			(ruleID == "" || run.Config.RuleID == ruleID) &&
			(batchID == "" || run.Config.ScopeBatchID == batchID) {
			refs = append(refs, run)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].CreatedAt.Equal(refs[j].CreatedAt) {
			return refs[i].ID < refs[j].ID
		}
		return refs[i].CreatedAt.After(refs[j].CreatedAt)
	})
	total := len(refs)
	if offset > total {
		offset = total
	}
	end := offset + min(limit, total-offset)
	rows := refs[offset:end]
	result := map[string]any{"total": total, "offset": offset, "limit": limit}
	if query.Get("view") == "summary" {
		entries := make([]map[string]any, len(rows))
		for i, run := range rows {
			entries[i] = runListSummary(run)
		}
		result["items"] = entries
	} else {
		entries := make([]Run, len(rows))
		for i, run := range rows {
			entries[i] = *runForDisplay(run)
		}
		result["items"] = entries
	}
	return result, nil
}

// List rows keep full evidence on the detail endpoint.
func runListSummary(r *Run) map[string]any {
	type probeEvidence struct {
		Path string `json:"path"`
	}
	type verdict struct {
		Model             string         `json:"model"`
		Index             int            `json:"index"`
		ExecutionStatus   string         `json:"execution_status"`
		QualityVerdict    string         `json:"quality_verdict"`
		AnswerMatch       string         `json:"answer_match,omitempty"`
		Review            string         `json:"review,omitempty"`
		Error             string         `json:"error,omitempty"`
		UpstreamCondition string         `json:"upstream_condition,omitempty"`
		Evidence          *probeEvidence `json:"evidence,omitempty"`
	}
	samples := make([]verdict, len(r.Samples))
	for i := range r.Samples {
		s := &r.Samples[i]
		samples[i] = verdict{Model: s.Model, Index: s.Index, ExecutionStatus: s.ExecutionStatus, QualityVerdict: s.QualityVerdict,
			AnswerMatch: displayAnswerMatch(r, s), Review: s.Review, Error: s.Error, UpstreamCondition: s.UpstreamCondition}
		switch s.Evidence.Path {
		case providerConfigProbe, "provider_native_probe", "cpa_management_probe":
			samples[i].Evidence = &probeEvidence{Path: s.Evidence.Path}
		}
	}
	cfg := &r.Config
	config := map[string]any{
		"auth_id": cfg.AuthID, "rule_id": cfg.RuleID, "scope_batch_id": cfg.ScopeBatchID, "provider_observation": cfg.ProviderObservation,
		"recovery_check": cfg.RecoveryCheck,
		"models":         slices.Clone(cfg.Models), "samples": cfg.Samples, "question_id": cfg.QuestionID,
		"question":   map[string]string{"id": cfg.Question.ID, "title": cfg.Question.Title},
		"parameters": map[string]string{"reasoning_effort": cfg.Parameters.ReasoningEffort},
	}
	if cfg.ProviderName != "" {
		config["provider_name"] = cfg.ProviderName
	}
	if cfg.SubjectIndex != "" {
		config["subject_auth_index"] = cfg.SubjectIndex
	}
	if cfg.CatalogMissingModel != "" {
		config["catalog_missing_model"] = cfg.CatalogMissingModel
	}
	row := map[string]any{
		"id": r.ID, "source": r.Source, "status": r.Status, "created_at": r.CreatedAt,
		"credential_email": r.CredentialEmail, "action_status": r.ActionStatus, "credential_action": r.CredentialAction, "upstream_condition": r.UpstreamCondition, "pruned_samples": r.PrunedSamples,
		"error": r.Error, "block_reason": r.BlockReason, "samples": samples,
		"config": config,
	}
	if r.CancelRequested {
		row["cancel_requested"] = true
	}
	if r.SkipDetail != nil {
		row["skip_detail"] = cloneValue(r.SkipDetail)
	}
	if r.PreflightDetail != nil {
		row["preflight_detail"] = cloneValue(r.PreflightDetail)
	}
	if preview := manualAnswerPreview(r); preview != nil {
		row["answer_preview"] = preview
	}
	return row
}

var previewDocument = regexp.MustCompile(`(?i)^(?:<\?xml\s.*?\?>\s*)?(?:<!--.*?-->\s*)*(?:<!doctype\s+|<)(html|svg)(?:[\s/>]|$)`)
var previewFence = regexp.MustCompile(`^(\x60{3,}|~{3,})(.*)$`)
var previewHTMLFragment = regexp.MustCompile(`(?i)^<(?:head|body|style|main|div|section|article|p|table|h[1-6])(?:[\s/>]|$)`)

// Summaries inspect only the bounded prefix. Displaying the work uses the full
// retained answer from the detail endpoint.
func previewDocumentKind(prefix string) string {
	var fence, language string
	for line := range strings.SplitSeq(prefix, "\n") {
		line = strings.TrimSpace(line)
		if marker := previewFence.FindStringSubmatch(line); marker != nil {
			if fence == "" {
				fence, language = marker[1], strings.ToLower(strings.TrimSpace(marker[2]))
			} else if strings.HasPrefix(marker[1], fence) && strings.TrimSpace(marker[2]) == "" {
				fence, language = "", ""
			}
			continue
		}
		if fence != "" && language != "" && language != "html" && language != "svg" && language != "xml" {
			continue
		}
		if match := previewDocument.FindStringSubmatch(line); match != nil {
			return strings.ToLower(match[1])
		}
		if language == "html" && previewHTMLFragment.MatchString(line) {
			return "html"
		}
	}
	return ""
}

// Ignore leading whitespace before limiting the work used to format the excerpt.
func manualAnswerPreview(run *Run) *AnswerPreview {
	if run.Source != "manual_test" {
		return nil
	}
	const maxBytes, maxRunes = 4096, 96
	for i := range run.Samples {
		sample := &run.Samples[i]
		output := strings.TrimLeftFunc(sample.Output, func(r rune) bool { return r == '\uFEFF' || unicode.IsSpace(r) })
		end := min(len(output), maxBytes)
		for end > 0 && end < len(output) && !utf8.RuneStart(output[end]) {
			end--
		}
		text := strings.Join(strings.Fields(output[:end]), " ")
		if text == "" {
			continue
		}
		preview := &AnswerPreview{SampleSlot: SampleSlot{Model: sample.Model, Index: sample.Index}}
		if kind := previewDocumentKind(output[:end]); kind != "" {
			preview.Kind = kind
			return preview
		}
		truncated := end < len(output)
		count := 0
		for i := range text {
			if count == maxRunes {
				text = strings.TrimSuffix(text[:i], " ")
				truncated = true
				break
			}
			count++
		}
		preview.Kind, preview.Text, preview.Truncated = "text", text, truncated
		return preview
	}
	return nil
}

// Read the page and its handling records under the same engine revision.
func (en *Engine) queryRules(query url.Values) map[string]any {
	en.mu.Lock()
	defer en.mu.Unlock()
	ids := en.sortedRuleIDsLocked()
	offset, limit := pageBounds(query)
	offset = min(offset, len(ids))
	pageIDs := ids[offset : offset+min(limit, len(ids)-offset)]
	views := make([]ruleView, len(pageIDs))
	positions := make(map[string]int, len(pageIDs))
	for i, id := range pageIDs {
		views[i] = viewRule(en.ruleForDisplayLocked(en.rules[id]))
		positions[id] = i
	}
	orphaned := make([]HandlingSummary, 0)
	for _, d := range en.managed {
		if i, ok := positions[d.RuleID]; ok && activeDisposition(d) {
			views[i].Handling = append(views[i].Handling, handlingSummary(d))
		} else if en.orphanedHandlingLocked(d) {
			orphaned = append(orphaned, handlingSummary(d))
		}
	}
	for _, v := range views {
		sort.Slice(v.Handling, func(i, j int) bool { return v.Handling[i].AuthID < v.Handling[j].AuthID })
	}
	sort.Slice(orphaned, func(i, j int) bool { return orphaned[i].AuthID < orphaned[j].AuthID })
	return map[string]any{"items": views, "total": len(ids), "offset": offset, "limit": limit, "orphaned_handling": orphaned}
}

func trafficSource(source string) string {
	switch source {
	case "production", "manual_test", "scheduled_probe", "judge", "internal_other":
		return source
	default:
		return "unknown"
	}
}

func page[T any](items []T, req pluginapi.ManagementRequest) map[string]any {
	offset, limit := pageBounds(req.Query)
	total := len(items)
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	if items == nil {
		items = []T{}
	}
	return map[string]any{"items": items[offset:end], "total": total, "offset": offset, "limit": limit}
}

func pageBounds(query url.Values) (offset, limit int) {
	offset, _ = strconv.Atoi(query.Get("offset"))
	limit, _ = strconv.Atoi(query.Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	return offset, limit
}

func (en *Engine) Run(id string) (*Run, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	r := en.runs[id]
	if r == nil || r.HistoryCleared {
		return nil, fmt.Errorf("not_found: run")
	}
	return runForDisplay(r), nil
}

// Compare against the frozen question on a read-only copy, including older
// manual runs. This display result does not enter quality policy or metrics.
func runForDisplay(run *Run) *Run {
	out := cloneRun(run)
	out.AnswerPreview = manualAnswerPreview(run)
	for i := range out.Samples {
		out.Samples[i].AnswerMatch = displayAnswerMatch(out, &out.Samples[i])
	}
	return out
}

func displayAnswerMatch(r *Run, s *Sample) string {
	if r.Source == "manual_test" && r.Config.Judge.Mode == "manual" && s.ExecutionStatus == "completed" && s.Error == "" && s.Review == "" && s.QualityVerdict != "correct" && s.QualityVerdict != "incorrect" {
		match, _ := DeterministicJudge(r.Config.Question, s.Output)
		return match
	}
	return s.AnswerMatch
}

func (en *Engine) Runs() []Run {
	en.mu.Lock()
	defer en.mu.Unlock()
	out := []Run{}
	for _, r := range en.runs {
		if !r.HistoryCleared {
			out = append(out, *cloneRun(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
