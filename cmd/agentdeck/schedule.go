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

const launchdLabel = "dev.kenchy.agentdeck.sync"

func launchdPlist() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "LaunchAgents", launchdLabel+".plist")
}

func cmdInstallSchedule(args []string) error {
	fs := flag.NewFlagSet("install-schedule", flag.ExitOnError)
	every := fs.Int("every", 15, "minutes between syncs")
	watch := fs.Bool("watch", false, "stay resident and long-poll so console commands run within seconds")
	fs.Parse(reorder(args))
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.EvalSymlinks(self)
	h, _ := os.UserHomeDir()
	logDir := filepath.Join(h, ".config", "agentdeck")
	os.MkdirAll(logDir, 0o700)
	// Capture the interactive PATH so launchd/systemd can find nvm's npm, brew, etc.
	pathEnv := os.Getenv("PATH")

	switch runtime.GOOS {
	case "darwin":
		// Resident mode: keep the process alive and let it long-poll; the
		// timer knob becomes "how often to reconcile" instead of "how often to
		// wake up".
		args := `<string>sync</string><string>-q</string>`
		interval := fmt.Sprintf("  <key>StartInterval</key><integer>%d</integer>\n", *every*60)
		if *watch {
			args = fmt.Sprintf(`<string>watch</string><string>--sync-every</string><string>%d</string>`, *every)
			interval = "  <key>KeepAlive</key><true/>\n  <key>ThrottleInterval</key><integer>10</integer>\n"
		}
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string>%s</array>
%s
  <key>RunAtLoad</key><true/>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string><key>HOME</key><string>%s</string></dict>
  <key>StandardOutPath</key><string>%s/sync.log</string>
  <key>StandardErrorPath</key><string>%s/sync.log</string>
</dict></plist>
`, launchdLabel, self, args, interval, xmlEscape(pathEnv), h, logDir, logDir)
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
		mode := fmt.Sprintf("timer, every %d min", *every)
		if *watch {
			mode = fmt.Sprintf("resident watch, reconcile every %d min", *every)
		}
		fmt.Printf("installed launchd agent %s (%s)\nlog: %s/sync.log\n", launchdLabel, mode, logDir)
	case "linux":
		unitDir := filepath.Join(h, ".config", "systemd", "user")
		os.MkdirAll(unitDir, 0o755)
		if *watch {
			// A long-running unit with Restart=always: survives network drops,
			// server restarts and reboots (with lingering enabled).
			svc := fmt.Sprintf("[Unit]\nDescription=AgentDeck watch (remote jobs + periodic sync)\nAfter=network-online.target\n\n[Service]\nType=simple\nEnvironment=PATH=%s\nExecStart=%s watch --sync-every %d\nRestart=always\nRestartSec=10\n\n[Install]\nWantedBy=default.target\n", pathEnv, self, *every)
			if err := os.WriteFile(filepath.Join(unitDir, "agentdeck-watch.service"), []byte(svc), 0o644); err != nil {
				return err
			}
			// The old timer would duplicate work; stop it if present.
			exec.Command("systemctl", "--user", "disable", "--now", "agentdeck-sync.timer").Run()
			for _, a := range [][]string{{"daemon-reload"}, {"enable", "--now", "agentdeck-watch.service"}} {
				if out, err := exec.Command("systemctl", append([]string{"--user"}, a...)...).CombinedOutput(); err != nil {
					return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(a, " "), err, out)
				}
			}
			fmt.Printf("installed systemd user service agentdeck-watch.service (reconcile every %d min)\n", *every)
			fmt.Println("tip: run `loginctl enable-linger $USER` so it survives logout/reboot")
			return nil
		}
		svc := fmt.Sprintf("[Unit]\nDescription=AgentDeck sync\n\n[Service]\nType=oneshot\nEnvironment=PATH=%s\nExecStart=%s sync -q\n", pathEnv, self)
		tmr := fmt.Sprintf("[Unit]\nDescription=AgentDeck sync timer\n\n[Timer]\nOnBootSec=2min\nOnUnitActiveSec=%dmin\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n", *every)
		os.WriteFile(filepath.Join(unitDir, "agentdeck-sync.service"), []byte(svc), 0o644)
		os.WriteFile(filepath.Join(unitDir, "agentdeck-sync.timer"), []byte(tmr), 0o644)
		for _, a := range [][]string{{"daemon-reload"}, {"enable", "--now", "agentdeck-sync.timer"}} {
			if out, err := exec.Command("systemctl", append([]string{"--user"}, a...)...).CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(a, " "), err, out)
			}
		}
		fmt.Printf("installed systemd user timer agentdeck-sync.timer (every %d min)\n", *every)
	default:
		return fmt.Errorf("scheduling not supported on %s; run `agentdeck sync` from your own scheduler", runtime.GOOS)
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
		exec.Command("systemctl", "--user", "disable", "--now", "agentdeck-sync.timer").Run()
		exec.Command("systemctl", "--user", "disable", "--now", "agentdeck-watch.service").Run()
		h, _ := os.UserHomeDir()
		os.Remove(filepath.Join(h, ".config", "systemd", "user", "agentdeck-sync.service"))
		os.Remove(filepath.Join(h, ".config", "systemd", "user", "agentdeck-sync.timer"))
		os.Remove(filepath.Join(h, ".config", "systemd", "user", "agentdeck-watch.service"))
		fmt.Println("removed systemd user timer")
	}
	return nil
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
