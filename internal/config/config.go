// Package config loads ~/.xkb/config.yaml and falls back to the xissue config
// for the local daemon address and token.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xpier/xkb/internal/gate"
)

// AgentModel is an xissue agent plus an optional model override.
type AgentModel struct {
	Agent string `yaml:"agent" json:"agent"`
	Model string `yaml:"model" json:"model"`
}

// Config is the xkb runtime configuration.
type Config struct {
	DBPath               string     `yaml:"dbPath"`
	MaxConcurrent        int        `yaml:"maxConcurrent"`
	DoneMaxCount         int        `yaml:"doneMaxCount"`
	DoneTTLHours         int        `yaml:"doneTTLHours"`
	XIssueURL            string     `yaml:"xissueURL"`
	XIssueToken          string     `yaml:"xissueToken"`
	XIssueDataDir        string     `yaml:"xissueDataDir"`
	SpecAgent            AgentModel `yaml:"specAgent"`
	SmallExecAgent       AgentModel `yaml:"smallExecAgent"`
	LargeExecAgent       AgentModel `yaml:"largeExecAgent"`
	SmallMaxTouches      int        `yaml:"smallMaxTouches"`
	LargeMaxTouches      int        `yaml:"largeMaxTouches"`
	SmallMaxDiffFiles    int        `yaml:"smallMaxDiffFiles"`
	SmallMaxDiffLines    int        `yaml:"smallMaxDiffLines"`
	LargeMaxDiffFiles    int        `yaml:"largeMaxDiffFiles"`
	LargeMaxDiffLines    int        `yaml:"largeMaxDiffLines"`
	TodoStallMinutes     int        `yaml:"todoStallMinutes"`
	VerifyTimeoutSeconds int        `yaml:"verifyTimeoutSeconds"`
	ReviewTimeoutSeconds int        `yaml:"reviewTimeoutSeconds"`
	RunTimeoutMinutes    int        `yaml:"runTimeoutMinutes"`
	AutoUpdate           bool       `yaml:"autoUpdate"`
	ServerPort           int        `yaml:"serverPort"`
	ServerURL            string     `yaml:"serverURL"`
}

// Home returns the xkb data directory (XKB_HOME overrides ~/.xkb).
func Home() string {
	if h := os.Getenv("XKB_HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".xkb"
	}
	return filepath.Join(h, ".xkb")
}

// Default returns the documented defaults.
func Default() *Config {
	home := Home()
	userHome, _ := os.UserHomeDir()
	return &Config{
		DBPath:               filepath.Join(home, "xkb.db"),
		MaxConcurrent:        5,
		DoneMaxCount:         20,
		DoneTTLHours:         24,
		XIssueURL:            "http://127.0.0.1:7789",
		XIssueDataDir:        filepath.Join(userHome, ".xissue"),
		SpecAgent:            AgentModel{Agent: "devin", Model: "swe-2-high"},
		SmallExecAgent:       AgentModel{Agent: "omp"},
		LargeExecAgent:       AgentModel{Agent: "devin", Model: "swe-2-high"},
		SmallMaxTouches:      2,
		LargeMaxTouches:      3,
		SmallMaxDiffFiles:    2,
		SmallMaxDiffLines:    120,
		LargeMaxDiffFiles:    4,
		LargeMaxDiffLines:    300,
		TodoStallMinutes:     15,
		VerifyTimeoutSeconds: 180,
		ReviewTimeoutSeconds: 600,
		RunTimeoutMinutes:    30,
		ServerPort:           7790,
		ServerURL:            "http://127.0.0.1:7790",
		AutoUpdate:           true,
	}
}

// Load reads ~/.xkb/config.yaml, creating it with defaults when absent.
func Load() (*Config, error) {
	cfg := Default()
	path := filepath.Join(Home(), "config.yaml")
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		if err := save(path, cfg); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		raw := struct {
			*Config `yaml:",inline"`
		}{cfg}
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}

	if cfg.XIssueURL == "" || cfg.XIssueToken == "" {
		applyXIssueConfig(cfg)
	}
	fillDefaults(cfg)
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(Home(), "xkb.db")
	}
	return cfg, nil
}

