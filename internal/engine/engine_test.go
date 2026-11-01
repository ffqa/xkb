package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/reviewer"
	"github.com/xpier/xkb/internal/store"
	"github.com/xpier/xkb/internal/xissue"
)

type fakeX struct {
	mu           sync.Mutex
	runSeq       int64
	runs         map[int64]*xissue.RunInfo
	changes      map[int64][]xissue.RepoChange
	runBodies    []map[string]any
	messages     []string
	submits      []string
	cancels      []int64
	archives     []int64
	issueCreates int
	runStarts    int
	runFail      bool
	submitError  string
	projectsFail bool
}

func newFakeX() *fakeX {
	return &fakeX{runs: map[int64]*xissue.RunInfo{}, changes: map[int64][]xissue.RepoChange{}}
}

func (f *fakeX) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		switch {
		case r.Method == "GET" && p == "/api/health":
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.Method == "GET" && p == "/api/projects":
			if f.projectsFail {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(w).Encode([]xissue.Project{})
		case r.Method == "POST" && p == "/api/projects":
			json.NewEncoder(w).Encode(map[string]any{"id": 1})
		case r.Method == "POST" && strings.HasSuffix(p, "/repos"):
			json.NewEncoder(w).Encode(map[string]any{"id": 1})
		case r.Method == "POST" && strings.HasSuffix(p, "/issues"):
			f.issueCreates++
			json.NewEncoder(w).Encode(map[string]any{"id": 1})
		case r.Method == "POST" && strings.HasSuffix(p, "/runs"):
			f.runStarts++
			if f.runFail {
				http.Error(w, "queue full", http.StatusTooManyRequests)
				return
			}
			f.runSeq++
			f.runBodies = append(f.runBodies, body)
			run := &xissue.RunInfo{ID: f.runSeq, IssueID: 1, Status: "running"}
			f.runs[f.runSeq] = run
			json.NewEncoder(w).Encode(run)
		case r.Method == "GET" && strings.HasSuffix(p, "/changes"):
			var id int64
			fmt.Sscanf(p, "/api/runs/%d/changes", &id)
			json.NewEncoder(w).Encode(f.changes[id])
		case r.Method == "GET" && strings.HasPrefix(p, "/api/runs/"):
			var id int64
			fmt.Sscanf(p, "/api/runs/%d", &id)
			json.NewEncoder(w).Encode(f.runs[id])
		case r.Method == "POST" && strings.HasSuffix(p, "/message"):
			f.messages = append(f.messages, body["text"].(string))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && strings.HasSuffix(p, "/submit"):
			f.submits = append(f.submits, body["commitMessage"].(string))
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{
				"repoName": "repo", "commit": "abc123", "error": f.submitError,
			}}})
		case r.Method == "POST" && strings.HasSuffix(p, "/cancel"):
			var id int64
			fmt.Sscanf(p, "/api/runs/%d/cancel", &id)
			f.cancels = append(f.cancels, id)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "PATCH" && strings.HasPrefix(p, "/api/issues/"):
			var id int64
			fmt.Sscanf(p, "/api/issues/%d", &id)
			f.archives = append(f.archives, id)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+p, http.StatusNotFound)
		}
	})
}

func testEngine(t *testing.T) (*Engine, *fakeX, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := newFakeX()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	cfg := &config.Config{
		MaxConcurrent: 2, DoneMaxCount: 20, DoneTTLHours: 24,
		VerifyTimeoutSeconds: 5, ReviewTimeoutSeconds: 5,
		SmallExecAgent: config.AgentModel{Agent: "omp"},
		LargeExecAgent: config.AgentModel{Agent: "devin", Model: "swe-2-high"},
	}
	eng := New(st, xissue.New(srv.URL, "", t.TempDir()), cfg)
	eng.Log = func(string, ...any) {}
	return eng, f, st
}

