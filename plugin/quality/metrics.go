package quality

import (
	"fmt"
	"maps"
	"sort"

	bolt "go.etcd.io/bbolt"
)

// Reads share scalar totals only while the committed store revision is unchanged.
// All run updates publish under the engine mutex after their transaction commits.
type metricsProjection struct {
	revision int
	values   map[string]any
}

func (en *Engine) Metrics() (map[string]any, error) {
	return en.metrics(false)
}

func (en *Engine) metrics(summary bool) (map[string]any, error) {
	en.mu.Lock()
	var revision int
	if summary {
		err := en.store.db.View(func(tx *bolt.Tx) error {
			revision = tx.ID()
			return nil
		})
		if err != nil {
			en.mu.Unlock()
			return nil, fmt.Errorf("unavailable: read metrics revision: %w", err)
		}
		if en.summary.revision == revision && en.summary.values != nil {
			result := maps.Clone(en.summary.values)
			en.mu.Unlock()
			result["dropped_usage_events"] = en.dropped.Load()
			return result, nil
		}
	}
	correct, incorrect, unknown := 0, 0, 0
	covered, planned, unlocatedPruned := 0, 0, 0
	errors := map[string]int{}
	latencies := []int64{}
	ttfts := []int64{}
	var knownTokens, judgeTokens, controlTokens int64
	judgeUnknown, controlUnknown := 0, 0
	unknownUsage := 0
	for _, r := range en.runs {
		if r.HistoryCleared || r.PreflightDetail != nil || r.SkipDetail != nil || r.Status == "skipped" {
			continue
		}
		runCovered, runPlanned := retainedCoverage(r)
		covered += runCovered
		planned += runPlanned
		unlocatedPruned += max(0, r.PrunedSamples-len(r.PrunedSlots))
		for _, s := range r.Controls {
			if s.Usage != nil {
				controlTokens += s.Usage.Total
			} else {
				controlUnknown++
			}
			if r.Config.Judge.Mode == "llm" {
				if s.JudgeUsage != nil {
					judgeTokens += s.JudgeUsage.Total
				} else {
					judgeUnknown++
				}
			}
		}
		for _, s := range r.Samples {
			if r.Config.Judge.Mode == "llm" {
				if s.JudgeUsage != nil {
					judgeTokens += s.JudgeUsage.Total
				} else {
					judgeUnknown++
				}
			}
			if !summary {
				latencies = append(latencies, s.TotalMS)
				if s.TTFTMS != nil {
					ttfts = append(ttfts, *s.TTFTMS)
				}
				if s.ExecutionStatus != "completed" {
					errors[s.ExecutionStatus]++
				}
			}
			verdict := s.QualityVerdict
			if s.ExecutionStatus != "completed" || s.Error != "" {
				verdict = "unknown"
			}
			switch verdict {
			case "correct":
				correct++
			case "incorrect":
				incorrect++
			default:
				unknown++
			}
			if s.Usage != nil {
				knownTokens += s.Usage.Total
			} else {
				unknownUsage++
			}
		}
	}
	var rate, coverage any
	if correct+incorrect > 0 {
		rate = float64(correct) / float64(correct+incorrect)
	}
	if planned > 0 {
		coverage = float64(covered) / float64(planned)
	}
	budget := en.budget
	result := map[string]any{
		"correct": correct, "incorrect": incorrect, "unknown": unknown, "planned": planned, "covered": covered,
		"quality_pass_rate": rate, "coverage": coverage, "known_tokens": knownTokens + judgeTokens + controlTokens,
		"unknown_usage": unknownUsage, "dropped_usage_events": en.dropped.Load(), "unlocated_pruned_samples": unlocatedPruned,
	}
	if summary {
		en.summary = metricsProjection{revision: revision, values: maps.Clone(result)}
		en.mu.Unlock()
		return result, nil
	}
	en.mu.Unlock()
	sources := map[string]map[string]int64{}
	err := en.store.All("usage", func(raw []byte) error {
		var event UsageEvent
		if err := decode(raw, &event); err != nil {
			return err
		}
		source := trafficSource(event.Source)
		bucket := sources[source]
		if bucket == nil {
			bucket = map[string]int64{"attempts": 0, "failed": 0, "known_tokens": 0, "unknown_usage": 0}
			sources[source] = bucket
		}
		bucket["attempts"]++
		if event.Failed {
			bucket["failed"]++
		}
		if event.TokensKnown && event.Tokens >= 0 {
			bucket["known_tokens"] += event.Tokens
		} else {
			bucket["unknown_usage"]++
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("unavailable: read usage metrics: %w", err)
	}
	result["latency_ms"] = percentiles(latencies)
	result["ttft_ms"] = percentiles(ttfts)
	result["traffic_sources"] = sources
	result["subject_known_tokens"] = knownTokens
	result["judge_known_tokens"] = judgeTokens
	result["control_known_tokens"] = controlTokens
	result["judge_unknown_usage"] = judgeUnknown
	result["control_unknown_usage"] = controlUnknown
	result["error_types"] = errors
	result["budget"] = budget
	result["usage_semantics"] = "execution attempts, not inbound requests"
	return result, nil
}

// Coverage counts positions in the frozen plan. A retained duplicate can still
// occupy a pruned position. Older count-only trims cannot identify a position
// to remove from the denominator, so their coverage is a lower bound.
func retainedCoverage(r *Run) (covered, planned int) {
	if r.Config.Samples <= 0 {
		return 0, 0
	}
	models := map[string]bool{}
	for _, model := range r.Config.Models {
		if model != "" {
			models[model] = true
		}
	}
	planned = len(models) * r.Config.Samples
	seen := map[SampleSlot]bool{}
	for i := range r.Samples {
		s := &r.Samples[i]
		if !models[s.Model] || s.Index < 0 || s.Index >= r.Config.Samples {
			continue
		}
		slot := SampleSlot{Model: s.Model, Index: s.Index}
		judged := s.ExecutionStatus == "completed" && s.Error == "" && (s.QualityVerdict == "correct" || s.QualityVerdict == "incorrect")
		seen[slot] = seen[slot] || judged
	}
	for _, judged := range seen {
		if judged {
			covered++
		}
	}
	for _, slot := range r.PrunedSlots {
		if !models[slot.Model] || slot.Index < 0 || slot.Index >= r.Config.Samples {
			continue
		}
		if _, occupied := seen[slot]; !occupied {
			planned--
			seen[slot] = false
		}
	}
	return covered, planned
}

func percentiles(values []int64) any {
	if len(values) == 0 {
		return nil
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return map[string]any{"count": len(values), "p50": values[(len(values)-1)/2], "p95": values[(len(values)-1)*95/100]}
}
