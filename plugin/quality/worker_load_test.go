package quality

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"
)

// Exercise admission, bbolt receipts, the actual queue and workers using only
// four independent synthetic targets. External work takes 2 ms per sample.
type workerLoadHost struct {
	lifecycleHost
	targets          []Target
	entered, release chan struct{}
}

func (h *workerLoadHost) Targets(ctx context.Context) ([]Target, error) {
	if h.entered != nil {
		select {
		case h.entered <- struct{}{}:
		default:
		}
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h.targets, nil
}
func loadEngine(t *testing.T, pauseRead bool) (*Engine, *workerLoadHost) {
	t.Helper()
	h := &workerLoadHost{}
	for i := range 4 {
		id := fmt.Sprintf("offline-%d", i)
		h.targets = append(h.targets, Target{AuthID: id, Provider: "offline", Models: []string{"a"}, IdentityVersion: id, PayloadConfigVersion: "payload-v1", EffectiveAvailable: true})
	}
	if pauseRead {
		h.entered = make(chan struct{}, 1)
		h.release = make(chan struct{})
	}
	h.execute = func(ctx context.Context, e Execution) Sample {
		select {
		case <-time.After(2 * time.Millisecond):
			return Sample{ExecutionStatus: "completed", Output: "4", Usage: &Usage{Total: 1}}
		case <-ctx.Done():
			return Sample{ExecutionStatus: "cancelled"}
		}
	}
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Queue = 64
	en, err := New(cfg, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := en.Close(); err != nil {
			t.Error(err)
		}
	})
	return en, h
}

func TestOfflineWorkerThroughput(t *testing.T) {
	en, h := loadEngine(t, false)
	const jobs = 48
	var start, finish runtime.MemStats
	runtime.ReadMemStats(&start)
	begin := time.Now()
	ids := make([]string, 0, jobs)
	for i := range jobs {
		config := lifeConfig()
		config.AuthID = h.targets[i%len(h.targets)].AuthID
		config.IdempotencyKey = fmt.Sprintf("fixture-%d", i)
		run, err := en.Submit(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	for {
		en.mu.Lock()
		done := 0
		for _, id := range ids {
			run := en.runs[id]
			if run.CompletedAt != nil {
				if run.Status != "completed" {
					en.mu.Unlock()
					t.Fatalf("fixture failed: %s", run.Status)
				}
				done++
			}
		}
		en.mu.Unlock()
		if done == jobs {
			break
		}
		if time.Since(begin) > 15*time.Second {
			t.Fatalf("%d/%d jobs completed", done, jobs)
		}
		time.Sleep(time.Millisecond)
	}
	duration := time.Since(begin)
	runtime.ReadMemStats(&finish)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.calls) != jobs {
		t.Fatalf("unexpected model callback count %d", len(h.calls))
	}
	t.Logf("jobs=%d workers=2 targets=4 elapsed=%s jobs/s=%.1f allocated_bytes/job=%d", jobs, duration, jobs/duration.Seconds(), (finish.TotalAlloc-start.TotalAlloc)/jobs)
}

func TestOfflineReadContention(t *testing.T) {
	en, h := loadEngine(t, true)
	config := lifeConfig()
	config.AuthID = h.targets[0].AuthID
	done := make(chan error, 1)
	go func() { _, err := en.Submit(t.Context(), config); done <- err }()
	<-h.entered
	timer := time.AfterFunc(50*time.Millisecond, func() { close(h.release) })
	begin := time.Now()
	en.Config()
	wait := time.Since(begin)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	timer.Stop()
	t.Logf("unrelated settings wait=%s for 50ms fake directory delay", wait)
}