func newCard(t *testing.T, st *store.Store, mut func(*store.Card)) *store.Card {
	t.Helper()
	c := &store.Card{
		ProjectPath: t.TempDir(), Title: "card", Priority: "P2", Size: "small",
		Status: store.StatusTodo, Touches: []string{"internal/a"}, VerifyCmd: "true",
		Goal: "g", OutOfScope: "o", Acceptance: "a",
	}
	if mut != nil {
		mut(c)
	}
	saved, err := st.CreateCard(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestTickRespectsSlotsDepsAndConflicts(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	project := t.TempDir()

	c1 := newCard(t, st, func(c *store.Card) { c.ProjectPath = project; c.Priority = "P1"; c.Touches = []string{"internal/a"} })
	c2 := newCard(t, st, func(c *store.Card) {
		c.ProjectPath = project
		c.Priority = "P2"
		c.Touches = []string{"internal/a/x.go"}
	})
	newCard(t, st, func(c *store.Card) { c.ProjectPath = project; c.Priority = "P3"; c.Touches = []string{"web/"} })
	c4 := newCard(t, st, func(c *store.Card) {
		c.ProjectPath = project
		c.Priority = "P2"
		c.Touches = []string{"docs/"}
		c.DependsOn = []string{c1.ID}
	})
	c5 := newCard(t, st, func(c *store.Card) { c.ProjectPath = project; c.Priority = "P4"; c.Touches = []string{"other/"} })

	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f.runSeq != 2 {
		t.Fatalf("expected 2 dispatches, got %d", f.runSeq)
	}
	for id, want := range map[string]string{
		c1.ID: store.StatusRunning, c2.ID: store.StatusTodo,
		c4.ID: store.StatusTodo, c5.ID: store.StatusTodo,
	} {
		got, _ := st.GetCard(ctx, id)
		if got.Status != want {
			t.Fatalf("%s status %s want %s", id, got.Status, want)
		}
	}
	if len(f.runBodies) != 2 || f.runBodies[0]["agent"] != "devin" {
		t.Fatalf("P1 should route to large agent: %+v", f.runBodies)
	}

	// Finish the two active cards; the blocked candidates now dispatch.
	for _, id := range []string{c1.ID} {
		c, _ := st.GetCard(ctx, id)
		c.Status = store.StatusDone
		st.UpdateCard(ctx, c)
	}
	active, _ := st.ListCards(ctx, store.CardFilter{Statuses: []string{store.StatusRunning}})
	for _, c := range active {
		c.Status = store.StatusDone
		st.UpdateCard(ctx, c)
	}
	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f.runSeq != 4 {
		t.Fatalf("expected 4 dispatches total, got %d", f.runSeq)
	}
	for _, id := range []string{c2.ID, c4.ID} {
		got, _ := st.GetCard(ctx, id)
		if got.Status != store.StatusRunning {
			t.Fatalf("%s not dispatched: %s", id, got.Status)
		}
	}
}

func TestDispatchFailureReusesIssueAndBlocks(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	f.runFail = true
	card := newCard(t, st, nil)

	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusTodo || got.RetryCount != 1 || got.XIssueIssueID != 1 {
		t.Fatalf("first failure: %+v", got)
	}
	if got.LastError == "" {
		t.Fatal("missing last_error after dispatch failure")
	}

	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetCard(ctx, card.ID)
	if got.Status != store.StatusBlocked || got.RetryCount != 2 {
		t.Fatalf("second failure: %+v", got)
	}
	if f.issueCreates != 1 || f.runStarts != 2 {
		t.Fatalf("issue must be reused: creates=%d starts=%d", f.issueCreates, f.runStarts)
	}
}

func TestTransientDispatchFailureKeepsCardQueued(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	f.projectsFail = true
	card := newCard(t, st, nil)
	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusTodo || got.RetryCount != 0 || got.LastError == "" {
		t.Fatalf("transient failure must not consume retries: %+v", got)
	}
	if f.runStarts != 0 {
		t.Fatalf("unexpected run start: %d", f.runStarts)
	}
}

func TestSubmitFailureIsNotDone(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	worktree := newGitRepo(t)
	f.submitError = "merge conflict"
	card := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusReview
		c.XIssueRunID = 1
		c.Priority = "P3"
		c.Touches = []string{"docs/"}
		c.VerifyCmd = "true"
	})
	f.runs[1] = &xissue.RunInfo{ID: 1, IssueID: 1, Status: "completed", TargetBranch: "main"}
	f.changes[1] = []xissue.RepoChange{{
		RepoName: "repo", WorktreePath: worktree,
		Files: []xissue.FileChange{{Path: "docs/a.md", Additions: 3}},
	}}
	if err := eng.VerifyCard(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status == store.StatusDone {
		t.Fatal("merge failure must not mark the card done")
	}
	if got.Status != store.StatusRunning || got.RetryCount != 1 {
		t.Fatalf("expected retry after merge failure: %+v", got)
	}
	if !strings.Contains(got.LastError, "merge conflict") {
		t.Fatalf("last_error: %s", got.LastError)
	}
	if len(f.messages) != 1 {
		t.Fatalf("expected warm-session message, got %v", f.messages)
	}
}

func TestReviewerFailureBlocksImmediately(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	worktree := newGitRepo(t)
	runGit(t, worktree, "checkout", "-b", "xkb-1")
	if err := os.MkdirAll(filepath.Join(worktree, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "internal", "a.go"), []byte("package internal\n"+strings.Repeat("// line\n", 20)), 0o644); err != nil {
		t.Fatal(err)
	}
	card := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusReview
		c.XIssueRunID = 1
		c.Priority = "P2"
		c.Touches = []string{"internal/"}
		c.VerifyCmd = "true"
	})
	f.runs[1] = &xissue.RunInfo{ID: 1, IssueID: 1, Status: "completed", TargetBranch: "main"}
	f.changes[1] = []xissue.RepoChange{{
		RepoName: "repo", WorktreePath: worktree, Patch: "diff",
		Files: []xissue.FileChange{{Path: "internal/a.go", Additions: 20}},
	}}
	eng.Reviewer.Exec = func(context.Context, string, []string, string, ...string) (string, error) {
		return "", errors.New("reviewer exploded")
	}
	if err := eng.VerifyCard(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusBlocked || got.RetryCount != 0 {
		t.Fatalf("reviewer failure must block without burning retries: %+v", got)
	}
	if !strings.Contains(got.LastError, "审查进程失败") {
		t.Fatalf("last_error: %s", got.LastError)
	}
	if len(f.messages) != 0 {
		t.Fatalf("reviewer failures must not message the coder: %v", f.messages)
	}
	runs, _ := st.ListReviewRuns(ctx, card.ID)
	if len(runs) != 2 || runs[1].Stage != "ai_review" || runs[1].Verdict != "FAIL" {
		t.Fatalf("review records: %+v", runs)
	}
}

