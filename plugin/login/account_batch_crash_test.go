package login

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

func TestBatchAdmissionReserveProtectsInFlightCancellation(t *testing.T) {
	opts := offlineOptions(t)
	h := newFakeHost()
	opts.Call = h.call
	entered, gate := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(gate)
		}
	}()
	opts.Prepare = func(context.Context, string, string, string) error { close(entered); <-gate; return nil }
	s := offlineService(t, opts)
	const key = "reserve-active-write"
	payload := map[string]any{"source": conversionFixture(t), "indexes": []int{0}}
	if r := batchRequest(t, s, context.Background(), "/conversion/upload", key, payload); r.StatusCode != 202 {
		t.Fatal(r.StatusCode)
	}
	<-entered
	// Grow a private test file sparsely past the ordinary admission watermark.
	if err := os.Truncate(s.journal.db.Path(), journalMaxBytes-journalReserveBytes+1); err != nil {
		t.Fatal(err)
	}
	r := batchRequest(t, s, context.Background(), "/conversion/upload", "reserve-rejected-write", payload)
	if r.StatusCode != 507 || ojson.GetBytes(r.Body, "code").String() != "storage_capacity" {
		t.Fatal("admission consumed the maintenance reserve")
	}
	if r := lookupBatch(s, "reserve-rejected-write"); r.StatusCode != 404 || ojson.GetBytes(r.Body, "code").String() != "command_not_accepted" {
		t.Fatal("disk-capacity rejection left an accepted command")
	}
	r = batchRequest(t, s, context.Background(), "/batches/cancel", "reserve-cancel-write", map[string]string{"id": key})
	if r.StatusCode != 200 {
		t.Fatalf("could not cancel from maintenance reserve: %d", r.StatusCode)
	}
	released = true
	close(gate)
	waitBatch(t, s, key, "cancelled")
	if h.saves != 0 {
		t.Fatal("cancelled attempt crossed SDK save boundary")
	}
	t.Log("ordinary admission rejected at reserve; accepted cancellation and terminal receipt persisted; SDK saves=0")
}

func TestBatchReceiptAtCapacityAfterConfirmedSDKWrite(t *testing.T) {
	opts := offlineOptions(t)
	host := newFakeHost()
	var service *Service
	opts.Call = func(method string, in, out any) error {
		err := host.call(method, in, out)
		if method == "host.auth.save" && err == nil {
			return os.Truncate(service.journal.db.Path(), journalMaxBytes)
		}
		return err
	}
	service = offlineService(t, opts)
	const key = "batch-no-space-after-write"
	p := map[string]any{"source": conversionFixture(t), "indexes": []int{0}}
	r := batchRequest(t, service, context.Background(), "/conversion/upload", key, p)
	if r.StatusCode != 202 {
		t.Fatal(r.StatusCode)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		r = lookupBatch(service, key)
		if r.StatusCode == 409 && ojson.GetBytes(r.Body, "outcome_unknown").Bool() {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("in-flight storage failure did not stop acceptance")
		}
		time.Sleep(time.Millisecond)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	service = offlineService(t, opts)
	v := waitBatch(t, service, key, "completed")
	if host.saves != 1 || v.Completed != 1 || v.Items[0].Status != "uploaded" {
		t.Fatal("recovery did not consume the matching confirmed SDK receipt")
	}
	t.Log("final progress write exceeded maintenance watermark; before restart outcome_unknown=true; same-volume recovery matched the exact committed SDK receipt; SDK saves=1")
}

func TestBatchProcessKilledAfterFakeSDKWrite(t *testing.T) {
	const childState = "MODEL_QUALITY_TEST_ISOLATED_BATCH_PROCESS"
	const key = "killed-after-remote-save"
	payload := map[string]any{"source": conversionBatchFixture(t), "indexes": []int{0}}
	if root := os.Getenv(childState); root != "" {
		// This branch only runs in the disposable subprocess started below.
		h := newFakeHost()
		call := func(method string, in, out any) error {
			err := h.call(method, in, out)
			if method != "host.auth.save" || err != nil {
				return err
			}
			file, err := os.OpenFile(filepath.Join(root, "fake-sdk-writes"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = file.WriteString("save\n")
			err = errors.Join(err, file.Sync(), file.Close())
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(root, "fake-save-complete"), []byte("ready"), 0600); err != nil {
				return err
			}
			// The fake SDK effect is durable, while its return to the plugin is held.
			time.Sleep(20 * time.Second)
			return errors.New("test parent did not stop the process")
		}
		service, err := New(Options{Dir: filepath.Join(root, "accounts"), Call: call})
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		if r := batchRequest(t, service, context.Background(), "/conversion/upload", key, payload); r.StatusCode != 202 {
			t.Fatal(r.StatusCode)
		}
		waitBatch(t, service, key, "reconciliation_required")
		return
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBatchProcessKilledAfterFakeSDKWrite$", "-test.timeout=15s")
	process.Env = append(os.Environ(), childState+"="+root)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if process.ProcessState == nil {
			_ = process.Process.Kill()
			_ = process.Wait()
		}
	}()
	deadline := time.Now().Add(8 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(root, "fake-save-complete"))
		if string(data) == "ready" {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("child never reached the fake external write")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("expected abrupt process termination")
	}
	host := newFakeHost()
	s := offlineService(t, Options{Dir: filepath.Join(root, "accounts"), Call: host.call})
	first := waitBatch(t, s, key, "reconciliation_required")
	if len(first.Items) != 1 || first.Items[0].Status != "uncertain" {
		t.Fatal("abrupt exit lost the write barrier")
	}
	if r := batchRequest(t, s, context.Background(), "/conversion/upload", key, payload); r.StatusCode != 202 || ojson.GetBytes(r.Body, "status").String() != "reconciliation_required" {
		t.Fatal("same request did not return durable unknown outcome")
	}
	const anotherKey = "new-key-after-same-unknown-write"
	if r := batchRequest(t, s, context.Background(), "/conversion/upload", anotherKey, payload); r.StatusCode != 202 {
		t.Fatal(r.StatusCode)
	}
	waitBatch(t, s, anotherKey, "reconciliation_required")
	saves, err := os.ReadFile(filepath.Join(root, "fake-sdk-writes"))
	if err != nil || string(saves) != "save\n" || host.saves != 0 {
		t.Fatal("unknown external write count changed across restart")
	}
	t.Log("process killed after fake save, before SDK acknowledgement; same key and new key both require reconciliation; SDK saves before crash=1 after restart=0")
}
