package login

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func BenchmarkAccountListWithHistory(b *testing.B) {
	h := newFakeHost()
	s, err := New(Options{Dir: filepath.Join(b.TempDir(), "accounts"), Call: h.call})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	s.accountMu.Lock()
	s.mu.Lock()
	now := time.Now()
	for i := range 64 {
		id := fmt.Sprintf("a-%d", i)
		email := id + "@example.test"
		a := storedAccount{View: Account{ID: id, Revision: 1, LastJobStatus: "cancelled"}, EmailHash: s.journal.fingerprint("account-email", email), SecretRevision: 1, Credentials: Credentials{Email: email, Password: "fixture", TOTPSecret: "JBSWY3DPEHPK3PXP"}}
		for n := range 32 {
			job := historyJob(fmt.Sprintf("j-%d-%d", i, n), id, uint64(n+1), now)
			s.jobs[job.View.ID] = &session{record: job}
			a.View.LastJobID = job.View.ID
		}
		s.accounts[id] = a
	}
	s.mu.Unlock()
	s.accountMu.Unlock()
	b.ReportAllocs()
	for b.Loop() {
		if list, err := s.Accounts(); err != nil || len(list) != 64 {
			b.Fatalf("accounts %d, err %v", len(list), err)
		}
	}
}

func BenchmarkInspectionSettingsLock(b *testing.B) {
	host := newFakeHost()
	var armed atomic.Bool
	var lists atomic.Int64
	entered := make(chan struct{})
	service, err := New(Options{Dir: filepath.Join(b.TempDir(), "accounts"), Call: func(method string, in, out any) error {
		if method == "host.auth.list" {
			lists.Add(1)
			if armed.CompareAndSwap(true, false) {
				entered <- struct{}{}
				time.Sleep(15 * time.Millisecond)
			}
		}
		return host.call(method, in, out)
	}})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { service.Close() })
	account, err := service.ImportAccounts([]Credentials{{Email: "lock-fixture@example.test", Password: "offline", TOTPSecret: "JBSWY3DPEHPK3PXP"}})
	if err != nil {
		b.Fatal(err)
	}
	id := account[0].ID
	service.accountMu.Lock()
	entry := service.accounts[id]
	entry.View.AuthID = "missing-lock-fixture.json"
	err = service.saveAccountLocked(entry)
	service.accountMu.Unlock()
	if err != nil {
		b.Fatal(err)
	}
	var wait time.Duration
	finished := make(chan error, 1)
	for b.Loop() {
		armed.Store(true)
		go func() { _, err := service.InspectAccounts(id); finished <- err }()
		<-entered
		start := time.Now()
		_, err := service.AccountSettings()
		wait += time.Since(start)
		if err != nil {
			b.Fatal(err)
		}
		if err := <-finished; err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(wait.Nanoseconds())/float64(b.N), "settings-wait-ns/op")
	b.ReportMetric(float64(lists.Load())/float64(b.N), "sdk-lists/op")
}

func BenchmarkInspectionBatchThroughput(b *testing.B) {
	host := newFakeHost()
	s, err := New(Options{Dir: filepath.Join(b.TempDir(), "accounts"), Call: host.call})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	entries := make([]Credentials, 12)
	for i := range entries {
		entries[i] = Credentials{Email: fmt.Sprintf("inspection-%d@example.test", i), Password: "offline", TOTPSecret: "JBSWY3DPEHPK3PXP"}
	}
	if _, err := s.ImportAccounts(entries); err != nil {
		b.Fatal(err)
	}
	var n int
	for b.Loop() {
		n++
		r := s.Handle(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: RoutePrefix + "/accounts/inspect", Body: []byte(`{}`), Headers: http.Header{"Idempotency-Key": {fmt.Sprintf("fixture-inspect-%d", n)}}})
		if r.StatusCode != 200 {
			b.Fatal(r.StatusCode)
		}
	}
	b.ReportMetric(float64(len(entries)*b.N)/b.Elapsed().Seconds(), "accounts/s")
}
