package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Ken-Chy129/agentdeck/internal/apply"
	"github.com/Ken-Chy129/agentdeck/internal/bundle"
	"github.com/Ken-Chy129/agentdeck/internal/collect"
	"github.com/Ken-Chy129/agentdeck/internal/inventory"
	"github.com/Ken-Chy129/agentdeck/internal/protocol"
)

// ScanLocal digests every skill directory under SkillsDir.
func ScanLocal(c *Config, lock *Lock) []protocol.LocalSkill {
	var out []protocol.LocalSkill
	ents, err := os.ReadDir(c.SkillsDir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(c.SkillsDir, e.Name())
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			continue
		}
		set, err := bundle.ReadDir(p)
		if err != nil {
			continue
		}
		ls := protocol.LocalSkill{Name: e.Name(), Digest: set.Digest(), Size: set.Size()}
		if le, ok := lock.Skills[e.Name()]; ok {
			ls.Managed, ls.Version = true, le.Version
		}
		out = append(out, ls)
	}
	return out
}

type Options struct {
	DryRun    bool
	Inventory bool
	Log       func(format string, a ...any)
}

func appliedState(lock *Lock) []protocol.AppliedState {
	out := []protocol.AppliedState{}
	for name, le := range lock.Skills {
		out = append(out, protocol.AppliedState{ID: le.ResourceID, Kind: "skill", Name: name, Digest: le.Digest})
	}
	for name, le := range lock.Env {
		out = append(out, protocol.AppliedState{ID: le.ResourceID, Kind: "env", Name: name, Digest: le.Digest})
	}
	for name, le := range lock.Configs {
		out = append(out, protocol.AppliedState{ID: le.ResourceID, Kind: "config", Name: name, Digest: le.Digest})
	}
	return out
}

