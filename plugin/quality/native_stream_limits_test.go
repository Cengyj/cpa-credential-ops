package quality

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func nativeStreamEvent(t *testing.T, content, finish string) []byte {
	t.Helper()
	var terminal any
	if finish != "" {
		terminal = finish
	}
	raw, err := json.Marshal(map[string]any{
		"id": "chatcmpl-offline-stream", "object": "chat.completion.chunk", "created": 1730000000,
		"model": "wire", "system_fingerprint": "offline-fingerprint",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": content}, "finish_reason": terminal}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type nativeStreamCounts struct{ opens, reads, closes int }

func attachNativeStream(t *testing.T, host *NativeHost, chunks [][]byte, check func(pluginapi.HostModelExecutionRequest)) *nativeStreamCounts {
	t.Helper()
	counts := &nativeStreamCounts{}
	fallback := host.Call
	host.Call = func(method string, in, out any) error {
		switch method {
		case "host.model.execute_stream":
			counts.opens++
			if check != nil {
				check(in.(pluginapi.HostModelExecutionRequest))
			}
			*out.(*pluginapi.HostModelStreamResponse) = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "offline-stream"}
			return nil
		case "host.model.stream_read":
			if in.(pluginapi.HostModelStreamReadRequest).StreamID != "offline-stream" {
				t.Fatal("read the wrong stream")
			}
			response := out.(*pluginapi.HostModelStreamReadResponse)
			if counts.reads < len(chunks) {
				response.Payload = chunks[counts.reads]
			} else {
				response.Done = true
			}
			counts.reads++
			return nil
		case "host.model.stream_close":
			if in.(pluginapi.HostModelStreamCloseRequest).StreamID != "offline-stream" {
				t.Fatal("closed the wrong stream")
			}
			counts.closes++
			return nil
		default:
			if fallback != nil {
				return fallback(method, in, out)
			}
			t.Errorf("unexpected native callback %s", method)
			return fmt.Errorf("unexpected callback")
		}
	}
	return counts
}

func nativeStreamChunks(t *testing.T, answer string, sse bool) [][]byte {
	t.Helper()
	chunks := make([][]byte, 0, len(answer)+2)
	for _, character := range answer {
		chunks = append(chunks, nativeStreamEvent(t, string(character), ""))
	}
	chunks = append(chunks, nativeStreamEvent(t, "", "stop"))
	chunks = append(chunks, []byte(`{"id":"chatcmpl-offline-stream","model":"wire","choices":[],"usage":{"prompt_tokens":21,"completion_tokens":512,"total_tokens":533}}`))
	if sse {
		var joined bytes.Buffer
		for _, raw := range chunks {
			joined.WriteString("data: ")
			joined.Write(raw)
			joined.WriteString("\n\n")
		}
		joined.WriteString("data: [DONE]\n\n")
		return [][]byte{joined.Bytes()}
	}
	return chunks
}

func TestManualNativeHTMLStreamUsesAnswerLimitForProviderAndFile(t *testing.T) {
	answer := "<!DOCTYPE html><html><body><svg>" + strings.Repeat(`<path d="M0 0L10 10"/>`, 400) + "</svg></body></html>"
	for _, provider := range []bool{false, true} {
		for _, sse := range []bool{false, true} {
			t.Run(fmt.Sprintf("provider=%v/sse=%v", provider, sse), func(t *testing.T) {
				en, _, _ := semanticsEngine(t)
				var fixture *configAdapterFixture
				host := &NativeHost{Call: func(method string, _, out any) error {
					switch method {
					case "host.auth.list":
						return decode([]byte(`{"files":[{"id":"auth-a","auth_index":"index-a","provider":"offline"}]}`), out)
					case "host.auth.models":
						return decode([]byte(`{"credentials":[{"id":"auth-a","auth_index":"index-a","provider":"offline","models":["a"]}]}`), out)
					case "host.auth.get_runtime":
						return decode([]byte(`{"auth":{"id":"auth-a","auth_index":"index-a","provider":"offline","status":"active","source":"file"}}`), out)
					}
					t.Fatalf("unexpected credential callback %s", method)
					return nil
				}}
				config := lifeConfig()
				if provider {
					fixture = newConfigAdapterFixture(t)
					host = fixture.host
					for _, route := range fixture.routes() {
						if route.family == "codex" && route.key["api-key"] == "single-key-one" {
							config.AuthID, config.Provider, config.Models = route.id, route.executor, []string{"alias"}
						}
					}
					if config.AuthID == "auth-a" {
						t.Fatal("fake provider route is missing")
					}
				}
				en.host = host
				config.QuestionID, config.Judge.Mode = "visual-pelican-html", "manual"
				config.Parameters = Parameters{Stream: true, MaxTokens: 2048}
				chunks := nativeStreamChunks(t, answer, sse)
				wireBytes := 0
				for _, chunk := range chunks {
					wireBytes += len(chunk)
				}
				if wireBytes <= en.cfg.MaxResponseBytes || len(answer) >= en.cfg.MaxResponseBytes {
					t.Fatal("fixture does not reproduce envelope overhead exhausting the old answer limit")
				}
				if sse && len(chunks[0]) <= newNativeStreamCollector(en.cfg.MaxResponseBytes).frameLimit {
					t.Fatal("aggregated callback does not exceed the individual frame limit")
				}
				counts := attachNativeStream(t, host, chunks, func(req pluginapi.HostModelExecutionRequest) {
					if req.AuthID != config.AuthID || req.ForcedProvider != config.Provider || req.Model != config.Models[0] || !req.Stream || ojson.GetBytes(req.Body, "max_tokens").Int() != 2048 {
						t.Fatal("manual stream lost its credential binding or token budget", req)
					}
				})
				body, err := json.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				reply := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: http.MethodPost, Path: "/v0/management/plugins/model-quality/runs", Body: body, Headers: http.Header{"Authorization": {"Bearer offline-panel"}}})
				if reply.StatusCode != http.StatusAccepted {
					t.Fatalf("normal manual submission failed: %d %s", reply.StatusCode, reply.Body)
				}
				id := ojson.GetBytes(reply.Body, "run_id").String()
				en.execute(<-en.queue)
				detail := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/model-quality/runs/detail", Query: url.Values{"id": {id}}})
				var run Run
				if err := json.Unmarshal(detail.Body, &run); err != nil || detail.StatusCode != http.StatusOK || run.Status != "completed" || len(run.Samples) != 1 {
					t.Fatalf("manual result unavailable: %d %s %v", detail.StatusCode, detail.Body, err)
				}
				sample := run.Samples[0]
				if sample.ExecutionStatus != "completed" || sample.Output != answer || sample.Error != "" || sample.QualityVerdict != "unknown" || sample.Evidence.Verified || sample.Comparable || sample.Usage == nil || sample.Usage.Total != 533 || sample.TTFTMS == nil {
					t.Fatalf("HTML answer was truncated, lost or promoted to quality evidence: %+v", sample)
				}
				if provider && (sample.Evidence.Path != "provider_native_probe" || fixture.calls != 0 || fixture.writes != 0) {
					t.Fatal("manual provider test bypassed native routing or mutated the provider", sample.Evidence, fixture.calls, fixture.writes)
				}
				if counts.opens != 1 || counts.reads != len(chunks)+1 || counts.closes != 1 || en.budget.Calls != 1 || en.budget.ReservedTokens != 2048 || en.budget.KnownTokens != 533 || len(en.budgetPending) != 0 {
					t.Fatal("stream cleanup or once-only budget accounting changed", counts, en.budget)
				}
			})
		}
	}
}

