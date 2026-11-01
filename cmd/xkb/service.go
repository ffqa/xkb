package main

import (
	"context"
	"flag"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xpier/xkb/internal/config"
)

const serviceLabel = "dev.xpier.xkb"

func cmdService(cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: xkb service [install|uninstall|start|stop|restart|status|logs]")
	}
	switch args[0] {
	case "install":
		return installService(cfg, args[1:])
	case "uninstall":
		return uninstallService()
	case "start":
		return startService()
	case "stop":
		return stopService()
	case "restart":
		return restartService()
	case "status":
		return statusService(cfg)
	case "logs":
		return logsService()
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func installService(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	port := fs.Int("port", cfg.ServerPort, "HTTP port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	bin, err := persistentBinary()
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".xkb", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	logFile := filepath.Join(logDir, "xkb.log")

	switch runtime.GOOS {
	case "darwin":
		dir := filepath.Join(home, "Library", "LaunchAgents")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(dir, serviceLabel+".plist")
		env := fmt.Sprintf("<key>HOME</key><string>%s</string>", html.EscapeString(home))
		if v := os.Getenv("XKB_HOME"); v != "" {
			env += fmt.Sprintf("<key>XKB_HOME</key><string>%s</string>", html.EscapeString(v))
		}
		env += fmt.Sprintf("<key>PATH</key><string>%s</string>", html.EscapeString(os.Getenv("PATH")))
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array>
    <string>%s</string><string>server</string><string>--port</string><string>%d</string>
  </array>
  <key>EnvironmentVariables</key><dict>%s</dict>
  <key>WorkingDirectory</key><string>%s</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, serviceLabel, html.EscapeString(bin), *port, env, html.EscapeString(home), html.EscapeString(logFile), html.EscapeString(logFile))
		if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
			return err
		}
		uid := os.Getuid()
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, serviceLabel)).Run()
		if out, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), path).CombinedOutput(); err != nil {
			_ = exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", uid, serviceLabel)).Run()
			if len(out) > 0 {
				fmt.Print(string(out))
			}
		}
		fmt.Printf("installed %s (launchd)\n  binary: %s\n  port: %d\n  log: %s\n", serviceLabel, bin, *port, logFile)
		fmt.Println("  status: xkb service status")
		return nil

	case "linux":
		dir := filepath.Join(home, ".config", "systemd", "user")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(dir, "xkb.service")
		env := "Environment=HOME=" + home
		if v := os.Getenv("XKB_HOME"); v != "" {
			env += "\nEnvironment=XKB_HOME=" + v
		}
		unit := fmt.Sprintf(`[Unit]
Description=xkb scheduler and HTTP/SSE server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s server --port %d
Restart=always
RestartSec=3
%s

[Install]
WantedBy=default.target
`, bin, *port, env)
		if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
			return err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", "xkb.service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl enable: %w: %s", err, out)
		}
		fmt.Printf("installed xkb.service (systemd user)\n  binary: %s\n  port: %d\n  logs: journalctl --user -u xkb -f\n", bin, *port)
		return nil
	default:
		return fmt.Errorf("service install is not supported on %s", runtime.GOOS)
	}
}

func uninstallService() error {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		uid := os.Getuid()
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, serviceLabel)).Run()
		_ = os.Remove(filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"))
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", "xkb.service").Run()
		_ = os.Remove(filepath.Join(home, ".config", "systemd", "user", "xkb.service"))
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	default:
		return fmt.Errorf("service uninstall is not supported on %s", runtime.GOOS)
	}
	fmt.Println("uninstalled xkb service")
	return nil
}

func startService() error {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		uid := os.Getuid()
		path := filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("service not installed; run xkb service install")
		}
		if out, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), path).CombinedOutput(); err != nil {
			if kerr := exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", uid, serviceLabel)).Run(); kerr != nil {
				return fmt.Errorf("launchctl bootstrap: %w: %s", err, out)
			}
		}
	case "linux":
		if out, err := exec.Command("systemctl", "--user", "start", "xkb.service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl start: %w: %s", err, out)
		}
	}
	fmt.Println("started xkb service")
	return nil
}

