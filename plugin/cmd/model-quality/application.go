//go:build !credential_ops_host_oauth

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/cliproxyapi-plugins/model-quality/login"
	"github.com/cliproxyapi-plugins/model-quality/loginbundle"
	"github.com/cliproxyapi-plugins/model-quality/quality"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// application owns all plugin services; the official CPA only supplies public callbacks.
type application struct {
	quality *quality.Engine
	login   *login.Service
}

func accountManagementRoutes() []quality.ManagementRoute {
	var routes []quality.ManagementRoute
	for _, route := range login.ManagementRoutes() {
		routes = append(routes, quality.ManagementRoute{Method: route["method"], Path: route["path"]})
	}
	return routes
}

func newApplication(cfg quality.Config, native *quality.NativeHost) (runtimeEngine, error) {
	directory, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	cfg.DataDir = directory
	engine, err := quality.New(cfg, native)
	if err != nil {
		return nil, err
	}
	manifest, err := loginbundle.Ensure(directory)
	if err != nil {
		return nil, errors.Join(err, engine.Close())
	}
	service, err := login.New(login.Options{
		Dir: filepath.Join(directory, "accounts"), Call: callback, RuntimeManifest: manifest,
		FinishWrite: engine.FinishCredentialLogin, PendingWrites: engine.PendingCredentialLogins,
		Prepare: func(ctx context.Context, authID, identity, commitID string) error {
			if authID == "" {
				return nil
			}
			_, err := engine.PrepareCredentialLogin(ctx, quality.CredentialLoginPrepareRequest{
				AuthID: authID, ExpectedIdentity: identity, PreparationID: commitID,
			})
			return err
		},
	})
	if err != nil {
		return nil, errors.Join(err, engine.Close())
	}
	return &application{quality: engine, login: service}, nil
}

func (a *application) ReconfigureQuality(input quality.Config) error {
	return a.quality.ReconfigureQuality(input)
}

func (a *application) Close() error {
	return errors.Join(a.login.Close(), a.quality.Close())
}

func (a *application) Handle(ctx context.Context, request pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	if strings.HasPrefix(request.Path, login.RoutePrefix+"/") {
		return a.login.Handle(ctx, request)
	}
	return a.quality.Handle(ctx, request)
}

func (a *application) Usage(event quality.UsageEvent) { a.quality.Usage(event) }
