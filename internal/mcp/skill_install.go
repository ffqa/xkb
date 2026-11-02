package mcp

import (
	"fmt"
	"os"
	"path/filepath"
)

// skillNames are the repository's built-in skills; only these are installed.
var skillNames = []string{"xkb-planner", "xkb-project-workflow", "xkb-self-dogfooding"}

type skillTarget struct {
	agent string
	dir   string // agent skills root, <root>/<name>/SKILL.md
}

// InstallSkills symlinks the built-in skills into the supported agent skill
// roots (omp, opencode, devin) and returns the registered paths. Re-running
// replaces the symlinks in place; any other existing entry is left untouched
// and reported as an error so a hand-copied skill is never destroyed.
func InstallSkills() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	src, err := skillSourceDir()
	if err != nil {
		return nil, err
	}
	targets := []skillTarget{
		{agent: "omp", dir: filepath.Join(home, ".omp", "agent", "skills")},
		{agent: "opencode", dir: filepath.Join(home, ".config", "opencode", "skills")},
		{agent: "devin", dir: filepath.Join(home, ".config", "devin", "skills")},
	}
	var registered []string
	var firstErr error
	for _, t := range targets {
		if err := os.MkdirAll(t.dir, 0o755); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", t.agent, err)
			}
			continue
		}
		for _, name := range skillNames {
			link := filepath.Join(t.dir, name)
			if err := linkSkill(filepath.Join(src, name), link); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s/%s: %w", t.agent, name, err)
				}
				continue
			}
			registered = append(registered, link)
		}
	}
	return registered, firstErr
}

func linkSkill(src, link string) error {
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("已存在非软链条目，请先删除 %s", link)
	}
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(src, link)
}

// skillSourceDir locates the repo skills/ directory: XKB_SKILLS_DIR wins,
// otherwise walk up from the executable and the working directory.
func skillSourceDir() (string, error) {
	if dir := os.Getenv("XKB_SKILLS_DIR"); dir != "" {
		return dir, nil
	}
	var starts []string
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	for _, start := range starts {
		for dir := start; ; {
			cand := filepath.Join(dir, "skills")
			if _, err := os.Stat(filepath.Join(cand, "xkb-planner", "SKILL.md")); err == nil {
				return cand, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", fmt.Errorf("找不到内置 skills 目录（可用 XKB_SKILLS_DIR 指定）")
}