// Run performs one full sync. Server state wins.
func Run(ctx context.Context, c *Config, opt Options) (*protocol.SyncReport, error) {
	logf := opt.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	start := time.Now()
	cl := NewClient(c.Server, c.MachineToken)
	lock := c.LoadLock()
	local := ScanLocal(c, lock)
	localBy := map[string]protocol.LocalSkill{}
	for _, l := range local {
		localBy[l.Name] = l
	}

	req := protocol.SyncRequest{LocalSkills: local, Applied: appliedState(lock), CLIVersion: Version}
	if opt.Inventory {
		logf("collecting inventory…")
		req.Inventory = inventory.Collect(ctx)
		req.Snapshot = &protocol.Snapshot{Configs: collect.Configs(), Exports: collect.Exports()}
	}
	resp, err := cl.Sync(ctx, req)
	if err != nil {
		return nil, err
	}
	rep := &protocol.SyncReport{Resources: []protocol.ResourceResult{}, Jobs: []protocol.JobResult{}}

	var envs []protocol.DesiredResource
	desiredSkills := map[string]bool{}
	desiredEnv := map[string]bool{}
	desiredCfg := map[string]bool{}

	for _, d := range resp.Resources {
		switch d.Kind {
		case "skill":
			desiredSkills[d.Name] = true
			rep.Resources = append(rep.Resources, applySkill(ctx, cl, c, lock, localBy, d, opt.DryRun, logf))
		case "env":
			desiredEnv[d.Name] = true
			envs = append(envs, d)
		case "config":
			desiredCfg[d.Name] = true
			rep.Resources = append(rep.Resources, applyConfig(c, lock, d, opt.DryRun, logf))
		}
	}

	// skills no longer desired
	for name, le := range lock.Skills {
		if desiredSkills[name] {
			continue
		}
		res := protocol.ResourceResult{ID: le.ResourceID, Kind: "skill", Name: name, Action: "removed"}
		dir := filepath.Join(c.SkillsDir, name)
		if l, present := localBy[name]; present {
			if l.Digest != le.Digest {
				if bk, err := backup(c, name); err == nil {
					res.Backup = bk
				}
			}
			if !opt.DryRun {
				if err := os.RemoveAll(dir); err != nil {
					res.Action, res.Error = "failed", err.Error()
				}
			}
		}
		if !opt.DryRun {
			delete(lock.Skills, name)
		}
		logf("removed skill %s", name)
		rep.Resources = append(rep.Resources, res)
	}

	// env: render all at once
	envChanged := false
	for _, d := range envs {
		res := protocol.ResourceResult{ID: d.ID, Kind: "env", Name: d.Name, Digest: d.Digest, Action: "unchanged"}
		if le, ok := lock.Env[d.Name]; !ok || le.Digest != d.Digest {
			res.Action = "applied"
			envChanged = true
		}
		if !opt.DryRun {
			lock.Env[d.Name] = LockEntry{ResourceID: d.ID, Version: d.Version, VersionID: d.VersionID, Digest: d.Digest, SyncedAt: now()}
		}
		rep.Resources = append(rep.Resources, res)
	}
	for name, le := range lock.Env {
		if desiredEnv[name] {
			continue
		}
		envChanged = true
		rep.Resources = append(rep.Resources, protocol.ResourceResult{ID: le.ResourceID, Kind: "env", Name: name, Action: "removed"})
		if !opt.DryRun {
			delete(lock.Env, name)
		}
	}
	if !opt.DryRun && (envChanged || len(envs) > 0) {
		if changed, err := apply.RenderEnv(envs); err != nil {
			rep.Error = "env: " + err.Error()
		} else if changed {
			logf("rendered %d env vars -> %s", len(envs), apply.EnvFile())
		}
	} else if opt.DryRun && envChanged {
		logf("[dry-run] would render %d env vars", len(envs))
	}

	// configs no longer desired
	for name, le := range lock.Configs {
		if desiredCfg[name] {
			continue
		}
		res := protocol.ResourceResult{ID: le.ResourceID, Kind: "config", Name: name, Action: "removed"}
		if !opt.DryRun {
			if err := apply.RemoveConfig(le.Path, &lock.ConfigState); err != nil {
				res.Action, res.Error = "failed", err.Error()
			}
			delete(lock.Configs, name)
		}
		logf("removed config %s", name)
		rep.Resources = append(rep.Resources, res)
	}

	if !opt.DryRun {
		if err := c.SaveLock(lock); err != nil {
			rep.Error = "save lock: " + err.Error()
		}
		if err := Relink(c); err != nil {
			rep.Error = "relink: " + err.Error()
		}
	}

	// import requests
	if len(resp.ImportEnv) > 0 && !opt.DryRun {
		rep.Imported = map[string]string{}
		for _, n := range resp.ImportEnv {
			if v, ok := apply.ReadExport(n); ok {
				rep.Imported[n] = v
				logf("imported %s for deck", n)
			} else {
				logf("import %s: not set in login shell", n)
			}
		}
	}

	// jobs
	for _, j := range resp.Jobs {
		if opt.DryRun {
			logf("[dry-run] would run job #%d %s %s", j.ID, j.Type, string(j.Payload))
			continue
		}
		logf("job #%d %s %s", j.ID, j.Type, string(j.Payload))
		out, err := runJob(ctx, j)
		jr := protocol.JobResult{ID: j.ID, Status: "done", Output: out}
		if err != nil {
			jr.Status = "failed"
			jr.Output = out + "\nerror: " + err.Error()
		}
		rep.Jobs = append(rep.Jobs, jr)
	}

	rep.Duration = time.Since(start).Round(time.Millisecond).String()
	if !opt.DryRun {
		post := protocol.SyncRequest{LocalSkills: ScanLocal(c, lock), Applied: appliedState(lock), CLIVersion: Version}
		if len(rep.Jobs) > 0 && opt.Inventory {
			post.Inventory = inventory.Collect(ctx)
		}
		if _, err := cl.Sync(ctx, post); err != nil {
			logf("warn: post-sync state upload failed: %v", err)
		}
		if err := cl.Report(ctx, *rep); err != nil {
			logf("warn: report failed: %v", err)
		}
		rep.Imported = nil
	}
	return rep, nil
}

