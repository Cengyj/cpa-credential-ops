package quality

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type countedInventory struct {
	*semanticsHost
	reads atomic.Int64
	enter chan struct{}
	leave chan struct{}
}

func (h *countedInventory) Targets(ctx context.Context) ([]Target, error) {
	h.reads.Add(1)
	if h.enter != nil {
		select {
		case h.enter <- struct{}{}:
		default:
		}
		select {
		case <-h.leave:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h.targets, nil
}

func pagedInventory(size int) *countedInventory {
	h := &countedInventory{semanticsHost: &semanticsHost{targets: make([]Target, size)}}
	for i := range h.targets {
		id := fmt.Sprintf("entry-%05d", size-i)
		h.targets[i] = Target{AuthID: id, AuthIndex: id, Provider: "fixture", Models: []string{"fake-model"}, EffectiveAvailable: true}
	}
	return h
}

func directoryPage(t testing.TB, en *Engine, offset int, token string) (string, []Target) {
	t.Helper()
	qs := url.Values{"limit": {"100"}, "offset": {strconv.Itoa(offset)}}
	if token != "" {
		qs.Set("snapshot", token)
	}
	response := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: "/targets", Query: qs})
	var page struct {
		Items    []Target
		Snapshot string
	}
	if response.StatusCode != 200 || json.Unmarshal(response.Body, &page) != nil {
		t.Fatalf("read directory page %d: %d %s", offset, response.StatusCode, response.Body)
	}
	return page.Snapshot, page.Items
}

func TestDirectoryPagesPinMembershipAndBoundReads(t *testing.T) {
	h := pagedInventory(203)
	now := time.Now()
	en := &Engine{host: h, now: func() time.Time { return now }}
	token, first := directoryPage(t, en, 0, "")
	if len(first) != 100 || token == "" {
		t.Fatal("the first page does not identify its inventory snapshot")
	}
	h.targets = h.targets[1:]
	now = now.Add(5 * time.Second)
	newer, _ := directoryPage(t, en, 0, "")
	_, last := directoryPage(t, en, 200, token)
	if newer == token || len(last) != 3 || last[2].AuthID != "entry-00203" || h.reads.Load() != 2 {
		t.Fatal("continuation mixed two inventories or scanned the source again", last, h.reads.Load())
	}
}

func TestManualDirectoryReadReleasesEngineLock(t *testing.T) {
	en, base := lifeFixture(t)
	host := &countedInventory{semanticsHost: &semanticsHost{targets: []Target{base.target}}, enter: make(chan struct{}, 1), leave: make(chan struct{})}
	en.host = host
	done := make(chan error, 1)
	go func() {
		_, err := en.Submit(context.Background(), lifeConfig())
		done <- err
	}()
	<-host.enter
	unlocked := en.mu.TryLock()
	if unlocked {
		en.mu.Unlock()
	}
	close(host.leave)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !unlocked {
		t.Fatal("a blocked CPA inventory read holds the engine lock")
	}
}

// Each operation reads 20 authenticated pages from a changing synthetic directory.
func BenchmarkDisplayDirectoryPages(b *testing.B) {
	h := pagedInventory(2000)
	now := time.Now()
	en := &Engine{host: h, now: func() time.Time { return now }}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		now = now.Add(time.Minute)
		token := ""
		for offset := 0; offset < 2000; offset += 100 {
			token, _ = directoryPage(b, en, offset, token)
		}
	}
	b.ReportMetric(float64(h.reads.Load())/float64(b.N), "inventory_reads/op")
}

func TestConcurrentManualSubmissionRechecksReceiptAndQueue(t *testing.T) {
	en, base := lifeFixture(t)
	host := &countedInventory{semanticsHost: &semanticsHost{targets: []Target{base.target}}, enter: make(chan struct{}, 12), leave: make(chan struct{})}
	en.host = host
	en.queue = make(chan string, 1)
	type admission struct {
		id  string
		err error
	}
	results := make(chan admission, 12)
	for range 12 {
		go func() {
			config := lifeConfig()
			config.IdempotencyKey = "one-command"
			r, err := en.Submit(context.Background(), config)
			var id string
			if r != nil {
				id = r.ID
			}
			results <- admission{id, err}
		}()
	}
	for range 12 {
		<-host.enter
	}
	close(host.leave)
	var admitted string
	for range 12 {
		r := <-results
		if r.err != nil || r.id == "" || admitted != "" && r.id != admitted {
			t.Fatal("duplicate admission", r)
		}
		admitted = r.id
	}
	if len(en.queue) != 1 || len(en.runs) != 1 {
		t.Fatal("expected exactly one durable run")
	}
}

type delayedGroup struct {
	*groupPolicyFixture
	entered chan struct{}
	gate    chan struct{}
}

func (h *delayedGroup) CredentialSnapshot(ctx context.Context, id, index string) (CredentialSnapshot, error) {
	select {
	case h.entered <- struct{}{}:
	default:
	}
	select {
	case <-h.gate:
		return h.groupPolicyFixture.CredentialSnapshot(ctx, id, index)
	case <-ctx.Done():
		return CredentialSnapshot{}, ctx.Err()
	}
}

