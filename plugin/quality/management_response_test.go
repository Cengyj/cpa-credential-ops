package quality

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestManagementDenialAtCallerCancellation(t *testing.T) {
	for _, reply := range []bool{true, false} {
		name := "no response"
		if reply {
			name = "received outer 401"
		}
		t.Run(name, func(t *testing.T) {
			m, _ := NewManagementClient("")
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m.client.Transport = connectionTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == readinessPath {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"in_flight":0}`))}, nil
				}
				cancel()
				if !reply {
					return nil, r.Context().Err()
				}
				return &http.Response{StatusCode: 401, Body: http.NoBody}, nil
			})
			m.Start(dir, "")
			defer m.Close()
			configured, err := editConnection(t, m, "PUT", "offline-key", "configure", m.snapshot().Revision)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.request(ctx, "GET", "/business", nil, nil, 1024); err == nil {
				t.Fatal("cancelled or rejected request succeeded")
			}
			st := m.snapshot()
			record, err := m.files.readInstalled()
			if err != nil || record == nil {
				t.Fatal("could not read the configuration")
			}
			if reply {
				if st.State != "invalid" || record.State != "invalid" || record.Key != "" || configured.Revision == st.Revision {
					t.Fatalf("current denial was ignored: %s", st.State)
				}
			} else if st.State != "ready" || st.Revision != configured.Revision || record.State != "saved" {
				t.Fatalf("caller cancellation without a reply changed authorization: %s", st.State)
			}
		})
	}
}

func TestBusinessManagementBanPreservesBodyAndDeadline(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		const denial = `{"error":"IP banned due to too many failed attempts. Try again in 11m0s"}`
		var probes atomic.Int32
		m, _ := NewManagementClient("")
		m.client.Transport = connectionTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == readinessPath {
				probes.Add(1)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"in_flight":0}`))}, nil
			}
			return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(denial))}, nil
		})
		m.Start(dir, "offline-key")
		defer m.Close()
		synctest.Wait()
		resp, err := m.response(t.Context(), "GET", "/business", nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(body) != denial {
			t.Fatal("the original management reply could not be read")
		}
		st := m.snapshot()
		if st.Code != "management_temporarily_banned" || st.NextAttemptAt == nil || time.Until(*st.NextAttemptAt) < 11*time.Minute {
			t.Fatalf("business restriction lost its retry window: %s", st.Code)
		}
		time.Sleep(11 * time.Minute)
		synctest.Wait()
		if probes.Load() != 1 {
			t.Fatal("contacted host during its restriction")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if probes.Load() != 2 || !m.ready() {
			t.Fatal("background authorization did not recover")
		}
	})
}

func TestSlowBusinessDenialCannotChangeRotatedAuthorization(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	m, _ := NewManagementClient("")
	m.client.Transport = connectionTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == readinessPath {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"in_flight":0}`))}, nil
		}
		return &http.Response{StatusCode: 403, Body: reader}, nil
	})
	m.Start(t.TempDir(), "")
	defer m.Close()
	// Unblock the transport even if the assertion before rotation fails.
	defer reader.Close()
	first, err := editConnection(t, m, "PUT", "offline-old", "configure", m.snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- m.request(t.Context(), "GET", "/business", nil, nil, 1024) }()
	// Pipe writes complete only after the reader received the prefix.
	if _, err := io.WriteString(writer, `{"error":"IP banned due to too many failed attempts. `); err != nil {
		t.Fatal(err)
	}
	if st := m.snapshot(); st.State != "restricted" {
		t.Fatalf("the response headers did not stop admission during the slow body: %s", st.State)
	}
	next, err := editConnection(t, m, "PUT", "offline-new", "configure", first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, `Try again in 11m0s"}`); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if <-done == nil {
		t.Fatal("old request unexpectedly succeeded")
	}
	if st := m.snapshot(); st.State != "ready" || st.Revision != next.Revision {
		t.Fatal("late failure body altered the verified replacement")
	}
}
