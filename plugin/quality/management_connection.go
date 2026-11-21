package quality

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// This endpoint is implemented by CPA 8.0.76 and reads local counters only.
const readinessPath = "/v8/management/observability/requests/in-flight"

type ConnectionStatus struct {
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	State         string     `json:"state"`
	Code          string     `json:"code,omitempty"`
	Source        string     `json:"source,omitempty"`
	Configured    bool       `json:"configured"`
	CanMigrate    bool       `json:"can_migrate"`
	Revision      uint64     `json:"revision"`
}

type connectionState struct {
	status   ConnectionStatus
	files    connectionFiles
	ctx      context.Context
	cancel   context.CancelFunc
	wake     chan struct{}
	wg       sync.WaitGroup
	next     time.Time
	failures uint
}

func nextConnectionRevision(previous uint64) uint64 {
	for {
		n := rand.Uint64N(1<<52) + 1
		if n != previous {
			return n
		}
	}
}

func (m *ManagementClient) snapshot() ConnectionStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := m.status
	if !m.next.IsZero() && (out.State == "waiting" || out.State == "rate_limited" || out.State == "restricted" || out.invalidationPending()) {
		next := m.next.UTC()
		out.NextAttemptAt = &next
	}
	return out
}

func (st ConnectionStatus) invalidationPending() bool {
	return st.State == "invalid" && st.Code == "connection_invalidation_save_failed"
}

// RuntimeManagementClient leaves SDK-backed operations available when the
// deployment URL is invalid. The rejected URL is never retained or contacted.
func RuntimeManagementClient(base string) *ManagementClient {
	client, err := NewManagementClient(base)
	if err == nil {
		return client
	}
	return &ManagementClient{connectionState: connectionState{status: ConnectionStatus{State: "error", Code: "connection_endpoint_invalid"}}}
}

// Start is called once by the native instance owner after the quality store
// has acquired its single-instance lock. No network I/O runs on this path.
func (m *ManagementClient) Start(directory, deploymentKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx != nil || m.status.State == "closing" {
		return
	}
	m.status.Revision = nextConnectionRevision(0)
	m.files = connectionFiles(filepath.Join(directory, "management"))
	m.ctx, m.cancel = context.WithCancel(context.Background())
	if m.base == "" {
		return
	}
	m.wake = make(chan struct{}, 1)
	record, err := m.files.readRecord()
	switch {
	case err != nil:
		m.status = ConnectionStatus{State: "error", Code: "connection_store_invalid", Revision: nextConnectionRevision(0)}
	case record != nil:
		m.status.Revision, m.status.CanMigrate = record.Revision, false
		switch {
		case record.State == "cleared":
		case record.Base != m.base:
			m.status.State, m.status.Code = "error", "connection_endpoint_changed"
		case record.State == "invalid":
			m.status.State, m.status.Code, m.status.Configured = "invalid", "management_key_invalid", true
		default:
			m.key = record.Key
			m.status.State, m.status.Source, m.status.Configured = "waiting", "saved", true
		}
	case deploymentKey != "":
		m.status.CanMigrate = false
		if validManagementKey(deploymentKey) {
			m.key = deploymentKey
			m.status.State, m.status.Source, m.status.Configured = "waiting", "deployment", true
		} else {
			m.status.State, m.status.Code = "error", "deployment_key_invalid"
		}
	}
	m.wg.Add(1)
	go m.monitor()
}

func (m *ManagementClient) Close() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.key = ""
	m.status.State = "closing"
	m.mu.Unlock()
	m.wg.Wait()
	if m.client != nil {
		m.client.CloseIdleConnections()
	}
}

