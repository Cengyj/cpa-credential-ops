package quality

import (
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"testing/synctest"
	"time"
)

func TestLegacyDailyCallsDoNotCapExecutionAfterRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.DataDir = t.TempDir()
		cfg.DailyTokens = 201 * 64
		store, err := OpenStore(cfg.DataDir, cfg.MaxStorageBytes)
		if err != nil {
			t.Fatal(err)
		}
		budget := Budget{Day: time.Now().UTC().Format("2006-01-02"), Calls: 200, ReservedTokens: 200 * 64}
		legacy := legacySettingsFixture(t, cfg, 200, 32)
		for key, value := range map[string]any{"settings": legacy, "quality_host_input": legacy, "budget": budget, "budget:" + budget.Day: budget} {
			if err := store.Put("meta", key, value); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		host := &lifecycleHost{target: Target{AuthID: "auth-a", AuthIndex: "fixture", Provider: "offline", Models: []string{"a"}, EffectiveAvailable: true}}
		for attempt := 0; attempt < 2; attempt++ {
			func() {
				en, err := New(cfg, host)
				if err != nil {
					t.Fatal(err)
				}
				defer en.Close()
				run, err := en.Submit(t.Context(), lifeConfig())
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				result, err := en.Run(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if attempt == 0 && (result.Status != "completed" || len(result.Samples) != 1) {
					t.Fatalf("legacy cap blocked the 201st model call: status=%s err=%s", result.Status, result.Error)
				}
				if attempt == 1 && (result.Error != "token_budget_exhausted" || len(result.Samples) != 0) {
					t.Fatalf("persisted token ceiling was lost: status=%s err=%s", result.Status, result.Error)
				}
				if len(host.calls) != 1 || en.budget.Calls != 201 || en.budget.ReservedTokens != cfg.DailyTokens {
					t.Fatal("model attempts and budget ledger disagree", len(host.calls), en.budget)
				}
			}()
		}
	})
}

func TestJudgeExhaustsOnlyTokenBudget(t *testing.T) {
	en, host, _ := manualOnlyFixture(t)
	en.host = &controlAnswerHost{host}
	cfg := lifeConfig()
	cfg.Judge = Judge{Mode: "llm", AuthID: "auth-c", Provider: "offline", Model: "a"}
	en.cfg.DailyTokens = int64(cfg.Parameters.MaxTokens) + 511
	run, err := en.Submit(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	en.executeTask(en.nextRunnableLocked())
	result, err := en.Run(run.ID)
	if err != nil || len(result.Samples) != 1 || result.Samples[0].Reason != "judge_token_budget_exhausted" || result.Samples[0].QualityVerdict != "unknown" || host.executeCalls != 1 {
		t.Fatal("an unfunded judge call must preserve the unjudged subject answer", result, err)
	}
	f := fmt.Errorf("Bearer fake-private-text: %w", errTokenBudgetExhausted)
	if scrubError(f) != "token_budget_exhausted" || scrubError(errors.New("Bearer fake-private-text")) == "Bearer fake-private-text" {
		t.Fatal("return only the verified budget code when a wrapper contains sensitive data")
	}
}

func TestBudgetLateUsageRemainsOnAdmissionDay(t *testing.T) {
	en, _ := lifeFixture(t)
	en.cfg.DailyTokens = 1000
	dayOne, err := en.reserve(64)
	if err != nil {
		t.Fatal(err)
	}
	en.now = func() time.Time { return time.Date(2026, 11, 14, 0, 0, 1, 0, time.UTC) }
	dayTwo, err := en.reserve(64)
	if err != nil {
		t.Fatal(err)
	}
	if err = en.account(dayOne, &Usage{Total: 2000}); err != nil {
		t.Fatal(err)
	}
	if err = en.account(dayTwo, &Usage{Total: 80}); err != nil {
		t.Fatal(err)
	}
	if en.budget.Day != dayTwo || en.budget.Calls != 1 || en.budget.KnownTokens != 80 || en.budget.ReservedTokens != 64 {
		t.Fatal("prior-day completion polluted the current-day budget", en.budget)
	}
	var prior Budget
	if err = en.store.Get("meta", "budget:"+dayOne, &prior); err != nil || prior.KnownTokens != 2000 || prior.Calls != 1 {
		t.Fatal("prior-day usage was discarded", prior, err)
	}
	if _, err = en.reserve(64); err != nil {
		t.Fatal("prior-day usage incorrectly exhausted current-day budget", err)
	}
}

func TestBudgetLedgerRetentionProtectsUnfinishedCalls(t *testing.T) {
	en, _ := lifeFixture(t)
	en.cfg.RetentionDays = 1
	oldDay, err := en.reserve(64)
	if err != nil {
		t.Fatal(err)
	}
	en.now = func() time.Time { return time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC) }
	if _, err = en.reserve(64); err != nil {
		t.Fatal(err)
	}
	var retained Budget
	if err = en.store.Get("meta", "budget:"+oldDay, &retained); err != nil {
		t.Fatal("in-flight prior day was pruned", err)
	}
	if err = en.account(oldDay, nil); err != nil {
		t.Fatal(err)
	}
	if err = en.store.Get("meta", "budget:"+oldDay, &retained); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired settled day was retained indefinitely", err)
	}
}

func TestLegacyBudgetAndClockRollbackCannotResetDailyLimit(t *testing.T) {
	en, _ := lifeFixture(t)
	en.cfg.DailyTokens = 128
	oldDay := en.now().Format("2006-01-02")
	en.budget = Budget{Day: oldDay, Calls: 1, KnownTokens: 123, ReservedTokens: 64}
	if err := en.store.Put("meta", "budget", en.budget); err != nil {
		t.Fatal(err)
	}
	en.now = func() time.Time { return time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) }
	if _, err := en.reserve(64); err != nil {
		t.Fatal(err)
	}
	var previous Budget
	if err := en.store.Get("meta", "budget:"+oldDay, &previous); err != nil || previous.KnownTokens != 123 {
		t.Fatal("legacy active budget was not preserved on first rollover", previous, err)
	}
	en.now = func() time.Time { return time.Date(2026, 11, 13, 13, 0, 0, 0, time.UTC) }
	if _, err := en.reserve(64); err == nil {
		t.Fatal("clock rollback reset an already exhausted day")
	}
}

func TestBudgetUsesUTCAndSaturatesUntrustedTokenTotals(t *testing.T) {
	en, _ := lifeFixture(t)
	en.now = func() time.Time {
		return time.Date(2026, 11, 14, 1, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	}
	day, err := en.reserve(64)
	if err != nil || day != "2026-11-13" {
		t.Fatal("budget date was not UTC", day, err)
	}
	if err = en.account(day, &Usage{Total: math.MaxInt64}); err != nil {
		t.Fatal(err)
	}
	if err = en.account(day, &Usage{Total: 1}); err != nil {
		t.Fatal(err)
	}
	if en.budget.KnownTokens != math.MaxInt64 {
		t.Fatal("token accounting overflowed", en.budget)
	}
	if _, err = en.reserve(64); err == nil {
		t.Fatal("overflowed token total bypassed budget")
	}
}
