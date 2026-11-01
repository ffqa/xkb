package reviewer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/store"
)

func TestShouldSkipAIReview(t *testing.T) {
	r := New(0)
	base := &store.Card{Priority: "P2"}
	cases := []struct {
		name   string
		card   store.Card
		files  []string
		lines  int
		expect bool
	}{
		{"docs only", *base, []string{"README.md", "docs/a.md"}, 200, true},
		{"one small code file", *base, []string{"main.go"}, 8, true},
		{"one large code file", *base, []string{"main.go"}, 30, false},
		{"two code files", *base, []string{"a.go", "b.go"}, 5, false},
		{"p0", store.Card{Priority: "P0"}, []string{"main.go"}, 5, false},
		{"security", store.Card{Priority: "P2", SecuritySensitive: true}, []string{"README.md"}, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.ShouldSkipAIReview(&tc.card, tc.files, tc.lines); got != tc.expect {
				t.Fatalf("got %v want %v", got, tc.expect)
			}
		})
	}
}

func TestParseResult(t *testing.T) {
	out := "review done:\n```json\n{\"verdict\":\"fail\",\"findings\":[{\"severity\":\"high\",\"file\":\"a.go\",\"line\":3,\"issue\":\"x\"}]}\n```"
	res, err := parseResult(out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "FAIL" || !res.Failing() {
		t.Fatalf("result: %+v", res)
	}
	if _, err := parseResult("no json"); err == nil {
		t.Fatal("expected error")
	}
}

func TestMechanicalOnly(t *testing.T) {
	res := Result{Verdict: "FAIL", Findings: []Finding{{Severity: "low", Mechanical: true}, {Severity: "blocking", Mechanical: true}}}
	if res.Failing() {
		t.Fatal("mechanical findings must not block")
	}
	if !res.MechanicalOnly() {
		t.Fatal("expected mechanical only")
	}
}

func TestRunReadOnlyReview(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	var gotPrompt string
	var gotEnv []string
	r := New(0)
	r.Exec = func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
		gotPrompt = strings.Join(args, " ")
		gotEnv = env
		return `{"verdict":"PASS","findings":[]}`, nil
	}
	card := &store.Card{ID: "xkb-1", Title: "t", Goal: "do the thing", OutOfScope: "none", Acceptance: "- ok", Touches: []string{"a.go"}}
	res, err := r.RunReadOnlyReview(context.Background(), Request{
		Card: card, Reviewer: config.AgentModel{Agent: "omp"}, Diff: "+diff", WorktreePath: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "PASS" {
		t.Fatalf("verdict %s", res.Verdict)
	}
	if !strings.Contains(gotPrompt, "do the thing") || !strings.Contains(gotPrompt, "+diff") {
		t.Fatalf("prompt missing content: %s", gotPrompt)
	}
	if len(gotEnv) == 0 || !strings.Contains(gotEnv[0], "OPENCODE_PERMISSION") {
		t.Fatalf("read-only env missing: %v", gotEnv)
	}
}

func TestRunReadOnlyReviewDetectsMutation(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	r := New(0)
	r.Exec = func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
		os.WriteFile(filepath.Join(dir, "a.go"), []byte("changed\n"), 0o644)
		return `{"verdict":"PASS"}`, nil
	}
	_, err := r.RunReadOnlyReview(context.Background(), Request{
		Card: &store.Card{ID: "xkb-1"}, Reviewer: config.AgentModel{Agent: "omp"}, WorktreePath: dir,
	})
	if err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("expected mutation error, got %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestDevinReviewUsesReadOnlyCopy(t *testing.T) {
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	os.WriteFile(filepath.Join(worktree, "a.go"), []byte("package a\n"), 0o644)
	runGit(t, worktree, "add", ".")
	runGit(t, worktree, "commit", "-m", "init")

	r := New(0)
	r.Exec = func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
		if dir == worktree {
			t.Fatal("devin must not run in the real worktree")
		}
		if _, err := os.Stat(filepath.Join(dir, "a.go")); err != nil {
			t.Fatalf("review copy missing file: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hack.go"), []byte("x"), 0o644); err == nil {
			t.Fatal("review copy must be read-only")
		}
		return `{"verdict":"PASS","findings":[]}`, nil
	}
	res, err := r.RunReadOnlyReview(context.Background(), Request{
		Card:     &store.Card{ID: "xkb-1", ProjectPath: worktree},
		Reviewer: config.AgentModel{Agent: "devin"},
		Diff:     "+diff", WorktreePath: worktree,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "PASS" {
		t.Fatalf("verdict: %+v", res)
	}
}

func TestDevinReviewDetectsRealWorktreeMutation(t *testing.T) {
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	os.WriteFile(filepath.Join(worktree, "a.go"), []byte("package a\n"), 0o644)
	runGit(t, worktree, "add", ".")
	runGit(t, worktree, "commit", "-m", "init")

	r := New(0)
	r.Exec = func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
		// Devin runs in a copy, but simulate an out-of-copy write.
		os.WriteFile(filepath.Join(worktree, "a.go"), []byte("changed\n"), 0o644)
		return `{"verdict":"PASS"}`, nil
	}
	_, err := r.RunReadOnlyReview(context.Background(), Request{
		Card:         &store.Card{ID: "xkb-1", ProjectPath: worktree},
		Reviewer:     config.AgentModel{Agent: "devin"},
		WorktreePath: worktree,
	})
	if err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("expected worktree mutation error, got %v", err)
	}
}

func TestDevinCommandArgOrder(t *testing.T) {
	_, args, _ := commandFor(config.AgentModel{Agent: "devin", Model: "swe-2-high"}, "PROMPT")
	if len(args) < 2 || args[len(args)-2] != "-p" || args[len(args)-1] != "PROMPT" {
		t.Fatalf("prompt must follow -p: %v", args)
	}
	foundModel := false
	for i, a := range args {
		if a == "--model" && i+1 < len(args) && args[i+1] == "swe-2-high" {
			foundModel = true
		}
	}
	if !foundModel {
		t.Fatalf("model flag missing: %v", args)
	}
}
