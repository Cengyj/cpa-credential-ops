package quality

import (
	"fmt"
	"strings"
	"testing"
)

func TestAmbiguousProviderResponsesNeverBecomeAnswerEvidence(t *testing.T) {
	for _, tc := range []struct{ name, family, body string }{
		{"completion finish", "openai-compatibility", `{"model":"wire","choices":[{"message":{"content":"5"},"finish_reason":"stop","finish_reason":"length"}]}`},
		{"completion content", "openai-compatibility", `{"model":"wire","choices":[{"message":{"content":"5","content":"4"},"finish_reason":"stop"}]}`},
		{"responses status", "codex", "data: " + `{"type":"response.completed","response":{"status":"completed","status":"incomplete","model":"wire","output":[{"type":"message","status":"completed","content":[{"type":"output_text","text":"5"}]}]}}` + "\n\n"},
		{"claude stop", "claude", `{"model":"wire","type":"message","role":"assistant","stop_reason":"end_turn","stop_reason":"max_tokens","content":[{"type":"text","text":"5"}]}`},
		{"gemini finish", "gemini", `{"modelVersion":"wire","candidates":[{"finishReason":"STOP","finishReason":"MAX_TOKENS","content":{"role":"model","parts":[{"text":"5"}]}}]}`},
		{"interaction status", "interactions", `{"model":"wire","status":"completed","status":"failed","steps":[{"type":"model_output","content":[{"type":"text","text":"5"}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := parseProviderAnswer(tc.family, []byte(tc.body), 4096); err == nil {
				t.Fatal("ambiguous upstream answer was accepted")
			}
		})
	}
}

func TestAmbiguousCompletionStreamNeverBecomesEvidence(t *testing.T) {
	for _, body := range []string{
		`{"model":"wire","choices":[{"delta":{"content":"5"},"finish_reason":"stop","finish_reason":"length"}]}`,
		`{"model":"wire","choices":[{"delta":{"content":"5","content":"4"},"finish_reason":"stop"}]}`,
	} {
		c := newCollector(4096)
		if err := c.Feed([]byte("data: " + body + "\n\ndata: [DONE]\n\n")); err == nil && c.End() == nil {
			t.Fatal("ambiguous streaming completion was accepted")
		}
	}
}

func TestResponsesCompletionCannotFollowStreamEnd(t *testing.T) {
	completed := "data: " + `{"type":"response.completed","response":{"status":"completed","model":"wire","output":[{"type":"message","status":"completed","content":[{"type":"output_text","text":"5"}]}]}}` + "\n\n"
	if _, _, _, err := parseManagedResponses("data: [DONE]\n\n" + completed); err == nil {
		t.Fatal("completion after the terminal sentinel was accepted")
	}
	if output, model, _, err := parseManagedResponses(completed + "data: [DONE]\n\n"); err != nil || output != "5" || model != "wire" {
		t.Fatal("valid completion before stream end was rejected", output, model, err)
	}
}

func TestCompletionStreamCannotSwitchResponseModels(t *testing.T) {
	c := newCollector(4096)
	first := `{"model":"one","choices":[{"delta":{"content":"5"},"finish_reason":null}]}`
	last := `{"model":"two","choices":[{"delta":{},"finish_reason":"stop"}]}`
	if err := c.Feed([]byte("data: " + first + "\n\ndata: " + last + "\n\ndata: [DONE]\n\n")); err == nil && c.End() == nil {
		t.Fatal("mixed-model answer was accepted")
	}
}

func TestAmbiguousAnswersCannotDisableDeleteOrRestoreCredential(t *testing.T) {
	for _, action := range []string{disableCredential, deleteCredential, "restore"} {
		t.Run(action, func(t *testing.T) {
			f := managementTestFixture(t)
			policy := action
			if action == "restore" {
				policy = disableCredential
			}
			rule := f.rule(t, policy, true, action == "restore")
			if action == "restore" {
				f.answers["a"] = "5"
				if _, err := f.round(t, rule); err != nil || len(f.patches) != 1 || !f.entries["one"].Disabled {
					t.Fatal("fixture did not establish answer-owned suspension", err)
				}
			}
			before := len(f.patches)
			answer := "5"
			if action == "restore" {
				answer = "4"
			}
			f.upstreamBody = fmt.Sprintf("data: "+`{"type":"response.completed","response":{"status":"completed","status":"incomplete","model":"a","output":[{"type":"message","status":"completed","content":[{"type":"output_text","text":%q}]}]}}`+"\n\n", answer)
			runs, err := f.round(t, rule)
			if err != nil || len(runs) != 1 {
				t.Fatal("fixture round failed", runs, err)
			}
			if len(f.patches) != before || f.deletes != 0 {
				t.Fatalf("ambiguous answer mutated CPA: patches=%v deletes=%d", f.patches, f.deletes)
			}
			for _, sample := range runs[0].Samples {
				if sample.ExecutionStatus == "completed" || sample.QualityVerdict != "unknown" || sample.UpstreamCondition != "" {
					t.Fatal("ambiguous answer became judged or upstream-error evidence", sample)
				}
			}
			if strings.HasSuffix(runs[0].ActionStatus, "pending") {
				t.Fatal("ambiguous answer left a pending write")
			}
		})
	}
}
