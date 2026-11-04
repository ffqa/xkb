package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xpier/xkb/internal/store"
	"github.com/xpier/xkb/internal/xissue"
)

func TestTickTracksTodoWaitReasons(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	project := t.TempDir()
	c1 := newCard(t, st, func(c *store.Card) { c.ProjectPath = project; c.Priority = "P1"; c.Touches = []string{"a/"} })
	c2 := newCard(t, st, func(c *store.Card) { c.ProjectPath = project; c.Priority = "P2"; c.Touches = []string{"b/"} })
	c3 := newCard(t, st, func(c *store.Card) { c.ProjectPath = project; c.Priority = "P3"; c.Touches = []string{"c/"} })

	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f.runSeq != 2 {
		t.Fatalf("expected 2 dispatches, got %d", f.runSeq)
	}
	for _, id := range []string{c1.ID, c2.ID} {
		got, _ := st.GetCard(ctx, id)
		if got.Status != store.StatusRunning || got.WaitReason != "" {
			t.Fatalf("%s should be running without wait: %+v", id, got)
		}
	}
	got, _ := st.GetCard(ctx, c3.ID)
	if got.Status != store.StatusTodo {
		t.Fatalf("third card should stay todo: %s", got.Status)
	}
	if !strings.Contains(got.WaitReason, "等待槽位") {
		t.Fatalf("missing capacity wait reason: %q", got.WaitReason)
	}
	if got.WaitSince == nil {
		t.Fatal("wait_since must be recorded")
	}
}

func TestTickBlocksMissingDependency(t *testing.T) {
	eng, _, st := testEngine(t)
	ctx := context.Background()
	card := newCard(t, st, func(c *store.Card) { c.DependsOn = []string{"xkb-999"} })

	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusBlocked {
		t.Fatalf("missing dependency must block, got %s", got.Status)
	}
	if !strings.Contains(got.LastError, "依赖卡片不存在") {
		t.Fatalf("last_error: %s", got.LastError)
	}
	if got.WaitSince != nil {
		t.Fatalf("blocked card must not keep wait_since: %+v", got.WaitSince)
	}
}

func TestTickBlocksDependencyCycle(t *testing.T) {
	eng, _, st := testEngine(t)
	ctx := context.Background()
	c1 := newCard(t, st, nil)
	c2 := newCard(t, st, nil)
	c1.DependsOn = []string{c2.ID}
	c2.DependsOn = []string{c1.ID}
	if err := st.UpdateCard(ctx, c1); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCard(ctx, c2); err != nil {
		t.Fatal(err)
	}

	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{c1.ID, c2.ID} {
		got, _ := st.GetCard(ctx, id)
		if got.Status != store.StatusBlocked {
			t.Fatalf("%s should be blocked, got %s", id, got.Status)
		}
		if !strings.Contains(got.LastError, "依赖环") {
			t.Fatalf("%s last_error: %s", id, got.LastError)
		}
	}
}

func TestBlockedUpstreamKeepsDependentWaiting(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	up := newCard(t, st, func(c *store.Card) { c.Status = store.StatusBlocked })
	dep := newCard(t, st, func(c *store.Card) { c.DependsOn = []string{up.ID} })

	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, dep.ID)
	if got.Status != store.StatusTodo {
		t.Fatalf("dependent must stay queued, got %s", got.Status)
	}
	if !strings.Contains(got.WaitReason, "上游阻塞卡") {
		t.Fatalf("wait reason: %q", got.WaitReason)
	}
	if f.runSeq != 0 {
		t.Fatalf("unexpected dispatch: %d", f.runSeq)
	}

	up, _ = st.GetCard(ctx, up.ID)
	up.Status = store.StatusDone
	if err := st.UpdateCard(ctx, up); err != nil {
		t.Fatal(err)
	}
	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetCard(ctx, dep.ID)
	if got.Status != store.StatusRunning || got.WaitReason != "" {
		t.Fatalf("dependent should dispatch after upstream done: %+v", got)
	}
}

func TestVerifierRejectsOversizedDiff(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	worktree := newGitRepo(t)
	runGit(t, worktree, "checkout", "-b", "xkb-1")
	if err := os.MkdirAll(filepath.Join(worktree, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		body := "package internal\n" + strings.Repeat("// filler\n", 60)
		if err := os.WriteFile(filepath.Join(worktree, "internal", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	card := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusReview
		c.XIssueRunID = 1
		c.Size = "small"
		c.Priority = "P2"
		c.Touches = []string{"internal/"}
		c.VerifyCmd = "true"
	})
	f.runs[1] = &xissue.RunInfo{ID: 1, IssueID: 1, Status: "completed", TargetBranch: "main"}
	f.changes[1] = []xissue.RepoChange{{
		RepoName: "repo", WorktreePath: worktree, Patch: "diff",
		Files: []xissue.FileChange{
			{Path: "internal/a.go", Additions: 60},
			{Path: "internal/b.go", Additions: 60},
			{Path: "internal/c.go", Additions: 60},
		},
	}}

	if err := eng.VerifyCard(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusRunning || got.RetryCount != 1 {
		t.Fatalf("oversized diff must be sent back: %+v", got)
	}
	if !strings.Contains(got.LastError, "预算") {
		t.Fatalf("last_error must name the budget: %s", got.LastError)
	}
	if len(f.messages) != 1 || !strings.Contains(f.messages[0], "预算") {
		t.Fatalf("warm-session message: %v", f.messages)
	}
	if len(f.submits) != 0 {
		t.Fatalf("oversized card must not merge: %v", f.submits)
	}
}
