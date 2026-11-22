package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (en *Engine) Handle(ctx context.Context, req pluginapi.ManagementRequest) pluginapi.ManagementResponse {

	path := strings.TrimPrefix(req.Path, "/v0/management/plugins/model-quality")
	var result any
	var err error
	status := 200
	headers := http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}}
	switch req.Method + " " + path {
	case "GET /connection", "PUT /connection", "DELETE /connection":
		native, ok := en.host.(*NativeHost)
		if !ok || native.Management == nil {
			err = fmt.Errorf("unavailable: backend management adapter unavailable")
		} else {
			result, err = native.Management.handleConnection(ctx, req)
		}
	case "GET /targets":
		result, err = en.targetPage(ctx, req)
	case "GET /questions":
		items := Questions()
		for i := range items {
			items[i].Answer = ""
		}
		result = page(items, req)
	case "GET /rules/question-preview":
		// Only fixed rule questions have a reference that exists before dispatch.
		for _, q := range Questions() {
			if q.ID == req.Query.Get("id") && q.Kind == "logic" {
				result = q
				break
			}
		}
		if result == nil {
			err = fmt.Errorf("not_found: fixed question reference")
		}
	case "GET /runs":
		result, err = en.QueryRuns(req.Query)
	case "GET /runs/detail":
		if key := req.Query.Get("idempotency_key"); key != "" && req.Query.Get("id") == "" {
			result, err = en.runByReceipt(key)
		} else {
			result, err = en.Run(req.Query.Get("id"))
		}
	case "DELETE /rules/run-history":
		result, err = en.clearRuleHistory()
	case "DELETE /runs/history":
		if len(req.Query) != 0 || len(req.Body) != 0 {
			err = fmt.Errorf("invalid_request: clearing all manual tests does not take parameters")
		} else {
			result, err = en.clearManualHistory()
		}
	case "DELETE /runs":
		var body struct {
			ID string `json:"id"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			err = en.DeleteManualRun(body.ID)
		}
	case "POST /runs":
		var c RunConfig
		if err = DecodeStrict(req.Body, &c); err == nil {
			var run *Run
			run, err = en.Submit(ctx, c)
			if run != nil {
				result = map[string]string{"run_id": run.ID}
				status = 202
			}
		}
	case "POST /runs/cancel":
		var body struct {
			ID string `json:"id"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			err = en.Cancel(body.ID)
		}
	case "POST /runs/review":
		var body struct {
			ID          string `json:"id"`
			SampleIndex *int   `json:"sample_index"`
			Verdict     string `json:"verdict"`
			Note        string `json:"note"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			if body.SampleIndex == nil {
				err = fmt.Errorf("invalid_request: sample_index required")
			} else {
				err = en.Review(body.ID, *body.SampleIndex, body.Verdict, body.Note)
			}
		}
	case "POST /rules/scope-preview":
		var body ruleWrite
		if body, err = decodeRuleWrite(req.Body, true); err == nil {
			result, err = en.previewRuleScope(ctx, body.Rule)
		}
	case "GET /rules/commands":
		result, err = en.ruleCommandStatus(req.Query.Get("idempotency_key"))
	case "GET /rules":
		result = en.queryRules(req.Query)
	case "POST /rules/handling/acknowledge":
		var body struct {
			RuleID   string `json:"rule_id"`
			AuthID   string `json:"auth_id"`
			ActionID string `json:"action_id"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			err = en.acknowledgeHandling(ctx, body.RuleID, body.AuthID, body.ActionID)
		}
	case "POST /rules", "PUT /rules":
		update := req.Method == "PUT"
		var body ruleWrite
		if body, err = decodeRuleWrite(req.Body, update); err == nil {
			var r *Rule
			if r, err = en.saveRule(ctx, body.Rule, update, body.Activation); err == nil {
				result = viewRule(*r)
			}
		}
	case "POST /rules/run":
		var body struct {
			Key      string `json:"idempotency_key"`
			ID       string `json:"id"`
			Revision uint64 `json:"revision"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			if body.Key != "" {
				result, err = en.acceptRuleRun(ctx, body.Key, body.ID, body.Revision)
				status = 202
				break
			}
			var receipt ruleDispatch
			receipt, err = en.dispatchRule(ctx, body.ID, body.Revision, false)
			result = receipt
			if err == nil && receipt.Count > 0 {
				status = 202
			}
		}
	case "PUT /rules/scheduling":
		var body struct {
			Expected *bool `json:"expected"`
			Enabled  *bool `json:"enabled"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			if body.Expected == nil || body.Enabled == nil {
				err = fmt.Errorf("invalid_request: expected and enabled required")
			} else {
				err = en.setScheduling(*body.Expected, *body.Enabled)
				result = map[string]bool{"scheduling_enabled": *body.Enabled}
			}
		}
	case "PUT /rules/timer":
		var body struct {
			ID       string `json:"id"`
			Revision uint64 `json:"revision"`
			Enabled  *bool  `json:"enabled"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			if body.Enabled == nil || body.ID == "" || body.Revision == 0 {
				err = fmt.Errorf("invalid_request: id, revision and enabled required")
			} else {
				var r *Rule
				if r, err = en.setRuleTimer(ctx, body.ID, body.Revision, *body.Enabled); err == nil {
					result = viewRule(*r)
				}
			}
		}
	case "POST /rules/pause":
		var body struct {
			ID       string `json:"id"`
			Revision uint64 `json:"revision"`
			Paused   bool   `json:"paused"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			err = en.PauseRule(body.ID, body.Revision, body.Paused)
		}
	case "DELETE /rules":
		var body struct {
			ID       string `json:"id"`
			Revision uint64 `json:"revision"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			err = en.DeleteRule(ctx, body.ID, body.Revision)
		}
	case "GET /actions":
		var items []Action
		items, err = en.Actions(ctx)
		result = page(items, req)
	case "POST /actions/preview", "POST /actions/release":
		var body struct {
			RuleID         string `json:"rule_id"`
			ActionID       string `json:"action_id"`
			ActionRevision uint64 `json:"action_revision"`
			PauseRule      bool   `json:"pause_rule"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			if path == "/actions/preview" {
				result, err = en.Preview(ctx, body.RuleID, body.ActionID, body.ActionRevision)
			} else {
				err = en.Release(ctx, body.RuleID, body.ActionID, body.ActionRevision, body.PauseRule)
			}
		}
	case "GET /history":
		result, err = en.store.historyPage(req.Query)
	case "GET /metrics":
		result, err = en.metrics(req.Query.Get("view") == "summary")
	case "GET /presets":
		var items []Preset
		items, err = en.Presets()
		result = page(items, req)
	case "POST /presets":
		var body struct {
			Name   string    `json:"name"`
			Config RunConfig `json:"config"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			result, err = en.SavePreset(body.Name, body.Config)
		}
	case "DELETE /presets":
		var body struct {
			ID string `json:"id"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			err = en.DeletePreset(body.ID)
		}
	case "GET /settings":
		result = en.settingsResponse()
	case "PUT /settings":
		var body struct {
			Config
			LegacyDailyCalls       *int  `json:"daily_calls"` // Ignored for old clients submitting a full configuration.
			LegacyMaxSamples       *int  `json:"max_samples"`
			IncludeProviderTargets *bool `json:"include_provider_targets"`
		}
		if err = DecodeStrict(req.Body, &body); err == nil {
			err = en.updateConfig(body.Config, true)
			result = en.settingsResponse()
		}
	default:
		err = fmt.Errorf("not_found: route")
	}
	if err != nil {
		var verify *connectionVerificationError
		if errors.As(err, &verify) {
			if delay := verify.retryAfterHeader(); delay != "" {
				headers.Set("Retry-After", delay)
			}
		}
		failure, mappedStatus := describeFailure(err, req.Method)
		status = mappedStatus
		result = map[string]any{"error": failure}
	} else if result == nil {
		result = map[string]bool{"ok": true}
	}
	body, e := json.Marshal(result)
	if e != nil {
		status = 500
		body = []byte(`{"error":{"code":"serialization_error"}}`)
	}
	return pluginapi.ManagementResponse{StatusCode: status, Headers: headers, Body: body}
}

func (en *Engine) settingsResponse() any {
	cfg := en.Config()
	return struct {
		Config
		IncludeProviderTargets bool             `json:"include_provider_targets"`
		Capabilities           HostCapabilities `json:"capabilities"`
	}{cfg, cfg.IncludeProviderTargets, en.Capabilities()}
}
