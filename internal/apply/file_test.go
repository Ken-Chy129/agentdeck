package apply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ken-Chy129/agentdeck/internal/collect"
)

func withHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// The console edits a redacted copy. Saving it back must not replace the real
// key with the placeholder text the operator saw.
func TestWriteConfigFileRestoresRedactedSecret(t *testing.T) {
	dir := withHome(t)
	target := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{
  "apiKey": "sk-lmnopqrstuvwx0123456789",
  "model": "claude-opus-4"
}
`
	if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	redacted := collect.Redact(original)
	if !strings.Contains(redacted, "<redacted:") {
		t.Fatalf("precondition: expected the key to be redacted, got %s", redacted)
	}
	edited := strings.Replace(redacted, "claude-opus-4", "claude-opus-5", 1)

	if _, err := WriteConfigFile("~/.claude/settings.json", edited); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "sk-lmnopqrstuvwx0123456789") {
		t.Fatalf("real key was lost, file is now:\n%s", got)
	}
	if strings.Contains(string(got), "<redacted:") {
		t.Fatalf("placeholder was written through:\n%s", got)
	}
	if !strings.Contains(string(got), "claude-opus-5") {
		t.Fatalf("edit was not applied:\n%s", got)
	}
}

// If the secret changed on the machine after the console read it, the
// fingerprint no longer resolves. Writing the placeholder would corrupt the
// file, so the job must fail loudly instead.
func TestWriteConfigFileRejectsUnresolvablePlaceholder(t *testing.T) {
	dir := withHome(t)
	target := filepath.Join(dir, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"apiKey":"brand-new-value-9876543210"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := WriteConfigFile("~/.gemini/settings.json", `{"apiKey":"<redacted:deadbeef>"}`)
	if err == nil {
		t.Fatal("want an error when the fingerprint can't be resolved")
	}
	got, _ := os.ReadFile(target)
	if !strings.Contains(string(got), "brand-new-value-9876543210") {
		t.Fatalf("file was modified despite the error:\n%s", got)
	}
}

func TestWriteConfigFileRefusesUnknownPath(t *testing.T) {
	withHome(t)
	if _, err := WriteConfigFile("~/.ssh/id_rsa", "pwned"); err == nil {
		t.Fatal("want an error for a path outside the collected set")
	}
}

// A save with no changes shouldn't churn a backup file.
func TestWriteConfigFileNoopWhenUnchanged(t *testing.T) {
	dir := withHome(t)
	target := filepath.Join(dir, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"hooks":[]}`
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := WriteConfigFile("~/.codex/hooks.json", body)
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Fatalf("want no backup for an unchanged write, got %q", backup)
	}
	if _, err := os.Stat(target + ".agentdeck-bak"); !os.IsNotExist(err) {
		t.Fatal("backup file was created for an unchanged write")
	}
}

// The previous contents must survive a real edit so a bad save is recoverable.
func TestWriteConfigFileKeepsBackup(t *testing.T) {
	dir := withHome(t)
	target := filepath.Join(dir, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"hooks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := WriteConfigFile("~/.codex/hooks.json", `{"hooks":["x"]}`)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("backup unreadable: %v", err)
	}
	if string(old) != `{"hooks":[]}` {
		t.Fatalf("backup has the wrong contents: %s", old)
	}
}

// Rewriting one export must not disturb the rest of the rc file: it holds
// plenty we never collected, and clobbering it would break the user's shell.
func TestSetExportOnlyTouchesItsLine(t *testing.T) {
	dir := withHome(t)
	rc := filepath.Join(dir, ".zshrc")
	body := `# my shell
export PATH="$HOME/bin:$PATH"
export EDITOR=vim
alias ll='ls -la'
`
	if err := os.WriteFile(rc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetExport("~/.zshrc", "EDITOR", "nvim", false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(rc)
	want := `# my shell
export PATH="$HOME/bin:$PATH"
export EDITOR=nvim
alias ll='ls -la'
`
	if string(got) != want {
		t.Fatalf("rc file mangled:\n%s", got)
	}
}

// Values with spaces or quotes must survive a round trip through the shell.
func TestSetExportQuotesUnsafeValues(t *testing.T) {
	dir := withHome(t)
	rc := filepath.Join(dir, ".bashrc")
	if err := os.WriteFile(rc, []byte("export GREETING=hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetExport("~/.bashrc", "GREETING", "hello there's space", false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(rc)
	if !strings.Contains(string(got), `export GREETING='hello there'\''s space'`) {
		t.Fatalf("value was not quoted safely: %s", got)
	}
}

// The shell applies the last assignment, so that's the one we must replace.
func TestSetExportReplacesLastOccurrence(t *testing.T) {
	dir := withHome(t)
	rc := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(rc, []byte("export A=1\nexport A=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetExport("~/.zshrc", "A", "3", false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(rc)
	if string(got) != "export A=1\nexport A=3\n" {
		t.Fatalf("wrong line replaced:\n%s", got)
	}
}

func TestSetExportRemove(t *testing.T) {
	dir := withHome(t)
	rc := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(rc, []byte("export A=1\nexport B=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetExport("~/.zshrc", "A", "", true); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(rc)
	if string(got) != "export B=2\n" {
		t.Fatalf("want only B left, got:\n%s", got)
	}
}

func TestSetExportRefusesForeignFile(t *testing.T) {
	withHome(t)
	if _, err := SetExport("~/.ssh/config", "A", "1", false); err == nil {
		t.Fatal("want an error for a file outside the collected rc set")
	}
}
