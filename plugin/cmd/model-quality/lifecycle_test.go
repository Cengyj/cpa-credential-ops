package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cliproxyapi-plugins/model-quality/quality"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type testRuntimeEngine struct {
	closed atomic.Int32
}

func (*testRuntimeEngine) ReconfigureQuality(quality.Config) error { return nil }

func (e *testRuntimeEngine) Close() error {
	e.closed.Add(1)
	return nil
}
func (*testRuntimeEngine) Handle(context.Context, pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{StatusCode: 200}
}
func (*testRuntimeEngine) Usage(quality.UsageEvent) {}

func TestRuntimeShutdownCancelsAndDrainsLeasesBeforeClosing(t *testing.T) {
	var r engineRuntime
	engine := &testRuntimeEngine{}
	if err := r.replace(func() (runtimeEngine, error) { return engine, nil }); err != nil {
		t.Fatal(err)
	}
	instance, release, err := r.acquire()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.close() }()
	<-instance.ctx.Done()
	if _, _, err := r.acquire(); err == nil {
		t.Fatal("shutdown admitted a new callback")
	}
	if engine.closed.Load() != 0 {
		t.Fatal("store closed while an admitted callback was active")
	}
	select {
	case <-done:
		t.Fatal("shutdown did not drain admitted callback")
	default:
	}
	release()
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := r.close(); err != nil || engine.closed.Load() != 1 {
		t.Fatal("shutdown was not idempotent", err)
	}
}

func TestProviderConfigReloadKeepsInFlightEngine(t *testing.T) {
	var runtime engineRuntime
	settings := runtimeConfig{settings: quality.DefaultConfig(), managementURL: "http://127.0.0.1:18379"}
	first := &testRuntimeEngine{}
	if err := runtime.configure(settings, false, func() (runtimeEngine, error) { return first, nil }); err != nil {
		t.Fatal(err)
	}
	instance, release, err := runtime.acquire()
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.configure(settings, true, func() (runtimeEngine, error) { return nil, errors.New("unexpected restart") })
	release()
	if err != nil || instance.ctx.Err() != nil || first.closed.Load() != 0 {
		t.Fatal("provider config reload interrupted a running operation", err)
	}
	settings.settings.Workers++
	if err := runtime.configure(settings, true, func() (runtimeEngine, error) { return &testRuntimeEngine{}, nil }); err != nil || first.closed.Load() != 1 {
		t.Fatal("changed plugin settings were not applied", err)
	}
	if err := runtime.close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeReplacementWaitsForOldCallbacksAndUsesNewContext(t *testing.T) {
	var r engineRuntime
	old := &testRuntimeEngine{}
	newEngine := &testRuntimeEngine{}
	if err := r.replace(func() (runtimeEngine, error) { return old, nil }); err != nil {
		t.Fatal(err)
	}
	instance, release, err := r.acquire()
	if err != nil {
		t.Fatal(err)
	}
	created := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.replace(func() (runtimeEngine, error) {
			if old.closed.Load() != 1 {
				return nil, errors.New("old store remains open")
			}
			close(created)
			return newEngine, nil
		})
	}()
	<-instance.ctx.Done()
	select {
	case <-created:
		t.Fatal("replacement opened before old lease drained")
	default:
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	current, releaseNew, err := r.acquire()
	if err != nil {
		t.Fatal(err)
	}
	if current.engine != newEngine || current.ctx.Err() != nil || current == instance {
		t.Fatal("replacement reused stale instance")
	}
	releaseNew()
	if err := r.close(); err != nil {
		t.Fatal(err)
	}
}

func TestFailedRuntimeReplacementLeavesNoClosedInstancePublished(t *testing.T) {
	var r engineRuntime
	engine := &testRuntimeEngine{}
	if err := r.replace(func() (runtimeEngine, error) { return engine, nil }); err != nil {
		t.Fatal(err)
	}
	expected := errors.New("store unavailable")
	if err := r.replace(func() (runtimeEngine, error) { return nil, expected }); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	if _, _, err := r.acquire(); err == nil || engine.closed.Load() != 1 {
		t.Fatal("failed replacement retained old engine")
	}
}

func TestOrdinaryQualityReloadReusesBackgroundServices(t *testing.T) {
	var r engineRuntime
	settings := runtimeConfig{settings: quality.DefaultConfig()}
	app := &testRuntimeEngine{}
	starts := 0
	create := func() (runtimeEngine, error) { starts++; return app, nil }
	if err := r.configure(settings, false, create); err != nil {
		t.Fatal(err)
	}
	defer r.close()
	settings.settings.DailyTokens++
	if err := r.configure(settings, true, create); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || app.closed.Load() != 0 {
		t.Fatalf("changing budget recreated background services: starts=%d closes=%d", starts, app.closed.Load())
	}
	t.Logf("startup + budget change: starts=%d closes=%d", starts, app.closed.Load())
}
