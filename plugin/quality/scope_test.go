package quality

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func scopedTarget(id string, models ...string) Target {
	return Target{AuthID: id, AuthIndex: "ix-" + id, Provider: "offline", RuntimeStatus: "active", EffectiveAvailable: true, Models: models, catalogVerified: true}
}

func TestVisibleTargetsSeparatesLiveAvailabilityFromHistoricalError(t *testing.T) {
	en, _, _ := scopeFixture(t, scopedTarget("a", "current"), scopedTarget("b", "current"), scopedTarget("c", "current"))
	en.managed = map[string]*CredentialDisposition{
		"a": {AuthID: "a", State: "disabled", CatalogMissingModel: "old-model"},
		"b": {AuthID: "b", State: "disabled"},
		"c": {AuthID: "c", State: "acknowledged", UpstreamCondition: "unauthorized"},
	}
	targets, err := en.visibleTargets(context.Background())
	if err != nil || len(targets) != 3 {
		t.Fatal(targets, err)
	}
	current := targets[0]
	if !current.EffectiveAvailable || current.ManualDisabled || current.CredentialHandling != "disabled" || current.ErrorPolicyBlock != nil || current.HandlingStateReason == "" {
		t.Fatalf("current enabled state was replaced by a historical disabled claim: %+v", current)
	}
	if targets[1].ErrorPolicyBlock != nil || targets[2].ErrorPolicyBlock != nil {
		t.Fatal("non-error and acknowledged dispositions must not appear as error suspensions")
	}
}