func stopService() error {
	switch runtime.GOOS {
	case "darwin":
		uid := os.Getuid()
		if out, err := exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, serviceLabel)).CombinedOutput(); err != nil {
			if !strings.Contains(string(out), "No such process") && !strings.Contains(string(out), "Could not find service") {
				return fmt.Errorf("launchctl bootout: %w: %s", err, out)
			}
		}
	case "linux":
		if out, err := exec.Command("systemctl", "--user", "stop", "xkb.service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl stop: %w: %s", err, out)
		}
	}
	fmt.Println("stopped xkb service")
	return nil
}

func restartService() error {
	switch runtime.GOOS {
	case "darwin":
		uid := os.Getuid()
		label := fmt.Sprintf("gui/%d/%s", uid, serviceLabel)
		if out, err := exec.Command("launchctl", "kickstart", "-k", label).CombinedOutput(); err != nil {
			if out2, err2 := exec.Command("launchctl", "print", label).CombinedOutput(); err2 != nil {
				return fmt.Errorf("service not installed; run xkb service install")
			} else {
				_ = out2
			}
			return fmt.Errorf("launchctl kickstart: %w: %s", err, out)
		}
	case "linux":
		if out, err := exec.Command("systemctl", "--user", "restart", "xkb.service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl restart: %w: %s", err, out)
		}
	}
	fmt.Println("restarted xkb service")
	return nil
}

func statusService(cfg *config.Config) error {
	switch runtime.GOOS {
	case "darwin":
		uid := os.Getuid()
		out, err := exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", uid, serviceLabel)).CombinedOutput()
		if err != nil {
			fmt.Printf("service %s is not loaded\n", serviceLabel)
		} else {
			for _, line := range strings.Split(string(out), "\n") {
				t := strings.TrimSpace(line)
				if strings.HasPrefix(t, "state =") || strings.HasPrefix(t, "pid =") || strings.HasPrefix(t, "program =") {
					fmt.Println("  " + t)
				}
			}
		}
	case "linux":
		out, _ := exec.Command("systemctl", "--user", "status", "xkb.service", "--no-pager").CombinedOutput()
		fmt.Println(string(out))
	}
	port := cfg.ServerPort
	if port <= 0 {
		port = 7790
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/api/health", port), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("health: unavailable on :%d\n", port)
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("health: %s\n", strings.TrimSpace(string(body)))
	return nil
}

func logsService() error {
	home, _ := os.UserHomeDir()
	logFile := filepath.Join(home, ".xkb", "logs", "xkb.log")
	if _, err := os.Stat(logFile); err != nil {
		fmt.Printf("log file not found: %s\n", logFile)
		return nil
	}
	cmd := exec.Command("tail", "-n", "100", logFile)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// persistentBinary returns a stable path for the service. Repo builds are
// copied to ~/.local/bin; Homebrew installs use the versionless symlink.
func persistentBinary() (string, error) {
	raw, err := os.Executable()
	if err != nil {
		return "", err
	}
	real := raw
	if r, err := filepath.EvalSymlinks(raw); err == nil {
		real = r
	}
	cleanRaw := filepath.ToSlash(raw)
	cleanReal := filepath.ToSlash(real)
	if strings.HasPrefix(cleanRaw, "/opt/homebrew/bin/") ||
		strings.HasPrefix(cleanRaw, "/usr/local/bin/") ||
		strings.HasPrefix(cleanRaw, "/home/linuxbrew/.linuxbrew/bin/") {
		return raw, nil
	}
	if idx := strings.Index(cleanReal, "/Cellar/xkb/"); idx >= 0 {
		stable := filepath.Join(cleanReal[:idx], "bin", "xkb")
		if _, err := os.Stat(stable); err == nil {
			return stable, nil
		}
	}
	home, _ := os.UserHomeDir()
	target := filepath.Join(home, ".local", "bin", "xkb")
	if cleanRaw == filepath.ToSlash(target) || cleanReal == filepath.ToSlash(target) {
		return target, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := copyExecutable(real, target); err != nil {
		return "", err
	}
	return target, nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(dst)
		if err2 := os.Rename(tmp, dst); err2 != nil {
			return err2
		}
	}
	return nil
}
