//go:build !credential_ops_host_oauth

// Package ui provides the public, inert model-quality browser application.
package ui

import "embed"

// Files contains static assets only; it does not contain credentials or state.
//
//go:embed index.html app.js client.js view.js login.js style.css workspace.js commands.js conversion.js markup.js
var Files embed.FS
