package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xpier/xkb/internal/config"
)

const (
	autoUpdateFirstDelay = 45 * time.Second
	autoUpdateInterval   = time.Hour
)

// startAutoUpdate checks for a newer release in the background. It only runs
// for persistent installs (~/.local/bin or Homebrew), never for repo builds.
func startAutoUpdate(ctx context.Context, cfg *config.Config) {
	if cfg == nil || !cfg.AutoUpdate {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if !autoUpdateEligible(exe) {
		return
	}
	go func() {
		timer := time.NewTimer(autoUpdateFirstDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			uctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			updated, err := updateSelf(uctx, false, false, false)
			cancel()
			if err != nil {
				log.Printf("xkb auto-update: %v", err)
			} else if updated {
				restartSelf()
			}
			timer.Reset(autoUpdateInterval)
		}
	}()
}

func autoUpdateEligible(exe string) bool {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	clean := filepath.ToSlash(exe)
	home, _ := os.UserHomeDir()
	local := filepath.ToSlash(filepath.Join(home, ".local", "bin", "xkb"))
	return clean == local ||
		strings.Contains(clean, "/Cellar/xkb/") ||
		strings.Contains(clean, "/opt/homebrew/opt/xkb/") ||
		strings.Contains(clean, "/usr/local/Cellar/xkb/")
}

func restartSelf() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if isBrewInstall(exe) {
		if p, lerr := exec.LookPath("xkb"); lerr == nil {
			exe = p
		}
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		log.Printf("xkb auto-update: restart failed: %v", err)
	}
}
