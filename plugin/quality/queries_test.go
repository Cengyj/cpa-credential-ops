package quality

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	bolt "go.etcd.io/bbolt"
)

func TestHistorySummaryOmitsLongEvidenceOnlyOnListRequests(t *testing.T) {
	en, _ := lifeFixture(t)
	long := strings.Repeat("frozen answer ", 5000)
	for _, source := range []string{"manual_test", "scheduled_probe"} {
		for i := range 41 {
			id := fmt.Sprintf("%s-%02d", source, i)
			cfg := lifeConfig()
			cfg.Samples, cfg.Judge.Mode = 2, "manual"
			q := Question{Title: "Frozen question", Prompt: "frozen prompt", Kind: "logic", Answer: "4"}
			r := &Run{ID: id, Source: source, Config: Frozen{RunConfig: cfg, Question: q}, Status: "completed", ActionStatus: "observe_only", Samples: []Sample{
				{Model: cfg.Models[0], Index: 0, Output: long, ExecutionStatus: "completed", QualityVerdict: "unknown"},
				{Model: cfg.Models[0], Index: 1, Output: "4", ExecutionStatus: "completed", QualityVerdict: "unknown"},
			}}
			en.runs[id] = r
		}
	}
	for _, tc := range []struct{ source, limit string }{{"manual_test", "10"}, {"scheduled_probe", "20"}} {
		t.Run(tc.source, func(t *testing.T) {
			req := pluginapi.ManagementRequest{Method: "GET", Path: "/v0/management/plugins/model-quality/runs", Query: url.Values{"source": {tc.source}, "limit": {tc.limit}, "offset": {"10"}}}
			full := en.Handle(context.Background(), req)
			req.Query.Set("view", "summary")
			short := en.Handle(context.Background(), req)
			t.Logf("page JSON: full %d bytes, summary %d bytes", len(full.Body), len(short.Body))
			if full.StatusCode != 200 || short.StatusCode != 200 || !strings.Contains(string(full.Body), long) || strings.Contains(string(short.Body), "frozen prompt") || len(short.Body)*10 >= len(full.Body) {
				t.Fatal("expected compact list and backward-compatible full list")
			}
			var page struct {
				Items                []Run
				Total, Offset, Limit int
			}
			if err := json.Unmarshal(short.Body, &page); err != nil || page.Total != 41 || page.Offset != 10 || len(page.Items) != page.Limit {
				t.Fatal("wrong page", err)
			}
			row := page.Items[0]
			if len(row.Samples) != 2 || row.Samples[1].Model != row.Config.Models[0] || row.Samples[1].Index != 1 {
				t.Fatal("summary lost the identity of a planned sample")
			}
			if row.ID != tc.source+"-10" || row.Config.Question.Title != "Frozen question" || len(row.Samples) != 2 || row.Samples[1].QualityVerdict != "unknown" || (row.Samples[1].AnswerMatch == "correct") != (tc.source == "manual_test") {
				t.Fatal("summary lost list verdict or frozen configuration")
			}
			detail, err := en.Run(row.ID)
			if err != nil || detail.Config.Question.Prompt != "frozen prompt" || detail.Samples[0].Output != long {
				t.Fatal("summary affected detail")
			}
			if tc.source == "manual_test" {
				preview := row.AnswerPreview
				if preview == nil || preview.Model != row.Config.Models[0] || preview.Index != 0 || preview.Kind != "text" || !preview.Truncated || preview.Text != strings.TrimRight(long[:96], " ") || detail.AnswerPreview == nil || *detail.AnswerPreview != *preview {
					t.Fatal("list and detail should carry the same bounded answer preview")
				}
				if en.runs[row.ID].AnswerPreview != nil || row.Samples[0].Output != "" {
					t.Fatal("summary modified storage or included a full output")
				}
			} else if strings.Contains(string(short.Body), "\"answer_preview\"") || detail.AnswerPreview != nil {
				t.Fatal("scheduled rows must not include manual answer previews")
			}
		})
	}
}