func scopeFixture(t *testing.T, targets ...Target) (*Engine, *semanticsHost, *Rule) {
	t.Helper()
	en, h, _ := semanticsEngine(t)
	h.targets = targets
	en.host = &observationHost{h}
	r, err := en.SaveRule(context.Background(), Rule{TargetScope: scopeAll, Name: "scope", RunConfig: RunConfig{Models: []string{"a", "b"}, QuestionID: "logic-boxes"}, Enabled: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	return en, h, r
}

func TestScopeMembershipIsDynamicAndPerCredential(t *testing.T) {
	ctx := context.Background()
	b := scopedTarget("two", "a", "b", "c")
	b.Provider = "another"
	held, unknown, disabled, noIndex := scopedTarget("held", "a"), scopedTarget("unknown", "a", "b"), scopedTarget("disabled", "a"), scopedTarget("no-index", "a")
	unknown.RuntimeStatus = "error"
	disabled.ManualDisabled = true
	noIndex.AuthIndex = ""
	en, h, r := scopeFixture(t, scopedTarget("one", "a", "b"), b, held, unknown, disabled, noIndex, scopedTarget("none", "c"))
	en.rules["old-owner"] = &Rule{ID: "old-owner", RunConfig: RunConfig{AuthID: "held"}, ActionID: "retained"}
	if receipt, err := en.dispatchRule(ctx, r.ID, r.Revision+1, false); err == nil || receipt.Count != 0 {
		t.Fatal("accepted stale revision")
	}
	d, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
	if err != nil || d.Count != 6 || d.Skipped != 0 || d.BatchID == "" || len(en.queue) != 1 || h.executeCalls != 0 {
		t.Fatal(d, err)
	}
	if _, err = en.dispatchRule(ctx, r.ID, r.Revision, false); err == nil {
		t.Fatal("queued overlapping batch")
	}
	for _, child := range en.runs {
		f := child.Config
		want := r.Models
		provider := "offline"
		index := "ix-" + f.AuthID
		if f.AuthID == "two" {
			want = r.Models
			provider = "another"
		}
		if child.Status == "blocked" {
			want = r.Models
			if f.AuthID == "no-index" {
				index = ""
			}
			if child.PreflightDetail == nil || child.PreflightDetail.RunID != child.ID || len(child.Samples) != 0 || child.CompletedAt == nil {
				t.Fatal("incomplete skipped child", child)
			}
		}
		if f.SubjectIndex != index || f.Provider != provider || !reflect.DeepEqual(f.Models, want) || f.ScopeBatchID != d.BatchID {
			t.Fatal("incorrect pinned child", f)
		}
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if h.executeCalls != 6 || h.pauseCalls != 0 {
		t.Fatal("wrong call count", h.executeCalls)
	}
	h.targets = []Target{scopedTarget("later", "a", "b")}
	r2, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
	if err != nil || r2.Count != 1 || r2.BatchID == d.BatchID {
		t.Fatal(r2, err)
	}
	var newChild string
	for id, run := range en.runs {
		if run.Config.ScopeBatchID == r2.BatchID {
			newChild = id
			if run.Config.AuthID != "later" {
				t.Fatal("stale member")
			}
		}
	}
	if err = en.Cancel(newChild); err != nil {
		t.Fatal(err)
	}
	h.targets = nil
	empty, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
	if err != nil || empty.Count != 0 || empty.BatchID != "" || len(en.runs) != 7 || h.executeCalls != 6 || en.rules["old-owner"].ActionID != "retained" {
		t.Fatal(empty, err)
	}
}

func TestScopeLargeDirectoryAtomicQueueAndRestart(t *testing.T) {
	ctx := context.Background()
	targets := make([]Target, maxScopePending+1)
	for i := range targets {
		targets[i] = scopedTarget(fmt.Sprintf("id-%04d", i), "a")
	}
	en, h, r := scopeFixture(t, targets...)
	en.queue = make(chan string, 1)
	checkFull := func(want int) string {
		t.Helper()
		receipt, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
		if err != nil || receipt.Count != 0 || receipt.Skipped != want || len(receipt.SkipDetails) != want || receipt.BatchID == "" {
			t.Fatalf("queue-full receipt: total %d, skipped %d, batch %q, err %v", receipt.Count, receipt.Skipped, receipt.BatchID, err)
		}
		for _, detail := range receipt.SkipDetails {
			run := en.runs[detail.RunID]
			if detail.Code != "queue_full" || run == nil || run.Status != "skipped" || run.Config.ScopeBatchID != receipt.BatchID || run.CompletedAt == nil || len(run.Samples) != 0 {
				t.Fatal("invalid overflow record", detail)
			}
		}
		return receipt.BatchID
	}
	largeBatch := checkFull(len(targets))
	if len(en.runs) != len(targets) || len(en.queue) != 0 {
		t.Fatal("partial overflow admission")
	}
	h.targets = targets[:137]
	en.queue <- "occupied"
	en.runs["occupied"] = &Run{ID: "occupied", Status: "queued"}
	occupiedBatch := checkFull(len(h.targets))
	if len(en.runs) != len(targets)+len(h.targets)+1 || len(en.queue) != 1 {
		t.Fatal("queue overflow was not atomic")
	}
	<-en.queue
	delete(en.runs, "occupied")
	d, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
	if err != nil || d.Count != 137 || len(en.queue) != 1 {
		t.Fatal(d, err)
	}
	q, err := en.QueryRuns(url.Values{"scope_batch_id": {d.BatchID}, "offset": {"100"}, "limit": {"100"}})
	if err != nil || q["total"] != 137 || len(q["items"].([]Run)) != 37 {
		t.Fatal("batch page", err)
	}
	en.runs = map[string]*Run{}
	en.rules = map[string]*Rule{}
	en.scopeBatches = nil
	en.queue = make(chan string, 1)
	if err = en.load(); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, run := range en.runs {
		counts[run.Config.ScopeBatchID]++
		if run.Config.ScopeBatchID == d.BatchID && run.Status != "interrupted" || run.Config.ScopeBatchID != d.BatchID && run.Status != "skipped" {
			t.Fatal(run.Status)
		}
	}
	if !reflect.DeepEqual(counts, map[string]int{largeBatch: len(targets), occupiedBatch: 137, d.BatchID: 137}) ||
		len(en.queue) != 0 || h.executeCalls != 0 || en.rules[r.ID].LastScopeBatch != d.BatchID {
		t.Fatal("restart replayed or lost membership")
	}
}

func TestObservationModelLimitsDoNotExpandManualOrDefaultBudgets(t *testing.T) {
	ctx := context.Background()
	en, host, rule := scopeFixture(t)
	models := make([]string, 50)
	for i := range models {
		models[i] = fmt.Sprintf("fixture-%02d", i)
	}
	host.targets = []Target{scopedTarget("full", models...), scopedTarget("partial", models[0])}
	rule.Models, rule.Samples, rule.Parameters.ReasoningEffort = models, 2, "high"
	if _, err := en.SaveRule(ctx, *rule, true); err == nil {
		t.Fatal("default budget expanded without an explicit setting")
	}
	cfg := en.Config()
	cfg.MaxModels, cfg.MaxSamples = 50, 100
	if err := en.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.MaxModels = 51
	if cfg.Validate() == nil {
		t.Fatal("accepted more than 50 models")
	}
	rule.Samples = 3
	if _, err := en.SaveRule(ctx, *rule, true); err == nil {
		t.Fatal("sample budget not enforced")
	}
	rule.Samples = 2
	rule, err := en.SaveRule(ctx, *rule, true)
	if err != nil {
		t.Fatal(err)
	}
	d, err := en.dispatchRule(ctx, rule.ID, rule.Revision, false)
	if err != nil || d.Count != 2 {
		t.Fatal(d, err)
	}
	for _, r := range en.runs {
		want := models
		if !reflect.DeepEqual(r.Config.Models, want) || r.Config.Parameters.ReasoningEffort != "high" || r.Config.Samples != 2 {
			t.Fatal("incorrect frozen model set or parameters")
		}
	}
	single := rule.RunConfig
	single.AuthID, single.Provider = "full", "offline"
	if _, err := en.Submit(ctx, single); err == nil {
		t.Fatal("manual endpoint inherited extended rule limits")
	}
	one, err := en.SaveRule(ctx, Rule{RunConfig: single, TargetScope: "single", Enabled: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := en.dispatchRule(ctx, one.ID, one.Revision, false); err != nil {
		t.Fatal("extended single-credential rule could not be dispatched", err)
	}
}

func TestScopeRechecksBeforeCallAndRespectsBudget(t *testing.T) {
	for name, mutate := range map[string]func(*Target){
		"disabled": func(t *Target) { t.ManualDisabled = true }, "cooling": func(t *Target) { t.EffectiveAvailable = false }, "unregistered": func(t *Target) { t.Models = nil }, "index": func(t *Target) { t.AuthIndex = "replaced" }, "provider": func(t *Target) { t.Provider = "replaced" }, "status": func(t *Target) { t.RuntimeStatus = "pending" }, "held": func(t *Target) { t.RetainedPause = true },
	} {
		t.Run(name, func(t *testing.T) {
			en, h, r := scopeFixture(t, scopedTarget("one", "a", "b"))
			d, err := en.dispatchRule(context.Background(), r.ID, r.Revision, false)
			if err != nil || d.Count != 1 {
				t.Fatal(d, err)
			}
			mutate(&h.targets[0])
			task := en.nextRunnableLocked()
			en.executeTask(task)
			wantStatus, wantCalls := "blocked", 0
			if name == "cooling" || name == "status" || name == "unregistered" {
				wantStatus, wantCalls = "completed", 2
			}
			if en.runs[task.id].Status != wantStatus || h.executeCalls != wantCalls || en.budget.Calls != wantCalls {
				t.Fatal(en.runs[task.id], h.executeCalls)
			}
		})
	}
	en, h, r := scopeFixture(t, scopedTarget("one", "a", "b"), scopedTarget("two", "a"))
	en.cfg.DailyCalls = 1
	if _, err := en.dispatchRule(context.Background(), r.ID, r.Revision, false); err != nil {
		t.Fatal(err)
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if h.executeCalls != 1 || en.budget.Calls != 1 || h.pauseCalls != 0 {
		t.Fatal("shared budget bypassed")
	}
}

func TestScopeSchedulingAndLegacyConfiguration(t *testing.T) {
	ctx := context.Background()
	en, h, r := scopeFixture(t, scopedTarget("one", "a"))
	h.targets = append(h.targets, scopedTarget("auth-a", "a"))
	legacy, err := en.SaveRule(ctx, Rule{RunConfig: RunConfig{AuthID: "auth-a", Provider: "offline", Models: []string{"a"}, QuestionID: "logic-boxes", Parameters: Parameters{MaxTokens: 257}}, Enabled: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Parameters.MaxTokens = 0
	legacy.Name = "edited"
	legacy, err = en.SaveRule(ctx, *legacy, true)
	if err != nil || legacy.Parameters.MaxTokens != 257 || legacy.TargetScope != "" || en.rules[legacy.ID].AuthID != "auth-a" {
		t.Fatal(legacy, err)
	}
	r.SchedulingEnabled = true
	r, err = en.SaveRule(ctx, *r, true)
	if err != nil {
		t.Fatal(err)
	}
	before := en.cfg
	en.Tick(en.now().Add(2 * time.Hour))
	if len(en.queue) != 0 {
		t.Fatal("no global permission")
	}
	if err = en.setScheduling(false, true); err != nil {
		t.Fatal(err)
	}
	if err = en.setScheduling(false, true); err == nil {
		t.Fatal("stale switch")
	}
	before.SchedulingEnabled = true
	if !reflect.DeepEqual(before, en.cfg) {
		t.Fatal("settings overwritten")
	}
	en.Tick(en.now().Add(2 * time.Hour))
	en.Tick(en.now().Add(2 * time.Hour))
	if len(en.runs) != 2 || len(en.queue) != 1 {
		t.Fatal("duplicate or missing scheduled batch")
	}
	for _, v := range en.runs {
		if !v.Config.Scheduled || v.Config.RuleID != r.ID {
			t.Fatal("wrong source")
		}
	}
	if err = en.setScheduling(true, false); err != nil {
		t.Fatal(err)
	}
	for task := en.nextRunnableLocked(); task != nil; task = en.nextRunnableLocked() {
		en.executeTask(task)
	}
	if h.executeCalls != 0 || en.budget.Calls != 0 {
		t.Fatal("called after scheduling was turned off")
	}
	d, err := en.dispatchRule(ctx, legacy.ID, 0, false)
	if err != nil || d.RunID == "" || d.BatchID != "" || d.Count != 1 {
		t.Fatal("legacy single rule dispatch", d, err)
	}
}

func TestNativeScopeUsesPinnedLookupsAndVerifiesCatalog(t *testing.T) {
	ctx := context.Background()
	en, _, r := scopeFixture(t)
	target := pluginapi.HostAuthFileEntry{ID: "one", AuthIndex: "idx", Provider: "offline", Source: "file", Status: "active"}
	catalog := pluginapi.HostAuthModels{ID: "one", AuthIndex: "idx", Provider: "offline", Models: []string{"a", "b"}}
	var full, pinned, calls int
	h := &NativeHost{Call: func(method string, request, response any) error {
		switch method {
		case "host.auth.list":
			full++
			return decode([]byte(`{"files":[{"id":"one","auth_index":"idx"}]}`), response)
		case "host.auth.get_runtime":
			if request.(pluginapi.HostAuthGetRequest).AuthIndex != "idx" {
				t.Fatal("unbound runtime read")
			}
			response.(*pluginapi.HostAuthGetRuntimeResponse).Auth = target
		case "host.auth.models":
			if request.(pluginapi.HostAuthModelsRequest).AuthIndex == "idx" {
				pinned++
			} else {
				full++
			}
			response.(*pluginapi.HostAuthModelsResponse).Credentials = []pluginapi.HostAuthModels{catalog}
		case "host.model.execute":
			calls++
			return fmt.Errorf("offline only")
		default:
			t.Fatal(method)
		}
		return nil
	}}
	en.host = h
	for _, field := range []string{"index", "provider", "id"} {
		c := catalog
		s := target
		switch field {
		case "index":
			catalog.AuthIndex = "other"
		case "id":
			catalog.ID = "other"
		case "provider":
			catalog.Provider = "other"
		case "cooldown":
			target.NextRetryAfter = time.Now().Add(time.Hour)
		}
		d, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
		want := 1
		if field == "id" {
			want = 2
		}
		if err != nil || d.Count != want || d.Skipped != 0 || len(en.queue) != 0 {
			t.Fatal(field, d, err)
		}
		for _, run := range en.runs {
			if run.Config.ScopeBatchID == d.BatchID && (run.Status != "blocked" || run.PreflightDetail == nil || run.PreflightDetail.Code != "identity_conflict") {
				t.Fatal("identity conflict was not retained", run)
			}
		}
		catalog = c
		target = s
	}
	full = 0
	d, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
	if err != nil || d.Count != 1 {
		t.Fatal(d, err)
	}
	en.executeTask(en.nextRunnableLocked())
	if full != 2 || pinned != 2 || calls != 2 {
		t.Fatal("execution expanded the whole directory", full, pinned, calls)
	}
}
