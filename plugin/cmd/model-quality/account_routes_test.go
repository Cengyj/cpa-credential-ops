//go:build !credential_ops_host_oauth

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/cliproxyapi-plugins/model-quality/login"
	"github.com/cliproxyapi-plugins/model-quality/quality"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestBundledAccountRoutes(t *testing.T) {
	seen := make(map[string]bool)
	for _, route := range accountManagementRoutes() {
		seen[route.Method+" "+route.Path] = true
	}
	for _, path := range []string{"/login/accounts/import", "/login/commit"} {
		if !seen["POST /v0/management/plugins/model-quality"+path] {
			t.Errorf("missing account route %s", path)
		}
	}
}

func TestAccountReadsNeverEstablishBackendSession(t *testing.T) {
	const key = "offline-account-list-only"
	const accounts = login.RoutePrefix + "/accounts"
	headers := http.Header{"Authorization": {"Bearer " + key}}
	for _, tc := range []struct {
		name, method, path string
		headers            http.Header
		status             int
		session            bool
	}{
		{"account list", http.MethodGet, accounts, headers, 200, false},
		{"alternative header", http.MethodGet, accounts, http.Header{"X-Management-Key": {key}}, 200, false},
		{"missing header", http.MethodGet, accounts, nil, 200, false},
		{"malformed header", http.MethodGet, accounts, http.Header{"Authorization": {"Bearer bad\nvalue"}}, 200, false},
		{"account settings", http.MethodGet, accounts + "/settings", headers, 200, false},
		{"wrong path", http.MethodGet, accounts + "/", headers, 404, false},
		{"wrong method", http.MethodPost, accounts, headers, 405, false},
		{"public path", http.MethodGet, "/v0/resource/plugins/model-quality/login/accounts", headers, 404, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Error("account-list session establishment made a CPA management request")
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			t.Cleanup(cpa.Close)
			client, err := quality.NewManagementClient(cpa.URL)
			if err != nil {
				t.Fatal(err)
			}
			noCallback := func(method string, _, _ any) error {
				t.Errorf("unexpected host callback %s", method)
				return errors.New("unexpected offline callback")
			}
			native := &quality.NativeHost{Call: noCallback, Management: client}
			cfg := quality.DefaultConfig()
			cfg.DataDir = filepath.Join(t.TempDir(), "quality")
			engine, err := quality.New(cfg, native)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := engine.Close(); err != nil {
					t.Error(err)
				}
			})
			service, err := login.New(login.Options{Dir: filepath.Join(t.TempDir(), "accounts"), Call: noCallback})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := service.Close(); err != nil {
					t.Error(err)
				}
			})
			app := application{quality: engine, login: service}
			res := app.Handle(context.Background(), pluginapi.ManagementRequest{Method: tc.method, Path: tc.path, Headers: tc.headers})
			if res.StatusCode != tc.status || engine.Capabilities().ManagementSession != tc.session {
				t.Fatalf("status=%d session=%t", res.StatusCode, engine.Capabilities().ManagementSession)
			}
			if bytes.Contains(res.Body, []byte(key)) || tc.session && !bytes.Contains(res.Body, []byte(`"items":[]`)) {
				t.Fatal("unexpected account list response")
			}
		})
	}
}
