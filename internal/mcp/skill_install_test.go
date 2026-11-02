package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSkills creates a skills root holding every built-in skill.
func fakeSkills(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range skillNames {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestInstallSkillsSymlinksAllAgents(t *testing.T) {
	home := t.TempDir()
	src := fakeSkills(t)
	t.Setenv("HOME", home)
	t.Setenv("XKB_SKILLS_DIR", src)

	paths, err := InstallSkills()
	if err != nil {
		t.Fatalf("InstallSkills: %v", err)
	}
	if len(paths) != 3*len(skillNames) {
		t.Fatalf("registered %d paths, want %d: %v", len(paths), 3*len(skillNames), paths)
	}
	for _, root := range []string{".omp/agent/skills", ".config/opencode/skills", ".config/devin/skills"} {
		for _, name := range skillNames {
			link := filepath.Join(home, root, name)
			got, err := os.Readlink(link)
			if err != nil {
				t.Fatalf("%s: %v", link, err)
			}
			if got != filepath.Join(src, name) {
				t.Fatalf("%s -> %s, want %s", link, got, filepath.Join(src, name))
			}
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("unsupported agent dir touched: %v", err)
	}
}

func TestInstallSkillsIsIdempotent(t *testing.T) {
	home := t.TempDir()
	src := fakeSkills(t)
	t.Setenv("HOME", home)
	t.Setenv("XKB_SKILLS_DIR", src)

	if _, err := InstallSkills(); err != nil {
		t.Fatalf("first install: %v", err)
	}
	link := filepath.Join(home, ".omp", "agent", "skills", "xkb-planner")
	if err := os.Remove(link); err != nil { // stale link must be recreated
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent", link); err != nil {
		t.Fatal(err)
	}
	paths, err := InstallSkills()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(paths) != 3*len(skillNames) {
		t.Fatalf("registered %d paths on rerun: %v", len(paths), paths)
	}
	if got, _ := os.Readlink(link); got != filepath.Join(src, "xkb-planner") {
		t.Fatalf("stale link not repaired: %s", got)
	}
}

func TestInstallSkillsKeepsRealDirectory(t *testing.T) {
	home := t.TempDir()
	src := fakeSkills(t)
	t.Setenv("HOME", home)
	t.Setenv("XKB_SKILLS_DIR", src)

	real := filepath.Join(home, ".config", "devin", "skills", "xkb-planner")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "SKILL.md"), []byte("hand edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := InstallSkills()
	if err == nil || !strings.Contains(err.Error(), real) {
		t.Fatalf("want error naming %s, got %v", real, err)
	}
	data, err := os.ReadFile(filepath.Join(real, "SKILL.md"))
	if err != nil || string(data) != "hand edited" {
		t.Fatalf("hand-copied skill destroyed: %q, %v", data, err)
	}
}

func TestSkillSourceDirFindsRepoSkills(t *testing.T) {
	t.Setenv("XKB_SKILLS_DIR", "")
	dir, err := skillSourceDir()
	if err != nil {
		t.Fatalf("skillSourceDir: %v", err)
	}
	for _, name := range skillNames {
		if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err != nil {
			t.Fatalf("%s missing from %s: %v", name, dir, err)
		}
	}
}
