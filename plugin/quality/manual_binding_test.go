package quality

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestManualRequestChecksSelectedIdentityBeforeQueueing(t *testing.T) {
	file := scopedTarget("same-entry", "a")
	route := policyProvider()
	route.AuthID, route.AuthIndex, route.Provider = file.AuthID, file.AuthIndex, file.Provider
	en, host, _ := scopeFixture(t, file)
	cfg := RunConfig{AuthID: file.AuthID, Provider: file.Provider, Models: []string{"a"}, QuestionID: "logic-candy",
		ExpectedObjectType: objectProvider, ExpectedAuthIndex: file.AuthIndex, IdempotencyKey: "manual-selection"}
	post := func(status int) []byte {
		t.Helper()
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		response := en.Handle(t.Context(), pluginapi.ManagementRequest{Method: http.MethodPost, Path: "/v0/management/plugins/model-quality/runs", Body: raw})
		if response.StatusCode != status {
			t.Fatalf("wanted %d, got %d: %s", status, response.StatusCode, response.Body)
		}
		return response.Body
	}
	post(409)
	host.targets = []Target{route}
	cfg.ExpectedObjectType = objectOAuth
	post(409)
	host.targets = []Target{file}
	cfg.ExpectedAuthIndex = "old-index"
	post(409)
	if len(en.queue) != 0 || len(en.runs) != 0 || host.executeCalls != 0 {
		t.Fatal("mismatched selections must leave no queued work")
	}
	cfg.ExpectedAuthIndex = file.AuthIndex
	receipt := post(202)
	host.targets = []Target{route}
	if !bytes.Equal(receipt, post(202)) || len(en.queue) != 1 {
		t.Fatal("replaying a previously accepted request must return its original receipt")
	}
	cfg.ExpectedObjectType = objectProvider
	post(409)
	host.targets = []Target{file}
	cfg.ExpectedObjectType, cfg.ExpectedAuthIndex, cfg.IdempotencyKey = "", "", "older-manual-client"
	post(202)
}

func TestManualFileIsRecheckedBetweenAdmissionAndExecution(t *testing.T) {
	file := scopedTarget("same-entry", "a")
	route := policyProvider()
	route.AuthID, route.AuthIndex, route.Provider = file.AuthID, file.AuthIndex, file.Provider
	en, host, _ := scopeFixture(t, file)
	cfg := RunConfig{AuthID: file.AuthID, Provider: file.Provider, Models: []string{"a"}, QuestionID: "logic-candy"}
	run, err := en.Submit(t.Context(), cfg)
	if err != nil || run.Config.SubjectIndex != file.AuthIndex || run.Config.ExpectedObjectType != objectOAuth {
		t.Fatalf("manual run must freeze the observed file index: %v", err)
	}
	en.runs[run.ID].Status = "running"
	if err := en.checkExecution(t.Context(), run.ID, run.Config); err != nil {
		t.Fatalf("the original entry should remain eligible: %v", err)
	}
	changed := file
	changed.AuthIndex = "replacement"
	for _, current := range []Target{route, changed} {
		host.targets = []Target{current}
		_, err := en.evaluate(t.Context(), run.ID, run.Config, file.AuthID, file.Provider, "a", run.Source)
		if err == nil || host.executeCalls != 0 {
			t.Fatal("queued manual request reached a changed entry")
		}
	}
}
