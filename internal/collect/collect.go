// Package collect gathers CLI config files and shell environment exports from
// the local machine, redacting anything that looks like a credential before it
// leaves the box.
package collect

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Ken-Chy129/agentdeck/internal/protocol"
)

// Known config files per tool. Paths are relative to $HOME.
var configFiles = []struct{ tool, rel, format string }{
	{"claude", ".claude/settings.json", "json"},
	{"codex", ".codex/config.toml", "toml"},
	{"codex", ".codex/hooks.json", "json"},
	{"hermes", ".hermes/config.yaml", "yaml"},
	{"lark-cli", ".lark-cli/config.json", "json"},
	{"gh", ".config/gh/config.yml", "yaml"},
	{"gh", ".config/gh/hosts.yml", "yaml"},
	{"gemini", ".gemini/settings.json", "json"},
	{"opencode", ".config/opencode/opencode.json", "json"},
	{"agentdeck", ".config/agentdeck/env.sh", "sh"},
}

var rcFiles = []string{".zshenv", ".zprofile", ".zshrc", ".bash_profile", ".bashrc", ".profile"}

const maxFile = 256 << 10

var (
	secretKeyRe = regexp.MustCompile(`(?i)(key|token|secret|password|passwd|credential|auth|cookie)`)
	// value patterns that are almost certainly secrets regardless of key name
	secretValRe = regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9]{20,}|gho_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abp]-[A-Za-z0-9-]{20,}|AKIA[A-Z0-9]{16}|ya29\.[A-Za-z0-9_-]{20,}|eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,})`)
	// key = "value" / key: value / key=value with a secret-ish key name
	kvRe = regexp.MustCompile(`(?im)^(\s*"?[A-Za-z0-9_.-]*(?:key|token|secret|password|passwd|credential|apikey|api_key|auth)[A-Za-z0-9_.-]*"?\s*[:=]\s*)("[^"\n]*"|'[^'\n]*'|[^\s,#\n]+)`)
	// long random-looking strings (>= 32 chars of base64/hex-ish) anywhere
	longRandRe = regexp.MustCompile(`\b[A-Za-z0-9_\-]{32,}\b`)
	exportRe   = regexp.MustCompile(`^\s*export\s+([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
)

func fp(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:4])
}

func redactValue(v string) string {
	q := ""
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		q = string(v[0])
		v = v[1 : len(v)-1]
	}
	if v == "" {
		return q + q
	}
	return q + "<redacted:" + fp(v) + ">" + q
}

// Redact scrubs secrets from arbitrary config text.
func Redact(s string) string {
	s = kvRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := kvRe.FindStringSubmatch(m)
		return sub[1] + redactValue(sub[2])
	})
	s = secretValRe.ReplaceAllStringFunc(s, func(m string) string { return "<redacted:" + fp(m) + ">" })
	s = longRandRe.ReplaceAllStringFunc(s, func(m string) string {
		// leave paths / words alone: require some digit+letter mix
		hasDigit, hasAlpha := false, false
		for _, c := range m {
			if c >= '0' && c <= '9' {
				hasDigit = true
			} else if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				hasAlpha = true
			}
		}
		if hasDigit && hasAlpha && !strings.Contains(m, "_") || len(m) >= 40 && hasDigit && hasAlpha {
			return "<redacted:" + fp(m) + ">"
		}
		return m
	})
	return s
}

func home() string { h, _ := os.UserHomeDir(); return h }

// Configs reads every known config file that exists.
func Configs() []protocol.ConfigFile {
	var out []protocol.ConfigFile
	for _, c := range configFiles {
		p := filepath.Join(home(), c.rel)
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		cf := protocol.ConfigFile{Tool: c.tool, Path: "~/" + c.rel, Format: c.format, Size: fi.Size(), ModTime: fi.ModTime().UTC().Format("2006-01-02T15:04:05Z")}
		if fi.Size() > maxFile {
			cf.Truncated = true
			out = append(out, cf)
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		cf.Content = Redact(string(b))
		cf.Digest = "sha256:" + func() string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }()
		out = append(out, cf)
	}
	return out
}

// Exports parses `export NAME=value` lines from shell rc files. Values that look
// secret are replaced with a fingerprint; values that reference other variables
// (PATH=...:$PATH) are kept verbatim since they contain no secrets.
func Exports() []protocol.EnvExport {
	var out []protocol.EnvExport
	for _, rc := range rcFiles {
		p := filepath.Join(home(), rc)
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		line := 0
		for sc.Scan() {
			line++
			m := exportRe.FindStringSubmatch(sc.Text())
			if m == nil {
				continue
			}
			name, val := m[1], strings.TrimSpace(m[2])
			e := protocol.EnvExport{Name: name, File: "~/" + rc, Line: line}
			unq := strings.Trim(val, `"'`)
			switch {
			case strings.Contains(val, "$"+name) || strings.Contains(val, "${"+name):
				e.Kind = "append"
				e.Value = val
			case secretKeyRe.MatchString(name) || secretValRe.MatchString(unq):
				e.Kind = "secret"
				e.Value = "<redacted:" + fp(unq) + ">"
				e.Fingerprint = fp(unq)
			default:
				e.Kind = "plain"
				e.Value = unq
			}
			out = append(out, e)
		}
		f.Close()
	}
	return out
}
