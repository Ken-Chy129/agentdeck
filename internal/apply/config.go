package apply

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Ken-Chy129/agentdeck/internal/protocol"
)

// ConfigState remembers, per target file, which top-level keys deck wrote last
// time, so keys removed from the profile can be removed from the file too.
type ConfigState struct {
	Keys map[string][]string `json:"keys"` // path -> top-level keys written
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home(), p[2:])
	}
	return p
}

// ApplyConfig merges profile+override into the target file at top-level key
// granularity. Keys not present in profile/override are left untouched.
// Only json is implemented in this version; toml/yaml return an error so the
// server shows "failed" instead of silently doing nothing.
func ApplyConfig(d protocol.DesiredResource, state *ConfigState) (changed bool, backup string, err error) {
	if d.Format != "json" {
		return false, "", fmt.Errorf("format %q not supported by this agentdeck version yet", d.Format)
	}
	target := expand(d.Path)

	desired := map[string]any{}
	if strings.TrimSpace(d.Content) != "" {
		if err := json.Unmarshal([]byte(d.Content), &desired); err != nil {
			return false, "", fmt.Errorf("profile is not valid JSON: %w", err)
		}
	}
	if strings.TrimSpace(d.Override) != "" {
		ov := map[string]any{}
		if err := json.Unmarshal([]byte(d.Override), &ov); err != nil {
			return false, "", fmt.Errorf("override is not valid JSON: %w", err)
		}
		for k, v := range ov {
			desired[k] = v
		}
	}

	existing := map[string]any{}
	raw, readErr := os.ReadFile(target)
	if readErr == nil && len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return false, "", fmt.Errorf("existing %s is not valid JSON, refusing to touch it: %w", d.Path, err)
		}
	}

	merged := map[string]any{}
	for k, v := range existing {
		merged[k] = v
	}
	prev := state.Keys[d.Path]
	for _, k := range prev {
		if _, still := desired[k]; !still {
			delete(merged, k)
		}
	}
	keys := make([]string, 0, len(desired))
	for k, v := range desired {
		merged[k] = v
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return false, "", err
	}
	out = append(out, '\n')
	if readErr == nil && string(raw) == string(out) {
		state.Keys[d.Path] = keys
		return false, "", nil
	}
	if readErr == nil {
		backup = target + ".agentdeck-bak"
		_ = os.WriteFile(backup, raw, 0o600)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, "", err
	}
	tmp := target + ".agentdeck-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return false, "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		return false, "", err
	}
	state.Keys[d.Path] = keys
	return true, backup, nil
}

// RemoveConfig deletes the keys deck previously wrote to path.
func RemoveConfig(path string, state *ConfigState) error {
	prev := state.Keys[path]
	if len(prev) == 0 {
		return nil
	}
	target := expand(path)
	raw, err := os.ReadFile(target)
	if err != nil {
		delete(state.Keys, path)
		return nil
	}
	existing := map[string]any{}
	if err := json.Unmarshal(raw, &existing); err != nil {
		return err
	}
	for _, k := range prev {
		delete(existing, k)
	}
	out, _ := json.MarshalIndent(existing, "", "  ")
	delete(state.Keys, path)
	return os.WriteFile(target, append(out, '\n'), 0o600)
}
