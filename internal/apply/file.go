package apply

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Ken-Chy129/agentdeck/internal/collect"
)

// EditableFile is a config file the console is allowed to rewrite. Editing is
// restricted to the same files we collect, so a compromised console can't turn
// this into "write anywhere as the user".
func EditableFile(path string) bool {
	for _, rel := range collect.EditablePaths() {
		if path == rel {
			return true
		}
	}
	return false
}

// WriteConfigFile replaces the contents of a collected config file.
//
// The console only ever saw a redacted copy, so any <redacted:fp> placeholder
// still present in the submitted text means "keep whatever is there now".
// Resolving those against the real file here is what keeps secrets on the
// machine: they are never sent to the server, and a round-trip through the
// editor can't overwrite a key with its own placeholder.
func WriteConfigFile(path, content string) (backup string, err error) {
	if !EditableFile(path) {
		return "", fmt.Errorf("refusing to write %s: not a collected config file", path)
	}
	target := expand(path)
	raw, readErr := os.ReadFile(target)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", readErr
	}

	restored, missing := restoreRedacted(content, string(raw))
	if len(missing) > 0 {
		return "", fmt.Errorf("can't resolve redacted value(s) %s: the file changed since the console read it, sync and try again", strings.Join(missing, ", "))
	}

	if readErr == nil && string(raw) == restored {
		return "", nil
	}
	if readErr == nil {
		backup = target + ".agentdeck-bak"
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := target + ".agentdeck-tmp"
	if err := os.WriteFile(tmp, []byte(restored), mode); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	return backup, nil
}

// restoreRedacted swaps every <redacted:fp> in text back to the original value
// from the current file. Placeholders we can't resolve are reported instead of
// being written through, which would corrupt the secret.
func restoreRedacted(text, current string) (string, []string) {
	if !collect.RedactedRe.MatchString(text) {
		return text, nil
	}
	byFP := fingerprintIndex(current)
	var missing []string
	out := collect.RedactedRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := collect.RedactedRe.FindStringSubmatch(m)
		if v, ok := byFP[sub[1]]; ok {
			return v
		}
		missing = append(missing, sub[1])
		return m
	})
	return out, missing
}

// fingerprintIndex maps fingerprint -> original value for every token in the
// file that redaction could have replaced. We hash candidate substrings the
// same way collect does, so the mapping is exact rather than positional: the
// console may have reordered or reformatted the file around them.
func fingerprintIndex(current string) map[string]string {
	idx := map[string]string{}
	for _, tok := range candidateSecrets(current) {
		idx[collect.Fingerprint(tok)] = tok
	}
	return idx
}

// Redaction happens at three granularities (whole value, a secret-looking
// substring, a long random token), so we offer candidates at all three rather
// than trying to replay which rule fired.
var (
	quotedRe    = regexp.MustCompile(`"([^"\n]*)"|'([^'\n]*)'`)
	bareValueRe = regexp.MustCompile(`(?m)[:=]\s*([^\s,#\n"']+)`)
	tokenRe     = regexp.MustCompile(`[A-Za-z0-9_.\-]{8,}`)
)

// candidateSecrets pulls out the value tokens of a config file: anything
// between quotes, bare values after : or =, and any long-ish token. Over-
// collecting is harmless because we only ever look tokens up by fingerprint;
// a wrong guess simply never matches.
func candidateSecrets(s string) []string {
	var out []string
	for _, m := range quotedRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1], m[2])
	}
	for _, m := range bareValueRe.FindAllStringSubmatch(s, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	out = append(out, tokenRe.FindAllString(s, -1)...)
	return out
}

// SetExport rewrites (or removes) a single `export NAME=value` line in a shell
// rc file. Only that line is touched: the rest of the file, including comments
// and anything we never collected, is preserved byte for byte.
//
// An empty value means "delete the line". The last matching line wins, since
// that is the one the shell would have applied.
func SetExport(file, name, value string, remove bool) (backup string, err error) {
	if !collect.EditableRC(file) {
		return "", fmt.Errorf("refusing to write %s: not a collected rc file", file)
	}
	if !envNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid env name %q", name)
	}
	target := expand(file)
	raw, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(raw), "\n")
	re := exportLineRe(name)
	last := -1
	for i, l := range lines {
		if re.MatchString(l) {
			last = i
		}
	}
	if last < 0 {
		return "", fmt.Errorf("%s doesn't export %s", file, name)
	}
	if remove {
		lines = append(lines[:last], lines[last+1:]...)
	} else {
		lines[last] = "export " + name + "=" + shellQuote(value)
	}

	out := strings.Join(lines, "\n")
	if out == string(raw) {
		return "", nil
	}
	backup = target + ".agentdeck-bak"
	if err := os.WriteFile(backup, raw, 0o600); err != nil {
		return "", err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := target + ".agentdeck-tmp"
	if err := os.WriteFile(tmp, []byte(out), mode); err != nil {
		return "", err
	}
	return backup, os.Rename(tmp, target)
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func exportLineRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`^\s*export\s+` + regexp.QuoteMeta(name) + `=`)
}