func TestVerifyCommitsWorktreeForIncrementalReview(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	worktree := newGitRepo(t)
	runGit(t, worktree, "checkout", "-b", "xkb-1")
	if err := os.WriteFile(filepath.Join(worktree, "a.go"), []byte("package a\n\nfunc changed() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(worktree, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "internal", "new.go"), []byte("package internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	card := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusReview
		c.XIssueRunID = 1
		c.Priority = "P2"
		c.Touches = []string{"a.go", "internal/"}
		c.VerifyCmd = "true"
	})
	f.runs[1] = &xissue.RunInfo{ID: 1, IssueID: 1, Status: "completed", TargetBranch: "main"}
	f.changes[1] = []xissue.RepoChange{{
		RepoName: "repo", WorktreePath: worktree, Patch: "diff",
		Files: []xissue.FileChange{
			{Path: "a.go", Additions: 5},
			{Path: "internal/new.go", Additions: 15},
		},
	}}
	var reviewArgs string
	eng.Reviewer.Exec = func(_ context.Context, _ string, _ []string, _ string, args ...string) (string, error) {
		reviewArgs = strings.Join(args, " ")
		return `{"verdict":"PASS","findings":[]}`, nil
	}
	if err := eng.VerifyCard(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reviewArgs, "internal/new.go") {
		t.Fatalf("review diff missing untracked file: %s", reviewArgs)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusDone {
		t.Fatalf("card not done: %s", got.Status)
	}
	runs, _ := st.ListReviewRuns(ctx, card.ID)
	if len(runs) == 0 || runs[0].Stage != "gate_cmd" || runs[0].BaseSHA == runs[0].HeadSHA {
		t.Fatalf("worktree was not committed before review: %+v", runs)
	}
	status, err := exec.Command("git", "-C", worktree, "status", "--porcelain").Output()
	if err != nil || strings.TrimSpace(string(status)) != "" {
		t.Fatalf("worktree not clean: %q %v", status, err)
	}
	subject, err := exec.Command("git", "-C", worktree, "log", "-1", "--format=%s").Output()
	if err != nil || strings.TrimSpace(string(subject)) != "xkb round 1" {
		t.Fatalf("commit subject: %q %v", subject, err)
	}
}

func TestRunningTimeoutCancelsAndRetries(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	fixed := time.Now().UTC()
	eng.Now = func() time.Time { return fixed }
	eng.Config.RunTimeoutMinutes = 30
	started := fixed.Add(-2 * time.Hour)
	card := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusRunning
		c.XIssueRunID = 1
		c.StartedAt = &started
	})
	f.runs[1] = &xissue.RunInfo{ID: 1, IssueID: 1, Status: "running"}
	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusTodo || got.RetryCount != 1 {
		t.Fatalf("timeout handling: %+v", got)
	}
	if !strings.Contains(got.LastError, "运行超时") {
		t.Fatalf("last_error: %s", got.LastError)
	}
	if len(f.cancels) != 1 || f.cancels[0] != 1 {
		t.Fatalf("cancel calls: %v", f.cancels)
	}
}

