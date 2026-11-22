package quality

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func editConnection(t *testing.T, m *ManagementClient, method, key, mode string, revision uint64) (ConnectionStatus, error) {
	t.Helper()
	body := map[string]any{"revision": revision}
	if mode != "" {
		body["mode"] = mode
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return m.handleConnection(t.Context(), pluginapi.ManagementRequest{Path: "/v0/management/plugins/model-quality/connection", Method: method, Headers: http.Header{"Authorization": {"Bearer " + key}}, Body: raw})
}

func TestConnectionConfigureOnlyOnceAndDurableClear(t *testing.T) {
	const key = "private-offline-canary"
	var checks atomic.Int32
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks.Add(1)
		if r.Method != "GET" || r.URL.String() != readinessPath {
			t.Errorf("unexpected CPA route %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"in_flight":0,"by_provider":{}}`)
	}))
	defer cpa.Close()
	dir := t.TempDir()
	start := func(env string) *ManagementClient {
		m, err := NewManagementClient(cpa.URL)
		if err != nil {
			t.Fatal(err)
		}
		m.Start(dir, env)
		return m
	}
	m := start("")
	old := m.snapshot()
	if _, err := editConnection(t, m, "PUT", "wrong", "configure", old.Revision); err == nil || m.ready() {
		t.Fatal("invalid candidate installed")
	}
	var success, conflicts atomic.Int32
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			if _, err := editConnection(t, m, "PUT", key, "migrate", old.Revision); err == nil {
				success.Add(1)
			} else if strings.HasPrefix(err.Error(), "conflict:") {
				conflicts.Add(1)
			}
		})
	}
	group.Wait()
	if checks.Load() != 2 || success.Load() != 1 || conflicts.Load() != 11 {
		t.Fatal(checks.Load(), success.Load(), conflicts.Load())
	}
	for range 30 {
		st, err := m.handleConnection(t.Context(), pluginapi.ManagementRequest{Method: "GET"})
		if err != nil || st.State != "ready" || st.CanMigrate || !st.Configured {
			t.Fatal(st, err)
		}
		encoded, _ := json.Marshal(st)
		if bytes.Contains(encoded, []byte(key)) {
			t.Fatal("key in status")
		}
	}
	for path, mode := range map[string]os.FileMode{"management": 0700, "management/connection.json": 0600} {
		info, err := os.Stat(filepath.Join(dir, path))
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal(path, err)
		}
	}
	rev := m.snapshot().Revision
	m.Close()
	m = start("unused-deployment-secret")
	defer m.Close()
	for until := time.Now().Add(3 * time.Second); !m.ready() && time.Now().Before(until); {
		time.Sleep(time.Millisecond)
	}
	if st := m.snapshot(); st.State != "ready" || st.Source != "saved" || st.Revision != rev || checks.Load() != 3 {
		t.Fatal(st, checks.Load())
	}
	if _, err := editConnection(t, m, "DELETE", key, "", rev); err != nil {
		t.Fatal(err)
	}
	if _, err := editConnection(t, m, "PUT", key, "migrate", rev); err == nil {
		t.Fatal("old page reinstated cleared configuration")
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "management", "connection.json")); err != nil || bytes.Contains(raw, []byte(key)) {
		t.Fatal("clear marker retained key")
	}
	m.Close()
	cleared := start("unused-deployment-secret")
	defer cleared.Close()
	if s := cleared.snapshot(); s.State != "unconfigured" || s.CanMigrate || checks.Load() != 3 {
		t.Fatal(s)
	}
	t.Logf("12 concurrent migrations: installed=%d conflicts=%d; 30 reads: 0 probes; total CPA checks=%d (bad candidate, migration, restart)", success.Load(), conflicts.Load(), checks.Load())
}

func TestEscapedManagementKeySurvivesRestart(t *testing.T) {
	key := strings.Repeat(`\`, 8192)
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"in_flight":0}`)
	}))
	defer cpa.Close()
	dir := t.TempDir()
	client, err := NewManagementClient(cpa.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.Start(dir, "")
	_, err = editConnection(t, client, "PUT", key, "configure", client.snapshot().Revision)
	client.Close()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := NewManagementClient(cpa.URL)
	if err != nil {
		t.Fatal(err)
	}
	restored.Start(dir, "")
	defer restored.Close()
	if state := restored.snapshot(); !state.Configured || state.Source != "saved" {
		t.Fatalf("restart rejected a confirmed configuration: %+v", state)
	}
}

