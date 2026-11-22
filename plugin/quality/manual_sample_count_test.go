package quality

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestLargeManualSamplePlan(t *testing.T) {
	en, host := lifeFixture(t)
	cfg := lifeConfig()
	cfg.Samples = 33
	run, err := en.Submit(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	en.executeTask(en.nextRunnableLocked())
	var saved Run
	if err := en.store.Get("runs", run.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "completed" || len(saved.Samples) != 33 || len(host.calls) != 33 || en.budget.Calls != 33 {
		t.Fatalf("status=%s samples=%d calls=%d err=%s", saved.Status, len(saved.Samples), len(host.calls), saved.Error)
	}
	for i, sample := range saved.Samples {
		if sample.Index != i || sample.Output != "4" {
			t.Fatalf("missing or reordered answer at index %d", i)
		}
	}
}

func TestSamplePlanRepresentationAndIncompleteEvidence(t *testing.T) {
	en, _ := lifeFixture(t)
	cfg := lifeConfig()
	cfg.ControlAuthID, cfg.ControlProvider = "auth-b", "offline"
	cfg.Models = []string{"a", "b"}
	safeCount := min(int64(math.MaxInt), (1<<53)-1)
	cfg.Samples = int(safeCount / 4)
	if err := en.validateManual(&cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Samples++
	if err := en.validateManual(&cfg); err == nil {
		t.Fatal("inexact plan accepted")
	}
	cfg.Samples = -1
	if err := en.validateManual(&cfg); err == nil {
		t.Fatal("negative sample count accepted")
	}
	cfg.Samples = math.MaxInt/2 + 1
	cfg.Models = []string{"a", "b", "c", "d"}
	if got := RoundVerdict(&Run{Status: "completed", Config: Frozen{RunConfig: cfg}}); got != "unknown" {
		t.Fatalf("overflowed or incomplete plan judged %s", got)
	}
}

func TestLargeRoundStopsWhenStoreFillsAndKeepsCompletedSamples(t *testing.T) {
	en, host := lifeFixture(t)
	en.cfg.MaxStorageBytes = 1 << 20
	answer := strings.Repeat("x", 192<<10)
	host.execute = func(context.Context, Execution) Sample {
		return Sample{ExecutionStatus: "completed", Output: answer}
	}
	cfg := lifeConfig()
	cfg.Samples = 100_000
	run, err := en.Submit(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	en.executeTask(en.nextRunnableLocked())
	saved, err := en.Run(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "failed" || !strings.HasPrefix(saved.Error, "storage_limit:") || len(saved.Samples) == 0 ||
		len(saved.Samples) >= cfg.Samples || len(saved.Samples) != len(host.calls) || en.budget.Calls != len(host.calls) {
		t.Fatalf("status=%s err=%s samples=%d calls=%d", saved.Status, saved.Error, len(saved.Samples), len(host.calls))
	}
}