func TestAwaitingPermissionVisible(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	started := time.Now().UTC()
	card := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusRunning
		c.XIssueRunID = 1
		c.StartedAt = &started
	})
	f.runs[1] = &xissue.RunInfo{ID: 1, IssueID: 1, Status: "awaiting_permission"}
	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusRunning || got.RetryCount != 0 {
		t.Fatalf("awaiting permission must not retry: %+v", got)
	}
	if !strings.Contains(got.LastError, "awaiting_permission") {
		t.Fatalf("last_error: %s", got.LastError)
	}
	if len(f.cancels) != 0 {
		t.Fatalf("unexpected cancel: %v", f.cancels)
	}
}

func TestSubmittedRunMarksDoneWithoutResubmit(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	started := time.Now().UTC()
	card := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusRunning
		c.XIssueRunID = 1
		c.StartedAt = &started
	})
	f.runs[1] = &xissue.RunInfo{ID: 1, IssueID: 1, Status: "submitted"}
	if err := eng.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusDone || got.FinishedAt == nil {
		t.Fatalf("submitted run must be done: %+v", got)
	}
	if len(f.submits) != 0 {
		t.Fatalf("must not submit again: %v", f.submits)
	}
}

func TestVerifyGateFailRetriesThenBlocks(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	worktree := newGitRepo(t)
	runGit(t, worktree, "checkout", "-b", "xkb-1")
	if err := os.WriteFile(filepath.Join(worktree, "bad.go"), []byte("package bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	card := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusReview
		c.XIssueRunID = 1
		c.Touches = []string{"internal/good/"}
		c.VerifyCmd = "false"
	})
	f.runs[1] = &xissue.RunInfo{ID: 1, IssueID: 1, Status: "completed", TargetBranch: "main"}
	f.changes[1] = []xissue.RepoChange{{
		RepoName: "repo", WorktreePath: worktree, Patch: "diff",
		Files: []xissue.FileChange{{Path: "internal/bad.go", Additions: 5}},
	}}

	for i := range 3 {
		if err := eng.VerifyCard(ctx, card.ID); err != nil {
			t.Fatal(err)
		}
		got, _ := st.GetCard(ctx, card.ID)
		if i < 2 {
			if got.Status != store.StatusRunning || got.RetryCount != i+1 {
				t.Fatalf("round %d: %+v", i, got)
			}
			got.Status = store.StatusReview
			if err := st.UpdateCard(ctx, got); err != nil {
				t.Fatal(err)
			}
		} else if got.Status != store.StatusBlocked {
			t.Fatalf("round %d: expected blocked, got %s", i, got.Status)
		}
	}
	if len(f.messages) != 2 {
		t.Fatalf("messages: %d", len(f.messages))
	}
	if len(f.cancels) != 1 {
		t.Fatalf("cancels: %v", f.cancels)
	}
}