type connectionTransport func(*http.Request) (*http.Response, error)

func (f connectionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConnectionCoordinatorRetryAndShutdown(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		m, _ := NewManagementClient("")
		var calls atomic.Int32
		m.client.Transport = connectionTransport(func(r *http.Request) (*http.Response, error) {
			status := 200
			headers := http.Header{}
			switch calls.Add(1) {
			case 1:
				<-r.Context().Done()
				return nil, r.Context().Err()
			case 2:
				status = 503
			case 3:
				status = 429
				headers.Set("Retry-After", "18")
			case 4:
				status = 403
			}
			return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(`{"in_flight":0}`))}, nil
		})
		m.Start(dir, "test-only-key")
		defer m.Close()
		synctest.Wait()
		if m.ready() || calls.Load() != 1 {
			t.Fatal("initialization did not return during slow probe")
		}
		for range 8 {
			m.Start(dir, "ignored")
		}
		for range 4 {
			time.Sleep(4 * time.Second)
			synctest.Wait()
			if m.snapshot().State == "rate_limited" {
				break
			}
		}
		if m.snapshot().State != "rate_limited" || calls.Load() != 3 {
			t.Fatal(m.snapshot(), calls.Load())
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if calls.Load() != 3 {
			t.Fatal("ignored retry-after")
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if s := m.snapshot(); s.State != "restricted" || calls.Load() != 4 {
			t.Fatal(s, calls.Load())
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if !m.ready() || calls.Load() != 5 {
			t.Fatal(m.snapshot(), calls.Load())
		}
		m.Close()
		time.Sleep(10 * time.Minute)
		if calls.Load() != 5 {
			t.Fatal("work after close")
		}
		t.Log("one coordinator; five probes across timeout, 503, 429, 403 and success; zero after close")
	})
}

func TestConnectionStale401AndUpstream401(t *testing.T) {
	const key = "fake-current"
	oldSent, allowOld := make(chan struct{}), make(chan struct{})
	var probes, writes atomic.Int32
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case readinessPath:
			probes.Add(1)
			fmt.Fprint(w, `{"in_flight":0}`)
		case "/slow":
			close(oldSent)
			<-allowOld
			w.WriteHeader(401)
		case "/v8/management/requests/api-call":
			writes.Add(1)
			fmt.Fprint(w, `{"status_code":401,"body":"upstream rejected"}`)
		default:
			w.WriteHeader(401)
		}
	}))
	defer cpa.Close()
	dir := t.TempDir()
	m, _ := NewManagementClient(cpa.URL)
	m.Start(dir, "")
	defer m.Close()
	first, err := editConnection(t, m, "PUT", key, "configure", m.snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = m.request(context.Background(), "GET", "/slow", nil, nil, 1024) }()
	<-oldSent
	// Reusing the same accepted key still creates a new generation.
	next, err := editConnection(t, m, "PUT", key, "configure", first.Revision)
	close(allowOld)
	<-done
	if err != nil || !m.ready() || next.Revision == first.Revision {
		t.Fatal(next, err)
	}
	var result managementProbeResponse
	if err = m.request(t.Context(), "POST", "/v8/management/requests/api-call", nil, &result, 1024); err != nil || result.StatusCode != 401 || !m.ready() {
		t.Fatal(err, m.snapshot())
	}
	if err = m.request(t.Context(), "GET", "/forbidden-key", nil, nil, 1024); err == nil || m.snapshot().State != "invalid" {
		t.Fatal(err, m.snapshot())
	}
	m.Close()
	restart, _ := NewManagementClient(cpa.URL)
	restart.Start(dir, key)
	defer restart.Close()
	if st := restart.snapshot(); st.State != "invalid" || probes.Load() != 2 || writes.Load() != 1 {
		t.Fatal(st, probes.Load(), writes.Load())
	}
}

