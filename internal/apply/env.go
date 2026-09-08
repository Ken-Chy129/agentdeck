// Package apply materialises resources on the machine.
package apply

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Ken-Chy129/agentdeck/internal/protocol"
)

const envMarker = "# agentdeck-managed env (do not edit; managed at deck)"

func home() string { h, _ := os.UserHomeDir(); return h }

// EnvFile is where all env resources for this machine are rendered.
func EnvFile() string { return filepath.Join(home(), ".config", "agentdeck", "env.sh") }

// RenderEnv writes every env resource into env.sh (0600) and makes sure the
// user's shell rc files source it. Returns whether the file changed.
func RenderEnv(vars []protocol.DesiredResource) (changed bool, err error) {
	sort.Slice(vars, func(i, j int) bool { return vars[i].Name < vars[j].Name })
	var b strings.Builder
	b.WriteString(envMarker + "\n")
	for _, v := range vars {
		fmt.Fprintf(&b, "export %s=%s\n", v.Name, shellQuote(v.Value))
	}
	p := EnvFile()
	old, _ := os.ReadFile(p)
	if string(old) == b.String() {
		return false, ensureSourced()
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		return false, err
	}
	return true, ensureSourced()
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_./:@%+,=", c)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

const sourceLine = `[ -f "$HOME/.config/agentdeck/env.sh" ] && . "$HOME/.config/agentdeck/env.sh"  # agentdeck`

// ensureSourced appends the source line to the rc files that exist (zshrc,
// bashrc, and profile as a fallback for login shells).
func ensureSourced() error {
	candidates := []string{".zshrc", ".bashrc"}
	found := false
	for _, rc := range candidates {
		p := filepath.Join(home(), rc)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		found = true
		if err := appendOnce(p, sourceLine); err != nil {
			return err
		}
	}
	if !found {
		return appendOnce(filepath.Join(home(), ".profile"), sourceLine)
	}
	return nil
}

func appendOnce(path, line string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "agentdeck/env.sh") {
			return nil
		}
	}
	if _, err := f.Seek(0, 2); err != nil {
		return err
	}
	_, err = f.WriteString("\n" + line + "\n")
	return err
}

// ReadExport finds the current value of NAME from the user's rc files by
// evaluating them in a login shell. Used for import requests from the console.
func ReadExport(name string) (string, bool) {
	// Prefer a real login shell so nvm/brew shims and sourced files resolve.
	for _, sh := range []string{"zsh", "bash"} {
		p, err := lookPath(sh)
		if err != nil {
			continue
		}
		out, err := runShell(p, `-lic`, `printf %s "$`+name+`"`)
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), true
		}
	}
	return "", false
}
