package quality

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
	"net/url"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func ruleAPI(en *Engine, method, path, body string, qs url.Values) pluginapi.ManagementResponse {
	return en.Handle(context.Background(), pluginapi.ManagementRequest{Path: path, Method: method, Body: []byte(body), Query: qs})
}

func TestRuleCreationReceiptSurvivesDeletionAndParameterConflict(t *testing.T) {
	en, _, _ := scopeFixture(t, scopedTarget("one", "a", "b"))
	const body = `{"idempotency_key":"create-fixture","models":["a"],"question_id":"logic-boxes","target_scope":"all_enabled","activation":"keep","name":"one"}`
	var first, repeated Rule
	for _, result := range []*Rule{&first, &repeated} {
		reply := ruleAPI(en, "POST", "/rules", body, nil)
		if reply.StatusCode != 200 || json.Unmarshal(reply.Body, result) != nil || result.ID == "" {
			t.Fatalf("create: %d %s", reply.StatusCode, reply.Body)
		}
	}
	if first.ID != repeated.ID {
		t.Fatal("retry created a second rule")
	}
	if reply := ruleAPI(en, "POST", "/rules", `{"idempotency_key":"create-fixture","models":["b"],"question_id":"logic-boxes","target_scope":"all_enabled","activation":"keep","name":"one"}`, nil); reply.StatusCode != 409 {
		t.Fatalf("changed command returned %d", reply.StatusCode)
	}
	if err := en.DeleteRule(context.Background(), first.ID, first.Revision); err != nil {
		t.Fatal(err)
	}
	read := ruleAPI(en, "GET", "/rules/commands", "", url.Values{"idempotency_key": {"create-fixture"}})
	var receipt struct {
		RuleID string `json:"rule_id"`
		State  string
	}
	if read.StatusCode != 200 || json.Unmarshal(read.Body, &receipt) != nil || receipt.RuleID != first.ID || receipt.State != "completed" {
		t.Fatal("deletion lost the creation receipt", string(read.Body))
	}
	if reply := ruleAPI(en, "POST", "/rules", body, nil); reply.StatusCode != 410 {
		t.Fatal("replay after deletion", reply.StatusCode, string(reply.Body))
	}
}

type pausedDirectory struct {
	Host
	reached, proceed chan struct{}
}