func TestPendingConfigurationCannotUndoConfirmedInvalidation(t *testing.T) {
	verifying, resume := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != readinessPath {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if probes.Add(1) == 2 {
			close(verifying)
			select {
			case <-resume:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, `{"in_flight":0}`)
	}))
	defer cpa.Close()
	const key = "fake-rotating"
	m, _ := NewManagementClient(cpa.URL)
	m.Start(t.TempDir(), "")
	defer m.Close()
	first, err := editConnection(t, m, "PUT", key, "configure", m.snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := editConnection(t, m, "PUT", key, "configure", first.Revision)
		done <- err
	}()
	<-verifying
	denial := m.request(t.Context(), "GET", "/rotated", nil, nil, 1024)
	close(resume)
	result := <-done
	if denial == nil || result == nil || !strings.HasPrefix(result.Error(), "conflict:") || m.snapshot().State != "invalid" {
		t.Fatalf("current denial=%v, pending verification=%v, state=%s", denial, result, m.snapshot().State)
	}
}

func TestCurrentHealthDenialDuringConcurrentBackoff(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		m, _ := NewManagementClient("")
		resume := make(chan struct{})
		var probes atomic.Int32
		m.client.Transport = connectionTransport(func(r *http.Request) (*http.Response, error) {
			response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"in_flight":0}`))}
			if r.URL.Path != readinessPath {
				response.StatusCode = 429
				response.Header.Set("Retry-After", "3600")
			} else if probes.Add(1) == 2 {
				<-resume
				response.StatusCode = 401
			}
			return response, nil
		})
		m.Start(dir, "fake-current")
		defer m.Close()
		synctest.Wait()
		m.mu.RLock()
		due := m.next
		m.mu.RUnlock()
		time.Sleep(time.Until(due))
		synctest.Wait()
		err := m.request(t.Context(), "GET", "/limited", nil, nil, 1024)
		close(resume)
		synctest.Wait()
		if err == nil || probes.Load() != 2 || m.snapshot().State != "invalid" {
			t.Fatalf("business result=%v, probes=%d, state=%s", err, probes.Load(), m.snapshot().State)
		}
	})
}

func TestConnectionCorruptionAndWriteFailure(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "management")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(private, "connection.json")
	if err := os.WriteFile(record, []byte(`{"version":2,"key":"future-canary"}`), 0600); err != nil {
		t.Fatal(err)
	}
	m, _ := NewManagementClient("")
	m.Start(dir, "other")
	defer m.Close()
	if s := m.snapshot(); s.State != "error" || s.CanMigrate || s.Code != "connection_store_invalid" {
		t.Fatal(s)
	}
	if err := os.Chmod(private, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := editConnection(t, m, "DELETE", "", "", m.snapshot().Revision); err == nil || !strings.HasPrefix(err.Error(), "connection_save_failed:") {
		t.Fatal(err)
	}
	if encoded, _ := json.Marshal(m.snapshot()); bytes.Contains(encoded, []byte("future-canary")) {
		t.Fatal("diagnostic revealed stored material")
	}
}

func TestConcurrentHealthAndBusinessRetryWindows(t *testing.T) {
	for _, tt := range []struct {
		name, businessRetry, healthRetry, state, code string
		businessStatus, healthStatus                  int
		window                                        time.Duration
	}{
		{"health rate limit after business outage", "", "300", "rate_limited", "management_rate_limited", 503, 429, 5 * time.Minute},
		{"health outage extends existing limit", "60", "300", "rate_limited", "management_rate_limited", 429, 503, 5 * time.Minute},
		{"health restriction after business limit", "180", "", "restricted", "management_temporarily_banned", 429, 403, 11 * time.Minute},
		{"late success preserves business limit", "180", "", "rate_limited", "management_rate_limited", 429, 200, 3 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			synctest.Test(t, func(t *testing.T) {
				m, _ := NewManagementClient("")
				resume := make(chan struct{})
				var probes atomic.Int32
				m.client.Transport = connectionTransport(func(r *http.Request) (*http.Response, error) {
					status, retry := tt.businessStatus, tt.businessRetry
					body := `{"in_flight":0}`
					if r.URL.Path == readinessPath {
						status, retry = 200, ""
						if probes.Add(1) == 2 {
							select {
							case <-resume:
							case <-r.Context().Done():
								return nil, r.Context().Err()
							}
							status, retry = tt.healthStatus, tt.healthRetry
							if status == 403 {
								body = `{"error":"IP banned due to too many failed attempts. Try again in 11m0s"}`
							}
						}
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {retry}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				m.Start(dir, "offline-test")
				defer m.Close()
				synctest.Wait()
				m.mu.RLock()
				due := m.next
				m.mu.RUnlock()
				time.Sleep(time.Until(due))
				synctest.Wait()
				if probes.Load() != 2 || m.request(t.Context(), "GET", "/business", nil, nil, 1024) == nil {
					t.Fatal("expected an overlapping health check and failed business request")
				}
				close(resume)
				synctest.Wait()
				st := m.snapshot()
				if st.State != tt.state || st.Code != tt.code || st.NextAttemptAt == nil || time.Until(*st.NextAttemptAt) < tt.window {
					t.Fatalf("overlapping responses lost restriction: %s/%s, next %v", st.State, st.Code, st.NextAttemptAt)
				}
				time.Sleep(time.Until(*st.NextAttemptAt) - time.Millisecond)
				synctest.Wait()
				if probes.Load() != 2 {
					t.Fatal("contacted CPA before its retry window elapsed")
				}
				time.Sleep(time.Millisecond)
				synctest.Wait()
				if probes.Load() != 3 || !m.ready() {
					t.Fatal("single coordinator did not recover at the agreed deadline")
				}
			})
		})
	}
}

func TestConnectionInvalidationDiskRepair(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(fmt.Sprintf("rotate=%v", rotate), func(t *testing.T) {
			dir := t.TempDir()
			synctest.Test(t, func(t *testing.T) {
				m, _ := NewManagementClient("")
				var probes atomic.Int32
				m.client.Transport = connectionTransport(func(r *http.Request) (*http.Response, error) {
					probes.Add(1)
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"in_flight":0}`))}, nil
				})
				m.Start(dir, "")
				defer m.Close()
				st, err := editConnection(t, m, "PUT", "offline-old", "configure", m.snapshot().Revision)
				if err != nil {
					t.Fatal(err)
				}
				private := filepath.Join(dir, "management")
				if err := os.Chmod(private, 0755); err != nil {
					t.Fatal(err)
				}
				m.observeFailure(st.Revision, &http.Response{StatusCode: 401})
				denied := m.snapshot()
				if denied.Code != "connection_invalidation_save_failed" || denied.NextAttemptAt == nil || m.ready() {
					t.Fatalf("denial must suspend use and schedule local marker repair: %+v", denied)
				}
				time.Sleep(2 * time.Second)
				synctest.Wait()
				if st := m.snapshot(); st.Code != denied.Code || st.NextAttemptAt == nil || !st.NextAttemptAt.After(*denied.NextAttemptAt) {
					t.Fatalf("disk still unavailable, expected a later local retry: %+v", st)
				}
				if err := os.Chmod(private, 0700); err != nil {
					t.Fatal(err)
				}
				expectedState, expectedRevision, expectedProbes := "invalid", denied.Revision, int32(1)
				if rotate {
					next, err := editConnection(t, m, "PUT", "offline-new", "configure", denied.Revision)
					if err != nil {
						t.Fatal(err)
					}
					expectedState, expectedRevision, expectedProbes = "saved", next.Revision, 2
				}
				time.Sleep(10 * time.Second)
				synctest.Wait()
				record, err := m.files.readInstalled()
				if err != nil || record == nil || record.State != expectedState || record.Revision != expectedRevision ||
					record.State == "invalid" && record.Key != "" || probes.Load() != expectedProbes {
					t.Fatal("local marker repair lost the revision, retained denied material, or made an external call")
				}
				if st := m.snapshot(); st.NextAttemptAt != nil || !rotate && st.Code != "management_key_invalid" {
					t.Fatal(st)
				}
			})
		})
	}
}

