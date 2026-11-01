package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCreatesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XKB_HOME", home)
	t.Setenv("HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrent != 5 || cfg.DoneMaxCount != 20 || cfg.DoneTTLHours != 24 || cfg.RunTimeoutMinutes != 30 {
		t.Fatalf("defaults: %+v", cfg)
	}
	if cfg.SmallExecAgent.Agent != "omp" || cfg.LargeExecAgent.Agent != "devin" {
		t.Fatalf("routes: %+v %+v", cfg.SmallExecAgent, cfg.LargeExecAgent)
	}
	if _, err := os.Stat(filepath.Join(home, "config.yaml")); err != nil {
		t.Fatalf("config file not created: %v", err)
	}
	if cfg.DBPath != filepath.Join(home, "xkb.db") {
		t.Fatalf("db path: %s", cfg.DBPath)
	}
}

func TestLoadFallsBackToXIssueConfig(t *testing.T) {
	home := t.TempDir()
	xkbHome := t.TempDir()
	t.Setenv("XKB_HOME", xkbHome)
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".xissue"), 0o755)
	err := os.WriteFile(filepath.Join(home, ".xissue", "config.yaml"),
		[]byte("listen: 127.0.0.1:9999\ntoken: secret\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.XIssueURL != "http://127.0.0.1:9999" || cfg.XIssueToken != "secret" {
		t.Fatalf("xissue fallback: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XKB_HOME", home)
	t.Setenv("HOME", t.TempDir())
	body := "maxConcurrent: 3\ndoneMaxCount: 7\nxissueURL: http://example.test:1\nxissueToken: t\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrent != 3 || cfg.DoneMaxCount != 7 || cfg.XIssueURL != "http://example.test:1" || cfg.XIssueToken != "t" {
		t.Fatalf("overrides: %+v", cfg)
	}
}