func TestScheduledSummaryKeepsOriginalErrorSource(t *testing.T) {
	for _, row := range []*Run{
		{Source: "scheduled_probe", ActionStatus: "observe_only", Samples: []Sample{
			{ExecutionStatus: "failed", Error: "upstream HTTP 429", UpstreamCondition: "quota_exhausted"},
			{ExecutionStatus: "failed", Error: "upstream HTTP 429"},
		}},
		{Source: "scheduled_probe", Config: Frozen{CatalogMissingModel: "removed-model"}, Status: "skipped"},
	} {
		raw, err := json.Marshal(runListSummary(row))
		if err != nil {
			t.Fatal(err)
		}
		var summary Run
		if err := json.Unmarshal(raw, &summary); err != nil {
			t.Fatal(err)
		}
		if summary.UpstreamCondition != "" || summary.Config.CatalogMissingModel != row.Config.CatalogMissingModel || len(summary.Samples) != len(row.Samples) {
			t.Fatal("summary changed the recorded source")
		}
		for i, sample := range row.Samples {
			if summary.Samples[i].UpstreamCondition != sample.UpstreamCondition || summary.Samples[i].Error != sample.Error {
				t.Fatal("summary lost sample evidence on an observation-only rule")
			}
		}
	}
}

func TestSummaryMatchesRecordedProbePathsWithoutFullEvidence(t *testing.T) {
	paths := []string{"provider_config_probe", "provider_native_probe", "cpa_management_probe", "", "unrecognized-private-route"}
	run := &Run{Source: "scheduled_probe", CancelRequested: true}
	for _, path := range paths {
		run.Samples = append(run.Samples, Sample{Evidence: Evidence{Path: path, Reason: "detail-only-marker", AuthID: "detail-only-marker"}})
	}
	raw, err := json.Marshal(runListSummary(run))
	if err != nil {
		t.Fatal(err)
	}
	var pageRow Run
	if err = json.Unmarshal(raw, &pageRow); err != nil {
		t.Fatal(err)
	}
	if len(pageRow.Samples) != len(paths) {
		t.Fatal("summary lost a sample")
	}
	for i, expected := range []string{"provider_config_probe", "provider_native_probe", "cpa_management_probe", "", ""} {
		if got := pageRow.Samples[i].Evidence.Path; got != expected {
			t.Fatalf("summary sample %d probe path = %q, want %q", i, got, expected)
		}
	}
	if !pageRow.CancelRequested || strings.Contains(string(raw), "private-route") || strings.Contains(string(raw), "detail-only-marker") {
		t.Fatal("summary dropped a pending cancellation or exposed detail-only evidence")
	}
}

func TestRunSummaryKeepsFrozenObjectIdentity(t *testing.T) {
	en, _ := lifeFixture(t)
	const providerEmail = "供应商 · Frozen provider · route-a"
	for _, tc := range []struct {
		id, authID, index, name, email, status string
		provider                               bool
	}{
		{"provider-completed", "provider-a", "route-a", "Frozen provider", "", "completed", true},
		{"provider-skipped", "provider-a", "route-a", "Frozen provider", providerEmail, "skipped", true},
		{"provider-legacy", "provider-a", "route-a", "", providerEmail, "skipped", true},
		{"provider-unnamed", "provider-a", "route-a", "", "", "skipped", true},
		{"other-route", "provider-b", "route-b", "Frozen provider", "", "completed", true},
		{"oauth-completed", "account.json", "account-index", "", "frozen@example.test", "completed", false},
		{"oauth-skipped", "account.json", "account-index", "", "frozen@example.test", "skipped", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			config := lifeConfig()
			config.AuthID = tc.authID
			config.Parameters.ReasoningEffort = "high"
			run := &Run{ID: tc.id, Source: "scheduled_probe", Status: tc.status, ActionStatus: "observe_only", CredentialEmail: tc.email,
				Config: Frozen{RunConfig: config, SubjectIndex: tc.index, ProviderName: tc.name, ProviderObservation: tc.provider}}
			if tc.status == "skipped" {
				run.SkipDetail = &ScopeSkipDetail{NotExecuted: true, RunID: tc.id, AuthID: tc.authID, Email: tc.email,
					Code: "identity_unverified", Reason: "Synthetic scope identity was not verified for this round."}
			} else {
				run.Samples = []Sample{{Model: config.Models[0], ExecutionStatus: "completed", QualityVerdict: "correct", Output: "4"}}
			}
			en.runs = map[string]*Run{run.ID: run}
			original, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			response := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: "/v0/management/plugins/model-quality/runs",
				Query: url.Values{"view": {"summary"}, "source": {"scheduled_probe"}}})
			var page struct{ Items []Run }
			if response.StatusCode != 200 || json.Unmarshal(response.Body, &page) != nil || len(page.Items) != 1 {
				t.Fatal("summary response unavailable", response.StatusCode, string(response.Body))
			}
			row := page.Items[0]
			if row.Config.AuthID != tc.authID || row.Config.SubjectIndex != tc.index || row.Config.ProviderName != tc.name ||
				row.Config.ProviderObservation != tc.provider || row.CredentialEmail != tc.email {
				t.Fatalf("summary changed frozen identity: got auth=%q index=%q name=%q provider=%t email=%q", row.Config.AuthID,
					row.Config.SubjectIndex, row.Config.ProviderName, row.Config.ProviderObservation, row.CredentialEmail)
			}
			if row.Status != tc.status || row.ActionStatus != run.ActionStatus || row.Config.Parameters.ReasoningEffort != "high" ||
				len(row.Samples) != len(run.Samples) || (row.SkipDetail != nil) != (run.SkipDetail != nil) {
				t.Fatal("identity projection changed this round's outcome", row)
			}
			detail, err := en.Run(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			detailJSON, err := json.Marshal(detail)
			if err != nil || string(detailJSON) != string(original) {
				t.Fatal("summary changed the complete historical record", err)
			}
			stored, err := json.Marshal(en.runs[run.ID])
			if err != nil || string(stored) != string(original) {
				t.Fatal("summary mutated stored evidence", err)
			}
		})
	}
}

