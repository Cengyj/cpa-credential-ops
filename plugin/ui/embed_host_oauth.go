//go:build credential_ops_host_oauth

// Package ui provides the public, inert model-quality browser application.
package ui

import (
	"embed"
	"strings"
	"sync"
	"text/template"
)

//go:embed index.html app.js client.js view.js style.css workspace.js commands.js conversion.js markup.js host_oauth/accounts.html host_oauth/login.js
var assets embed.FS

// HTML comment delimiters keep index.html usable as the full edition's static page.
var hostIndex = sync.OnceValues(func() (string, error) {
	page, err := template.New("index.html").Delims("<!--{{", "}}-->").ParseFS(assets, "index.html", "host_oauth/accounts.html")
	if err != nil {
		return "", err
	}
	var result strings.Builder
	if err := page.Execute(&result, nil); err != nil {
		return "", err
	}
	return result.String(), nil
})

type hostOAuthFiles struct{}

// Files serves the shared workspaces with the host OAuth account page.
var Files hostOAuthFiles

func (hostOAuthFiles) ReadFile(name string) ([]byte, error) {
	switch name {
	case "index.html":
		page, err := hostIndex()
		return []byte(page), err
	case "login.js":
		return assets.ReadFile("host_oauth/login.js")
	default:
		return assets.ReadFile(name)
	}
}
