package quality

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const directoryFreshness = 2 * time.Second
const directoryContinuationTTL = 2 * time.Minute
const maxDirectorySnapshots = 4

// A display snapshot contains projected targets only. Admission and dispatch
// always ask the host for their own current inventory and execution evidence.
type directorySnapshot struct {
	revision  uint64
	id        string
	created   time.Time
	items     []Target
	providers providerInventoryStatus
}

type directoryRead struct {
	done     chan struct{}
	snapshot *directorySnapshot
	err      error
}

type directoryPages struct {
	revision atomic.Uint64
	mu       sync.Mutex
	recent   []*directorySnapshot
	reading  *directoryRead
}

func (en *Engine) targetPage(ctx context.Context, req pluginapi.ManagementRequest) (map[string]any, error) {
	s, err := en.directorySnapshot(ctx, req.Query.Get("snapshot"), req.Query.Get("fresh") == "1")
	if err != nil {
		return nil, err
	}
	rows := page(s.items, req)
	rows["snapshot"] = s.id
	rows["capabilities"] = en.Capabilities()
	rows["provider_directory_verified"] = s.providers.Verified
	if s.providers.Reason != "" {
		rows["provider_directory_reason"] = s.providers.Reason
	}
	return rows, nil
}

func (en *Engine) directorySnapshot(ctx context.Context, token string, fresh bool) (*directorySnapshot, error) {
	cache := &en.directory
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cache.mu.Lock()
		now := en.now()
		revision := cache.revision.Load()
		for _, s := range cache.recent {
			if token != "" && s.id == token && now.Before(s.created.Add(directoryContinuationTTL)) ||
				token == "" && !fresh && s.revision == revision && now.Before(s.created.Add(directoryFreshness)) {
				cache.mu.Unlock()
				return s, nil
			}
		}
		if token != "" {
			cache.mu.Unlock()
			return nil, fmt.Errorf("snapshot_expired: 目录快照已过期，请从第一页重新读取。")
		}
		if flight := cache.reading; flight != nil {
			cache.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-flight.done:
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if errors.Is(flight.err, context.Canceled) || flight.snapshot != nil && flight.snapshot.revision != cache.revision.Load() {
					continue
				}
				return flight.snapshot, flight.err
			}
		}
		flight := &directoryRead{done: make(chan struct{})}
		cache.reading = flight
		cache.mu.Unlock()
		return en.refreshDirectory(ctx, flight)
	}
}

func (en *Engine) refreshDirectory(ctx context.Context, flight *directoryRead) (*directorySnapshot, error) {
	cache := &en.directory
	for attempt := 1; ; attempt++ {
		s, err := en.readDisplayDirectory(ctx)
		cache.mu.Lock()
		if err == nil && s.revision != cache.revision.Load() {
			// Keep all readers on one flight while replacing an invalidated scan.
			// Continuous changes must not keep a host request scanning indefinitely.
			if attempt == 1 {
				cache.mu.Unlock()
				continue
			}
			err = errors.New("unavailable: 读取期间目标目录持续变化，请稍后重试。")
		}
		if err == nil {
			s.id, s.created = ID(), en.now()
			flight.snapshot = s
			recent := []*directorySnapshot{s}
			for _, old := range cache.recent {
				if len(recent) < maxDirectorySnapshots && s.created.Before(old.created.Add(directoryContinuationTTL)) {
					recent = append(recent, old)
				}
			}
			cache.recent = recent
		}
		flight.err = err
		cache.reading = nil
		close(flight.done)
		cache.mu.Unlock()
		return flight.snapshot, err
	}
}

func (en *Engine) readDisplayDirectory(ctx context.Context) (*directorySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := &directorySnapshot{revision: en.directory.revision.Load(), providers: providerInventoryStatus{Verified: true}}
	items, err := en.visibleTargets(context.WithValue(ctx, providerInventoryStatusKey{}, &s.providers))
	if err != nil {
		return nil, err
	}
	for i := range items {
		t := &items[i]
		t.Models, t.Reasons = slices.Clone(t.Models), slices.Clone(t.Reasons)
		t.ErrorPolicyBlock = cloneValue(t.ErrorPolicyBlock)
		if reasoning, ok := t.Reasoning.(map[string]*thinkingOptions); ok {
			t.Reasoning = maps.Clone(reasoning)
		}
	}
	if h, ok := en.host.(*NativeHost); ok && h.Management != nil && h.Management.ready() {
		h.Management.addModelReasoning(ctx, items)
	}
	for i := range items {
		items[i].providerProbeModels, items[i].reasoningModelIDs = nil, nil
	}
	s.items = items
	return s, ctx.Err()
}
