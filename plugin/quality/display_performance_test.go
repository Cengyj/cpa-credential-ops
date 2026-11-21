package quality

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	bolt "go.etcd.io/bbolt"
)

// Exercise the same authenticated handler and serialization as a polling tab.
func BenchmarkDisplayHistoryAndMetrics(b *testing.B) {
	store, err := OpenStore(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	en := &Engine{store: store, runs: map[string]*Run{}}
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err = store.Write(func(tx *bolt.Tx) error {
		for i := range 5000 {
			id := fmt.Sprintf("test-%08d", i)
			when := epoch.Add(time.Duration(i) * time.Minute)
			r := &Run{ID: id, Status: "completed", CompletedAt: &when, Config: Frozen{RunConfig: RunConfig{Models: []string{"fixture"}, Samples: 4}}}
			for pos := range 4 {
				r.Samples = append(r.Samples, Sample{Model: "fixture", Index: pos, ExecutionStatus: "completed", QualityVerdict: "correct"})
			}
			en.runs[id] = r
			if err := put(tx, "audit", id, Audit{ID: id, Time: when, Kind: "offline_fixture", RunID: id}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	for _, scenario := range []string{"history", "metrics"} {
		b.Run(scenario, func(b *testing.B) {
			req := pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/model-quality/" + scenario, Query: url.Values{"view": {"summary"}, "limit": {"20"}}}
			b.ReportAllocs()
			for b.Loop() {
				if response := en.Handle(b.Context(), req); response.StatusCode != http.StatusOK {
					b.Fatal(response.StatusCode)
				}
			}
		})
	}
}

func TestDisplayProjectionsFollowCommittedChanges(t *testing.T) {
	en, _ := lifeFixture(t)
	now := en.now().Add(time.Hour)
	for _, id := range []string{"a", "b", "c"} {
		if err := en.store.Put("audit", id, Audit{ID: id, Time: now}); err != nil {
			t.Fatal(err)
		}
	}
	first := func() Audit {
		t.Helper()
		page, err := en.store.historyPage(url.Values{"limit": {"1"}})
		if err != nil {
			t.Fatal(err)
		}
		return page["items"].([]Audit)[0]
	}
	if row := first(); row.ID != "a" {
		t.Fatal(row)
	}
	if err := en.store.Write(func(tx *bolt.Tx) error { return tx.Bucket([]byte("audit")).Delete([]byte("a")) }); err != nil {
		t.Fatal(err)
	}
	if row := first(); row.ID != "b" {
		t.Fatal("stale index after delete", row)
	}
	summary := func() map[string]any {
		t.Helper()
		rows, err := en.metrics(true)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	previous := summary()
	previous["correct"] = 42
	if summary()["correct"] != 0 {
		t.Fatal("caller mutated summary")
	}
	c := changes()
	c.runs["job"] = &Run{ID: "job", Samples: []Sample{{ExecutionStatus: "completed", QualityVerdict: "correct"}}}
	en.mu.Lock()
	err := en.commitLocked(c)
	en.mu.Unlock()
	if err != nil || summary()["correct"] != 1 {
		t.Fatal("stale totals after commit", err)
	}
	en.dropped.Add(1)
	if summary()["dropped_usage_events"] != uint64(1) {
		t.Fatal("volatile counter hidden by revision")
	}
}