func TestSlowGroupObservationKeepsNewOwnership(t *testing.T) {
	en, h, rule := retainedProviderGroup(t)
	for id, s := range h.snapshots {
		s.Disabled = false
		h.snapshots[id] = s
	}
	host := &delayedGroup{h, make(chan struct{}, 1), make(chan struct{})}
	en.host = host
	done := make(chan error, 1)
	go func() { _, err := en.visibleTargets(context.Background()); done <- err }()
	<-host.entered
	if !en.mu.TryLock() {
		close(host.gate)
		<-done
		t.Fatal("provider verification blocked unrelated operations")
	}
	newer := *en.managed["b"]
	newer.State, newer.ActionID = "attention", "new-action"
	change := changes()
	change.disposition = &newer
	err := en.commitLocked(change)
	en.mu.Unlock()
	if stop := en.PauseRule(rule.ID, rule.Revision, true); stop != nil {
		t.Fatal(stop)
	}
	close(host.gate)
	if err != nil || <-done != nil {
		t.Fatal("group reconciliation", err)
	}
	if en.managed["a"].State != "disabled" || en.managed["b"].State != "attention" || en.managed["b"].ActionID != newer.ActionID || en.rules[rule.ID].Enabled || len(h.writes) != 0 {
		t.Fatal("a slow read replaced current authority or partially retired the group")
	}
}

type delayedRemoval struct {
	*semanticsHost
	entered, gate chan struct{}
}

func (h *delayedRemoval) Capabilities() HostCapabilities {
	return HostCapabilities{ManagementSession: true}
}
func (h *delayedRemoval) removedCredentials(ctx context.Context, items []*CredentialDisposition, _ int) ([]*CredentialDisposition, error) {
	close(h.entered)
	select {
	case <-h.gate:
		return items, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestMissingFileReadKeepsReplacementOwnership(t *testing.T) {
	en, h, _ := semanticsEngine(t)
	host := &delayedRemoval{h, make(chan struct{}), make(chan struct{})}
	en.host = host
	old := &CredentialDisposition{AuthID: "missing", RuleID: "r", ActionID: "old", State: "disabled", Snapshot: CredentialSnapshot{AuthID: "missing", AuthIndex: "idx"}}
	en.managed = map[string]*CredentialDisposition{old.AuthID: old}
	done := make(chan error, 1)
	go func() { _, err := en.visibleTargets(context.Background()); done <- err }()
	<-host.entered
	if !en.mu.TryLock() {
		close(host.gate)
		<-done
		t.Fatal("file absence read held the business lock")
	}
	next := *old
	next.ActionID = "replacement"
	change := changes()
	change.disposition = &next
	err := en.commitLocked(change)
	en.mu.Unlock()
	close(host.gate)
	if readErr := <-done; err != nil || readErr != nil {
		t.Fatal(err, readErr)
	}
	if got := en.managed[old.AuthID]; got.State != "disabled" || got.ActionID != "replacement" {
		t.Fatal("stale file removal retired a new owner")
	}
}

func TestConcurrentDirectoryReadersShareOnlyDisplaySnapshot(t *testing.T) {
	h := pagedInventory(2)
	h.enter, h.leave = make(chan struct{}, 1), make(chan struct{})
	now := time.Now()
	en := &Engine{host: h, now: func() time.Time { return now }}
	results := make(chan *directorySnapshot, 13)
	read := func() {
		s, err := en.directorySnapshot(context.Background(), "", false)
		if err != nil {
			t.Error(err)
		}
		results <- s
	}
	go read()
	<-h.enter
	for range 12 {
		go read()
	}
	abandoned, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := en.directorySnapshot(abandoned, "", false); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled observer waited for the inventory")
	}
	close(h.leave)
	first := <-results
	for range 12 {
		if s := <-results; s != first {
			t.Fatal("concurrent readers received different snapshots")
		}
	}
	if h.reads.Load() != 1 {
		t.Fatal("duplicate inventory reads", h.reads.Load())
	}
	h.targets[0].Models[0] = "replacement"
	if first.items[1].Models[0] != "fake-model" {
		t.Fatal("snapshot aliased a mutable SDK model slice")
	}
	second, err := en.directorySnapshot(context.Background(), "", true)
	if err != nil || second == first || second.items[1].Models[0] != "replacement" || h.reads.Load() != 2 {
		t.Fatal("explicit current read failed", err)
	}
	now = now.Add(directoryContinuationTTL)
	response := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: "/targets", Query: url.Values{"snapshot": {first.id}}})
	if response.StatusCode != 409 || !strings.Contains(string(response.Body), `"snapshot_expired"`) || h.reads.Load() != 2 {
		t.Fatal("expired cursor mixed datasets", response.StatusCode, string(response.Body))
	}
}

