package quality

import (
	"context"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	bolt "go.etcd.io/bbolt"
)

func TestCoverageKeepsExactTotalsForLargeFrozenPlans(t *testing.T) {
	en, _ := lifeFixture(t)
	for _, id := range []string{"one", "two"} {
		en.runs[id] = &Run{
			Config:        Frozen{RunConfig: RunConfig{Models: []string{"a", "b"}, Samples: math.MaxInt}},
			PrunedSamples: math.MaxInt, PrunedSlots: []SampleSlot{{Model: "b", Index: 0}},
			Samples: []Sample{{Model: "a", Index: 0, ExecutionStatus: "completed", QualityVerdict: "correct"}},
		}
	}
	count := big.NewInt(int64(math.MaxInt))
	planned := new(big.Int).Mul(count, big.NewInt(4))
	planned.Sub(planned, big.NewInt(2))
	unlocated := new(big.Int).Sub(count, big.NewInt(1))
	unlocated.Mul(unlocated, big.NewInt(2))
	denominator, _ := planned.Float64()
	for _, view := range []string{"", "summary"} {
		req := pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/model-quality/metrics", Query: url.Values{"view": {view}}}
		response := en.Handle(t.Context(), req)
		var data map[string]any
		if response.StatusCode != http.StatusOK || decode(response.Body, &data) != nil {
			t.Fatal("metrics unavailable")
		}
		checkCount := func(key string, want *big.Int) {
			t.Helper()
			if strconv.IntSize == 64 {
				if data[key] != want.String() {
					t.Fatalf("%s: %v, want exact decimal %s", key, data[key], want)
				}
			} else if data[key] != float64(want.Int64()) {
				t.Fatalf("%s: %v, want %s", key, data[key], want)
			}
		}
		checkCount("planned", planned)
		checkCount("unlocated_pruned_samples", unlocated)
		if data["covered"] != 2.0 || data["coverage"] != 2/denominator {
			t.Fatalf("incorrect large-plan coverage: %v / %v = %v", data["covered"], data["planned"], data["coverage"])
		}
	}
}

func TestMetricsCountDistinctPlannedSlots(t *testing.T) {
	for _, tc := range []struct {
		name, trims                 string
		planned, covered, unlocated int
	}{
		{"duplicates", "", 4, 2, 0},
		{"count only", `"pruned_samples":2,`, 4, 2, 2},
		{"known duplicates and outside plan", `"pruned_samples":5,"pruned_sample_slots":[{"model":"a","index":0},{"model":"a","index":1},{"model":"b","index":1},{"model":"b","index":1},{"model":"outside","index":1}],`, 3, 2, 0},
		{"mixed ages", `"pruned_samples":3,"pruned_sample_slots":[{"model":"b","index":1}],`, 3, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			en, _ := lifeFixture(t)
			var run Run
			err := decode([]byte(`{`+tc.trims+`"config":{"models":["a","b"],"samples":2},"samples":[
				{"model":"a","index":0,"quality_verdict":"correct","execution_status":"completed"},
				{"model":"a","index":0,"quality_verdict":"incorrect","execution_status":"completed"},
				{"model":"b","index":0,"quality_verdict":"correct","execution_status":"completed"},
				{"model":"a","index":1,"quality_verdict":"unknown","execution_status":"incomplete"},
				{"model":"a","index":-1,"quality_verdict":"incorrect","execution_status":"completed"},
				{"model":"outside","index":1,"quality_verdict":"correct","execution_status":"completed"},
				{"model":"b","index":2,"quality_verdict":"correct","execution_status":"completed"}
			]}`), &run)
			if err != nil {
				t.Fatal(err)
			}
			en.runs["round"] = &run
			stats, err := en.Metrics()
			if err != nil {
				t.Fatal(err)
			}
			want := float64(tc.covered) / float64(tc.planned)
			if stats["planned"] != tc.planned || stats["covered"] != tc.covered || stats["coverage"] != want || stats["unlocated_pruned_samples"] != tc.unlocated {
				t.Errorf("planned=%v covered=%v coverage=%v unlocated=%v, want %v/%v=%v, unlocated=%v", stats["planned"], stats["covered"], stats["coverage"], stats["unlocated_pruned_samples"], tc.covered, tc.planned, want, tc.unlocated)
			}
			if stats["correct"] != 4 || stats["incorrect"] != 2 || stats["quality_pass_rate"] != float64(4)/6 {
				t.Error("recorded judgement counts changed", stats)
			}
		})
	}
}

