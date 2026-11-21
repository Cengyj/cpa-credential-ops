package quality

import (
	"errors"
	"fmt"
	"os"

	bolt "go.etcd.io/bbolt"
)

// Only quality.Config participates in this comparison. Connection settings and
// explicit timer grants have separate storage and independent update paths.
func (en *Engine) loadQualitySettings() error {
	input := en.cfg
	var saved, prior Config
	savedErr := en.store.Get("meta", "settings", &saved)
	previousGlobal := saved.SchedulingEnabled
	priorErr := en.store.Get("meta", "quality_host_input", &prior)
	for _, err := range []error{savedErr, priorErr} {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if savedErr != nil {
		saved = input
		previousGlobal = input.SchedulingEnabled
	} else {
		if err := saved.Validate(); err != nil {
			return err
		}
		if priorErr == nil {
			if err := prior.Validate(); err != nil {
				return err
			}
			saved = mergeQualityInput(saved, prior, input)
		}
		// Legacy stores contain only a digest. It cannot identify individual
		// host edits, so preserve the operator's effective settings at migration.
		saved.DataDir, saved.Workers, saved.Queue = input.DataDir, input.Workers, input.Queue
	}
	if err := saved.Validate(); err != nil {
		return err
	}
	if priorErr != nil || prior != input || savedErr != nil {
		stop := priorErr == nil && prior.SchedulingEnabled && !input.SchedulingEnabled
		// Loading a volume must apply the same explicit permission transition
		// as a hot edit. A newly opened global gate has no missed authorized work.
		change := en.scheduleSettingsChanges(saved, stop, previousGlobal)
		change.hostInput = &input
		if err := en.commitLocked(change); err != nil {
			return err
		}
	}
	en.cfg = saved
	return nil
}

func putQualityHostInput(tx *bolt.Tx, input Config) error {
	if err := put(tx, "meta", "quality_host_input", input); err != nil {
		return err
	}
	return put(tx, "meta", "config_input", fingerprint(input))
}

// ReconfigureQuality applies the changed host fields without interrupting
// login tasks or replacing the verified connection and its transport.
func (en *Engine) ReconfigureQuality(input Config) error {
	if err := input.Validate(); err != nil {
		return err
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	if input.DataDir != en.cfg.DataDir || input.Workers != en.cfg.Workers || input.Queue != en.cfg.Queue {
		return fmt.Errorf("conflict: structural settings require replacement")
	}
	var prior, saved Config
	if err := en.store.Get("meta", "quality_host_input", &prior); err != nil {
		return err
	}
	if err := en.store.Get("meta", "settings", &saved); err != nil {
		return err
	}
	next := mergeQualityInput(saved, prior, input)
	c := en.scheduleSettingsChanges(next, prior.SchedulingEnabled && !input.SchedulingEnabled, en.cfg.SchedulingEnabled)
	c.hostInput = &input
	if err := en.commitLocked(c); err != nil {
		return err
	}
	en.cfg = next
	if !en.Capabilities().QualityPause {
		en.cfg.AutomaticPause = false
	}
	return nil
}

func hostSetting[T comparable](saved, prior, input T) T {
	if input != prior {
		return input
	}
	return saved
}

// Retain panel changes to each setting unless its own host input changed.
func mergeQualityInput(saved, prior, input Config) Config {
	saved.IncludeProviderTargets = hostSetting(saved.IncludeProviderTargets, prior.IncludeProviderTargets, input.IncludeProviderTargets)
	saved.SchedulingEnabled = hostSetting(saved.SchedulingEnabled, prior.SchedulingEnabled, input.SchedulingEnabled)
	saved.AutomaticPause = hostSetting(saved.AutomaticPause, prior.AutomaticPause, input.AutomaticPause)
	saved.MaxModels = hostSetting(saved.MaxModels, prior.MaxModels, input.MaxModels)
	saved.MaxSamples = hostSetting(saved.MaxSamples, prior.MaxSamples, input.MaxSamples)
	saved.MaxOutputTokens = hostSetting(saved.MaxOutputTokens, prior.MaxOutputTokens, input.MaxOutputTokens)
	saved.MaxResponseBytes = hostSetting(saved.MaxResponseBytes, prior.MaxResponseBytes, input.MaxResponseBytes)
	saved.DailyCalls = hostSetting(saved.DailyCalls, prior.DailyCalls, input.DailyCalls)
	saved.DailyTokens = hostSetting(saved.DailyTokens, prior.DailyTokens, input.DailyTokens)
	saved.RetentionDays = hostSetting(saved.RetentionDays, prior.RetentionDays, input.RetentionDays)
	saved.MaxStorageBytes = hostSetting(saved.MaxStorageBytes, prior.MaxStorageBytes, input.MaxStorageBytes)
	return saved
}
