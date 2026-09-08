// skillhub is the per-machine CLI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Ken-Chy129/skillhub/internal/bundle"
	"github.com/Ken-Chy129/skillhub/internal/inventory"
	"github.com/Ken-Chy129/skillhub/internal/protocol"
	"github.com/Ken-Chy129/skillhub/internal/sync"
)

const usage = `skillhub %s — sync skills & report CLI inventory to your SkillHub server

usage:
  skillhub login <server-url> <enroll-token> [--name NAME]   enroll this machine
  skillhub sync [--dry-run] [--no-inventory] [-q]            reconcile skills, run queued jobs
  skillhub status                                            show lock vs local state
  skillhub push <dir>... [--note TEXT] [--no-assign]         publish local skill dir(s) as new version
  skillhub inventory [--json]                                print what would be reported
  skillhub relink                                            rebuild agent-dir symlinks
  skillhub install-schedule [--every MIN] | uninstall-schedule   launchd (macOS) / systemd --user (linux) timer
  skillhub config                                            print config path & contents
`

func main() {
	if len(os.Args) < 2 {
		fmt.Printf(usage, sync.Version)
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "login":
		err = cmdLogin(ctx, os.Args[2:])
	case "sync":
		err = cmdSync(ctx, os.Args[2:])
	case "status":
		err = cmdStatus()
	case "push":
		err = cmdPush(ctx, os.Args[2:])
	case "inventory":
		err = cmdInventory(ctx, os.Args[2:])
	case "relink":
		var c *sync.Config
		if c, err = sync.LoadConfig(); err == nil {
			err = sync.Relink(c)
		}
	case "install-schedule":
		err = cmdInstallSchedule(os.Args[2:])
	case "uninstall-schedule":
		err = cmdUninstallSchedule()
	case "config":
		fmt.Println(sync.ConfigPath())
		b, e := os.ReadFile(sync.ConfigPath())
		if e == nil {
			var c sync.Config
			_ = json.Unmarshal(b, &c)
			c.MachineToken = c.MachineToken[:min(8, len(c.MachineToken))] + "…"
			out, _ := json.MarshalIndent(c, "", "  ")
			fmt.Println(string(out))
		}
	case "version", "--version", "-v":
		fmt.Println("skillhub " + sync.Version)
	default:
		fmt.Printf(usage, sync.Version)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	name := fs.String("name", "", "machine name (default: short hostname)")
	skillsDir := fs.String("skills-dir", "", "canonical skills dir (default ~/.agents/skills)")
	fs.Parse(reorder(args))
	if fs.NArg() < 2 {
		return fmt.Errorf("usage: skillhub login <server-url> <enroll-token> [--name NAME]")
	}
	server, tok := strings.TrimRight(fs.Arg(0), "/"), fs.Arg(1)
	host, _ := os.Hostname()
	if *name == "" {
		*name = strings.Split(host, ".")[0]
	}
	cl := sync.NewClient(server, "")
	resp, err := cl.Enroll(ctx, protocol.EnrollRequest{EnrollToken: tok, Name: *name, OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: host})
	if err != nil {
		return err
	}
	c := &sync.Config{Server: server, MachineID: resp.MachineID, MachineToken: resp.MachineToken, Name: resp.Name, SkillsDir: *skillsDir}
	if err := c.Save(); err != nil {
		return err
	}
	fmt.Printf("enrolled as %q (%s) at %s\nconfig: %s\nnext: skillhub sync\n", resp.Name, resp.MachineID, server, sync.ConfigPath())
	return nil
}

func cmdSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "show what would change")
	noInv := fs.Bool("no-inventory", false, "skip CLI inventory collection")
	quiet := fs.Bool("q", false, "quiet")
	fs.Parse(reorder(args))
	c, err := sync.LoadConfig()
	if err != nil {
		return err
	}
	logf := func(f string, a ...any) { fmt.Printf(f+"\n", a...) }
	if *quiet {
		logf = func(string, ...any) {}
	}
	rep, err := sync.Run(ctx, c, sync.Options{DryRun: *dry, Inventory: !*noInv, Log: logf})
	if err != nil {
		return err
	}
	if !*quiet {
		counts := map[string]int{}
		for _, s := range rep.Skills {
			counts[s.Action]++
		}
		var parts []string
		for _, k := range []string{"installed", "updated", "removed", "unchanged", "failed"} {
			if counts[k] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
			}
		}
		if len(parts) == 0 {
			parts = []string{"no skills assigned"}
		}
		fmt.Printf("sync done in %s: %s; %d jobs\n", rep.Duration, strings.Join(parts, ", "), len(rep.Jobs))
		for _, s := range rep.Skills {
			if s.Error != "" {
				fmt.Printf("  ! %s: %s\n", s.Name, s.Error)
			}
		}
		for _, j := range rep.Jobs {
			fmt.Printf("  job #%d %s\n%s\n", j.ID, j.Status, indent(j.Output))
		}
		if rep.Error != "" {
			fmt.Println("  ! " + rep.Error)
		}
	}
	return nil
}