func TestBadEndpointIsolatedFromSDKAndReportsSafeError(t *testing.T) {
	m := RuntimeManagementClient("http://user:private-canary@outside.example/")
	m.Start(t.TempDir(), "unused-secret")
	defer m.Close()
	st := m.snapshot()
	raw, _ := json.Marshal(st)
	if st.State != "error" || st.Code != "connection_endpoint_invalid" || bytes.Contains(raw, []byte("canary")) || m.client != nil {
		t.Fatal(st)
	}
	if _, err := editConnection(t, m, "PUT", "fake", "configure", st.Revision); err == nil {
		t.Fatal("accepted invalid endpoint")
	}
	if _, err := m.response(t.Context(), "POST", "/ignored", nil); err == nil {
		t.Fatal("accepted business request")
	}
}

func TestQualityReloadKeepsPanelBudgetAndTimerGrant(t *testing.T) {
	en, _, rule := scopeFixture(t, scopedTarget("one", "a", "b"))
	input := en.cfg
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	armed, err := en.setRuleTimer(t.Context(), rule.ID, rule.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	panel := en.Config()
	panel.DailyTokens = 4200
	if err := en.UpdateConfig(panel); err != nil {
		t.Fatal(err)
	}
	input.RetentionDays++
	if err := en.ReconfigureQuality(input); err != nil {
		t.Fatal(err)
	}
	if current := en.Config(); current.DailyTokens != 4200 || current.RetentionDays != input.RetentionDays || !en.timerAllowed(armed) || current.SchedulingEnabled {
		t.Fatal("live reload changed unrelated quality permissions")
	}
	en.cfg = input
	if err := en.load(); err != nil {
		t.Fatal(err)
	}
	if current := en.Config(); current.DailyTokens != 4200 || current.RetentionDays != input.RetentionDays || !en.timerAllowed(en.rules[rule.ID]) {
		t.Fatal("reopen lost merged config")
	}
}

func TestConfigurationBanReturnsSafeRetryAfterAndNoSavedCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"IP banned due to too many failed attempts. Try again in 18m0s"}`)
	}))
	defer server.Close()
	m, _ := NewManagementClient(server.URL)
	dir := t.TempDir()
	m.Start(dir, "")
	defer m.Close()
	en := &Engine{host: &NativeHost{Management: m}}
	req := pluginapi.ManagementRequest{Method: "PUT", Path: "/v0/management/plugins/model-quality/connection", Headers: http.Header{"Authorization": {"Bearer fake-banned-canary"}}, Body: []byte(fmt.Sprintf(`{"revision":%d,"mode":"configure"}`, m.snapshot().Revision))}
	response := en.Handle(t.Context(), req)
	if response.StatusCode != 503 || response.Headers.Get("Retry-After") != "1081" || !bytes.Contains(response.Body, []byte("management_temporarily_banned")) || bytes.Contains(response.Body, []byte("fake-banned-canary")) {
		t.Fatal(response.StatusCode, response.Headers, string(response.Body))
	}
	if st := m.snapshot(); st.Configured || !st.CanMigrate || st.State != "unconfigured" {
		t.Fatal(st)
	}
	if _, err := os.Stat(filepath.Join(dir, "management")); !os.IsNotExist(err) {
		t.Fatal("rejected candidate persisted")
	}
}

func TestConnectionBackoffReportsCurrentCauseAndInvalidResponse(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		m, _ := NewManagementClient("")
		var calls atomic.Int32
		m.client.Transport = connectionTransport(func(r *http.Request) (*http.Response, error) {
			body := `{"in_flight":0}`
			if calls.Add(1) == 1 {
				body = `{"unexpected":0}`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		m.Start(dir, "offline")
		defer m.Close()
		synctest.Wait()
		rev := m.snapshot().Revision
		check := func(code string, after time.Duration) {
			t.Helper()
			st := m.snapshot()
			raw, _ := json.Marshal(st)
			var state struct {
				Next *time.Time `json:"next_attempt_at"`
			}
			_ = json.Unmarshal(raw, &state)
			if st.Code != code || state.Next == nil || time.Until(*state.Next) < after {
				t.Errorf("expected %s and next attempt after %s, got %s", code, after, raw)
			}
		}
		check("management_invalid_response", time.Second)
		headers := http.Header{"Retry-After": {"90"}}
		m.observeFailure(rev, &http.Response{StatusCode: 429, Header: headers})
		check("management_rate_limited", 90*time.Second)
		// A previously in-flight 503 must not shorten the host's limit.
		m.observeFailure(rev, &http.Response{StatusCode: 503})
		check("management_rate_limited", 90*time.Second)
		time.Sleep(91 * time.Second)
		synctest.Wait()
		if calls.Load() != 2 || !m.ready() {
			t.Fatal(m.snapshot(), calls.Load())
		}
	})
}

func TestConnectionCloseCancelsActiveBusinessTransport(t *testing.T) {
	m, _ := NewManagementClient("")
	waiting := make(chan struct{})
	m.client.Transport = connectionTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == readinessPath {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"in_flight":0}`))}, nil
		}
		close(waiting)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	m.Start(t.TempDir(), "")
	if _, err := editConnection(t, m, "PUT", "fake", "configure", m.snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.request(ctx, "GET", "/slow", nil, nil, 128) }()
	<-waiting
	m.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled read succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Close left background HTTP request running")
	}
}
