package quality

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type Engine struct {
	loginHolds      map[string]*credentialLoginHold
	loginGeneration uint64
	managed         map[string]*CredentialDisposition
	mu              sync.Mutex
	store           *Store
	host            Host
	cfg             Config
	runs            map[string]*Run
	rules           map[string]*Rule
	timers          map[string]bool
	active          map[string]context.CancelFunc
	busyAuth        map[string]struct{}
	queue           chan string
	scopeBatches    map[string][]string
	finishedRules   map[string]bool
	roundRemaining  map[string]int
	roundEnded      chan struct{}
	scanning        map[string]bool
	snapshotSlots   chan struct{}
	events          chan UsageEvent
	dropped         atomic.Uint64
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	closed          bool
	closeDone       chan struct{}
	closeErr        error
	queueChanged    chan struct{}
	fault           error
	now             func() time.Time
	budget          Budget
	budgetPending   map[string]int

	// In-flight scans and captured ticks never cross an instance-wide revocation.
	scheduleGeneration uint64
	scheduleRetry      map[string]scheduleRetry
	loadedAt           time.Time
}

func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func New(c Config, h Host) (*Engine, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	s, e := OpenStore(c.DataDir, c.MaxStorageBytes)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	en := &Engine{store: s, host: h, cfg: c, runs: map[string]*Run{}, rules: map[string]*Rule{}, active: map[string]context.CancelFunc{}, busyAuth: map[string]struct{}{}, queue: make(chan string, c.Queue), events: make(chan UsageEvent, 256), ctx: ctx, cancel: cancel, now: func() time.Time { return time.Now().UTC() }}
	en.roundEnded = make(chan struct{}, 1)
	if e = en.load(); e != nil {
		cancel()
		return nil, errors.Join(e, s.Close())
	}
	if !en.Capabilities().QualityPause {
		// Keep saved settings and pause ownership intact; only the effective capability changes.
		en.cfg.AutomaticPause = false
	}
	for i := 0; i < c.Workers; i++ {
		en.wg.Add(1)
		go en.worker()
	}
	en.wg.Add(2)
	go en.background()
	go en.watchFinishedRounds()
	return en, nil
}
func (en *Engine) load() error {
	en.loadedAt = en.now()
	previousProviders, err := en.previousProviderSetting()
	if err != nil {
		return err
	}
	if err := en.loadLoginHolds(); err != nil {
		return err
	}
	if err := en.loadDispositions(); err != nil {
		return err
	}
	var timers map[string]bool
	if err := en.store.Get("meta", ruleTimersKey, &timers); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	en.timers = timers
	if e := en.store.All("rules", func(raw []byte) error {
		var r Rule
		if e := json.Unmarshal(raw, &r); e != nil {
			return e
		}
		r.Status = ruleStatus(&r)
		en.rules[r.ID] = &r
		return nil
	}); e != nil {
		return e
	}
	if err := en.loadQualitySettings(); err != nil {
		return err
	}
	if err := en.migrateRuleScopes(previousProviders); err != nil {
		return err
	}
	var interrupted []*Run
	if e := en.store.All("runs", func(raw []byte) error {
		var r Run
		if e := json.Unmarshal(raw, &r); e != nil {
			return e
		}
		if r.Status == "queued" || r.Status == "running" {
			r.Status = "interrupted"
			now := en.now()
			r.CompletedAt = &now
			r.Error = "process stopped; upstream calls are never replayed automatically"
			interrupted = append(interrupted, &r)
		}
		en.runs[r.ID] = &r
		return nil
	}); e != nil {
		return e
	}
	for _, r := range interrupted {
		if e := en.store.Put("runs", r.ID, r); e != nil {
			return e
		}
		en.noteRoundEndLocked(r.Config.RuleID)
	}
	if err := en.interruptCredentialWrites(); err != nil {
		return err
	}
	if err := en.recoverDisabledSources(); err != nil {
		return err
	}
	if e := en.store.Get("meta", "budget", &en.budget); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return nil
}
func (en *Engine) Close() error {
	en.mu.Lock()
	if en.closed {
		done := en.closeDone
		en.mu.Unlock()
		if done != nil {
			<-done
		}
		en.mu.Lock()
		err := en.closeErr
		en.mu.Unlock()
		return err
	}
	en.closed = true
	en.closeDone = make(chan struct{})
	if en.cancel != nil {
		en.cancel()
	}
	for _, cancel := range en.active {
		cancel()
	}
	en.notifyWorkersLocked()
	en.mu.Unlock()
	en.wg.Wait()
	en.mu.Lock()
	defer en.mu.Unlock()
	now := en.now()
	pending := changes()
	for id, r := range en.runs {
		if r.Status == "queued" {
			next := cloneRun(r)
			next.Status = "interrupted"
			next.CompletedAt = &now
			pending.runs[id] = next
		}
	}
	persistErr := en.commitLocked(pending)
	en.closeErr = errors.Join(persistErr, en.store.Close())
	close(en.closeDone)
	return en.closeErr
}
func (en *Engine) Config() Config {
	en.mu.Lock()
	defer en.mu.Unlock()
	c := en.cfg
	if !en.Capabilities().QualityPause {
		c.AutomaticPause = false
	}
	return c
}
func (en *Engine) UpdateConfig(c Config) error {
	return en.updateConfig(c, false)
}
func (en *Engine) updateConfig(c Config, keepProviderTargets bool) error {
	if e := c.Validate(); e != nil {
		return e
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	if c.AutomaticPause && !en.Capabilities().QualityPause {
		return unsupportedQuality()
	}
	if c.DataDir != en.cfg.DataDir || c.Workers != en.cfg.Workers || c.Queue != en.cfg.Queue {
		return fmt.Errorf("data_dir/workers/queue require host reconfiguration")
	}
	if keepProviderTargets {
		c.IncludeProviderTargets = en.cfg.IncludeProviderTargets
	}
	if e := en.writeScheduleSettings(c, en.cfg.SchedulingEnabled && !c.SchedulingEnabled); e != nil {
		return e
	}
	en.cfg = c
	return nil
}
