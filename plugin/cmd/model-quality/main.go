package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/cliproxyapi-plugins/model-quality/quality"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

var runtime engineRuntime

var staticResources = []struct{ name, contentType string }{
	{"index.html", "text/html; charset=utf-8"},
	{"app.js", "application/javascript; charset=utf-8"},
	{"client.js", "application/javascript; charset=utf-8"},
	{"view.js", "application/javascript; charset=utf-8"},
	{"login.js", "application/javascript; charset=utf-8"},
	{"style.css", "text/css; charset=utf-8"},
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil || host == nil || uint32(host.abi_version) != pluginabi.ABIVersion {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, length C.size_t, response *C.cliproxy_buffer) (status C.int) {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	defer func() {
		if recover() != nil {
			writeResponse(response, failure("internal_error", "plugin callback failed"))
			status = 1
		}
	}()
	if method == nil || uint64(length) > 8<<20 || (request == nil && length != 0) {
		writeResponse(response, failure("invalid_request", "invalid native request"))
		return 1
	}
	var raw []byte
	if request != nil && length > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(length))
	}
	result, err := handle(C.GoString(method), raw)
	if err != nil {
		writeResponse(response, failure("plugin_error", err.Error()))
		return 1
	}
	b, err := json.Marshal(result)
	if err != nil {
		writeResponse(response, failure("serialization_error", "cannot encode response"))
		return 1
	}
	out, _ := json.Marshal(pluginabi.Envelope{OK: true, Result: b})
	writeResponse(response, out)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	if err := runtime.close(); err != nil {
		log.WithError(err).Error("model-quality: shutdown store")
	}
}
func failure(code, message string) []byte {
	b, _ := json.Marshal(pluginabi.Envelope{Error: &pluginabi.Error{Code: code, Message: message}})
	return b
}
func writeResponse(out *C.cliproxy_buffer, b []byte) {
	if len(b) > 0 {
		out.ptr = C.CBytes(b)
		out.len = C.size_t(len(b))
	}
}
func callback(method string, request, response any) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	cm := C.CString(method)
	defer C.free(unsafe.Pointer(cm))
	payload := C.CBytes(raw)
	defer C.free(payload)
	var out C.cliproxy_buffer
	status := C.call_host_api(cm, (*C.uint8_t)(payload), C.size_t(len(raw)), &out)
	if out.ptr == nil {
		return fmt.Errorf("host callback %s unavailable", method)
	}
	defer C.free_host_buffer(out.ptr, out.len)
	if uint64(out.len) > 8<<20 {
		return fmt.Errorf("host response exceeds native buffer limit")
	}
	b := C.GoBytes(out.ptr, C.int(out.len))
	return decodeHostResponse(b, int(status), response)
}
func registration() any {
	return map[string]any{"schema_version": pluginabi.SchemaVersion, "metadata": pluginapi.Metadata{Name: "Credential Ops", Version: quality.Version, Author: "Cengyj", GitHubRepository: "https://github.com/Cengyj/cpa-credential-ops", ConfigFields: []pluginapi.ConfigField{{Name: "data_dir", Type: pluginapi.ConfigFieldTypeString, Default: "model-quality-data", Description: "Dedicated persistent plugin database directory"}, {Name: "scheduling_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: "Enable scheduled observation"}}}, "capabilities": map[string]bool{"management_api": true, "usage_plugin": true}}
}
func handle(method string, raw []byte) (any, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req struct {
			ConfigYAML    []byte `json:"config_yaml"`
			SchemaVersion uint32 `json:"schema_version"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		cfg := quality.DefaultConfig()
		if len(req.ConfigYAML) > 0 {
			if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
				return nil, fmt.Errorf("invalid plugin configuration")
			}
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		directory, err := filepath.Abs(cfg.DataDir)
		if err != nil {
			return nil, err
		}
		cfg.DataDir = directory
		managementURL := os.Getenv("MODEL_QUALITY_CPA_URL")
		if err := runtime.configure(runtimeConfig{settings: cfg, managementURL: managementURL}, method == pluginabi.MethodPluginReconfigure, func() (runtimeEngine, error) {
			management := quality.RuntimeManagementClient(managementURL)
			native := &quality.NativeHost{Call: callback, MaxResponseBytes: cfg.MaxResponseBytes, Management: management}
			app, err := newApplication(cfg, native)
			if err != nil {
				management.Close()
				return nil, err
			}
			management.Start(cfg.DataDir, os.Getenv("MODEL_QUALITY_MANAGEMENT_KEY"))
			return &connectedApplication{runtimeEngine: app, management: management}, nil
		}); err != nil {
			return nil, err
		}
		return registration(), nil
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		return struct{}{}, runtime.close()
	case pluginabi.MethodManagementRegister:
		resources := make([]map[string]string, 0, len(staticResources))
		for _, resource := range staticResources {
			route := map[string]string{"Path": "/" + resource.name}
			if resource.name == "index.html" {
				route["Menu"] = "Credential Ops"
				route["Description"] = "手动测试与凭证质量运维"
			}
			resources = append(resources, route)
		}
		routes := quality.ManagementRoutes()
		for index := range routes {
			routes[index].Path = "/v0/management/plugins/model-quality" + routes[index].Path
		}
		routes = append(routes, accountManagementRoutes()...)
		return map[string]any{"routes": routes, "resources": resources}, nil
	case pluginabi.MethodManagementHandle:
		var req struct {
			pluginapi.ManagementRequest
			HostCallbackID string `json:"host_callback_id"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if name, ok := strings.CutPrefix(req.Path, "/v0/resource/plugins/model-quality/"); ok {
			return serveStaticResource(name, req.ManagementRequest)
		}
		instance, release, err := runtime.acquire()
		if err != nil {
			return nil, err
		}
		defer release()
		return instance.engine.Handle(quality.WithManagementCallbackID(instance.ctx, req.HostCallbackID), req.ManagementRequest), nil
	case pluginabi.MethodUsageHandle:
		var record pluginapi.UsageRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		instance, release, err := runtime.acquire()
		if err != nil {
			return struct{}{}, nil
		}
		defer release()
		var ttft *int64
		if record.TTFT > 0 {
			ms := record.TTFT.Milliseconds()
			ttft = &ms
		}
		at := record.RequestedAt
		if at.IsZero() {
			at = time.Now().UTC()
		}
		instance.engine.Usage(quality.UsageEvent{ID: record.RequestID, Source: "unknown", Provider: record.Provider, Model: record.Model, Failed: record.Failed, LatencyMS: record.Latency.Milliseconds(), TTFTMS: ttft, Tokens: record.Detail.TotalTokens, TokensKnown: record.Detail.TotalTokens > 0, Time: at})
		return struct{}{}, nil
	}
	return nil, fmt.Errorf("unknown plugin method")
}
