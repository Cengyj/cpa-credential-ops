//go:build credential_ops_host_oauth

package ui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHostOAuthAssets(t *testing.T) {
	page, err := Files.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`href="/management.html#/oauth" target="_top"`, `href="/management.html#/auth-files" target="_top"`, `id="page-manual"`, `id="page-operations"`} {
		if !bytes.Contains(page, []byte(want)) {
			t.Errorf("missing %q in the host OAuth page", want)
		}
	}
	for _, omitted := range []string{`id="account-import-dialog"`, `id="login-workbench"`, "<!--{{"} {
		if bytes.Contains(page, []byte(omitted)) {
			t.Errorf("unexpected %q in the host OAuth page", omitted)
		}
	}
}

// The optional browser check exercises the same resources the native host serves.
func TestHostOAuthBrowser(t *testing.T) {
	node := os.Getenv("HOST_OAUTH_UI_TEST_NODE")
	if node == "" {
		t.Skip("set HOST_OAUTH_UI_TEST_NODE to a Node executable with Playwright to run the browser check")
	}
	dir := t.TempDir()
	for _, name := range []string{"index.html", "app.js", "client.js", "view.js", "login.js", "style.css", "workspace.js", "commands.js", "markup.js", "conversion.js"} {
		data, err := Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, "../../scripts/verify_host_oauth_ui.cjs", dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}
