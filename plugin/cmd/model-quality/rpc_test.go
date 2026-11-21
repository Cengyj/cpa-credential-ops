package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHostErrorRetainsStructuredStatus(t *testing.T) {
	err := decodeHostResponse([]byte(`{"ok":false,"error":{"code":"host_call_failed","message":"denied","http_status":401}}`), 0, nil)
	var detail *pluginabi.Error
	if !errors.As(err, &detail) || detail.StatusCode() != 401 || detail.ErrorCode() != "host_call_failed" {
		t.Fatal("structured host error lost", err)
	}
}

func TestPublicResourceGETPreconditions(t *testing.T) {
	const path = "/v0/resource/plugins/model-quality/index.html"
	request := func(headers http.Header) pluginapi.ManagementResponse {
		t.Helper()
		payload, err := json.Marshal(pluginapi.ManagementRequest{Method: http.MethodGet, Path: path, Headers: headers})
		if err != nil {
			t.Fatal(err)
		}
		result, err := handle(pluginabi.MethodManagementHandle, payload)
		if err != nil {
			t.Fatal(err)
		}
		return result.(pluginapi.ManagementResponse)
	}
	original := request(nil)
	tag := original.Headers.Get("ETag")
	for _, tc := range []struct {
		name   string
		header http.Header
		want   int
	}{
		{"requested older representation", http.Header{"If-Match": {`"previous-package"`}}, 412},
		{"weak current validator cannot satisfy strong comparison", http.Header{"If-Match": {tag}}, 412},
		{"same text without weak prefix cannot satisfy strong comparison", http.Header{"If-Match": {strings.TrimPrefix(tag, "W/")}}, 412},
		{"failed prerequisite before revalidation", http.Header{"If-Match": {`"previous-package"`}, "If-None-Match": {tag}}, 412},
		{"existing public path", http.Header{"If-Match": {"*"}}, 200},
		{"passed prerequisite and current cached copy", http.Header{"If-Match": {"*"}, "If-None-Match": {tag}}, 304},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := request(tc.header)
			if got.StatusCode != tc.want || (len(got.Body) != 0) != (tc.want == 200) {
				t.Fatalf("status %d, body bytes %d; want %d", got.StatusCode, len(got.Body), tc.want)
			}
		})
	}
}

func TestPublicResourcesStayStaticAndMatchExactRegisteredPaths(t *testing.T) {
	for _, resource := range staticResources {
		raw, err := json.Marshal(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/model-quality/" + resource.name})
		if err != nil {
			t.Fatal(err)
		}
		result, err := handle(pluginabi.MethodManagementHandle, raw)
		if err != nil {
			t.Fatal(err)
		}
		response := result.(pluginapi.ManagementResponse)
		if response.StatusCode != 200 || len(response.Body) == 0 || response.Headers.Get("Content-Type") != resource.contentType || response.Headers.Get("Content-Security-Policy") == "" {
			t.Fatal("static resource contract", resource.name, response.StatusCode)
		}
		tag := fmt.Sprintf(`W/"%x"`, sha256.Sum256(response.Body))
		if response.Headers.Get("ETag") != tag || response.Headers.Get("Cache-Control") != "private, no-cache" ||
			response.Headers.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("static response must revalidate the exact served representation", resource.name)
		}
		for _, tc := range []struct {
			validators []string
			want       int
		}{
			{[]string{tag}, 304}, {[]string{strings.TrimPrefix(tag, "W/")}, 304}, {[]string{"*"}, 304},
			{[]string{`"unrelated,tag", ` + tag}, 304}, {[]string{`"old"`, tag}, 304},
			{[]string{`"other"`}, 200}, {[]string{"broken-" + tag}, 200},
		} {
			req := pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/model-quality/" + resource.name,
				Headers: http.Header{"If-None-Match": tc.validators}}
			raw, _ := json.Marshal(req)
			result, err := handle(pluginabi.MethodManagementHandle, raw)
			if err != nil {
				t.Fatal(err)
			}
			got := result.(pluginapi.ManagementResponse)
			if got.StatusCode != tc.want || (len(got.Body) == 0) != (tc.want == 304) ||
				got.Headers.Get("ETag") != tag || got.Headers.Get("Cache-Control") != response.Headers.Get("Cache-Control") ||
				got.Headers.Get("Content-Security-Policy") != response.Headers.Get("Content-Security-Policy") ||
				got.Headers.Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("%s with %v returned %d, body %d bytes", resource.name, tc.validators, got.StatusCode, len(got.Body))
			}
		}
	}
	for _, req := range []pluginapi.ManagementRequest{
		{Method: "POST", Path: "/v0/resource/plugins/model-quality/index.html"},
		{Method: "GET", Path: "/v0/resource/plugins/model-quality/nested/app.js"},
		{Method: "GET", Path: "/v0/resource/plugins/model-quality/../app.js"},
		{Method: "GET", Path: "/v0/resource/plugins/model-quality/login/accounts", Headers: http.Header{"If-None-Match": {"*"}}},
	} {
		raw, _ := json.Marshal(req)
		result, err := handle(pluginabi.MethodManagementHandle, raw)
		if err != nil || result.(pluginapi.ManagementResponse).StatusCode != 404 {
			t.Fatal("accepted unregistered public resource", req, err)
		}
	}
}

func TestHostEnvelopeRequiresConsistentSuccessAndResult(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		status int
	}{
		{`{"ok":true,"result":null}`, 0},
		{`{"ok":true}`, 0},
		{`{"ok":true,"result":{},"error":{"code":"error","message":"failed"}}`, 0},
		{`{"ok":true,"result":{}}`, 1},
		{`{"ok":false,"result":{}}`, 0},
		{`{"ok":true,"result":"unexpected string"}`, 0},
		{`null`, 0},
		{`malformed`, 0},
	} {
		var result struct{ Value string }
		if err := decodeHostResponse([]byte(tc.raw), tc.status, &result); err == nil {
			t.Fatal("accepted invalid host response", tc.raw, tc.status)
		}
	}
	var result struct{ Value string }
	if err := decodeHostResponse([]byte(`{"ok":true,"result":{"Value":"ok"}}`), 0, &result); err != nil || result.Value != "ok" {
		t.Fatal(result, err)
	}
}
