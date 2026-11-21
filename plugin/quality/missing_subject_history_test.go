package quality

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestFixedMissingSubjectHistory(t *testing.T) {
	ctx := context.Background()
	type historyFixture struct {
		Scenario string         `json:"scenario"`
		Summary  map[string]any `json:"summary"`
		Detail   *Run           `json:"detail"`
	}
	var fixtures []historyFixture
	for _, kind := range []string{"provider", "provider_group", "oauth"} {
		t.Run(kind, func(t *testing.T) {
			target := policyProvider()
			if kind == "oauth" {
				target = scopedTarget("file", "a")
			}
			// The same ID spelling and executor cannot establish the object type.
			target.AuthID, target.AuthIndex, target.Provider = "codex:apikey:0123456789ab", "same-index", "codex"
			if target.IsProvider {
				target.ProviderGroupID = "selected-group"
			}
			en, h, _ := scopeFixture(t, target)
			draft := Rule{Name: "fixed subject", Enabled: true, TargetScope: "single", ObjectTypes: []string{"oauth", "provider"},
				RunConfig: RunConfig{Models: []string{"a"}, QuestionID: "logic-boxes", Samples: 1}}
			id := target.AuthID
			if kind == "provider_group" {
				draft.TargetGroup, id = target.ProviderGroupID, target.ProviderGroupID
			} else {
				draft.AuthID, draft.Provider = id, target.Provider
			}
			rule, err := en.SaveRule(ctx, draft, false)
			if err != nil {
				t.Fatal(err)
			}
			h.targets = nil
			clear(en.rules)
			if err := en.load(); err != nil {
				t.Fatal(err)
			}
			for _, outage := range []bool{false, true} {
				if outage {
					en.host = partialProviderDirectory{en.host}
				}
				receipt, err := en.dispatchRule(ctx, rule.ID, rule.Revision, false)
				if err != nil {
					t.Fatal(err)
				}
				diagnostics := append(receipt.SkipDetails, receipt.PreflightDetails...)
				if len(diagnostics) != 1 || diagnostics[0].AuthID != id {
					t.Fatalf("expected one diagnostic for the original binding, got %+v", receipt)
				}
				if kind == "oauth" && !receipt.Verified {
					t.Error("an unrelated provider directory outage was attributed to a saved file binding")
				}
				code := "credential_missing"
				if kind == "provider_group" {
					code = "group_missing"
				}
				if outage && kind != "oauth" {
					code = "provider_directory_unavailable"
				}
				if diagnostics[0].Code != code {
					t.Errorf("wrong diagnostic: got %s, want %s", diagnostics[0].Code, code)
				}
				run, err := en.Run(diagnostics[0].RunID)
				if err != nil || len(run.Samples) != 0 || run.Config.AuthID != id || h.executeCalls != 0 || len(en.queue) != 0 {
					t.Fatalf("unexpected model work or binding: %v, %+v", err, run)
				}
				list, err := en.QueryRuns(url.Values{"view": {"summary"}, "scope_batch_id": {receipt.BatchID}})
				if err != nil {
					t.Fatal(err)
				}
				rows := list["items"].([]map[string]any)
				if len(rows) != 1 {
					t.Fatal("expected one result for this occurrence")
				}
				scenario := kind + "-absent"
				if outage {
					scenario = kind + "-outage"
				}
				fixtures = append(fixtures, historyFixture{scenario, rows[0], run})
				field := "skip_detail"
				detail := run.SkipDetail
				if detail == nil {
					field, detail = "preflight_detail", run.PreflightDetail
				}
				for _, value := range []any{diagnostics[0], rows[0][field], detail} {
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					var evidence map[string]any
					if err := json.Unmarshal(raw, &evidence); err != nil {
						t.Fatal(err)
					}
					if evidence["object_type"] != kind || evidence["auth_id"] != id {
						t.Errorf("wrong frozen subject evidence: %s", raw)
					}
					if kind == "provider" && evidence["auth_index"] != target.AuthIndex {
						t.Errorf("missing bound route index: %s", raw)
					}
					if !outage && kind == "provider" && strings.Contains(evidence["reason"].(string), "凭证") {
						t.Error("provider called a credential in retained reason")
					}
				}
			}
			h.targets = []Target{target}
			if partial, ok := en.host.(partialProviderDirectory); ok {
				en.host = partial.Host
			}
			receipt, err := en.dispatchRule(ctx, rule.ID, rule.Revision, false)
			if err != nil || receipt.Count != 1 || receipt.Skipped != 0 {
				t.Fatalf("restored binding was not admitted: %+v %v", receipt, err)
			}
			task := en.nextRunnableLocked()
			if task == nil {
				t.Fatal("no worker task for the restored binding")
			}
			en.executeTask(task)
			if h.executeCalls != 1 || en.runs[task.id].Status != "completed" {
				t.Fatal("restored binding did not execute exactly once", h.executeCalls, en.runs[task.id].Status)
			}
			if kind != "provider" {
				return
			}
			h.targets[0].Models = []string{"other"}
			receipt, err = en.dispatchRule(ctx, rule.ID, rule.Revision, false)
			if err != nil || receipt.Count != 1 {
				t.Fatal("expected one catalog check", receipt, err)
			}
			en.executeTask(en.nextRunnableLocked())
			run, err := en.Run(receipt.RunID)
			if err != nil || run.Status != "completed" || len(run.Samples) != 0 || h.executeCalls != 1 ||
				run.PreflightDetail == nil || !strings.Contains(run.PreflightDetail.Reason, "供应商路由") {
				t.Fatalf("wrong provider catalog evidence, calls=%d, run=%+v", h.executeCalls, run)
			}
			page, err := en.QueryRuns(url.Values{"view": {"summary"}, "scope_batch_id": {receipt.BatchID}})
			if err != nil {
				t.Fatal(err)
			}
			fixtures = append(fixtures, historyFixture{"provider-catalog", page["items"].([]map[string]any)[0], run})
		})
	}
	// The browser regression replays these real list and detail projections.
	if path := os.Getenv("QUALITY_HISTORY_TEST_FIXTURE"); path != "" && !t.Failed() {
		raw, err := json.Marshal(fixtures)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

type fixedRuleReadCounter struct {
	*observationHost
	reads int
}

func (h *fixedRuleReadCounter) Targets(ctx context.Context) ([]Target, error) {
	h.reads++
	return h.observationHost.Targets(ctx)
}

func TestFixedRuleRequestVerifiesSelectionBeforeSaving(t *testing.T) {
	file := scopedTarget("same-id", "a")
	route := policyProvider()
	route.AuthID, route.AuthIndex, route.Provider = file.AuthID, file.AuthIndex, file.Provider
	en, host, _ := scopeFixture(t, file)
	counter := &fixedRuleReadCounter{observationHost: &observationHost{host}}
	en.host = counter
	before := len(en.rules)
	body := map[string]any{"name": "selected route", "enabled": true, "target_scope": "single", "object_types": []string{objectOAuth, objectProvider},
		"auth_id": file.AuthID, "provider": file.Provider, "target_auth_index": file.AuthIndex, "models": []string{"a"}, "samples": 1,
		"question_id": "logic-boxes", "expected_target_object_type": objectProvider}
	request := func(method, path string, want int) map[string]any {
		t.Helper()
		beforeReads := counter.reads
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp := en.Handle(t.Context(), pluginapi.ManagementRequest{Method: method, Path: "/v0/management/plugins/model-quality" + path, Body: raw})
		if resp.StatusCode != want {
			t.Fatalf("%s %s wanted %d, got %d: %s", method, path, want, resp.StatusCode, resp.Body)
		}
		if want != 400 && counter.reads-beforeReads != 1 {
			t.Fatal("save or preview did not reuse its single current directory read", counter.reads-beforeReads)
		}
		var result map[string]any
		if err := json.Unmarshal(resp.Body, &result); err != nil {
			t.Fatal(err)
		}
		if _, leaked := result["expected_target_object_type"]; leaked {
			t.Fatal("request-only precondition leaked into response")
		}
		return result
	}
	request(http.MethodPost, "/rules", 409)
	request(http.MethodPost, "/rules/scope-preview", 409)
	en.managed = map[string]*CredentialDisposition{file.AuthID: {AuthID: file.AuthID, State: "disabled", Operation: disableCredential, UpstreamCondition: "unauthorized"}}
	request(http.MethodPost, "/rules", 409)
	delete(en.managed, file.AuthID)
	if len(en.rules) != before || len(en.runs) != 0 || host.executeCalls != 0 {
		t.Fatal("source change admitted the replacement")
	}
	delete(body, "expected_target_object_type")
	body["target_object_type"] = objectProvider
	older := request(http.MethodPost, "/rules", 200)
	if older["target_object_type"] != objectOAuth {
		t.Fatal("older client forged a verified origin")
	}
	body["expected_target_object_type"] = objectProvider
	delete(body, "target_object_type")
	host.targets = []Target{route}
	originalIndex := body["target_auth_index"]
	body["target_auth_index"] = "different-index"
	request(http.MethodPost, "/rules", 409)
	body["target_auth_index"] = originalIndex
	host.targets = []Target{route, file}
	request(http.MethodPost, "/rules", 409)
	host.targets = nil
	request(http.MethodPost, "/rules", 409)
	host.targets = []Target{route}
	request(http.MethodPost, "/rules/scope-preview", 200)
	saved := request(http.MethodPost, "/rules", 200)
	if saved["target_object_type"] != objectProvider {
		t.Fatal("wrong recorded origin", saved)
	}
	body["id"], body["revision"] = saved["id"], saved["revision"]
	other := file
	other.AuthID, other.AuthIndex = "selected-other", "new-index"
	host.targets = []Target{other}
	body["auth_id"], body["target_auth_index"] = other.AuthID, other.AuthIndex
	request(http.MethodPut, "/rules", 409)
	if original := en.rules[saved["id"].(string)]; original.Revision != 1 || original.AuthID != route.AuthID {
		t.Fatal("rejected update changed the saved binding")
	}
	body["expected_target_object_type"] = objectOAuth
	next := request(http.MethodPut, "/rules", 200)
	if next["target_object_type"] != objectOAuth || next["revision"] != float64(2) {
		t.Fatal("a valid new binding was not saved", next)
	}
	body["id"], body["revision"] = next["id"], next["revision"]
	delete(body, "expected_target_object_type")
	body["name"] = "older client rename"
	request(http.MethodPut, "/rules", 200)
	delete(body, "id")
	delete(body, "revision")
	body["expected_target_object_type"] = objectProviderGroup
	request(http.MethodPost, "/rules", 400)
	body["expected_target_object_type"] = objectOAuth
	delete(body, "target_auth_index")
	request(http.MethodPost, "/rules", 400)
	body["target_auth_index"] = other.AuthIndex
	body["target_scope"] = scopeAll
	request(http.MethodPost, "/rules", 400)
	if len(en.rules) != before+2 || len(en.runs) != 0 || host.executeCalls != 0 {
		t.Fatal("saving rules performed model work or accepted an invalid binding")
	}
}

func TestFixedBindingRetainsVerifiedOrigin(t *testing.T) {
	ctx := context.Background()
	for _, origin := range []string{objectOAuth, objectProvider} {
		t.Run(origin, func(t *testing.T) {
			file, route := scopedTarget("codex:apikey:0123456789ab", "a"), policyProvider()
			file.Provider, file.Email = "codex", "replacement@example.test"
			route.AuthID, route.AuthIndex, route.Provider = file.AuthID, file.AuthIndex, file.Provider
			route.ProviderName, route.ProviderGroupID, route.ProviderDisableUnit = "Synthetic provider", "native-group", "group"
			peer := route
			peer.AuthID, peer.AuthIndex = "codex:apikey:abcdef001234", "peer-index"
			first, after := file, route
			if origin == objectProvider {
				first, after = route, file
			}
			en, h, _ := scopeFixture(t, first, peer)
			r, err := en.SaveRule(ctx, Rule{Name: "original", Enabled: true, TargetScope: "single", TargetObjectType: objectType(after), ObjectTypes: []string{objectOAuth, objectProvider},
				RunConfig: RunConfig{AuthID: first.AuthID, Provider: first.Provider, Models: []string{"a"}, Samples: 1, QuestionID: "logic-boxes"}}, false)
			if err != nil {
				t.Fatal(err)
			}
			if r.TargetObjectType != origin {
				t.Fatal("new rule trusted the requested origin instead of the directory", r.TargetObjectType)
			}
			queued, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
			if err != nil || queued.Count != 1 {
				t.Fatal(queued, err)
			}
			h.targets = []Target{after, peer}
			en.executeTask(en.nextRunnableLocked())
			if h.executeCalls != 0 || en.runs[queued.RunID].Status != "blocked" {
				t.Fatal("queued work called the replacement", h.executeCalls, en.runs[queued.RunID].Status)
			}
			rename := *r
			rename.Name, rename.TargetScope, rename.TargetObjectType = "renamed", "", objectType(after)
			if _, err := en.SaveRule(ctx, rename, true); err == nil || en.rules[r.ID].TargetObjectType != origin {
				t.Fatal("renaming adopted a different object type or trusted the client")
			}
			// Selecting an unrelated group cannot expand an original file into that group's peers.
			if origin == objectOAuth {
				en.rules[r.ID].AuthorizedGroups = []string{route.ProviderGroupID}
			}
			for _, preview := range []bool{true, false} {
				var receipt ruleDispatch
				if preview {
					receipt, err = en.previewRuleScope(ctx, *en.rules[r.ID])
				} else {
					receipt, err = en.dispatchRule(ctx, r.ID, r.Revision, false)
				}
				if err != nil || len(receipt.PreflightDetails) != 1 || receipt.PreflightDetails[0].Code != "bound_origin_changed" ||
					receipt.PreflightDetails[0].ObjectType != origin || receipt.PreflightDetails[0].Email != "" || len(en.queue) != 0 {
					t.Fatalf("wrong type was admitted or attributed, preview=%t: %+v %v", preview, receipt, err)
				}
				if !preview {
					stored, err := en.Run(receipt.RunID)
					if err != nil || stored.Status != "blocked" || stored.CredentialEmail != "" || stored.Config.ProviderName != "" ||
						stored.Config.ProviderObservation != (origin == objectProvider) || len(stored.Samples) != 0 {
						t.Fatal("the original binding used metadata from its replacement", stored, err)
					}
				}
			}
			if h.executeCalls != 0 || h.pauseCalls != 0 || h.recoverCalls != 0 || h.releaseCalls != 0 {
				t.Fatal("a changed origin was called or changed")
			}
		})
	}
}

func TestOldFixedRuleLearnsOriginOnlyFromItsAdmittedBinding(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"route", "file", "index missing", "index changed", "route unverified"} {
		t.Run(scenario, func(t *testing.T) {
			target := policyProvider()
			if scenario == "file" {
				target = scopedTarget("codex:apikey:0123456789ab", "a")
			}
			en, h, _ := scopeFixture(t, target)
			r, err := en.SaveRule(ctx, Rule{Name: "pre-upgrade", Enabled: true, TargetScope: "single", ObjectTypes: []string{objectOAuth, objectProvider},
				RunConfig: RunConfig{AuthID: target.AuthID, Provider: target.Provider, Models: []string{"a"}, Samples: 1, QuestionID: "logic-boxes"}}, false)
			if err != nil {
				t.Fatal(err)
			}
			r.TargetObjectType = ""
			if scenario == "index missing" {
				r.TargetAuthIndex = ""
			}
			ch := changes()
			ch.rules[r.ID] = r
			if err := en.commitLocked(ch); err != nil {
				t.Fatal(err)
			}
			h.targets = nil
			missingType := func() *Run {
				t.Helper()
				receipt, err := en.dispatchRule(ctx, r.ID, r.Revision, false)
				if err != nil || len(receipt.PreflightDetails) != 1 {
					t.Fatal("expected a missing-bound-object occurrence", receipt, err)
				}
				run, err := en.Run(receipt.RunID)
				if err != nil || run.PreflightDetail == nil {
					t.Fatal(run, err)
				}
				return run
			}
			oldHistory := missingType()
			if oldHistory.PreflightDetail.ObjectType != objectUnknown {
				t.Fatal("an old ambiguous binding guessed its origin before a directory match")
			}
			if scenario == "index changed" {
				target.AuthIndex = "replacement-index"
			} else if scenario == "route unverified" {
				target.Reasons = []string{"provider_route_unverified"}
			}
			h.targets = []Target{target}
			if _, err := en.previewRuleScope(ctx, *r); err != nil || en.rules[r.ID].TargetObjectType != "" {
				t.Fatal("preview failed or altered the saved rule", err)
			}
			if _, err := en.dispatchRule(ctx, r.ID, r.Revision, false); err != nil {
				t.Fatal(err)
			}
			if task := en.nextRunnableLocked(); task != nil {
				en.executeTask(task)
			}
			want := objectUnknown
			if scenario == "route" || scenario == "file" {
				want = objectType(target)
				if h.executeCalls != 1 {
					t.Fatal("the matching binding was not executed once", h.executeCalls)
				}
			}
			h.targets = nil
			delete(en.rules, r.ID)
			if err := en.load(); err != nil {
				t.Fatal(err)
			}
			if current := missingType(); current.PreflightDetail.ObjectType != want || en.rules[r.ID].Revision != r.Revision {
				t.Fatal("wrong origin retained after restart", current.PreflightDetail.ObjectType, want)
			}
			if stored, err := en.Run(oldHistory.ID); err != nil || stored.PreflightDetail.ObjectType != objectUnknown {
				t.Fatal("later evidence rewrote an older occurrence", stored, err)
			}
		})
	}
}
