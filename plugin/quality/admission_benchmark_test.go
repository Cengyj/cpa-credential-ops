package quality

import (
	"context"
	"testing"
	"time"
)

func benchmarkManualEngine(b *testing.B, host Host) *Engine {
	b.Helper()
	config := DefaultConfig()
	config.DataDir = b.TempDir()
	config.DailyTokens = 1_000_000_000
	store, err := OpenStore(config.DataDir, config.MaxStorageBytes)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := store.Close(); err != nil {
			b.Error(err)
		}
	})
	return &Engine{cfg: config, host: host, ctx: context.Background(), now: func() time.Time { return time.Now().UTC() }, store: store, queue: make(chan string, 32), runs: map[string]*Run{}, rules: map[string]*Rule{}, active: map[string]context.CancelFunc{}, busyAuth: map[string]struct{}{}}
}

func BenchmarkQueryDuringSlowManualDirectory(b *testing.B) {
	h := &countedInventory{semanticsHost: &semanticsHost{targets: []Target{{AuthID: "auth-a", Provider: "offline", Models: []string{"a"}, EffectiveAvailable: true}}}, enter: make(chan struct{}, 1)}
	en := benchmarkManualEngine(b, h)
	var wait time.Duration
	b.ResetTimer()
	for range b.N {
		h.leave = make(chan struct{})
		done := make(chan error, 1)
		go func() { _, err := en.Submit(context.Background(), lifeConfig()); done <- err }()
		<-h.enter
		time.AfterFunc(25*time.Millisecond, func() { close(h.leave) })
		start := time.Now()
		en.Rules()
		wait += time.Since(start)
		if err := <-done; err != nil {
			b.Fatal(err)
		}
		<-en.queue
	}
	b.ReportMetric(float64(wait.Microseconds())/float64(b.N), "query_wait_us/op")
}

func BenchmarkManualTaskThroughput(b *testing.B) {
	h := &lifecycleHost{target: Target{AuthID: "auth-a", Provider: "offline", Models: []string{"a"}, EffectiveAvailable: true, IdentityVersion: "fixture"}}
	en := benchmarkManualEngine(b, h)
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for range b.N {
		r, err := en.Submit(context.Background(), lifeConfig())
		if err != nil {
			b.Fatal(err)
		}
		en.execute(<-en.queue)
		if en.runs[r.ID].Status != "completed" {
			b.Fatal("task did not finish")
		}
	}
	b.ReportMetric(float64(b.N)/time.Since(start).Seconds(), "tasks/s")
}