func TestNativeStreamFragmentationCountsOnlyReceivedWireBytes(t *testing.T) {
	answer := strings.Repeat("<p>中文&\"</p>", 12)
	for _, sse := range []bool{false, true} {
		for _, width := range []int{1, 7, 251} {
			t.Run(fmt.Sprintf("sse=%v/width=%d", sse, width), func(t *testing.T) {
				chunks := nativeStreamChunks(t, answer, sse)
				if sse {
					chunks[0] = append([]byte(": heartbeat\r\nevent: completion\r\nid: offline\r\n\r\n"), chunks[0]...)
				}
				c := newNativeStreamCollector(1024)
				wire := 0
				for _, raw := range chunks {
					wire += len(raw)
					for start := 0; start < len(raw); start += width {
						if err := c.FeedHostChunk(raw[start:min(start+width, len(raw))]); err != nil {
							t.Fatalf("valid fragmented event failed: %v", err)
						}
					}
				}
				if !sse {
					c.done = c.rawEvents && c.finish != ""
				}
				if err := c.End(); err != nil || c.text.String() != answer || c.received != wire || c.usage == nil || c.usage.Total != 533 {
					t.Fatal("fragmentation changed output, usage or wire byte accounting", err, c.received, wire, c.text.String())
				}
			})
		}
	}
}