func TestManualAnswerPreviewUsesFirstRetainedText(t *testing.T) {
	limit := strings.Repeat("答🙂", 48)
	for _, tc := range []struct {
		name, output, kind, text string
		truncated                bool
	}{
		{"collapsed whitespace", " \t甲\n\u3000 乙\r\n 🙂 ", "text", "甲 乙 🙂", false},
		{"exactly 96 runes", limit + "\n", "text", limit, false},
		{"over 96 runes", limit + "尾", "text", limit, true},
		{"padded answer", strings.Repeat(" ", 4100) + "21", "text", "21", false},
		{"utf8 scan boundary", "答" + strings.Repeat(" ", 4092) + "🙂", "text", "答", true},
		{"html", "<!DOCTYPE html>\n<html><body>Synthetic markup</body></html>", "html", "", false},
		{"fenced svg", "```svg\n<?xml version=\"1.0\"?>\n<!-- icon -->\n<SVG/>\n```", "svg", "", false},
		{"artwork after introduction", "这是生成的页面：\n\n<!doctype html><html></html>\n完毕。", "html", "", false},
		{"fenced fragment after introduction", "预览：\n~~~HTML\n<main>drawing</main>\n~~~\n说明", "html", "", false},
		{"other language fence", "```js\n<svg/>\n```", "text", "```js <svg/> ```", false},
		{"artwork after unrelated fence", "```js\n<svg/>\n```\n<svg/>", "svg", "", false},
		{"visual mention in text", "An <svg> element", "text", "An <svg> element", false},
		{"empty", "\t \u3000\r\n", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &Run{Source: "manual_test", Samples: []Sample{
				{Model: "a", ExecutionStatus: "failed", Error: "no response"},
				{Model: "a", Index: 1, Output: " \n"},
				{Model: "b", Index: 3, ExecutionStatus: "incomplete", Output: tc.output},
			}}
			preview := manualAnswerPreview(run)
			if tc.kind == "" {
				if preview != nil || runListSummary(run)["answer_preview"] != nil || runForDisplay(run).AnswerPreview != nil {
					t.Fatal("empty answers should omit the preview")
				}
				return
			}
			want := AnswerPreview{SampleSlot: SampleSlot{Model: "b", Index: 3}, Kind: tc.kind, Text: tc.text, Truncated: tc.truncated}
			if preview == nil || *preview != want {
				t.Fatalf("got %#v, want %#v", preview, want)
			}
		})
	}
}

