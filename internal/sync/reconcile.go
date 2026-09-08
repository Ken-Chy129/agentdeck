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
	"strings"
	"time"

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

// Run performs one full sync. Server state wins; local edits to managed skills
// are moved to <SkillsDir>/.agentdeck-backup/<name>-<ts>/ before being replaced.
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

	req := protocol.SyncRequest{LocalSkills: local, CLIVersion: Version}
	if opt.Inventory {
		logf("collecting inventory…")
		req.Inventory = inventory.Collect(ctx)
		req.Snapshot = &protocol.Snapshot{Configs: collect.Configs(), Exports: collect.Exports()}
	}
	resp, err := cl.Sync(ctx, req)
	if err != nil {
		return nil, err
	}
	rep := &protocol.SyncReport{Skills: []protocol.SkillResult{}, Jobs: []protocol.JobResult{}}

	desired := map[string]protocol.DesiredSkill{}
	for _, d := range resp.Skills {
		desired[d.Name] = d
	}

	// install / update
	for _, d := range resp.Skills {
		dir := filepath.Join(c.SkillsDir, d.Name)
		l, present := localBy[d.Name]
		le, managed := lock.Skills[d.Name]
		res := protocol.SkillResult{Name: d.Name, To: d.Version}
		switch {
		case present && l.Digest == d.Digest:
			res.Action = "unchanged"
			if !managed || le.Digest != d.Digest {
				lock.Skills[d.Name] = LockEntry{Version: d.Version, VersionID: d.VersionID, Digest: d.Digest, SyncedAt: now()}
			}
		default:
			if present {
				if managed {
					res.From = le.Version
					res.Action = "updated"
				} else {
					res.Action = "installed"
				}
				if !managed || l.Digest != le.Digest {
					// unmanaged dir or locally-modified managed dir -> back up first
					bk, err := backup(c, d.Name)
					if err != nil {
						res.Action, res.Error = "failed", "backup: "+err.Error()
						rep.Skills = append(rep.Skills, res)
						continue
					}
					res.Backup = bk
					if !opt.DryRun {
						logf("backed up local %s -> %s", d.Name, bk)
					}
				}
			} else {
				res.Action = "installed"
			}
			if opt.DryRun {
				logf("[dry-run] would %s %s v%d", res.Action, d.Name, d.Version)
				rep.Skills = append(rep.Skills, res)
				continue
			}
			arch, err := cl.Archive(ctx, d.Name, d.VersionID)
			if err != nil {
				res.Action, res.Error = "failed", err.Error()
				rep.Skills = append(rep.Skills, res)
				continue
			}
			set, err := bundle.Unpack(bytes.NewReader(arch))
			if err != nil {
				res.Action, res.Error = "failed", "unpack: "+err.Error()
				rep.Skills = append(rep.Skills, res)
				continue
			}
			if got := set.Digest(); got != d.Digest {
				res.Action, res.Error = "failed", fmt.Sprintf("digest mismatch: want %s got %s", d.Digest, got)
				rep.Skills = append(rep.Skills, res)
				continue
			}
			if err := set.WriteDir(dir); err != nil {
				res.Action, res.Error = "failed", err.Error()
				rep.Skills = append(rep.Skills, res)
				continue
			}
			lock.Skills[d.Name] = LockEntry{Version: d.Version, VersionID: d.VersionID, Digest: d.Digest, SyncedAt: now()}
			logf("%s %s v%d", res.Action, d.Name, d.Version)
		}
		rep.Skills = append(rep.Skills, res)
	}

	// remove: managed skills no longer desired
	for name, le := range lock.Skills {
		if _, want := desired[name]; want {
			continue
		}
		res := protocol.SkillResult{Name: name, Action: "removed", From: le.Version}
		dir := filepath.Join(c.SkillsDir, name)
		if l, present := localBy[name]; present {
			if l.Digest != le.Digest {
				bk, err := backup(c, name)
				if err == nil {
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
			logf("removed %s", name)
		} else {
			logf("[dry-run] would remove %s", name)
		}
		rep.Skills = append(rep.Skills, res)
	}

	if !opt.DryRun {
		if err := c.SaveLock(lock); err != nil {
			rep.Error = "save lock: " + err.Error()
		}
		if err := Relink(c); err != nil {
			rep.Error = "relink: " + err.Error()
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
		// Re-scan so the server sees post-sync state (and refreshed inventory if a job ran).
		post := protocol.SyncRequest{LocalSkills: ScanLocal(c, c.LoadLock()), CLIVersion: Version}
		if len(rep.Jobs) > 0 && opt.Inventory {
			post.Inventory = inventory.Collect(ctx)
		}
		if _, err := cl.Sync(ctx, post); err != nil {
			logf("warn: post-sync state upload failed: %v", err)
		}
		if err := cl.Report(ctx, *rep); err != nil {
			logf("warn: report failed: %v", err)
		}
	}
	return rep, nil
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

// Relink makes sure every *managed* skill dir in SkillsDir has a symlink in each
// LinkDir (e.g. ~/.claude/skills/<name> -> ../../.agents/skills/<name>). Existing
// real directories or foreign symlinks in LinkDirs are left untouched; unmanaged
// skills are not linked.
func Relink(c *Config) error {
	lock := c.LoadLock()
	for _, ld := range c.LinkDirs {
		if err := os.MkdirAll(ld, 0o755); err != nil {
			return err
		}
		for name := range lock.Skills {
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
				if fi.Mode()&os.ModeSymlink != 0 {
					if cur, _ := os.Readlink(link); cur == rel || cur == target {
						continue
					}
					if resolved, err := filepath.EvalSymlinks(link); err != nil || resolved != target {
						// dangling or points elsewhere: only replace if dangling
						if err == nil {
							continue
						}
						os.Remove(link)
					} else {
						continue
					}
				} else {
					continue // real dir/file owned by user
				}
			}
			if err := os.Symlink(rel, link); err != nil && !os.IsExist(err) {
				return err
			}
		}
		// prune dangling symlinks we may have created for removed skills
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
	}
	return "", fmt.Errorf("unsupported job type %q", j.Type)
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
