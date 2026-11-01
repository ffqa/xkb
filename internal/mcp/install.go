package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type installTarget struct {
	name string
	path string
	root string // top-level key holding servers
	form string // "args" or "opencode"
}

// Install registers `xkb mcp` in local agent configs and returns updated files.
func Install() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "xkb"
	}
	targets := []installTarget{
		{name: "opencode", path: filepath.Join(home, ".config", "opencode", "opencode.json"), root: "mcp", form: "opencode"},
		{name: "devin", path: filepath.Join(home, ".config", "devin", "mcp_config.json"), root: "mcpServers", form: "args"},
	}
	var updated []string
	var firstErr error
	for _, t := range targets {
		if err := installOne(t, exe); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", t.name, err)
			}
			continue
		}
		updated = append(updated, t.path)
	}
	return updated, firstErr
}

func installOne(t installTarget, exe string) error {
	cfg := map[string]any{}
	data, err := os.ReadFile(t.path)
	switch {
	case err == nil:
		if len(data) > 0 {
			if err := json.Unmarshal(data, &cfg); err != nil {
				return err
			}
		}
	case !os.IsNotExist(err):
		return err
	}
	servers, _ := cfg[t.root].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	if t.form == "opencode" {
		servers["xkb"] = map[string]any{"type": "local", "command": []string{exe, "mcp"}, "enabled": true}
	} else {
		servers["xkb"] = map[string]any{"command": exe, "args": []string{"mcp"}}
	}
	cfg[t.root] = servers
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(t.path, append(out, '\n'), 0o600)
}
