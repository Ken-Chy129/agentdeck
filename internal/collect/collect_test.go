package collect

import (
	"encoding/json"
	"strings"
	"testing"
)

// "auth": { matched the secret-key rule and got its opening brace replaced,
// which left the snapshot unparseable and made the file impossible to edit.
func TestRedactKeepsNestedBlocksParseable(t *testing.T) {
	in := `{
  "security": {
    "auth": {
      "selectedType": "vertex-ai"
    }
  }
}`
	out := Redact(in)
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("redacted snapshot is not valid JSON: %v\n%s", err, out)
	}
	if strings.Contains(out, "<redacted") {
		t.Fatalf("nothing here is a secret, but something was redacted:\n%s", out)
	}
}

func TestRedactStillHidesSecretValues(t *testing.T) {
	out := Redact(`{"apiKey": "sk-abcdefghijklmnop0123456789"}`)
	if strings.Contains(out, "sk-abcdefghijklmnop0123456789") {
		t.Fatalf("secret survived redaction: %s", out)
	}
	if !strings.Contains(out, "<redacted:") {
		t.Fatalf("expected a placeholder, got: %s", out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("redacted output must stay valid JSON: %v\n%s", err, out)
	}
}

// YAML block scalars sit in the same position as a value and must survive too.
func TestRedactKeepsYAMLBlockIndicators(t *testing.T) {
	for _, in := range []string{"token: |\n  abc\n", "secret: >\n  abc\n", "auth:\n  kind: none\n"} {
		if got := Redact(in); strings.Contains(got, "<redacted") && !strings.Contains(in, "abc123") {
			t.Fatalf("block indicator was redacted: %q -> %q", in, got)
		}
	}
}

func TestEditablePath(t *testing.T) {
	if !EditablePath("~/.claude/settings.json") {
		t.Fatal("a collected config file should be editable")
	}
	if EditablePath("~/.ssh/id_rsa") {
		t.Fatal("a file we never collect must not be editable")
	}
	if !EditableRC("~/.zshrc") {
		t.Fatal("collected rc files should be editable")
	}
	if EditableRC("~/.claude/settings.json") {
		t.Fatal("a config file is not an rc file")
	}
}