func TestDirectoryRetiresCachedDisableOnObservedCPAEnable(t *testing.T) {
	en, host, _ := retainedProviderGroup(t)
	token, rows := directoryPage(t, en, 0, "")
	if !rows[0].ManualDisabled || rows[0].CredentialHandling != "disabled" {
		t.Fatal("fixture did not cache a suspended entry", rows)
	}
	for id, snapshot := range host.snapshots {
		snapshot.Disabled = false
		host.snapshots[id] = snapshot
	}
	ctx := context.Background()
	// Scope preview and execution have their own current CPA read. It
	// confirms the provider was enabled while a display page was cached.
	if _, err := en.visibleTargets(ctx); err != nil {
		t.Fatal(err)
	}
	if en.managed["a"].State != "externally_enabled" || en.managed["b"].State != "externally_enabled" {
		t.Fatal("fresh observation did not reconcile both entries")
	}
	next, current := directoryPage(t, en, 0, "")
	if next == token || current[0].ManualDisabled || current[0].CredentialHandling != "" {
		t.Fatal("an ordinary listing returned a superseded local suspension")
	}
	_, old := directoryPage(t, en, 0, token)
	if !old[0].ManualDisabled || old[0].CredentialHandling != "disabled" {
		t.Fatal("invalidation mutated a pinned traversal")
	}
}

type capturedDisplayInventory struct {
	*observationHost
	afterRead func()
}

func (h *capturedDisplayInventory) Targets(ctx context.Context) ([]Target, error) {
	items, err := h.observationHost.Targets(ctx)
	h.afterRead()
	return items, err
}

func TestDirectoryUnpinnedReadersFollowLoginDuringScan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		en, host, _ := scopeFixture(t, scopedTarget("one", "before-login"))
		en.now = time.Now
		old, _ := directoryPage(t, en, 0, "")
		time.Sleep(directoryFreshness)

		captured, release := make(chan struct{}), make(chan struct{})
		reads := 0
		en.host = &capturedDisplayInventory{&observationHost{host}, func() {
			reads++
			if reads == 1 {
				close(captured)
				<-release
			}
		}}
		results := make(chan *directorySnapshot, 2)
		read := func() {
			snapshot, err := en.directorySnapshot(ctx, "", false)
			if err != nil {
				t.Error(err)
			}
			results <- snapshot
		}
		go read()
		<-captured
		go read()
		synctest.Wait()

		request := CredentialLoginPrepareRequest{AuthID: "one", ExpectedIdentity: "same-entry", PreparationID: "fake-login"}
		reply, prepareErr := en.PrepareCredentialLogin(ctx, request)
		host.targets = []Target{scopedTarget("one", "after-login")}
		finishErr := en.FinishCredentialLogin(request.AuthID, request.PreparationID, request.ExpectedIdentity, "committed")
		close(release)
		first, second := <-results, <-results
		if prepareErr != nil || !reply.Ready || finishErr != nil {
			t.Fatal("fixture login receipt", reply.Ready, prepareErr, finishErr)
		}
		if first == nil || first != second || len(first.items) != 1 || first.items[0].Models[0] != "after-login" {
			t.Fatalf("readers received superseded inventory after login: first=%+v second=%+v scans=%d", first, second, reads)
		}
		current, _ := directoryPage(t, en, 0, "")
		_, pinned := directoryPage(t, en, 0, old)
		if reads != 2 || current != first.id || current == old || pinned[0].Models[0] != "before-login" || host.executeCalls != 0 {
			t.Fatalf("scans=%d current=%q issued=%q previous=%q pinned=%v calls=%d", reads, current, first.id, old, pinned[0].Models, host.executeCalls)
		}
	})
}

func TestDirectoryLimitsReadsDuringContinuousInvalidation(t *testing.T) {
	en, host, _ := scopeFixture(t, scopedTarget("one", "fixture-model"))
	old, _ := directoryPage(t, en, 0, "")
	reads := 0
	changing := true
	en.host = &capturedDisplayInventory{&observationHost{host}, func() {
		reads++
		if changing {
			en.directory.revision.Add(1)
		}
	}}
	response := en.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: "/targets", Query: url.Values{"fresh": {"1"}}})
	var body struct{ Error queryFailure }
	if err := json.Unmarshal(response.Body, &body); err != nil {
		t.Fatal(err)
	}
	if reads != 2 || response.StatusCode != 503 || !body.Error.Retryable || body.Error.Source != "plugin" || body.Error.OutcomeUnknown {
		t.Fatalf("unresolved inventory churn: scans=%d status=%d error=%+v", reads, response.StatusCode, body.Error)
	}
	if len(en.directory.recent) != 1 || en.directory.recent[0].id != old {
		t.Fatal("an invalidated scan consumed a continuation slot")
	}
	changing = false
	_, pinned := directoryPage(t, en, 0, old)
	current, _ := directoryPage(t, en, 0, "")
	if reads != 3 || current == old || pinned[0].Models[0] != "fixture-model" {
		t.Fatal("a pinned page or a later stable read failed", reads, current, old, pinned)
	}
}
