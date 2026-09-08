package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func nowStr() string { return time.Now().UTC().Format(time.RFC3339) }

const launchdLabel = "dev.kenchy.skillhub.sync"

func launchdPlist() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "LaunchAgents", launchdLabel+".plist")
}

func cmdInstallSchedule(args []string) error {
	fs := flag.NewFlagSet("install-schedule", flag.ExitOnError)
	every := fs.Int("every", 15, "minutes between syncs")
	fs.Parse(reorder(args))
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.EvalSymlinks(self)
	h, _ := os.UserHomeDir()
	logDir := filepath.Join(h, ".config", "skillhub")
	os.MkdirAll(logDir, 0o700)
	// Capture the interactive PATH so launchd/systemd can find nvm's npm, brew, etc.
	pathEnv := os.Getenv("PATH")

	switch runtime.GOOS {
	case "darwin":
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>sync</string><string>-q</string></array>
  <key>StartInterval</key><integer>%d</integer>
  <key>RunAtLoad</key><true/>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string><key>HOME</key><string>%s</string></dict>
  <key>StandardOutPath</key><string>%s/sync.log</string>
  <key>StandardErrorPath</key><string>%s/sync.log</string>
</dict></plist>
`, launchdLabel, self, *every*60, xmlEscape(pathEnv), h, logDir, logDir)
		p := launchdPlist()
		os.MkdirAll(filepath.Dir(p), 0o755)
		exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel)).Run()
		if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
			return err
		}
		out, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), p).CombinedOutput()
		if err != nil {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
		}
		fmt.Printf("installed launchd agent %s (every %d min)\nlog: %s/sync.log\n", launchdLabel, *every, logDir)
	case "linux":
		unitDir := filepath.Join(h, ".config", "systemd", "user")
		os.MkdirAll(unitDir, 0o755)
		svc := fmt.Sprintf("[Unit]\nDescription=SkillHub sync\n\n[Service]\nType=oneshot\nEnvironment=PATH=%s\nExecStart=%s sync -q\n", pathEnv, self)
		tmr := fmt.Sprintf("[Unit]\nDescription=SkillHub sync timer\n\n[Timer]\nOnBootSec=2min\nOnUnitActiveSec=%dmin\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n", *every)
		os.WriteFile(filepath.Join(unitDir, "skillhub-sync.service"), []byte(svc), 0o644)
		os.WriteFile(filepath.Join(unitDir, "skillhub-sync.timer"), []byte(tmr), 0o644)
		for _, a := range [][]string{{"daemon-reload"}, {"enable", "--now", "skillhub-sync.timer"}} {
			if out, err := exec.Command("systemctl", append([]string{"--user"}, a...)...).CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(a, " "), err, out)
			}
		}
		fmt.Printf("installed systemd user timer skillhub-sync.timer (every %d min)\n", *every)
	default:
		return fmt.Errorf("scheduling not supported on %s; run `skillhub sync` from your own scheduler", runtime.GOOS)
	}
	return nil
}

func cmdUninstallSchedule() error {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel)).Run()
		os.Remove(launchdPlist())
		fmt.Println("removed launchd agent")
	case "linux":
		exec.Command("systemctl", "--user", "disable", "--now", "skillhub-sync.timer").Run()
		h, _ := os.UserHomeDir()
		os.Remove(filepath.Join(h, ".config", "systemd", "user", "skillhub-sync.service"))
		os.Remove(filepath.Join(h, ".config", "systemd", "user", "skillhub-sync.timer"))
		fmt.Println("removed systemd user timer")
	}
	return nil
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