func TestVerifyPassSkipsAIAndMerges(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	worktree := newGitRepo(t)
	card := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusReview
		c.XIssueRunID = 1
		c.Priority = "P3"
		c.Touches = []string{"docs/"}
		c.VerifyCmd = "true"
	})
	f.runs[1] = &xissue.RunInfo{ID: 1, IssueID: 1, Status: "completed", TargetBranch: "main"}
	f.changes[1] = []xissue.RepoChange{{
		RepoName: "repo", WorktreePath: worktree,
		Files: []xissue.FileChange{{Path: "docs/a.md", Additions: 3}},
	}}
	next := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusTodo
		c.DependsOn = []string{card.ID}
		c.Touches = []string{"other/"}
	})
	eng.Reviewer.Exec = func(context.Context, string, []string, string, ...string) (string, error) {
		t.Fatal("AI reviewer must be skipped")
		return "", nil
	}
	if err := eng.VerifyCard(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	gotNext, _ := st.GetCard(ctx, next.ID)
	if gotNext.Status != store.StatusRunning {
		t.Fatalf("downstream card not dispatched immediately: %s", gotNext.Status)
	}
	if f.runSeq != 1 {
		t.Fatalf("expected downstream run, runSeq=%d", f.runSeq)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusDone || got.FinishedAt == nil {
		t.Fatalf("card not done: %+v", got)
	}
	if len(f.submits) != 1 || !strings.Contains(f.submits[0], card.ID) {
		t.Fatalf("submits: %v", f.submits)
	}
	runs, _ := st.ListReviewRuns(ctx, card.ID)
	if len(runs) != 1 || runs[0].Stage != "gate_cmd" || runs[0].Verdict != "PASS" {
		t.Fatalf("reviews: %+v", runs)
	}
}

func TestMechanicalFindingsRetryThenSkipAI(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	worktree := newGitRepo(t)
	runGit(t, worktree, "checkout", "-b", "xkb-1")
	if err := os.MkdirAll(filepath.Join(worktree, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "internal", "a.go"), []byte("package internal\n"+strings.Repeat("// line\n", 20)), 0o644); err != nil {
		t.Fatal(err)
	}
	card := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusReview
		c.XIssueRunID = 1
		c.Priority = "P2"
		c.Touches = []string{"internal/"}
		c.VerifyCmd = "true"
	})
	f.runs[1] = &xissue.RunInfo{ID: 1, IssueID: 1, Status: "completed", TargetBranch: "main"}
	f.changes[1] = []xissue.RepoChange{{
		RepoName: "repo", WorktreePath: worktree,
		Files: []xissue.FileChange{{Path: "internal/a.go", Additions: 20, Deletions: 5}},
	}}
	eng.Reviewer.Exec = func(context.Context, string, []string, string, ...string) (string, error) {
		return `{"verdict":"FAIL","findings":[{"severity":"low","mechanical":true,"file":"internal/a.go","line":1,"issue":"dead code"}]}`, nil
	}
	if err := eng.VerifyCard(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusRunning || got.RetryCount != 1 || got.LastReviewedSHA == "" {
		t.Fatalf("expected mechanical retry: %+v", got)
	}
	if len(f.submits) != 0 {
		t.Fatal("must not submit before mechanical fix")
	}

	got.Status = store.StatusReview
	if err := st.UpdateCard(ctx, got); err != nil {
		t.Fatal(err)
	}
	eng.Reviewer.Exec = func(context.Context, string, []string, string, ...string) (string, error) {
		t.Fatal("second round must skip AI review")
		return "", nil
	}
	if err := eng.VerifyCard(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetCard(ctx, card.ID)
	if got.Status != store.StatusDone {
		t.Fatalf("expected done, got %s", got.Status)
	}
	if len(f.submits) != 1 {
		t.Fatalf("submits: %v", f.submits)
	}
}

func TestArchiveRules(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-25 * time.Hour)
	recent := time.Now().UTC().Add(-2 * time.Hour)
	for i := range 3 {
		newCard(t, st, func(c *store.Card) {
			c.Status = store.StatusDone
			c.Title = fmt.Sprintf("done-%d", i)
			fin := recent.Add(time.Duration(i) * time.Minute)
			c.FinishedAt = &fin
			c.XIssueIssueID = int64(100 + i)
		})
	}
	newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusDone
		c.FinishedAt = &old
		c.XIssueIssueID = 200
	})
	eng.Config.DoneMaxCount = 2
	if _, err := eng.Archive(ctx); err != nil {
		t.Fatal(err)
	}
	archived, _ := st.ListCards(ctx, store.CardFilter{Statuses: []string{store.StatusArchived}})
	if len(archived) != 2 {
		t.Fatalf("expected 2 archived (1 over max + 1 ttl), got %d", len(archived))
	}
	if len(f.archives) != 2 {
		t.Fatalf("xissue archives: %v", f.archives)
	}

	// Spec completion archives the rest an hour after the last finish.
	sp, err := st.CreateSpec(ctx, &store.Spec{ProjectPath: t.TempDir(), Title: "s", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		newCard(t, st, func(c *store.Card) {
			c.SpecID = sp.ID
			c.Status = store.StatusDone
			c.FinishedAt = &recent
		})
	}
	if _, err := eng.Archive(ctx); err != nil {
		t.Fatal(err)
	}
	specCards, _ := st.ListCards(ctx, store.CardFilter{SpecID: sp.ID})
	for _, c := range specCards {
		if c.Status != store.StatusArchived {
			t.Fatalf("spec card %s not archived: %s", c.ID, c.Status)
		}
	}
}