func fillDefaults(cfg *Config) {
	d := Default()
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = d.MaxConcurrent
	}
	if cfg.DoneMaxCount <= 0 {
		cfg.DoneMaxCount = d.DoneMaxCount
	}
	if cfg.DoneTTLHours <= 0 {
		cfg.DoneTTLHours = d.DoneTTLHours
	}
	if cfg.VerifyTimeoutSeconds <= 0 {
		cfg.VerifyTimeoutSeconds = d.VerifyTimeoutSeconds
	}
	if cfg.ReviewTimeoutSeconds <= 0 {
		cfg.ReviewTimeoutSeconds = d.ReviewTimeoutSeconds
	}
	if cfg.RunTimeoutMinutes <= 0 {
		cfg.RunTimeoutMinutes = d.RunTimeoutMinutes
	}
	if cfg.SpecAgent.Agent == "" {
		cfg.SpecAgent = d.SpecAgent
	}
	if cfg.SmallExecAgent.Agent == "" {
		cfg.SmallExecAgent = d.SmallExecAgent
	}
	if cfg.LargeExecAgent.Agent == "" {
		cfg.LargeExecAgent = d.LargeExecAgent
	}
	if cfg.SmallMaxTouches <= 0 {
		cfg.SmallMaxTouches = d.SmallMaxTouches
	}
	if cfg.LargeMaxTouches <= 0 {
		cfg.LargeMaxTouches = d.LargeMaxTouches
	}
	if cfg.SmallMaxDiffFiles <= 0 {
		cfg.SmallMaxDiffFiles = d.SmallMaxDiffFiles
	}
	if cfg.SmallMaxDiffLines <= 0 {
		cfg.SmallMaxDiffLines = d.SmallMaxDiffLines
	}
	if cfg.LargeMaxDiffFiles <= 0 {
		cfg.LargeMaxDiffFiles = d.LargeMaxDiffFiles
	}
	if cfg.LargeMaxDiffLines <= 0 {
		cfg.LargeMaxDiffLines = d.LargeMaxDiffLines
	}
	if cfg.TodoStallMinutes <= 0 {
		cfg.TodoStallMinutes = d.TodoStallMinutes
	}
	if cfg.XIssueDataDir == "" {
		cfg.XIssueDataDir = d.XIssueDataDir
	}
	if cfg.ServerPort <= 0 {
		cfg.ServerPort = d.ServerPort
	}
	if cfg.ServerURL == "" {
		cfg.ServerURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.ServerPort)
	}
}

// GateLimits converts the configured touch budgets for the admission gate.
func (c *Config) GateLimits() gate.Limits {
	limits := gate.DefaultLimits()
	if c == nil {
		return limits
	}
	if c.SmallMaxTouches > 0 {
		limits.SmallMaxTouches = c.SmallMaxTouches
	}
	if c.LargeMaxTouches > 0 {
		limits.LargeMaxTouches = c.LargeMaxTouches
	}
	return limits
}

// DiffBudget returns the maximum files and changed lines a card may produce.
// Oversized diffs are rejected by the mechanical gate so a stale large card
// cannot monopolise a slot for an hour.
func (c *Config) DiffBudget(size string) (files, lines int) {
	files, lines = 2, 120
	if size == "large" {
		files, lines = 4, 300
	}
	if c == nil {
		return files, lines
	}
	if size == "large" {
		if c.LargeMaxDiffFiles > 0 {
			files = c.LargeMaxDiffFiles
		}
		if c.LargeMaxDiffLines > 0 {
			lines = c.LargeMaxDiffLines
		}
		return files, lines
	}
	if c.SmallMaxDiffFiles > 0 {
		files = c.SmallMaxDiffFiles
	}
	if c.SmallMaxDiffLines > 0 {
		lines = c.SmallMaxDiffLines
	}
	return files, lines
}

func save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// applyXIssueConfig fills the URL and token from ~/.xissue/config.yaml. An
// explicit value in xkb's config always wins.
func applyXIssueConfig(cfg *Config) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(home, ".xissue", "config.yaml"))
	if err != nil {
		return
	}
	var xc struct {
		Listen  string `yaml:"listen"`
		Token   string `yaml:"token"`
		DataDir string `yaml:"dataDir"`
	}
	if yaml.Unmarshal(data, &xc) != nil {
		return
	}
	if xc.Listen != "" && cfg.XIssueURL == "http://127.0.0.1:7789" {
		cfg.XIssueURL = "http://" + strings.TrimPrefix(xc.Listen, "http://")
	}
	if cfg.XIssueToken == "" {
		cfg.XIssueToken = xc.Token
	}
	if xc.DataDir != "" && os.Getenv("XKB_HOME") != "" {
		// Tests using a temporary home must not touch the real xissue data.
		cfg.XIssueDataDir = xc.DataDir
	}
}