func applySkill(ctx context.Context, cl *Client, c *Config, lock *Lock, localBy map[string]protocol.LocalSkill, d protocol.DesiredResource, dry bool, logf func(string, ...any)) protocol.ResourceResult {
	dir := filepath.Join(c.SkillsDir, d.Name)
	l, present := localBy[d.Name]
	le, managed := lock.Skills[d.Name]
	res := protocol.ResourceResult{ID: d.ID, Kind: "skill", Name: d.Name, Digest: d.Digest}
	if present && l.Digest == d.Digest {
		res.Action = "unchanged"
		if !managed || le.Digest != d.Digest {
			lock.Skills[d.Name] = LockEntry{ResourceID: d.ID, Version: d.Version, VersionID: d.VersionID, Digest: d.Digest, SyncedAt: now()}
		}
		return res
	}
	res.Action = "applied"
	if present && (!managed || l.Digest != le.Digest) {
		bk, err := backup(c, d.Name)
		if err != nil {
			res.Action, res.Error = "failed", "backup: "+err.Error()
			return res
		}
		res.Backup = bk
		if !dry {
			logf("backed up local %s -> %s", d.Name, bk)
		}
	}
	if dry {
		logf("[dry-run] would install skill %s v%d", d.Name, d.Version)
		return res
	}
	arch, err := cl.Archive(ctx, d.VersionID)
	if err != nil {
		res.Action, res.Error = "failed", err.Error()
		return res
	}
	set, err := bundle.Unpack(bytes.NewReader(arch))
	if err != nil {
		res.Action, res.Error = "failed", "unpack: "+err.Error()
		return res
	}
	if got := set.Digest(); got != d.Digest {
		res.Action, res.Error = "failed", fmt.Sprintf("digest mismatch: want %s got %s", d.Digest, got)
		return res
	}
	if err := set.WriteDir(dir); err != nil {
		res.Action, res.Error = "failed", err.Error()
		return res
	}
	lock.Skills[d.Name] = LockEntry{ResourceID: d.ID, Version: d.Version, VersionID: d.VersionID, Digest: d.Digest, SyncedAt: now()}
	logf("installed skill %s v%d", d.Name, d.Version)
	return res
}

func applyConfig(c *Config, lock *Lock, d protocol.DesiredResource, dry bool, logf func(string, ...any)) protocol.ResourceResult {
	res := protocol.ResourceResult{ID: d.ID, Kind: "config", Name: d.Name, Digest: d.Digest}
	le, managed := lock.Configs[d.Name]
	if managed && le.Digest == d.Digest && le.Path == d.Path {
		// still verify the file has our keys (user may have hand-edited); cheap to re-apply
	}
	if dry {
		res.Action = "applied"
		if managed && le.Digest == d.Digest {
			res.Action = "unchanged"
		}
		logf("[dry-run] would merge config %s into %s", d.Name, d.Path)
		return res
	}
	if managed && le.Path != "" && le.Path != d.Path {
		_ = apply.RemoveConfig(le.Path, &lock.ConfigState)
	}
	changed, bk, err := apply.ApplyConfig(d, &lock.ConfigState)
	if err != nil {
		res.Action, res.Error = "failed", err.Error()
		return res
	}
	res.Backup = bk
	if changed {
		res.Action = "applied"
		logf("merged config %s -> %s", d.Name, d.Path)
	} else {
		res.Action = "unchanged"
	}
	lock.Configs[d.Name] = LockEntry{ResourceID: d.ID, Version: d.Version, VersionID: d.VersionID, Digest: d.Digest, Path: d.Path, SyncedAt: now()}
	return res
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func backup(c *Config, name string) (string, error) {
	src := filepath.Join(c.SkillsDir, name)
	dstDir := filepath.Join(c.SkillsDir, ".agentdeck-backup")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dstDir, name+"-"+time.Now().Format("20060102-150405"))
	set, err := bundle.ReadDir(src)
	if err != nil {
		return "", err
	}
	return dst, set.WriteDir(dst)
}

// Relink makes sure every managed skill dir has a symlink in each LinkDir.
func Relink(c *Config) error {
	lock := c.LoadLock()
	names := make([]string, 0, len(lock.Skills))
	for n := range lock.Skills {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, ld := range c.LinkDirs {
		if err := os.MkdirAll(ld, 0o755); err != nil {
			return err
		}
		for _, name := range names {
			target := filepath.Join(c.SkillsDir, name)
			if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
				continue
			}
			link := filepath.Join(ld, name)
			rel, err := filepath.Rel(ld, target)
			if err != nil {
				rel = target
			}
			if fi, err := os.Lstat(link); err == nil {
				if fi.Mode()&os.ModeSymlink == 0 {
					continue // real dir owned by user
				}
				if cur, _ := os.Readlink(link); cur == rel || cur == target {
					continue
				}
				if _, err := os.Stat(link); err == nil {
					continue // points somewhere valid, leave it
				}
				os.Remove(link) // dangling
			}
			if err := os.Symlink(rel, link); err != nil && !os.IsExist(err) {
				return err
			}
		}
		les, _ := os.ReadDir(ld)
		for _, le := range les {
			p := filepath.Join(ld, le.Name())
			fi, err := os.Lstat(p)
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				continue
			}
			if _, err := os.Stat(p); err != nil {
				if tgt, _ := os.Readlink(p); strings.Contains(tgt, filepath.Base(c.SkillsDir)) {
					os.Remove(p)
				}
			}
		}
	}
	return nil
}

