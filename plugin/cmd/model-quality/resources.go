package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/cliproxyapi-plugins/model-quality/ui"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type staticRepresentation struct {
	body        []byte
	contentType string
	etag        string
}

// Both plugin editions serve these fixed paths, but can embed different bytes.
var embeddedResources = sync.OnceValues(func() (map[string]staticRepresentation, error) {
	assets := make(map[string]staticRepresentation, len(staticResources))
	for _, resource := range staticResources {
		body, err := ui.Files.ReadFile(resource.name)
		if err != nil {
			return nil, err
		}
		// CPA can encode this content as identity, gzip, or zstd after the plugin returns.
		assets[resource.name] = staticRepresentation{body, resource.contentType, fmt.Sprintf(`W/"%x"`, sha256.Sum256(body))}
	}
	return assets, nil
})

func serveStaticResource(name string, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	if req.Method != http.MethodGet {
		return pluginapi.ManagementResponse{StatusCode: http.StatusNotFound}, nil
	}
	assets, err := embeddedResources()
	if err != nil {
		return pluginapi.ManagementResponse{}, err
	}
	asset, found := assets[name]
	if !found {
		return pluginapi.ManagementResponse{StatusCode: http.StatusNotFound}, nil
	}
	headers := http.Header{
		"Content-Type":            {asset.contentType},
		"Cache-Control":           {"private, no-cache"},
		"X-Content-Type-Options":  {"nosniff"},
		"Content-Security-Policy": {"default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-src 'self' blob:; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"},
	}
	headers.Set("ETag", asset.etag)
	// If-Match is checked first and requires strong comparison. Our validator
	// spans CPA's content encodings, so only the existence wildcard can match.
	if values := req.Headers.Values("If-Match"); len(values) > 0 && !anyRepresentation(values) {
		return pluginapi.ManagementResponse{StatusCode: http.StatusPreconditionFailed, Headers: headers}, nil
	}
	if matchesEntityTag(req.Headers.Values("If-None-Match"), asset.etag) {
		return pluginapi.ManagementResponse{StatusCode: http.StatusNotModified, Headers: headers}, nil
	}
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: asset.body}, nil
}

func anyRepresentation(values []string) bool {
	return len(values) == 1 && strings.Trim(values[0], " \t") == "*"
}

// GET uses weak comparison. A comma inside an opaque tag is part of the tag.
func matchesEntityTag(values []string, etag string) bool {
	opaque := strings.TrimPrefix(etag, "W/")
	if anyRepresentation(values) {
		return true
	}
	for _, value := range values {
		for value != "" {
			value = strings.TrimLeft(value, " \t,")
			value = strings.TrimPrefix(value, "W/")
			if !strings.HasPrefix(value, `"`) {
				break
			}
			end := strings.IndexByte(value[1:], '"')
			if end < 0 {
				break
			}
			tag := value[:end+2]
			value = strings.TrimLeft(value[end+2:], " \t")
			if value != "" && value[0] != ',' {
				break
			}
			if tag == opaque {
				return true
			}
		}
	}
	return false
}