func TestOverviewMetricsUseTheSameRetainedEvidenceAsFullMetrics(t *testing.T) {
	en, _ := lifeFixture(t)
	en.runs["rule"] = &Run{
		Source: "scheduled_probe", PrunedSamples: 1, PrunedSlots: []SampleSlot{{Model: "b", Index: 1}},
		Config: Frozen{RunConfig: RunConfig{Models: []string{"a", "b"}, Samples: 2, Judge: Judge{Mode: "llm"}}},
		Samples: []Sample{
			{Model: "a", Index: 0, QualityVerdict: "correct", ExecutionStatus: "completed", Usage: &Usage{Total: 9}, JudgeUsage: &Usage{Total: 3}},
			{Model: "a", Index: 1, QualityVerdict: "incorrect", ExecutionStatus: "completed", Usage: &Usage{Total: 4}},
			{Model: "b", Index: 0, QualityVerdict: "unknown", ExecutionStatus: "failed"},
		},
		Controls: []Sample{{Usage: &Usage{Total: 7}, JudgeUsage: &Usage{Total: 5}}},
	}
	en.runs["manual"] = &Run{Source: "manual_test", Config: Frozen{RunConfig: RunConfig{Models: []string{"a"}, Samples: 2}}, Samples: []Sample{{QualityVerdict: "unknown", AnswerMatch: "correct"}}}
	en.dropped.Store(6)
	req := pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/model-quality/metrics"}
	full := en.Handle(context.Background(), req)
	req.Query = url.Values{"view": {"summary"}}
	short := en.Handle(context.Background(), req)
	var detailed, overview map[string]any
	if full.StatusCode != http.StatusOK || short.StatusCode != http.StatusOK || decode(full.Body, &detailed) != nil || decode(short.Body, &overview) != nil {
		t.Fatal("metrics response unavailable")
	}
	want := map[string]float64{
		"correct": 1, "incorrect": 1, "unknown": 2, "planned": 5, "quality_pass_rate": .5, "coverage": .4,
		"known_tokens": 28, "unknown_usage": 2, "dropped_usage_events": 6, "covered": 2, "unlocated_pruned_samples": 0,
	}
	for key, value := range want {
		if detailed[key] != value || overview[key] != value {
			t.Errorf("%s: full %v, summary %v, want %v", key, detailed[key], overview[key], value)
		}
	}
	for _, key := range []string{"traffic_sources", "latency_ms", "ttft_ms", "budget", "subject_known_tokens", "judge_known_tokens", "control_known_tokens"} {
		if _, ok := detailed[key]; !ok {
			t.Errorf("full metrics lost %s", key)
		}
		if _, ok := overview[key]; ok {
			t.Errorf("overview included detail field %s", key)
		}
	}
	// A fault in a traffic event does not prevent reading the retained runs
	// used by the overview. The detailed traffic query reports that fault.
	if err := en.store.Write(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("usage")).Put([]byte("broken"), []byte("{"))
	}); err != nil {
		t.Fatal(err)
	}
	if res := en.Handle(context.Background(), req); res.StatusCode != http.StatusOK {
		t.Fatal("overview read the unrelated traffic table", res.StatusCode)
	}
	req.Query.Del("view")
	if res := en.Handle(context.Background(), req); res.StatusCode != http.StatusServiceUnavailable {
		t.Fatal("detailed metrics hid corrupt traffic", res.StatusCode)
	}
}