func (m *ManagementClient) signalLocked() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *ManagementClient) handleConnection(ctx context.Context, req pluginapi.ManagementRequest) (ConnectionStatus, error) {
	if req.Method == http.MethodGet {
		return m.snapshot(), nil
	}
	var input struct {
		Revision *uint64 `json:"revision"`
		Mode     string  `json:"mode,omitempty"`
	}
	if DecodeStrict(req.Body, &input) != nil || input.Revision == nil {
		return ConnectionStatus{}, errors.New("invalid_request: connection revision required")
	}
	clear := req.Method == http.MethodDelete
	if clear && input.Mode != "" || !clear && input.Mode != "configure" && input.Mode != "migrate" {
		return ConnectionStatus{}, errors.New("invalid_request: invalid connection operation")
	}
	if !m.edit.TryLock() {
		return ConnectionStatus{}, errors.New("conflict: another connection configuration is being verified")
	}
	defer m.edit.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.status
	if st.State == "closing" || m.ctx == nil {
		return st, errors.New("unavailable: background connection is offline")
	}
	if m.base == "" {
		return st, connectionError(st)
	}
	if st.Revision != *input.Revision || input.Mode == "migrate" && !st.CanMigrate {
		return st, errors.New("conflict: background configuration changed")
	}
	record := connectionRecord{Version: 1, Revision: nextConnectionRevision(st.Revision), Base: m.base, State: "cleared"}
	next := ConnectionStatus{State: "unconfigured", Revision: record.Revision}
	if !clear {
		key := managementKey(req.Headers)
		if key == "" {
			return st, errors.New("management_key_invalid: a valid management credential is required")
		}
		// Keep status and existing workers readable while validating the candidate.
		m.mu.Unlock()
		outcome := m.probe(ctx, key)
		m.mu.Lock()
		if m.status.State == "closing" || m.status.Revision != st.Revision {
			return m.status, errors.New("conflict: background configuration changed during verification")
		}
		if outcome.state != "ready" {
			return m.status, &connectionVerificationError{connectionError(ConnectionStatus{State: outcome.state, Code: outcome.code}), outcome.delay}
		}
		record.Key, record.State = key, "saved"
		next.State, next.Source, next.Configured = "ready", "saved", true
	}
	if err := m.files.saveRecord(record); err != nil {
		if errors.Is(err, errConnectionDurability) {
			m.key = ""
			m.status = ConnectionStatus{State: "error", Code: "connection_save_unknown", Revision: record.Revision}
			m.signalLocked()
			return m.status, err
		}
		return m.status, errors.New("connection_save_failed: restart recovery configuration could not be saved")
	}
	m.key, m.status, m.failures = record.Key, next, 0
	m.next = time.Now().Add(healthInterval())
	m.signalLocked()
	return next, nil
}

type connectionVerificationError struct {
	error
	retryAfter time.Duration
}

func (e *connectionVerificationError) retryAfterHeader() string {
	if e.retryAfter <= 0 {
		return ""
	}
	return strconv.FormatInt(int64((e.retryAfter-1)/time.Second)+1, 10)
}

type connectionObservation struct {
	state, code string
	delay       time.Duration
}

func healthInterval() time.Duration { return 90*time.Second + rand.N(30*time.Second) }

func managementFailure(resp *http.Response) connectionObservation {
	result := connectionObservation{state: "waiting", code: "management_network"}
	if resp == nil {
		return result
	}
	if wait := resp.Header.Get("Retry-After"); wait != "" {
		if sec, err := strconv.ParseUint(wait, 10, 32); err == nil {
			result.delay = time.Duration(sec) * time.Second
		} else if date, err := http.ParseTime(wait); err == nil {
			result.delay = time.Until(date)
		}
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		result.state, result.code = "invalid", "management_key_invalid"
	case http.StatusForbidden:
		result.state, result.code = "restricted", "management_restricted"
		result.delay = max(result.delay, time.Minute)
	case http.StatusTooManyRequests:
		result.state, result.code = "rate_limited", "management_rate_limited"
	default:
		result.code = "management_unavailable"
	}
	return result
}

// CPA reports a temporary IP restriction in a small JSON body. Peek at it
// without consuming the reply or taking the coordinator lock during I/O.
func managementBan(resp *http.Response) time.Duration {
	const limit = 2048
	if resp.Body == nil || resp.ContentLength > limit {
		return 0
	}
	reader := bufio.NewReaderSize(resp.Body, limit+1)
	resp.Body = struct {
		io.Reader
		io.Closer
	}{reader, resp.Body}
	raw, err := reader.Peek(limit + 1)
	if err != io.EOF {
		return 0
	}
	var denied struct {
		Error string `json:"error"`
	}
	if uniqueJSON(raw) != nil || json.Unmarshal(raw, &denied) != nil {
		return 0
	}
	text, found := strings.CutPrefix(denied.Error, "IP banned due to too many failed attempts. Try again in ")
	remaining, err := time.ParseDuration(text)
	if !found || err != nil || remaining <= 0 {
		return 0
	}
	return min(remaining, time.Duration(math.MaxInt64)-time.Second) + time.Second
}

func connectionError(st ConnectionStatus) error {
	code := st.Code
	if code == "" {
		code = "management_session_required"
	}
	return fmt.Errorf("%s: CPA background authorization is %s", code, st.State)
}

