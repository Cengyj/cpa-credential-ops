package quality

import (
	"fmt"
	"testing"
)

func TestCommercialChatAnswerRejectsToolAndForeignChoice(t *testing.T) {
	for _, tc := range []struct{ name, choice string }{
		{"legacy function", `"message":{"content":"5","function_call":{"name":"lookup","arguments":"{}"}}`},
		{"nonzero choice", `"index":1,"message":{"content":"5"}`},
		{"negative choice", `"index":-1,"message":{"content":"5"}`},
		{"string choice", `"index":"0","message":{"content":"5"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"model":"wire","choices":[{%s,"finish_reason":"stop"}]}`, tc.choice)
			if _, _, _, err := parseProviderAnswer("openai-compatibility", []byte(body), 4096); err == nil {
				t.Fatal("tool output or unrelated choice was accepted as an answer")
			}
		})
	}
	for _, fields := range []string{``, `"index":0,`} {
		body := fmt.Sprintf(`{"model":"wire","choices":[{%s"message":{"content":"4","function_call":null,"tool_calls":null},"finish_reason":"stop"}]}`, fields)
		if answer, _, _, err := parseProviderAnswer("openai-compatibility", []byte(body), 4096); err != nil || answer != "4" {
			t.Fatal("nullable unused tool fields rejected a valid answer", answer, err)
		}
	}
}

func TestCommercialChatStreamRejectsJoinedResponseIDs(t *testing.T) {
	for _, secondID := range []string{"second", "first", ""} {
		c := newCollector(4096)
		first := `{"id":"first","model":"wire","choices":[{"index":0,"delta":{"content":"5"}}]}`
		id := ""
		if secondID != "" {
			id = fmt.Sprintf(`"id":%q,`, secondID)
		}
		last := fmt.Sprintf(`{%s"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, id)
		err := c.Feed([]byte("data: " + first + "\n\ndata: " + last + "\n\ndata: [DONE]\n\n"))
		if err == nil {
			err = c.End()
		}
		if (err != nil) != (secondID == "second") {
			t.Fatalf("second id %q: %v", secondID, err)
		}
	}
}

func commercialResponsesFixture(answer, details, firstID, lastID string) string {
	before := ""
	if firstID != "" {
		before = fmt.Sprintf("data: "+`{"type":"response.created","response":{"id":%q,"status":"in_progress","model":"a"}}`+"\n\n", firstID)
	}
	return before + fmt.Sprintf("data: "+`{"type":"response.completed","response":{"id":%q,"status":"completed","incomplete_details":%s,"model":"a","output":[{"type":"message","status":"completed","content":[{"type":"output_text","text":%q}]}]}}`+"\n\n", lastID, details, answer)
}

func TestCommercialContradictoryResponsesCannotMutateCredential(t *testing.T) {
	for _, mode := range []string{"incomplete details", "joined response IDs"} {
		for _, action := range []string{disableCredential, deleteCredential, "restore"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				f := managementTestFixture(t)
				policy := action
				if action == "restore" {
					policy = disableCredential
				}
				rule := f.rule(t, policy, true, action == "restore")
				if action == "restore" {
					f.answers["a"] = "5"
					if _, err := f.round(t, rule); err != nil || len(f.patches) != 1 {
						t.Fatal("failed to establish owned disable", err)
					}
				}
				answer, details, firstID := "5", "null", "first"
				if action == "restore" {
					answer = "4"
				}
				if mode == "incomplete details" {
					details, firstID = `{"reason":"max_output_tokens"}`, "second"
				}
				before := len(f.patches)
				f.upstreamBody = commercialResponsesFixture(answer, details, firstID, "second")
				runs, err := f.round(t, rule)
				if err != nil || len(runs) != 1 {
					t.Fatal("fixture round failed", err)
				}
				if len(f.patches) != before || f.deletes != 0 {
					t.Fatalf("contradictory response mutated CPA: patches=%v deletes=%d", f.patches, f.deletes)
				}
				for _, s := range runs[0].Samples {
					if s.ExecutionStatus == "completed" || s.QualityVerdict != "unknown" || s.UpstreamCondition != "" {
						t.Fatal("contradictory response became actionable evidence", s)
					}
				}
			})
		}
	}
}

func TestCommercialResponsesIdentityAllowsConsistentAndLegacyStreams(t *testing.T) {
	for _, first := range []string{"same", ""} {
		body := commercialResponsesFixture("4", "null", first, "same")
		if output, model, _, err := parseManagedResponses(body); err != nil || output != "4" || model != "a" {
			t.Fatal("valid Responses completion rejected", output, model, err)
		}
	}
}