func (h *pausedDirectory) Targets(ctx context.Context) ([]Target, error) {
	select {
	case h.reached <- struct{}{}:
	default:
	}
	select {
	case <-h.proceed:
		return h.Host.Targets(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRuleRunCommandDisconnectRetryCleanupAndRestart(t *testing.T) {
	en, base, rule := scopeFixture(t, scopedTarget("one", "a", "b"), scopedTarget("two", "a", "b"))
	h := &pausedDirectory{en.host, make(chan struct{}, 1), make(chan struct{})}
	en.host = h
	reqCtx, cancel := context.WithCancel(context.Background())
	request := pluginapi.ManagementRequest{Method: "POST", Path: "/rules/run", Body: []byte(fmt.Sprintf(`{"id":"%s","revision":%d,"idempotency_key":"run-1"}`, rule.ID, rule.Revision))}
	first := en.Handle(reqCtx, request)
	cancel()
	if first.StatusCode != 202 {
		t.Fatalf("command not accepted: %d %s", first.StatusCode, first.Body)
	}
	<-h.reached
	inspect := func() ruleCommandReceipt {
		t.Helper()
		r := ruleAPI(en, "GET", "/rules/commands", "", url.Values{"idempotency_key": {"run-1"}})
		var v ruleCommandReceipt
		if r.StatusCode != 200 || json.Unmarshal(r.Body, &v) != nil {
			t.Fatalf("command lookup: %d %s", r.StatusCode, r.Body)
		}
		return v
	}
	if status := inspect(); status.State != "running" || status.Result != nil {
		t.Fatal(status)
	}
	var duplicates sync.WaitGroup
	for range 12 {
		duplicates.Go(func() {
			if reply := en.Handle(context.Background(), request); reply.StatusCode != 202 {
				t.Errorf("duplicate rejected: %d", reply.StatusCode)
			}
		})
	}
	duplicates.Wait()
	wrong := request
	wrong.Body = []byte(fmt.Sprintf(`{"id":"%s","revision":%d,"idempotency_key":"run-1"}`, rule.ID, rule.Revision+1))
	if reply := en.Handle(context.Background(), wrong); reply.StatusCode != 409 {
		t.Fatal("different params shared receipt", reply.StatusCode)
	}
	close(h.proceed)
	en.wg.Wait()
	final := inspect()
	if final.State != "completed" || final.Result == nil || final.Result.BatchID == "" || final.Result.Count != 2 || len(en.runs) != 2 || len(en.queue) != 1 || base.executeCalls != 0 {
		t.Fatal("batch receipt or admission changed", final, len(en.runs), len(en.queue))
	}
	for _, r := range en.runs {
		if err := en.Cancel(r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := en.clearRuleHistory(); err != nil {
		t.Fatal(err)
	}
	if err := en.DeleteRule(context.Background(), rule.ID, rule.Revision); err != nil {
		t.Fatal(err)
	}
	en.rules, en.runs = map[string]*Rule{}, map[string]*Run{}
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	if reply := en.Handle(context.Background(), request); reply.StatusCode != 202 {
		t.Fatal("lost retry receipt after history and rule removal", reply.StatusCode)
	}
	after := inspect()
	if after.Result == nil || after.Result.BatchID != final.Result.BatchID || len(en.runs) != 0 || en.commandAdmissions[rule.ID] != "" || base.executeCalls != 0 {
		t.Fatal("old command was replayed")
	}
}

func TestInterruptedRuleCommandsRemainTerminal(t *testing.T) {
	en, h, rule := scopeFixture(t, scopedTarget("one", "a", "b"))
	for _, state := range []string{"accepted", "running"} {
		cmd := ruleCommandRecord{ruleCommandReceipt: ruleCommandReceipt{Key: "fake-" + state, Kind: "run", RuleID: rule.ID, Revision: rule.Revision, State: state}, RequestHash: "known-fake-hash"}
		if err := en.store.Write(func(tx *bolt.Tx) error { return putRuleCommand(tx, &cmd) }); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := en.load(); err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{"accepted", "running"} {
			got, err := en.findRuleCommand("fake-"+state, "known-fake-hash")
			if err != nil || got.State != "interrupted" || got.Error == nil || got.Error.Code != "interrupted" {
				t.Fatal(got, err)
			}
		}
	}
	if len(en.runs) != 0 || len(en.queue) != 0 || h.executeCalls != 0 {
		t.Fatal("interrupted commands started new work")
	}
}

func TestManualDispatchConsumesAlreadyDueOccurrence(t *testing.T) {
	en, h, rule := scopeFixture(t, scopedTarget("one", "a", "b"))
	rule, err := en.setRuleTimer(context.Background(), rule.ID, rule.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	now := rule.NextRun.Add(5 * time.Hour)
	en.now = func() time.Time { return now }
	batch, err := en.dispatchRule(context.Background(), rule.ID, rule.Revision, false)
	if err != nil || batch.Count != 1 {
		t.Fatal(batch, err)
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	en.Tick(now)
	if len(en.runs) != 1 || len(en.queue) != 0 || h.executeCalls != 2 || !en.rules[rule.ID].NextRun.Equal(nextRun(*rule, now)) {
		t.Fatal("accepted manual round followed by a second overdue batch", len(en.runs), len(en.queue), h.executeCalls)
	}
}

func TestRuleCommandRejectsUnacceptedAndNeverRunsStoppedScan(t *testing.T) {
	en, base, rule := scopeFixture(t, scopedTarget("one", "a", "b"))
	h := &pausedDirectory{en.host, make(chan struct{}, 1), make(chan struct{})}
	en.host = h
	key := "fake-stop"
	missing := ruleAPI(en, "GET", "/rules/commands", "", url.Values{"idempotency_key": {key}})
	if missing.StatusCode != 404 || !strings.Contains(string(missing.Body), `"code":"command_not_accepted"`) {
		t.Fatal("ambiguous missing-command response", string(missing.Body))
	}
	aborted, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := en.acceptRuleRun(aborted, key, rule.ID, rule.Revision); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled request accepted", err)
	}
	if r, err := en.findRuleCommand(key, ""); err != nil || r != nil {
		t.Fatal("created a receipt before acceptance", r, err)
	}
	if _, err := en.acceptRuleRun(context.Background(), key, rule.ID, rule.Revision); err != nil {
		t.Fatal(err)
	}
	<-h.reached
	if err := en.PauseRule(rule.ID, rule.Revision, true); err != nil {
		t.Fatal(err)
	}
	close(h.proceed)
	en.wg.Wait()
	r, err := en.ruleCommandStatus(key)
	if err != nil || r.State != "failed" || r.Error == nil || r.Error.Code != "conflict" || r.Result != nil || len(en.runs) != 0 || base.executeCalls != 0 {
		t.Fatal("stale command survived a rule stop", r, err)
	}
}