func (m *ManagementClient) probe(ctx context.Context, key string) connectionObservation {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	resp, err := m.send(ctx, key, http.MethodGet, readinessPath, nil, "")
	if err != nil {
		return managementFailure(nil)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		seen := managementFailure(resp)
		if resp.StatusCode == http.StatusForbidden {
			if delay := managementBan(resp); delay > 0 {
				seen.delay = max(seen.delay, delay)
				seen.code = "management_temporarily_banned"
			}
		}
		return seen
	}
	var counts struct {
		InFlight *int64 `json:"in_flight"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(data) > 65536 || uniqueJSON(data) != nil || json.Unmarshal(data, &counts) != nil || counts.InFlight == nil || *counts.InFlight < 0 {
		return connectionObservation{state: "waiting", code: "management_invalid_response"}
	}
	return connectionObservation{state: "ready"}
}

func (m *ManagementClient) observeBan(revision uint64, resp *http.Response) {
	delay := managementBan(resp)
	if delay <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Revision == revision && m.status.State == "restricted" {
		m.mergeBackoffLocked(connectionObservation{state: "restricted", code: "management_temporarily_banned", delay: delay})
	}
}

func (m *ManagementClient) observeFailure(revision uint64, resp *http.Response) {
	if resp != nil && resp.StatusCode != 401 && resp.StatusCode != 403 && resp.StatusCode != 429 && resp.StatusCode < 500 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Revision != revision || m.status.State == "invalid" || m.status.State == "closing" || m.key == "" {
		return
	}
	o := managementFailure(resp)
	if m.status.State != "ready" && o.state != "invalid" {
		m.mergeBackoffLocked(o)
		return
	}
	m.applyLocked(o)
	m.signalLocked()
}

// Overlapping observations for one revision retain the most specific
// restriction and the latest server deadline, regardless of arrival order.
func (m *ManagementClient) mergeBackoffLocked(o connectionObservation) {
	switch o.state {
	case "restricted":
		if m.status.Code != "management_temporarily_banned" {
			m.status.State, m.status.Code = o.state, o.code
		}
	case "rate_limited":
		if m.status.State != "restricted" {
			m.status.State, m.status.Code = o.state, o.code
		}
	}
	if proposed := time.Now().Add(o.delay); o.delay > 0 && proposed.After(m.next) {
		m.next = proposed
		m.signalLocked()
	}
}

func (m *ManagementClient) applyLocked(o connectionObservation) {
	m.status.State, m.status.Code = o.state, o.code
	switch o.state {
	case "ready":
		m.failures = 0
		m.next = time.Now().Add(healthInterval())
	case "invalid":
		m.key = ""
		// An earlier candidate probe cannot reinstate a credential after
		// the current configuration has received an authoritative denial.
		m.status.Revision = nextConnectionRevision(m.status.Revision)
		m.failures = 0
		if m.files != "" {
			m.persistInvalidationLocked()
		}
	default:
		m.retryLocked(o.delay)
	}
}

func (m *ManagementClient) retryLocked(minimum time.Duration) {
	delay := time.Second << min(m.failures, 6)
	m.failures++
	m.next = time.Now().Add(max(delay+rand.N(delay/2), minimum))
}

// Saving the same keyless marker is idempotent, even if a previous rename
// succeeded but its directory sync failed. Only the denied revision may write it.
func (m *ManagementClient) persistInvalidationLocked() {
	record := connectionRecord{Version: 1, Revision: m.status.Revision, State: "invalid", Base: m.base}
	if err := m.files.saveRecord(record); err != nil {
		m.status.Code = "connection_invalidation_save_failed"
		m.retryLocked(0)
		return
	}
	m.status.Code = "management_key_invalid"
	m.next = time.Time{}
}

func (m *ManagementClient) monitor() {
	defer m.wg.Done()
	for {
		m.mu.RLock()
		key, st, next := m.key, m.status, m.next
		m.mu.RUnlock()
		if m.ctx.Err() != nil {
			return
		}
		repair := st.invalidationPending()
		if key == "" && !repair {
			select {
			case <-m.ctx.Done():
				return
			case <-m.wake:
				continue
			}
		}
		if delay := time.Until(next); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-m.ctx.Done():
				timer.Stop()
				return
			case <-m.wake:
				timer.Stop()
				continue
			case <-timer.C:
				continue
			}
		}
		if repair {
			m.mu.Lock()
			if m.status.invalidationPending() && m.status.Revision == st.Revision && m.next.Equal(next) {
				m.persistInvalidationLocked()
			}
			m.mu.Unlock()
			continue
		}
		o := m.probe(m.ctx, key)
		m.mu.Lock()
		if m.key == key && m.status.Revision == st.Revision && m.status.State != "closing" {
			if m.next.Equal(next) || o.state == "invalid" {
				m.applyLocked(o)
			} else if o.state != "ready" {
				m.mergeBackoffLocked(o)
			}
		}
		m.mu.Unlock()
	}
}