func cmdStatus() error {
	c, err := sync.LoadConfig()
	if err != nil {
		return err
	}
	fmt.Printf("server: %s\nmachine: %s (%s)\nskills dir: %s\n\n", c.Server, c.Name, c.MachineID, c.SkillsDir)
	lock := c.LoadLock()
	local := sync.ScanLocal(c, lock)
	managed, other := 0, 0
	for _, l := range local {
		if l.Managed {
			managed++
			le := lock.Skills[l.Name]
			state := "ok"
			if le.Digest != l.Digest {
				state = "MODIFIED locally"
			}
			fmt.Printf("  %-32s v%-3d %s\n", l.Name, le.Version, state)
		} else {
			other++
		}
	}
	for name := range lock.Skills {
		found := false
		for _, l := range local {
			if l.Name == name {
				found = true
			}
		}
		if !found {
			fmt.Printf("  %-32s      MISSING (deleted locally, will be reinstalled)\n", name)
		}
	}
	fmt.Printf("\n%d managed, %d unmanaged local skills\n", managed, other)
	return nil
}

func cmdPush(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	note := fs.String("note", "", "version note")
	noAssign := fs.Bool("no-assign", false, "do not assign to this machine")
	fs.Parse(reorder(args))
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: skillhub push <dir>...")
	}
	c, err := sync.LoadConfig()
	if err != nil {
		return err
	}
	cl := sync.NewClient(c.Server, c.MachineToken)
	lock := c.LoadLock()
	for _, dir := range fs.Args() {
		dir = filepath.Clean(dir)
		name := filepath.Base(dir)
		set, err := bundle.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		if err := set.ValidateSkill(); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		arch, err := set.Pack()
		if err != nil {
			return err
		}
		out, err := cl.Publish(ctx, name, arch, *note, !*noAssign)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		created, _ := out["created"].(bool)
		v, _ := out["version"].(map[string]any)
		ver, _ := v["version"].(float64)
		vid, _ := v["id"].(float64)
		if created {
			fmt.Printf("published %s v%d (%s, %d files)\n", name, int(ver), humanSize(set.Size()), len(set.Files))
		} else {
			fmt.Printf("%s unchanged (v%d)\n", name, int(ver))
		}
		// If this dir is the canonical one, mark it managed at this exact version so the next
		// sync sees it as unchanged instead of backing it up.
		if abs, _ := filepath.Abs(dir); abs == filepath.Join(c.SkillsDir, name) && !*noAssign {
			lock.Skills[name] = sync.LockEntry{Version: int(ver), VersionID: int64(vid), Digest: set.Digest(), SyncedAt: nowStr()}
		}
	}
	return c.SaveLock(lock)
}

func cmdInventory(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("inventory", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	inv := inventory.Collect(ctx)
	if *asJSON {
		b, _ := json.MarshalIndent(inv, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	fmt.Print(inventory.Summary(inv))
	return nil
}

// reorder moves flags after positionals to the front so `login URL TOKEN --name x` works.
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && needsValue(a) {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

func needsValue(flag string) bool {
	switch strings.TrimLeft(flag, "-") {
	case "name", "note", "skills-dir", "every":
		return true
	}
	return false
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}
