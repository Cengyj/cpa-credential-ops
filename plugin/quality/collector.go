package quality

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

type collector struct {
	pending         []byte
	pendingMetadata bool
	text            strings.Builder
	limit           int
	frameLimit      int
	transportLimit  int
	nativeStream    bool
	received        int
	done            bool
	rawEvents       bool
	finish, model   string
	id              string
	refusal         bool
	usage           *Usage
}

func newCollector(limit int) *collector {
	return &collector{limit: limit, frameLimit: limit, transportLimit: limit}
}

// Native streams include repeated JSON envelopes, reasoning and SSE metadata.
// The configured answer limit does not also measure these transport bytes.
func newNativeStreamCollector(limit int) *collector {
	const frameOverhead = 16 << 10
	const maxFrame = 16 << 20
	const maxTransport = 64 << 20
	frame := maxFrame
	if limit <= (maxFrame-frameOverhead)/6 {
		frame = max(0, limit)*6 + frameOverhead
	}
	transport := maxTransport
	if limit < maxTransport/64 {
		transport = max(1<<20, max(0, limit)*64)
	}
	return &collector{limit: limit, frameLimit: frame, transportLimit: transport, nativeStream: true}
}

func (c *collector) receive(size int) error {
	if size > c.transportLimit-c.received {
		if c.nativeStream {
			return fmt.Errorf("response transport limit exceeded")
		}
		return fmt.Errorf("response limit exceeded")
	}
	c.received += size
	return nil
}

func (c *collector) checkFrame(size int) error {
	if size > c.frameLimit-len(c.pending) {
		if c.nativeStream {
			return fmt.Errorf("response frame limit exceeded")
		}
		return fmt.Errorf("response limit exceeded")
	}
	return nil
}

func (c *collector) Feed(raw []byte) error {
	if err := c.receive(len(raw)); err != nil {
		return err
	}
	return c.feedLines(raw, false)
}

// Process each line before buffering the next one. A host callback may contain
// many small SSE frames, so its total payload is not a single frame's size.
func (c *collector) feedLines(raw []byte, hostBoundary bool) error {
	for len(raw) > 0 {
		pos := bytes.IndexByte(raw, '\n')
		if pos < 0 {
			if err := c.checkFrame(len(raw)); err != nil {
				return err
			}
			c.pending = append(c.pending, raw...)
			break
		}
		if err := c.checkFrame(pos); err != nil {
			return err
		}
		line := raw[:pos]
		if len(c.pending) > 0 {
			c.pending = append(c.pending, line...)
			line = c.pending
		}
		if err := c.sseLine(line); err != nil {
			return err
		}
		c.pending = c.pending[:0]
		c.pendingMetadata = false
		raw = raw[pos+1:]
	}
	if hostBoundary {
		return c.hostRecord()
	}
	return nil
}

func (c *collector) sseLine(raw []byte) error {
	line := bytes.TrimSpace(raw)
	if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) || bytes.HasPrefix(line, []byte("event:")) || bytes.HasPrefix(line, []byte("id:")) {
		return nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return fmt.Errorf("invalid SSE line")
	}
	data := bytes.TrimSpace(line[len("data:"):])
	if bytes.Equal(data, []byte("[DONE]")) {
		c.done = true
		return nil
	}
	if c.done {
		return fmt.Errorf("data after terminal event")
	}
	return c.event(data, true)
}

// Official executors can forward complete scanner records without delimiters.
// Consume those boundaries without adding synthetic bytes to the wire budget.
func (c *collector) hostRecord() error {
	line := bytes.TrimSpace(c.pending)
	if bytes.Equal(line, []byte("[DONE]")) {
		c.done = true
		c.pending = c.pending[:0]
		return nil
	}
	metadata := len(line) == 0 || bytes.HasPrefix(line, []byte(":")) || bytes.HasPrefix(line, []byte("event:")) || bytes.HasPrefix(line, []byte("id:"))
	if bytes.HasPrefix(line, []byte("data:")) {
		data := bytes.TrimSpace(line[len("data:"):])
		if bytes.Equal(data, []byte("[DONE]")) {
			c.done = true
		} else {
			if !possibleJSONObject(data) || !ojson.ValidBytes(data) {
				return nil
			}
			if c.done {
				return fmt.Errorf("data after terminal event")
			}
			if err := c.validEvent(data, true); err != nil {
				return err
			}
		}
	} else if metadata {
		// Retain a possible split comment until a newline or the next complete
		// scanner record starts; a callback boundary may split an SSE line.
		c.pendingMetadata = len(line) > 0
		if c.pendingMetadata {
			return nil
		}
	} else {
		return nil
	}
	c.pending = c.pending[:0]
	return nil
}
func (c *collector) End() error {
	if len(bytes.TrimSpace(c.pending)) > 0 && !c.pendingMetadata {
		return fmt.Errorf("incomplete SSE frame")
	}
	if !c.done || c.finish == "" {
		return fmt.Errorf("premature stream EOF")
	}
	return nil
}
func (c *collector) Completion(raw []byte) error {
	if err := c.event(raw, false); err != nil {
		return err
	}
	c.done = true
	return nil
}
func (c *collector) event(raw []byte, stream bool) error {
	if !ojson.ValidBytes(raw) {
		return fmt.Errorf("invalid upstream JSON")
	}
	return c.validEvent(raw, stream)
}

