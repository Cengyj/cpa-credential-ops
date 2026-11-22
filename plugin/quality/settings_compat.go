package quality

import (
	"fmt"
	"math"

	"github.com/MoeForge/moego/json"
	bolt "go.etcd.io/bbolt"
)

// Older readers validate both saved settings and their host baseline. Preserve
// their distinct retired limits on disk. The shadowed fields reproduce the
// historical JSON field order for the host-input digest.
type rollbackSettings struct {
	Config
	MaxSamples       int   `json:"max_samples"`
	MaxOutputTokens  int   `json:"max_output_tokens"`
	MaxResponseBytes int   `json:"max_response_bytes"`
	DailyCalls       int   `json:"daily_calls"`
	DailyTokens      int64 `json:"daily_tokens"`
	RetentionDays    int   `json:"retention_days"`
	MaxStorageBytes  int64 `json:"max_storage_bytes"`
}

type rollbackLimits struct {
	DailyCalls int `json:"daily_calls"`
	MaxSamples int `json:"max_samples"`
}

func putQualityConfig(tx *bolt.Tx, key string, cfg Config) error {
	meta := tx.Bucket([]byte("meta"))
	row := rollbackSettings{Config: cfg, MaxOutputTokens: cfg.MaxOutputTokens, MaxResponseBytes: cfg.MaxResponseBytes,
		DailyTokens: cfg.DailyTokens, RetentionDays: cfg.RetentionDays, MaxStorageBytes: cfg.MaxStorageBytes}
	var previous rollbackLimits
	if raw := meta.Get([]byte(key)); raw != nil {
		if err := json.Unmarshal(raw, &previous); err != nil {
			return fmt.Errorf("read saved quality configuration: %w", err)
		}
	}
	row.DailyCalls = previous.DailyCalls
	if row.DailyCalls < 1 {
		// Each accepted call has a positive token reservation. This fallback
		// for a new row adds no tighter bound for older readers of this data.
		row.DailyCalls = int(min(cfg.DailyTokens, math.MaxInt))
	}
	row.MaxSamples = previous.MaxSamples
	if row.MaxSamples < 1 || row.MaxSamples > 128 {
		row.MaxSamples = 128 // Largest value 0.32.0 accepts when reading a new volume.
	}
	if key == "quality_host_input" {
		if meta.Get([]byte(key)) == nil && meta.Get([]byte("config_input")) != nil {
			if err := recoverHostInputDigest(meta, &row); err != nil {
				return err
			}
		}
		if err := put(tx, "meta", "config_input", fingerprint(row)); err != nil {
			return err
		}
	}
	return put(tx, "meta", key, row)
}

// Pre-0.32 volumes may only have a digest. Use old operator values or the
// historical defaults only when the complete old input matches that digest.
func recoverHostInputDigest(meta *bolt.Bucket, row *rollbackSettings) error {
	var digest string
	if err := json.Unmarshal(meta.Get([]byte("config_input")), &digest); err != nil {
		return err
	}
	var settings rollbackLimits
	if raw := meta.Get([]byte("settings")); raw != nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return err
		}
	}
	for _, calls := range []int{settings.DailyCalls, 200} {
		for _, samples := range []int{settings.MaxSamples, 32} {
			candidate := *row
			candidate.DailyCalls, candidate.MaxSamples = calls, samples
			if calls > 0 && samples > 0 && samples <= 128 && fingerprint(candidate) == digest {
				*row = candidate
				return nil
			}
		}
	}
	return nil
}