// ---- jobs (whitelist) ----

var npmPkgRe = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
var npmVerRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+-]*$`)
var brewRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9@._+/-]*$`)

func runJob(ctx context.Context, j protocol.Job) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	switch j.Type {
	case protocol.JobEcho:
		var p struct{ Message string }
		_ = json.Unmarshal(j.Payload, &p)
		return "echo: " + p.Message, nil
	case protocol.JobNpmUpgrade:
		var p struct{ Package, Version string }
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return "", err
		}
		if !npmPkgRe.MatchString(p.Package) {
			return "", fmt.Errorf("refusing package name %q", p.Package)
		}
		if p.Version == "" {
			p.Version = "latest"
		}
		if !npmVerRe.MatchString(p.Version) {
			return "", fmt.Errorf("refusing version %q", p.Version)
		}
		return shell(ctx, "npm", "install", "-g", p.Package+"@"+p.Version)
	case protocol.JobBrewUpgrade:
		var p struct{ Formula string }
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return "", err
		}
		if !brewRe.MatchString(p.Formula) {
			return "", fmt.Errorf("refusing formula %q", p.Formula)
		}
		return shell(ctx, "brew", "upgrade", p.Formula)
	case protocol.JobShell:
		var p struct {
			Cmd        string `json:"cmd"`
			Cwd        string `json:"cwd"`
			TimeoutSec int    `json:"timeout_sec"`
		}
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return "", err
		}
		return runShell(ctx, p.Cmd, p.Cwd, p.TimeoutSec)
	}
	return "", fmt.Errorf("unsupported job type %q", j.Type)
}

// loginShell returns the shell used for `shell` jobs. A login shell is what
// makes nvm/brew/pnpm paths resolve the same way they do in an interactive
// session, which is exactly what you'd get by SSH-ing in yourself.
func loginShell() string {
	if s := os.Getenv("AGENTDECK_SHELL"); s != "" {
		return s
	}
	if s := os.Getenv("SHELL"); s != "" {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	for _, c := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "/bin/sh"
}

// runShell executes cmd through a login shell and returns combined output.
// Output is capped so a runaway command can't blow up the report.
func runShell(ctx context.Context, cmdStr, cwd string, timeoutSec int) (string, error) {
	cmdStr = strings.TrimSpace(cmdStr)
	if cmdStr == "" {
		return "", fmt.Errorf("empty command")
	}
	if timeoutSec <= 0 {
		timeoutSec = 600
	}
	if timeoutSec > 3600 {
		timeoutSec = 3600
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	sh := loginShell()
	cmd := exec.CommandContext(ctx, sh, "-lc", cmdStr)
	if cwd != "" {
		if strings.HasPrefix(cwd, "~") {
			h, _ := os.UserHomeDir()
			cwd = filepath.Join(h, strings.TrimPrefix(cwd, "~"))
		}
		cmd.Dir = cwd
	} else {
		cmd.Dir, _ = os.UserHomeDir()
	}
	cmd.Env = append(os.Environ(), "AGENTDECK_JOB=1", "CI=1", "TERM=dumb", "NO_COLOR=1")
	cmd.Stdin = nil
	isolate(cmd)

	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	start := time.Now()
	err := cmd.Run()
	out := buf.String()
	if len(out) > 200<<10 {
		out = out[:200<<10] + "\n…(output truncated)"
	}
	head := fmt.Sprintf("$ %s\n", cmdStr)
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	tail := fmt.Sprintf("\n[exit %d · %s · %s]", code, time.Since(start).Round(time.Millisecond), sh)
	if ctx.Err() == context.DeadlineExceeded {
		return head + out + fmt.Sprintf("\n[timed out after %ds]", timeoutSec), fmt.Errorf("timed out after %ds", timeoutSec)
	}
	if err != nil {
		return head + out + tail, err
	}
	return head + out + tail, nil
}

func shell(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := "$ " + name + " " + strings.Join(args, " ") + "\n" + buf.String()
	if len(out) > 32*1024 {
		out = out[:32*1024] + "\n…(truncated)"
	}
	return out, err
}