func TestNativeStreamLimitsRemainSeparateAndBounded(t *testing.T) {
	const limit = 1024
	exact := strings.Repeat("界", 341) + "a"
	frameLimit := newNativeStreamCollector(limit).frameLimit
	oversized := []byte(`{"choices":[{"delta":{"reasoning_content":"` + strings.Repeat("r", frameLimit) + `"}}]}`)
	transportChunk := []byte("data: " + `{"choices":[{"delta":{"reasoning_content":"` + strings.Repeat("r", 16000) + `"}}]}` + "\n\n")
	transportChunks := [][]byte{nativeStreamEvent(t, "4", "")}
	for n := 0; n < (1<<20)/len(transportChunk)+1; n++ {
		transportChunks = append(transportChunks, transportChunk)
	}
	for _, tc := range []struct {
		name, output, status, message, reason string
		chunks                                [][]byte
	}{
		{"escaped answer at limit", strings.Repeat("<", limit), "completed", "", "", [][]byte{nativeStreamEvent(t, strings.Repeat("<", limit), "stop")}},
		{"utf8 answer at limit", exact, "completed", "", "", [][]byte{nativeStreamEvent(t, exact, "stop")}},
		{"utf8 answer over limit", exact, "failed", "response answer limit exceeded", "", [][]byte{nativeStreamEvent(t, exact, ""), nativeStreamEvent(t, "b", "stop")}},
		{"raw frame over limit", "4", "failed", "response frame limit exceeded", "", [][]byte{nativeStreamEvent(t, "4", ""), oversized}},
		{"split frame over limit", "4", "failed", "response frame limit exceeded", "", [][]byte{nativeStreamEvent(t, "4", ""), []byte("data:" + strings.Repeat(" ", frameLimit-5)), []byte(" ")}},
		{"transport over limit", "4", "failed", "response transport limit exceeded", "", transportChunks},
		{"token stop", "4", "incomplete", "", "response output token limit reached", [][]byte{nativeStreamEvent(t, "4", "length")}},
		{"missing stop", "4", "failed", "premature stream EOF", "", [][]byte{nativeStreamEvent(t, "4", "")}},
		{"answer after stop", "4", "failed", "answer content after completion status", "", [][]byte{nativeStreamEvent(t, "4", "stop"), nativeStreamEvent(t, "5", "")}},
		{"changed identity", "4", "failed", "conflicting completion identity", "", [][]byte{nativeStreamEvent(t, "4", ""), []byte(`{"id":"other-response","choices":[{"delta":{},"finish_reason":"stop"}]}`)}},
		{"answer after terminal", "4", "failed", "data after terminal event", "", [][]byte{nativeStreamEvent(t, "4", "stop"), []byte("[DONE]"), nativeStreamEvent(t, "5", "")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &NativeHost{MaxResponseBytes: 2048}
			counts := attachNativeStream(t, host, tc.chunks, nil)
			sample := host.Execute(context.Background(), Execution{AuthID: "offline", Provider: "offline", Model: "alias", ResponseLimit: limit, Parameters: Parameters{Stream: true, MaxTokens: 2048}})
			if sample.ExecutionStatus != tc.status || sample.Output != tc.output || sample.Error != tc.message || sample.Reason != tc.reason || sample.QualityVerdict != "unknown" || sample.Evidence.Verified || sample.Comparable || counts.opens != 1 || counts.closes != 1 {
				t.Fatal("native stream boundary classification or cleanup changed", sample, counts)
			}
		})
	}
	for _, limit := range []int{1024, 262144, 2 << 20, int(^uint(0) >> 1)} {
		c := newNativeStreamCollector(limit)
		if c.frameLimit < 1024 || c.frameLimit > 16<<20 || c.transportLimit < 1<<20 || c.transportLimit > 64<<20 {
			t.Fatal("expanded budgets overflowed or exceeded hard caps", c.frameLimit, c.transportLimit)
		}
	}
}

func TestNativeStreamRejectsExcessBeforeBuffering(t *testing.T) {
	c := newNativeStreamCollector(1024)
	if err := c.FeedHostChunk([]byte("data:" + strings.Repeat(" ", c.frameLimit-5))); err != nil {
		t.Fatal(err)
	}
	before := len(c.pending)
	if err := c.FeedHostChunk(bytes.Repeat([]byte("x"), c.frameLimit)); err == nil || err.Error() != "response frame limit exceeded" || len(c.pending) != before {
		t.Fatal("oversized continuation was buffered before checking its frame limit", err, len(c.pending), before)
	}
	c = newNativeStreamCollector(1024)
	if err := c.FeedHostChunk(bytes.Repeat([]byte("\n"), c.transportLimit+1)); err == nil || err.Error() != "response transport limit exceeded" || len(c.pending) != 0 || c.text.Len() != 0 {
		t.Fatal("oversized aggregate callback was buffered before checking transport bytes", err)
	}
}

func TestNativeStreamLimitFailureStillHidesProviderSecrets(t *testing.T) {
	f := newConfigAdapterFixture(t)
	var selected providerRoute
	for _, route := range f.routes() {
		if route.family == "codex" && route.key["api-key"] == "single-key-one" {
			selected = route
		}
	}
	chunks := [][]byte{nativeStreamEvent(t, "single-key-one", ""), nativeStreamEvent(t, strings.Repeat("x", 1024), "stop")}
	counts := attachNativeStream(t, f.host, chunks, nil)
	sample := f.host.Execute(context.Background(), Execution{AuthID: selected.id, Provider: selected.executor, Model: "alias", ResponseLimit: 1024, Parameters: Parameters{Stream: true, MaxTokens: 2048}})
	if sample.ExecutionStatus != "incomplete" || sample.Output != "" || sample.ResponseModel != "" || sample.Error != "响应包含敏感配置内容，已隐藏。" || sample.QualityVerdict != "unknown" || counts.closes != 1 || f.writes != 0 || f.calls != 0 {
		t.Fatal("a failed stream exposed provider secrets or mutated configuration", sample, counts)
	}
}