func (c *collector) validEvent(raw []byte, stream bool) error {
	if uniqueJSON(raw) != nil {
		return fmt.Errorf("invalid upstream JSON")
	}
	root := ojson.ParseBytes(raw)
	if root.Get("error").Exists() {
		return fmt.Errorf("upstream error event")
	}
	if !observeResponseIdentity(root.Get("model"), &c.model) || !observeResponseIdentity(root.Get("id"), &c.id) {
		return fmt.Errorf("conflicting completion identity")
	}
	u := root.Get("usage")
	if u.Exists() && u.Raw != "null" {
		c.usage = parseUsage(u)
	}
	choices := root.Get("choices").Array()
	if len(choices) > 1 {
		return fmt.Errorf("unexpected multiple choices")
	}
	if len(choices) == 0 {
		if stream && u.Exists() {
			return nil
		}
		return fmt.Errorf("no completion choices")
	}
	choice := choices[0]
	if index := choice.Get("index"); index.Exists() && (index.Type != ojson.Number || index.Raw != "0") {
		return fmt.Errorf("unexpected completion choice index")
	}
	key := "message"
	if stream {
		key = "delta"
	}
	msg := choice.Get(key)
	if role := msg.Get("role"); role.Exists() && role.Raw != "null" && (role.Type != ojson.String || role.String() != "" && role.String() != "assistant") {
		return fmt.Errorf("non-assistant completion message")
	}
	if msg.Get("refusal").String() != "" {
		c.refusal = true
	}
	for _, key := range []string{"tool_calls", "function_call"} {
		if value := msg.Get(key); value.Exists() && value.Raw != "null" {
			return fmt.Errorf("tool output is not a completed answer")
		}
	}
	content := msg.Get("content")
	if content.Exists() && content.Raw != "null" {
		if content.Type != ojson.String {
			return fmt.Errorf("non-text content")
		}
		if c.finish != "" && content.String() != "" {
			return fmt.Errorf("answer content after completion status")
		}
		if len(content.String()) > c.limit-c.text.Len() {
			if c.nativeStream {
				return fmt.Errorf("response answer limit exceeded")
			}
			return fmt.Errorf("output limit exceeded")
		}
		c.text.WriteString(content.String())
	}
	finish := choice.Get("finish_reason").String()
	if finish != "" {
		if c.finish != "" && c.finish != finish {
			return fmt.Errorf("conflicting completion status")
		}
		c.finish = finish
	}
	return nil
}

// Missing identity fields are compatible with older endpoints and usage frames.
// Once supplied, an identity cannot change inside the same response stream.
func observeResponseIdentity(value ojson.Result, current *string) bool {
	if !value.Exists() || value.Raw == "null" {
		return true
	}
	if value.Type != ojson.String {
		return false
	}
	next := value.String()
	if next == "" {
		return true
	}
	if *current != "" && *current != next {
		return false
	}
	*current = next
	return true
}

func parseUsage(value ojson.Result) *Usage {
	counts := [3]int64{}
	for i, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		field := value.Get(key)
		if field.Type != ojson.Number {
			return nil
		}
		count, err := strconv.ParseInt(field.Raw, 10, 64)
		if err != nil || count < 0 {
			return nil
		}
		counts[i] = count
	}
	if counts[2] < counts[0] || counts[2]-counts[0] < counts[1] {
		return nil
	}
	return &Usage{Input: counts[0], Output: counts[1], Total: counts[2]}
}

// FeedHostChunk also accepts complete event records emitted without HTTP SSE delimiters by the host translator.
func (c *collector) FeedHostChunk(raw []byte) error {
	if err := c.receive(len(raw)); err != nil {
		return err
	}
	boundary := bytes.TrimSpace(raw)
	if c.pendingMetadata && (bytes.HasPrefix(boundary, []byte("{")) || bytes.HasPrefix(boundary, []byte("data:")) || bytes.HasPrefix(boundary, []byte(":")) || bytes.HasPrefix(boundary, []byte("event:")) || bytes.HasPrefix(boundary, []byte("id:")) || bytes.Equal(boundary, []byte("[DONE]"))) {
		c.pending = c.pending[:0]
		c.pendingMetadata = false
	}
	start := c.pending
	if len(start) == 0 {
		start = raw
	}
	if bytes.HasPrefix(bytes.TrimSpace(start), []byte("{")) {
		if err := c.checkFrame(len(raw)); err != nil {
			return err
		}
		frame := raw
		if len(c.pending) > 0 || !possibleJSONObject(raw) {
			c.pending = append(c.pending, raw...)
			frame = c.pending
		}
		if !possibleJSONObject(frame) || !ojson.ValidBytes(frame) {
			if len(c.pending) == 0 {
				c.pending = append(c.pending, raw...)
			}
			return nil
		}
		if c.done {
			return fmt.Errorf("data after terminal event")
		}
		c.rawEvents = true
		if err := c.validEvent(frame, true); err != nil {
			return err
		}
		c.pending = c.pending[:0]
		return nil
	}
	return c.feedLines(raw, true)
}

func possibleJSONObject(raw []byte) bool {
	value := bytes.TrimSpace(raw)
	return len(value) > 1 && value[0] == '{' && value[len(value)-1] == '}'
}
