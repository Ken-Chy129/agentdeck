// Package inventory collects CLI/runtime versions on the local machine.
// It never reads environment variables or config file contents; only names,
// versions and binary paths leave the machine.
package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Ken-Chy129/agentdeck/internal/protocol"
)

// Known agent CLIs and the npm package that provides them.
var knownTools = []struct{ bin, pkg string }{
	{"claude", "@anthropic-ai/claude-code"},
	{"codex", "@openai/codex"},
	{"gemini", "@google/gemini-cli"},
	{"cursor-agent", ""},
	{"opencode", "opencode-ai"},
	{"aider", ""},
	{"gh", ""},
	{"agentdeck", ""},
}

var runtimes = []struct {
	bin  string
	args []string
}{
	{"node", []string{"--version"}},
	{"npm", []string{"--version"}},
	{"pnpm", []string{"--version"}},
	{"bun", []string{"--version"}},
	{"go", []string{"version"}},
	{"python3", []string{"--version"}},
	{"uv", []string{"--version"}},
	{"brew", []string{"--version"}},
	{"docker", []string{"--version"}},
	{"git", []string{"--version"}},
}

var verRe = regexp.MustCompile(`\d+\.\d+(\.\d+)?([-+][0-9A-Za-z.\-]+)?`)

func run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func Collect(ctx context.Context) *protocol.Inventory {
	inv := &protocol.Inventory{OS: runtime.GOOS, Arch: runtime.GOARCH}
	inv.Hostname, _ = os.Hostname()

	for _, r := range runtimes {
		p, err := exec.LookPath(r.bin)
		if err != nil {
			continue
		}
		out, _ := run(ctx, r.bin, r.args...)
		inv.Runtimes = append(inv.Runtimes, protocol.Runtime{Name: r.bin, Version: firstVersion(out), Path: p})
	}

	npmPkgs := map[string]string{}
	if _, err := exec.LookPath("npm"); err == nil {
		if out, err := run(ctx, "npm", "ls", "-g", "--depth=0", "--json"); err == nil || out != "" {
			var doc struct {
				Dependencies map[string]struct {
					Version string `json:"version"`
				} `json:"dependencies"`
			}
			if jerr := json.Unmarshal([]byte(out), &doc); jerr == nil {
				for name, d := range doc.Dependencies {
					npmPkgs[name] = d.Version
					inv.NpmGlobal = append(inv.NpmGlobal, protocol.CLITool{Name: name, Package: name, Version: d.Version, Source: "npm-global"})
				}
			} else {
				inv.Errors = append(inv.Errors, "npm ls: "+jerr.Error())
			}
		}
	}
	sort.Slice(inv.NpmGlobal, func(i, j int) bool { return inv.NpmGlobal[i].Name < inv.NpmGlobal[j].Name })

	if _, err := exec.LookPath("brew"); err == nil {
		if out, err := run(ctx, "brew", "list", "--versions"); err == nil {
			for _, line := range strings.Split(out, "\n") {
				f := strings.Fields(line)
				if len(f) >= 2 {
					inv.Brew = append(inv.Brew, protocol.CLITool{Name: f[0], Version: f[len(f)-1], Source: "brew"})
				}
			}
		}
	}

	for _, t := range knownTools {
		p, err := exec.LookPath(t.bin)
		if err != nil {
			continue
		}
		out, _ := run(ctx, t.bin, "--version")
		tool := protocol.CLITool{Name: t.bin, Version: firstVersion(out), Path: p, Source: "binary", Package: t.pkg}
		real, _ := filepath.EvalSymlinks(p)
		switch {
		case strings.Contains(real, "/.local/share/claude/"):
			tool.Source = "native" // Anthropic's curl installer; upgrade with `claude update`
		case strings.Contains(real, "/Cellar/") || strings.Contains(real, "/homebrew/") || strings.Contains(real, "/linuxbrew/"):
			tool.Source = "brew"
		case strings.Contains(real, "/node_modules/") || strings.Contains(p, "/.nvm/") || strings.Contains(p, "/node_modules/"):
			tool.Source = "npm-global"
		}
		if t.pkg != "" {
			if v, ok := npmPkgs[t.pkg]; ok && tool.Source == "npm-global" && tool.Version == "" {
				tool.Version = v
			}
			if _, ok := npmPkgs[t.pkg]; !ok && tool.Source == "npm-global" {
				tool.Source = "binary"
			}
		}
		inv.Tools = append(inv.Tools, tool)
	}

	inv.Agents = detectAgents()
	return inv
}

func firstVersion(s string) string {
	return verRe.FindString(s)
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func detectAgents() []protocol.Agent {
	dirs := []struct{ name, dir string }{
		{"agents", filepath.Join(home(), ".agents", "skills")},
		{"claude", filepath.Join(home(), ".claude", "skills")},
		{"codex", filepath.Join(home(), ".codex", "skills")},
		{"gemini", filepath.Join(home(), ".gemini", "skills")},
	}
	var out []protocol.Agent
	for _, d := range dirs {
		a := protocol.Agent{Name: d.name, SkillsDir: d.dir}
		if ents, err := os.ReadDir(d.dir); err == nil {
			a.Exists = true
			for _, e := range ents {
				if strings.HasPrefix(e.Name(), ".") {
					continue
				}
				a.SkillN++
			}
		}
		out = append(out, a)
	}
	return out
}

// Summary renders a short human-readable view (used by `agentdeck inventory`).
func Summary(inv *protocol.Inventory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s/%s\n", inv.Hostname, inv.OS, inv.Arch)
	fmt.Fprintf(&b, "\nruntimes:\n")
	for _, r := range inv.Runtimes {
		fmt.Fprintf(&b, "  %-10s %-12s %s\n", r.Name, r.Version, r.Path)
	}
	fmt.Fprintf(&b, "\ntools:\n")
	for _, t := range inv.Tools {
		fmt.Fprintf(&b, "  %-14s %-12s %-10s %s\n", t.Name, t.Version, t.Source, t.Path)
	}
	fmt.Fprintf(&b, "\nnpm -g: %d packages, brew: %d formulae\n", len(inv.NpmGlobal), len(inv.Brew))
	for _, a := range inv.Agents {
		if a.Exists {
			fmt.Fprintf(&b, "  %-8s %3d skills  %s\n", a.Name, a.SkillN, a.SkillsDir)
		}
	}
	return b.String()
}