func TestReviewRequiresExplicitStoredSamplePosition(t *testing.T) {
	en, _ := lifeFixture(t)
	en.runs["test"] = &Run{ID: "test", Status: "completed", Samples: []Sample{{Model: "a", Index: 0, Output: "first"}, {Model: "a", Index: 0, Output: "second"}}}
	for _, body := range []string{`{"id":"test","verdict":"correct"}`, `{"id":"test","sample_index":null,"verdict":"correct"}`} {
		response := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: "/v0/management/plugins/model-quality/runs/review", Body: []byte(body)})
		if response.StatusCode != 400 || en.runs["test"].Samples[0].Review != "" {
			t.Fatalf("ambiguous review targeted sample zero: status=%d review=%q", response.StatusCode, en.runs["test"].Samples[0].Review)
		}
	}
	for index, verdict := range []string{"correct", "incorrect"} {
		body := fmt.Sprintf(`{"id":"test","sample_index":%d,"verdict":%q}`, index, verdict)
		response := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: "/v0/management/plugins/model-quality/runs/review", Body: []byte(body)})
		if response.StatusCode != 200 || en.runs["test"].Samples[index].Review != verdict {
			t.Fatalf("original position %d failed to retain its own review", index)
		}
	}
}

func TestRunPaginationIsStableFilteredAndDetached(t *testing.T) {
	en, _ := lifeFixture(t)
	at := en.now()
	for _, id := range []string{"c", "a", "b"} {
		en.runs[id] = &Run{ID: id, Source: "manual_test", CreatedAt: at, Config: Frozen{RunConfig: lifeConfig()}, Samples: []Sample{{Output: strings.Repeat(id, 128)}}}
	}
	en.runs["probe"] = &Run{ID: "probe", Source: "scheduled_probe", CreatedAt: at.Add(time.Second)}
	result, err := en.QueryRuns(url.Values{"source": {"manual_test"}, "offset": {"1"}, "limit": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	entries := result["items"].([]Run)
	if result["total"] != 3 || len(entries) != 1 || entries[0].ID != "b" {
		t.Fatal(result)
	}
	entries[0].Config.Models[0] = "mutated"
	entries[0].Samples[0].Output = "mutated"
	if en.runs["b"].Config.Models[0] == "mutated" || en.runs["b"].Samples[0].Output == "mutated" {
		t.Fatal("query leaked mutable state")
	}
	beyond, err := en.QueryRuns(url.Values{"offset": {"100"}, "limit": {"100"}})
	if err != nil || len(beyond["items"].([]Run)) != 0 || beyond["offset"] != 4 {
		t.Fatal(beyond, err)
	}
	if _, err = en.QueryRuns(url.Values{"source": {"production"}}); err == nil {
		t.Fatal("invalid run source accepted")
	}
}

func TestRulePageShowsOnlyItsActiveMarkersInStableOrder(t *testing.T) {
	en, _, _ := scopeFixture(t)
	en.rules = map[string]*Rule{"b": {ID: "b"}, "a": {ID: "a"}}
	en.managed = map[string]*CredentialDisposition{
		"3": {RuleID: "a", AuthID: "3", State: "disabled"},
		"2": {RuleID: "a", AuthID: "2", State: "restored"},
		"1": {RuleID: "a", AuthID: "1", State: "disabled"},
		"4": {RuleID: "b", AuthID: "4", State: "disabled"},
	}
	page := en.queryRules(url.Values{"limit": {"1"}})
	rules := page["items"].([]ruleView)
	if page["total"] != 2 || len(rules) != 1 || rules[0].ID != "a" || len(rules[0].Handling) != 2 || rules[0].Handling[0].AuthID != "1" || rules[0].Handling[1].AuthID != "3" {
		t.Fatal(page)
	}
}

func TestManualAnswerMatchUsesFrozenQuestionWithoutChangingHistory(t *testing.T) {
	en, _ := lifeFixture(t)
	config := lifeConfig()
	config.Judge.Mode = "manual"
	run := &Run{ID: "old", Source: "manual_test", Config: Frozen{RunConfig: config, Question: Question{Kind: "random", Answer: "7"}}, Samples: []Sample{
		{ExecutionStatus: "completed", Output: " 7\n", QualityVerdict: "unknown"},
		{ExecutionStatus: "completed", Output: "8"},
		{ExecutionStatus: "completed", Output: "probably 7"},
		{ExecutionStatus: "incomplete", Output: "8"},
		{ExecutionStatus: "failed", Error: "offline"},
		{ExecutionStatus: "completed", Output: "8", Review: "unknown"},
		{ExecutionStatus: "completed", Output: "8", QualityVerdict: "correct"},
	}}
	en.runs[run.ID] = run
	detail, err := en.Run(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := en.QueryRuns(url.Values{"source": {"manual_test"}})
	if err != nil {
		t.Fatal(err)
	}
	history := page["items"].([]Run)[0]
	for i, want := range []string{"correct", "incorrect", "unknown", "", "", "", ""} {
		if detail.Samples[i].AnswerMatch != want || history.Samples[i].AnswerMatch != want || run.Samples[i].AnswerMatch != "" || detail.Samples[i].QualityVerdict != run.Samples[i].QualityVerdict {
			t.Fatalf("unexpected display or stored verdict for sample %d", i)
		}
	}
	run.Source = "scheduled_probe"
	if runForDisplay(run).Samples[0].AnswerMatch != "" {
		t.Fatal("display match leaked into rule history")
	}
}

func TestDeleteManualRunPersistsAndPreservesIdempotency(t *testing.T) {
	en, _ := lifeFixture(t)
	cfg := lifeConfig()
	cfg.IdempotencyKey = "delete-manual-run"
	run, err := en.Submit(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := en.DeleteManualRun(run.ID); err == nil {
		t.Fatal("deleted queued run")
	}
	if err := en.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		res := en.Handle(context.Background(), pluginapi.ManagementRequest{
			Method: "DELETE", Path: "/v0/management/plugins/model-quality/runs",
			Body: []byte(fmt.Sprintf(`{"id":%q}`, run.ID)),
		})
		if res.StatusCode != 200 {
			t.Fatalf("delete status %d: %s", res.StatusCode, res.Body)
		}
	}
	if _, err := en.Run(run.ID); err == nil {
		t.Fatal("deleted run still visible")
	}
	if err := en.store.Get("runs", run.ID, &Run{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stored run: %v", err)
	}
	if _, err := en.Submit(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "gone:") {
		t.Fatal("deleted run replayed", err)
	}
	for _, rule := range en.rules {
		probe := semanticRound(en, rule, "correct", "correct")
		if err := en.DeleteManualRun(probe.ID); err == nil {
			t.Fatal("deleted rule evidence")
		}
	}
}

func TestClearManualHistoryAcrossPages(t *testing.T) {
	en, host := lifeFixture(t)
	cfg := lifeConfig()
	var deletedIDs []string
	for i := range 12 {
		cfg.IdempotencyKey = fmt.Sprintf("clear-manual-%d", i)
		run, err := en.Submit(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := en.Cancel(run.ID); err != nil {
			t.Fatal(err)
		}
		deletedIDs = append(deletedIDs, run.ID)
	}
	at := en.now()
	kept := []*Run{
		{ID: "queued", Source: "manual_test", Status: "queued"},
		{ID: "worker", Source: "manual_test", Status: "completed", CompletedAt: &at},
		{ID: "historical-rule-link", Source: "manual_test", Status: "completed", CompletedAt: &at, Config: Frozen{RuleID: "rule"}},
		{ID: "probe", Source: "scheduled_probe", Status: "completed", CompletedAt: &at},
	}
	for _, r := range kept {
		en.runs[r.ID] = r
		if err := en.store.Put("runs", r.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	en.active["worker"] = func() { t.Error("clearing history cancelled an active worker") }
	req := pluginapi.ManagementRequest{Method: http.MethodDelete, Path: "/v0/management/plugins/model-quality/runs/history"}
	filtered := req
	filtered.Query = url.Values{"id": {deletedIDs[0]}}
	if res := en.Handle(context.Background(), filtered); res.StatusCode != http.StatusBadRequest || len(en.runs) != len(deletedIDs)+len(kept) {
		t.Fatal("filtered request unexpectedly cleared history", res.StatusCode)
	}
	for _, deleted := range []int{len(deletedIDs), 0} {
		res := en.Handle(context.Background(), req)
		var counts historyClearResult
		if res.StatusCode != http.StatusOK || json.Unmarshal(res.Body, &counts) != nil || counts.Deleted != deleted || counts.Kept != 3 {
			t.Fatalf("clear response %d: %s", res.StatusCode, res.Body)
		}
	}
	for _, id := range deletedIDs {
		if en.runs[id] != nil || !errors.Is(en.store.Get("runs", id, &Run{}), os.ErrNotExist) {
			t.Fatal("finished manual test survived clear", id)
		}
	}
	for _, r := range kept {
		if en.runs[r.ID] != r || en.store.Get("runs", r.ID, &Run{}) != nil {
			t.Fatal("lost retained run", r.ID)
		}
	}
	if _, err := en.Submit(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "gone:") || len(host.calls) != 0 {
		t.Fatal("clearing history allowed a previous test to replay", err)
	}
}

func TestRuleHistoryFiltersBeforePagination(t *testing.T) {
	en, _ := lifeFixture(t)
	at := en.now()
	for i := 0; i < 45; i++ {
		id := fmt.Sprintf("probe-%02d", i)
		rule := "first"
		if i%2 == 0 {
			rule = "second"
		}
		en.runs[id] = &Run{ID: id, Source: "scheduled_probe", CreatedAt: at.Add(time.Duration(i) * time.Second), Config: Frozen{RuleID: rule, ScopeBatchID: "batch"}}
	}
	en.runs["manual"] = &Run{ID: "manual", Source: "manual_test", Config: Frozen{RuleID: "first"}}
	query := url.Values{"source": {"scheduled_probe"}, "rule_id": {"first"}, "scope_batch_id": {"batch"}, "offset": {"20"}, "limit": {"20"}}
	result, err := en.QueryRuns(query)
	if err != nil {
		t.Fatal(err)
	}
	rows := result["items"].([]Run)
	if result["total"] != 22 || len(rows) != 2 || rows[0].ID != "probe-03" || rows[1].ID != "probe-01" {
		t.Fatalf("rule history pagination: %+v", result)
	}
	query.Set("rule_id", "missing")
	result, err = en.QueryRuns(query)
	if err != nil || result["total"] != 0 || len(result["items"].([]Run)) != 0 {
		t.Fatal(result, err)
	}
}

func TestHistoryAndMetricsCorruptionReturnUnavailable(t *testing.T) {
	for _, endpoint := range []string{"history", "metrics"} {
		t.Run(endpoint, func(t *testing.T) {
			en, _ := lifeFixture(t)
			bucket := "audit"
			if endpoint == "metrics" {
				bucket = "usage"
			}
			if err := en.store.Write(func(tx *bolt.Tx) error { return tx.Bucket([]byte(bucket)).Put([]byte("broken"), []byte(`{`)) }); err != nil {
				t.Fatal(err)
			}
			response := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: "/v0/management/plugins/model-quality/" + endpoint})
			if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(response.Body), `"code":"unavailable"`) {
				t.Fatal(response.StatusCode, string(response.Body))
			}
		})
	}
}

func TestMetricsUnknownProvenanceAndQualityDenominators(t *testing.T) {
	en, _ := lifeFixture(t)
	en.runs["sample"] = &Run{Source: "scheduled_probe", PrunedSamples: 1, PrunedSlots: []SampleSlot{{Model: "a", Index: 0}}, Config: Frozen{RunConfig: RunConfig{Models: []string{"a"}, Samples: 5}}, Samples: []Sample{
		{Model: "a", Index: 1, ExecutionStatus: "completed", QualityVerdict: "correct", Usage: &Usage{Total: 10}},
		{Model: "a", Index: 2, ExecutionStatus: "completed", QualityVerdict: "incorrect", Usage: &Usage{Total: 20}},
		{Model: "a", Index: 3, ExecutionStatus: "failed", QualityVerdict: "unknown"},
	}}
	en.runs["manual"] = &Run{Source: "manual_test", Config: Frozen{RunConfig: RunConfig{Models: []string{"a"}, Samples: 1, Judge: Judge{Mode: "manual"}}, Question: Question{Kind: "logic", Answer: "4"}}, Samples: []Sample{
		{ExecutionStatus: "completed", Output: "4", QualityVerdict: "unknown"},
	}}
	if match := runForDisplay(en.runs["manual"]).Samples[0].AnswerMatch; match != "correct" {
		t.Fatalf("expected display-only match, got %q", match)
	}
	for id, source := range map[string]string{"legacy": "", "untrusted": "arbitrary", "business": "production"} {
		if err := en.store.Put("usage", id, UsageEvent{ID: id, Source: source}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := en.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if stats["quality_pass_rate"] != float64(.5) || stats["coverage"] != float64(2)/5 || stats["planned"] != 5 || stats["unknown"] != 2 || stats["unknown_usage"] != 2 {
		t.Fatal(stats)
	}
	sources := stats["traffic_sources"].(map[string]map[string]int64)
	if sources["unknown"]["attempts"] != 2 || sources["production"]["attempts"] != 1 || sources["production"]["unknown_usage"] != 1 || len(sources) != 2 {
		t.Fatal(sources)
	}
}

func TestErrorClassificationDoesNotInspectIncidentalMessageText(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   string
		status int
	}{
		{fmt.Errorf("model not available for target: conflict-model"), "invalid_request", 400},
		{fmt.Errorf("invalid unavailable model name"), "invalid_request", 400},
		{fmt.Errorf("blocked: paused credential"), "blocked", 409},
		{fmt.Errorf("host_call_failed: %w", &pluginabi.Error{Code: "host_call_failed", Message: "action_conflict", HTTPStatus: 500}), "conflict", 409},
		{fmt.Errorf("host_call_failed: %w", &pluginabi.Error{Code: "host_call_failed", Message: "internal unavailable data", HTTPStatus: 500}), "unavailable", 503},
		{fmt.Errorf("storage_limit: retention required"), "storage_limit", 507},
		{fmt.Errorf("read state: %w", fmt.Errorf("unavailable: database closed")), "unavailable", 503},
	} {
		code, status := classifyError(tc.err)
		if code != tc.code || status != tc.status {
			t.Fatal(tc.err, code, status)
		}
	}
}

func TestPresetMutationGuardAndInputOwnership(t *testing.T) {
	en, _ := lifeFixture(t)
	config := lifeConfig()
	preset, err := en.SavePreset("saved", config)
	if err != nil {
		t.Fatal(err)
	}
	config.Models[0] = "changed"
	if preset.Config.Models[0] == "changed" {
		t.Fatal("preset shares caller memory")
	}
	en.mu.Lock()
	en.closed = true
	en.mu.Unlock()
	if _, err = en.SavePreset("after close", lifeConfig()); err == nil {
		t.Fatal("closed engine saved preset")
	}
	if err = en.DeletePreset(preset.ID); err == nil {
		t.Fatal("closed engine removed preset")
	}
}

func TestSnapshotsDetachNestedRunAndRuleState(t *testing.T) {
	temperature, ttft := 0.2, int64(12)
	now := time.Now().UTC()
	original := &Run{Config: Frozen{RunConfig: RunConfig{Models: []string{"m"}, Parameters: Parameters{Temperature: &temperature}}}, CompletedAt: &now, Samples: []Sample{{TTFTMS: &ttft, Usage: &Usage{Total: 4}, JudgeUsage: &Usage{Total: 2}}}, Controls: []Sample{{Usage: &Usage{Total: 3}}}}
	copy := cloneRun(original)
	*copy.Config.Parameters.Temperature = 1
	copy.Config.Models[0] = "changed"
	*copy.CompletedAt = time.Time{}
	*copy.Samples[0].TTFTMS = 99
	copy.Samples[0].Usage.Total = 999
	copy.Samples[0].JudgeUsage.Total = 999
	copy.Controls[0].Usage.Total = 999
	if temperature != 0.2 || ttft != 12 || now.IsZero() || original.Config.Models[0] != "m" || original.Samples[0].Usage.Total != 4 || original.Samples[0].JudgeUsage.Total != 2 || original.Controls[0].Usage.Total != 3 {
		t.Fatal("snapshot leaked a nested mutable field")
	}
	rule := &Rule{RunConfig: original.Config.RunConfig}
	cloned := cloneRule(rule)
	*cloned.Parameters.Temperature = 0.8
	cloned.Models[0] = "rule change"
	if temperature != 0.2 || rule.Models[0] != "m" {
		t.Fatal("rule snapshot leaked config")
	}
}

func TestRuleDeletionAndAuditShareOneTransaction(t *testing.T) {
	en, _, owner := manualOnlyFixture(t)
	if err := en.DeleteRule(context.Background(), owner.ID, owner.Revision); err != nil {
		t.Fatal(err)
	}
	entries, err := en.History()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.RuleID == owner.ID && entry.Kind == "rule_deleted" {
			found = true
		}
	}
	if !found || en.rules[owner.ID] != nil {
		t.Fatal("deletion missing its audit")
	}
	second, err := en.SaveRule(context.Background(), Rule{RunConfig: lifeConfig(), Enabled: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = en.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = en.DeleteRule(context.Background(), second.ID, second.Revision); err == nil {
		t.Fatal("failed deletion claimed success")
	}
	if en.rules[second.ID] == nil || en.fault == nil {
		t.Fatal("failed deletion published state or failed to stop writes")
	}
}
