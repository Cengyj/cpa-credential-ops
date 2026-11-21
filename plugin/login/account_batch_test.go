package login

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/cliproxyapi-plugins/model-quality/login/account"
	"github.com/cliproxyapi-plugins/model-quality/login/conversion"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func batchRequest(t *testing.T, s *Service, ctx context.Context, path, key string, body any) pluginapi.ManagementResponse {
	t.Helper()
	raw, err := Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	return s.Handle(ctx, pluginapi.ManagementRequest{Method: http.MethodPost, Path: RoutePrefix + path, Body: raw, Headers: http.Header{"Idempotency-Key": {key}, "Prefer": {"respond-async"}}})
}

func lookupBatch(s *Service, key string) pluginapi.ManagementResponse {
	return s.Handle(context.Background(), pluginapi.ManagementRequest{Method: http.MethodGet, Path: RoutePrefix + "/batches", Query: url.Values{"id": {key}}})
}

func TestAsyncConversionAdmissionSurvivesLostReply(t *testing.T) {
	opts := offlineOptions(t)
	h := newFakeHost()
	opts.Call = h.call
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	defer resume()
	opts.Prepare = func(context.Context, string, string, string) error {
		close(entered)
		<-release
		return nil
	}
	s := offlineService(t, opts)
	const key = "fixture-async-conversion"
	body := map[string]any{"source": conversionFixture(t), "indexes": []int{0}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response := make(chan pluginapi.ManagementResponse, 1)
	go func() { response <- batchRequest(t, s, ctx, "/conversion/upload", key, body) }()
	select {
	case r := <-response:
		if r.StatusCode != 202 || ojson.GetBytes(r.Body, "id").String() != key {
			t.Fatalf("expected a durable batch ID before remote completion, got %d", r.StatusCode)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP submission blocked on the remote credential write")
	}
	cancel()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("accepted conversion did not run independently")
	}
	if r := lookupBatch(s, key); r.StatusCode != 200 || ojson.GetBytes(r.Body, "status").String() != "running" {
		t.Fatalf("missing running receipt: %s", r.Body)
	}
	if r := batchRequest(t, s, context.Background(), "/conversion/upload", key, body); r.StatusCode != 202 {
		t.Fatalf("duplicate submission %d", r.StatusCode)
	}
	if r := batchRequest(t, s, context.Background(), "/accounts/inspect", key, map[string]string{}); r.StatusCode != 409 {
		t.Fatalf("same key used by a different command: %d", r.StatusCode)
	}
	done := make(chan error, 1)
	go func() { _, err := s.AccountSettings(); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated settings blocked on conversion")
	}
	resume()
	deadline := time.Now().Add(3 * time.Second)
	for {
		r := lookupBatch(s, key)
		if r.StatusCode != 200 {
			t.Fatalf("lookup: %d", r.StatusCode)
		}
		if ojson.GetBytes(r.Body, "status").String() == "completed" {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("batch did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = offlineService(t, opts)
	if r := lookupBatch(s, key); r.StatusCode != 200 || ojson.GetBytes(r.Body, "items.0.status").String() != "uploaded" {
		t.Fatalf("lost final receipt: %s", r.Body)
	}
	if h.saves != 1 {
		t.Fatalf("writes=%d", h.saves)
	}
}

func waitBatch(t *testing.T, s *Service, key, status string) batchReceipt {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		view, err := s.lookupAccountBatch(key)
		if err != nil {
			t.Fatal(err)
		}
		if view.Status == status {
			return view
		}
		if !view.active() {
			t.Fatalf("status %s, want %s (code %s)", view.Status, status, view.Code)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", status)
	return batchReceipt{}
}

func TestBatchBackpressureAndCancellationAtSDKBoundary(t *testing.T) {
	h := newFakeHost()
	opts := offlineOptions(t)
	entered, gate := make(chan struct{}), make(chan struct{})
	var once, release sync.Once
	unblock := func() { release.Do(func() { close(gate) }) }
	defer unblock()
	opts.Call = func(method string, in, out any) error {
		if method == "host.auth.save" {
			once.Do(func() { close(entered); <-gate })
		}
		return h.call(method, in, out)
	}
	s := offlineService(t, opts)
	body := map[string]any{"source": conversionBatchFixture(t), "indexes": []int{0, 1}}
	accept := func(key string) int {
		return batchRequest(t, s, context.Background(), "/conversion/upload", key, body).StatusCode
	}
	active := "conversion-active-key"
	if code := accept(active); code != 202 {
		t.Fatalf("active %d", code)
	}
	<-entered
	for i := range batchQueueDepth {
		if code := accept(fmt.Sprintf("queued-key-%d", i)); code != 202 {
			t.Fatalf("queue %d", code)
		}
	}
	if code := accept("rejected-key"); code != 429 {
		t.Fatalf("backpressure %d", code)
	}
	if r := lookupBatch(s, "rejected-key"); r.StatusCode != 404 || ojson.GetBytes(r.Body, "code").String() != "command_not_accepted" {
		t.Fatal("a rejected request left an accepted receipt")
	}
	cancelBatch := func(key string) {
		r := batchRequest(t, s, context.Background(), "/batches/cancel", "cancel-"+key, map[string]string{"id": key})
		if r.StatusCode != 200 || ojson.GetBytes(r.Body, "status").String() != "cancelling" {
			t.Fatalf("cancel response %d %s", r.StatusCode, r.Body)
		}
	}
	cancelBatch(active)
	for i := range batchQueueDepth {
		cancelBatch(fmt.Sprintf("queued-key-%d", i))
	}
	if v, err := s.lookupAccountBatch(active); err != nil || v.Status != "cancelling" {
		t.Fatal("an unresolved SDK write was reported cancelled")
	}
	unblock()
	v := waitBatch(t, s, active, "cancelled")
	if v.Completed != 1 || len(v.Items) != 1 || v.Items[0].Status != "uploaded" || !v.CancelRequested {
		t.Fatalf("lost acknowledgement for a write that crossed cancellation: %+v", v)
	}
	for i := range batchQueueDepth {
		queued := waitBatch(t, s, fmt.Sprintf("queued-key-%d", i), "cancelled")
		if queued.Completed != 0 {
			t.Fatal("cancelled queued work was dispatched")
		}
	}
	if h.saves != 1 {
		t.Fatalf("writes after cancellation: %d", h.saves)
	}
}

func TestBatchReceiptCrashWindowsAndLegacyRetention(t *testing.T) {
	h := newFakeHost()
	opts := offlineOptions(t)
	opts.Call = h.call
	s := offlineService(t, opts)
	source := conversionFixture(t)
	body := map[string]any{"source": source, "indexes": []int{0}}
	items, err := selectedConversions(mustEncodeBatch(t, body), s.opts.Now())
	if err != nil {
		t.Fatal(err)
	}
	commitID := "conversion:" + conversion.Identity(items[0])
	name := items[0].Filename
	keys := []string{"matched-after-save", "different-batch-intent", "lost-after-admission"}
	if r := batchRequest(t, s, context.Background(), "/conversion/upload", keys[0], body); r.StatusCode != 202 {
		t.Fatal(r.StatusCode)
	}
	waitBatch(t, s, keys[0], "completed")
	canonical, err := canonicalRequest(mustEncodeBatch(t, body))
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := s.journal.fingerprint("POST/conversion/upload", canonical)
	// Restore the durable boundary just before a row outcome is published. Only
	// the first command actually obtained the SDK receipt for this conversion.
	for i, key := range keys {
		v := batchReceipt{Kind: "conversion", Status: "running", Total: 1}
		if i < 2 {
			v.Intent = &conversionOutcome{Index: 0, Filename: name}
			v.CommitID = commitID
		}
		if err := s.journal.acceptAccountBatch(s.journal.fingerprint(key), fingerprint, v); err != nil {
			t.Fatal(err)
		}
	}
	legacyKey := "retained-legacy-security-command"
	writes := 0
	invoke := func() (any, error) { writes++; return map[string]string{"status": "succeeded"}, nil }
	if _, err := s.Idempotent(legacyKey, "POST/accounts/security", []byte(`{}`), invoke); err != nil {
		t.Fatal(err)
	}
	if _, err := s.journal.prune(s.opts.Now().Add(31 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = offlineService(t, opts)
	for i, status := range []string{"completed", "reconciliation_required", "interrupted"} {
		v := waitBatch(t, s, keys[i], status)
		if i == 0 && (v.Completed != 1 || v.Items[0].Status != "uploaded") {
			t.Fatal("lost exact committed row")
		}
		if r := batchRequest(t, s, context.Background(), "/conversion/upload", keys[i], body); r.StatusCode != 202 || ojson.GetBytes(r.Body, "status").String() != status {
			t.Fatalf("replay lost %s", status)
		}
	}
	if _, err := s.Idempotent(legacyKey, "POST/accounts/security", []byte(`{}`), invoke); SafeCode(err) != "command_result_expired" || writes != 1 {
		t.Fatal("retention authorized an old side effect")
	}
	if h.saves != 1 {
		t.Fatalf("restart replayed %d saves", h.saves)
	}
}

func mustEncodeBatch(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestInspectionSDKReadDoesNotHoldAccountLockOrOverwriteNewRevision(t *testing.T) {
	h := newFakeHost()
	opts := offlineOptions(t)
	entered, gate := make(chan struct{}), make(chan struct{})
	var shouldBlock atomic.Bool
	var once, release sync.Once
	unblock := func() { release.Do(func() { close(gate) }) }
	defer unblock()
	opts.Call = func(method string, in, out any) error {
		if shouldBlock.Load() && method == "host.auth.list" {
			once.Do(func() { close(entered); <-gate })
		}
		return h.call(method, in, out)
	}
	s := offlineService(t, opts)
	a := importOfflineAccount(t, s)
	s.accountMu.Lock()
	record := s.accounts[a.ID]
	record.View.AuthID = "fixture-absent.json"
	if err := s.saveAccountLocked(record); err != nil {
		s.accountMu.Unlock()
		t.Fatal(err)
	}
	s.accountMu.Unlock()
	shouldBlock.Store(true)
	const key = "inspection-slow-sdk"
	payload := map[string]string{"id": a.ID}
	if r := batchRequest(t, s, context.Background(), "/accounts/inspect", key, payload); r.StatusCode != 202 {
		t.Fatal(r.StatusCode)
	}
	<-entered
	settings, err := s.AccountSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.IntervalMinutes = 20
	update := make(chan error, 1)
	go func() { _, err := s.UpdateAccountSettings(settings); update <- err }()
	select {
	case err := <-update:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated settings write waited behind the SDK read")
	}
	unblock()
	v := waitBatch(t, s, key, "failed")
	if v.Code != "conflict" {
		t.Fatal("failed to reject stale inspection evidence")
	}
	s.accountMu.Lock()
	checked := s.accounts[a.ID].View.LastCheckedAt
	s.accountMu.Unlock()
	if checked != "" {
		t.Fatal("stale health observation overwrote new revision")
	}
	const second = "inspection-current-sdk"
	if r := batchRequest(t, s, context.Background(), "/accounts/inspect", second, payload); r.StatusCode != 202 {
		t.Fatal(r.StatusCode)
	}
	waitBatch(t, s, second, "completed")
	s.accountMu.Lock()
	current := s.accounts[a.ID].View
	s.accountMu.Unlock()
	if current.HealthStatus != "missing" || current.LastCheckedAt == "" {
		t.Fatalf("fresh inspection not saved: %s", current.HealthStatus)
	}
}

func TestAccountListReadsOneDirectoryAndOneFilePerDistinctBinding(t *testing.T) {
	opts := offlineOptions(t)
	h := newFakeHost()
	var lists, gets atomic.Int32
	var fail atomic.Bool
	opts.Call = func(m string, in, out any) error {
		switch m {
		case "host.auth.list":
			lists.Add(1)
			if fail.Load() {
				return errors.New("offline")
			}
		case "host.auth.get":
			gets.Add(1)
		}
		return h.call(m, in, out)
	}
	s := offlineService(t, opts)
	source := conversionBatchFixture(t)
	items, err := selectedConversions(mustEncodeBatch(t, map[string]any{"source": source, "indexes": []int{0, 1}}), s.opts.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		name := item.Filename
		h.files[name] = item.Credential
		h.entries[name] = account.Entry{Index: name, Name: name, ID: name, Provider: "codex", Status: "active"}
		accounts, err := s.ImportAccounts([]Credentials{{Email: item.Email, Password: "offline", TOTPSecret: "JBSWY3DPEHPK3PXP"}})
		if err != nil {
			t.Fatal(err)
		}
		s.accountMu.Lock()
		a := s.accounts[accounts[0].ID]
		a.View.AuthID = name
		a.Principal = (account.LoginPrincipal{AccountID: item.AccountID, UserID: item.UserID}).Key()
		err = s.saveAccountLocked(a)
		s.accountMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	lists.Store(0)
	gets.Store(0)
	rows, err := s.Accounts()
	if err != nil || len(rows) != 2 || rows[0].PlanType != "plus" || rows[1].PlanType != "plus" || lists.Load() != 1 || gets.Load() != 2 {
		t.Fatalf("projection requests: list=%d get=%d err=%v", lists.Load(), gets.Load(), err)
	}
	lists.Store(0)
	gets.Store(0)
	if targets, err := s.targets(); err != nil || len(targets) != 2 || lists.Load() != 1 || gets.Load() != 2 {
		t.Fatalf("target projection requests: list=%d get=%d err=%v", lists.Load(), gets.Load(), err)
	}
	fail.Store(true)
	rows, err = s.Accounts()
	if err != nil || len(rows) != 2 || rows[0].BindingReadError == "" || rows[1].BindingReadError == "" {
		t.Fatal("SDK outage erased local account data")
	}
	if res := s.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: RoutePrefix + "/targets"}); res.StatusCode != 503 {
		t.Fatal("failed SDK directory was presented as empty")
	}
}

func TestConversionPreviewHasPrivateStableSourceReference(t *testing.T) {
	s := offlineService(t, offlineOptions(t))
	source := conversionFixture(t)
	preview := func(value string) string {
		r := conversionRequest(t, s, "preview", "", value)
		if r.StatusCode != 200 {
			t.Fatal(r.StatusCode)
		}
		return ojson.GetBytes(r.Body, "source_ref").String()
	}
	ref := preview(source)
	if len(ref) != 64 || preview(source) != ref || preview(source+" ") == ref {
		t.Fatal("preview identity not stable and source-specific")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = offlineService(t, s.opts)
	if preview(source) != ref {
		t.Fatal("same-volume restart changed source reference")
	}
}

func TestCommandCannotReportDefiniteFailureAfterEffectAndReceiptLoss(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "account-journal")
	j, err := openJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	const key = "unknown-command-receipt"
	const path = "POST/accounts/security"
	payload := []byte(`{}`)
	_, err = j.idempotent(key, path, payload, func() (any, error) {
		writes++
		if err := j.db.Close(); err != nil {
			t.Fatal(err)
		}
		return map[string]bool{"completed": true}, nil
	})
	res := errorReply(err)
	if ojson.GetBytes(res.Body, "code").String() != "idempotency_unknown" || !ojson.GetBytes(res.Body, "outcome_unknown").Bool() || ojson.GetBytes(res.Body, "retryable").Bool() {
		t.Fatalf("post-effect failure was not classified as unknown: %d %s", res.StatusCode, res.Body)
	}
	j, err = openJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.db.Close()
	_, err = j.idempotent(key, path, payload, func() (any, error) { writes++; return nil, nil })
	if SafeCode(err) != "idempotency_unknown" || writes != 1 {
		t.Fatal("unconfirmed command repeated after restart")
	}
}
