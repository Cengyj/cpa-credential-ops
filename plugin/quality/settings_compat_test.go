package quality

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// Match the field order in the 0.32.0 Config for its byte-level host digest.
func legacySettingsFixture(t *testing.T, cfg Config, calls, samples int) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil || !bytes.Contains(raw, []byte(`"daily_tokens":`)) || !bytes.Contains(raw, []byte(`"max_output_tokens":`)) ||
		bytes.Contains(raw, []byte(`"daily_calls":`)) || bytes.Contains(raw, []byte(`"max_samples":`)) {
		t.Fatal("unexpected current config format", err)
	}
	raw = bytes.Replace(raw, []byte(`"max_output_tokens":`), []byte(fmt.Sprintf(`"max_samples":%d,"max_output_tokens":`, samples)), 1)
	return bytes.Replace(raw, []byte(`"daily_tokens":`), []byte(fmt.Sprintf(`"daily_calls":%d,"daily_tokens":`, calls)), 1)
}

func TestRetiredLimitsNeverEnterSettingsOrOverwriteRollbackValues(t *testing.T) {
	for _, prior := range []bool{true, false} {
		t.Run(map[bool]string{true: "existing_host_input", false: "legacy_input_digest"}[prior], func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.DataDir = t.TempDir()
			store, err := OpenStore(cfg.DataDir, cfg.MaxStorageBytes)
			if err != nil {
				t.Fatal(err)
			}
			saved := legacySettingsFixture(t, cfg, math.MaxInt32, 77)
			input := legacySettingsFixture(t, cfg, 200, 32)
			digest := fmt.Sprintf("%x", sha256.Sum256(input))
			initial := map[string]any{"settings": saved, "config_input": digest}
			if prior {
				initial["quality_host_input"] = input
			}
			for key, value := range initial {
				if err := store.Put("meta", key, value); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			en, err := New(cfg, &lifecycleHost{})
			if err != nil {
				t.Fatal(err)
			}
			defer en.Close()
			checkDisk := func() {
				t.Helper()
				for key, expect := range map[string]json.RawMessage{"settings": saved, "quality_host_input": input} {
					var got json.RawMessage
					if err := en.store.Get("meta", key, &got); err != nil || !bytes.Equal(got, expect) {
						t.Fatalf("%s differs from the old volume's representation: got %s want %s (%v)", key, got, expect, err)
					}
				}
				var actualDigest string
				if err := en.store.Get("meta", "config_input", &actualDigest); err != nil || actualDigest != digest {
					t.Fatal("stored host baseline and legacy digest differ", err)
				}
			}
			checkDisk()
			request := func(method string, raw []byte) {
				t.Helper()
				reply := en.Handle(t.Context(), pluginapi.ManagementRequest{Method: method, Path: "/v0/management/plugins/model-quality/settings", Body: raw})
				if reply.StatusCode != 200 || bytes.Contains(reply.Body, []byte("daily_call")) || bytes.Contains(reply.Body, []byte(`"max_samples"`)) || !bytes.Contains(reply.Body, []byte("daily_tokens")) {
					t.Fatalf("unexpected settings response %d %s", reply.StatusCode, reply.Body)
				}
			}
			request(http.MethodGet, nil)
			request(http.MethodPut, legacySettingsFixture(t, cfg, 1, 1))
			checkDisk()
			changed := cfg
			changed.DailyTokens = 4096
			request(http.MethodPut, legacySettingsFixture(t, changed, 0, 8))
			saved = legacySettingsFixture(t, changed, math.MaxInt32, 77)
			checkDisk()
			if err := yaml.Unmarshal([]byte("daily_calls: 3\nmax_samples: 2\nretention_days: 60\n"), &cfg); err != nil {
				t.Fatal(err)
			}
			if err := en.ReconfigureQuality(cfg); err != nil {
				t.Fatal(err)
			}
			changed.RetentionDays = 60
			saved, input = legacySettingsFixture(t, changed, math.MaxInt32, 77), legacySettingsFixture(t, cfg, 200, 32)
			digest = fmt.Sprintf("%x", sha256.Sum256(input))
			checkDisk()
		})
	}
}

func TestNewVolumeCarriesOnlyRollbackCompatibility(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir, cfg.DailyTokens = t.TempDir(), 8192
	en, err := New(cfg, &lifecycleHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Close()
	for _, key := range []string{"settings", "quality_host_input"} {
		var row json.RawMessage
		if err := en.store.Get("meta", key, &row); err != nil || !bytes.Equal(row, legacySettingsFixture(t, cfg, 8192, 128)) {
			t.Fatalf("%s is incompatible with a previous reader: %s %v", key, row, err)
		}
	}
}