func TestRunOnceSetsOnlineStatus(t *testing.T) {
	eng, _, _ := testEngine(t)
	eng.RunOnce(context.Background())
	online, _ := eng.Status()
	if !online {
		t.Fatal("expected xissue online after RunOnce")
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	return dir
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

var _ = reviewer.Finding{}

func TestMediumFindingOverridesPassVerdict(t *testing.T) {
	eng, f, st := testEngine(t)
	ctx := context.Background()
	worktree := newGitRepo(t)
	runGit(t, worktree, "checkout", "-b", "xkb-1")
	if err := os.MkdirAll(filepath.Join(worktree, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "internal", "a.go"), []byte("package internal\n"+strings.Repeat("// line\n", 20)), 0o644); err != nil {
		t.Fatal(err)
	}
	card := newCard(t, st, func(c *store.Card) {
		c.Status = store.StatusReview
		c.XIssueRunID = 1
		c.Priority = "P2"
		c.Touches = []string{"internal/"}
		c.VerifyCmd = "true"
	})
	f.runs[1] = &xissue.RunInfo{ID: 1, IssueID: 1, Status: "completed", TargetBranch: "main"}
	f.changes[1] = []xissue.RepoChange{{
		RepoName: "repo", WorktreePath: worktree,
		Files: []xissue.FileChange{{Path: "internal/a.go", Additions: 20}},
	}}
	eng.Reviewer.Exec = func(context.Context, string, []string, string, ...string) (string, error) {
		return `{"verdict":"PASS","findings":[{"severity":"medium","mechanical":false,"file":"internal/a.go","line":1,"issue":"risk"}]}`, nil
	}
	if err := eng.VerifyCard(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(ctx, card.ID)
	if got.Status != store.StatusRunning || got.RetryCount != 1 {
		t.Fatalf("card not sent back: %+v", got)
	}
	runs, _ := st.ListReviewRuns(ctx, card.ID)
	if len(runs) < 2 || runs[len(runs)-1].Stage != "ai_review" || runs[len(runs)-1].Verdict != "FAIL" {
		t.Fatalf("review record must be FAIL: %+v", runs)
	}
}
