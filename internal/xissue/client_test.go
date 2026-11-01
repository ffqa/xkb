package xissue

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("X-XIssue") == "" {
			t.Errorf("missing X-XIssue header on %s %s", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "tok", t.TempDir())
}

func TestEnsureProjectAndRepoExisting(t *testing.T) {
	projectPath := t.TempDir()
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode([]Project{{
			ID: 5, Name: "proj", Repos: []Repo{{ID: 7, ProjectID: 5, RemoteURL: projectPath}},
		}})
	}))
	pid, rid, err := client.EnsureProjectAndRepo(context.Background(), projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 5 || rid != 7 {
		t.Fatalf("got %d %d", pid, rid)
	}
}

func TestEnsureProjectAndRepoCreates(t *testing.T) {
	projectPath := t.TempDir()
	var createdProject, createdRepo map[string]any
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/projects":
			json.NewEncoder(w).Encode([]Project{})
		case r.Method == "POST" && r.URL.Path == "/api/projects":
			json.NewDecoder(r.Body).Decode(&createdProject)
			json.NewEncoder(w).Encode(Project{ID: 9, Name: "p"})
		case r.Method == "POST" && r.URL.Path == "/api/projects/9/repos":
			json.NewDecoder(r.Body).Decode(&createdRepo)
			json.NewEncoder(w).Encode(Repo{ID: 12, ProjectID: 9})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	pid, rid, err := client.EnsureProjectAndRepo(context.Background(), projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 9 || rid != 12 {
		t.Fatalf("ids %d %d", pid, rid)
	}
	if createdProject["devMode"] != "single_main" || createdProject["name"] == "" {
		t.Fatalf("project body: %v", createdProject)
	}
	if createdRepo["remoteUrl"] != projectPath {
		t.Fatalf("repo body: %v", createdRepo)
	}
}

func TestCreateAndStartRun(t *testing.T) {
	var issueBody, runBody map[string]any
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects/5/issues":
			json.NewDecoder(r.Body).Decode(&issueBody)
			json.NewEncoder(w).Encode(Issue{ID: 11})
		case "/api/issues/11/runs":
			json.NewDecoder(r.Body).Decode(&runBody)
			json.NewEncoder(w).Encode(RunInfo{ID: 21, IssueID: 11})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	issueID, runID, err := client.CreateAndStartRun(context.Background(), 5, 7, "title", "body", "P0", "devin", "swe-2-high", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if issueID != 11 || runID != 21 {
		t.Fatalf("ids %d %d", issueID, runID)
	}
	if issueBody["priority"] != "urgent" || issueBody["repoId"] != float64(7) {
		t.Fatalf("issue body: %v", issueBody)
	}
	if runBody["agent"] != "devin" || runBody["model"] != "swe-2-high" || runBody["prompt"] != "prompt" {
		t.Fatalf("run body: %v", runBody)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestCreateAndStartRunKeepsIssueOnRunFailure(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/runs") {
			http.Error(w, "queue full", http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode(Issue{ID: 11})
	}))
	issueID, runID, err := client.CreateAndStartRun(context.Background(), 5, 7, "t", "b", "P2", "omp", "", "p")
	if err == nil {
		t.Fatal("expected run failure")
	}
	if issueID != 11 || runID != 0 {
		t.Fatalf("issue must be returned for reuse: %d %d", issueID, runID)
	}
}

func TestSubmitMergeLocalChecksResults(t *testing.T) {
	cases := []struct {
		name, body, want string
		wantErr          bool
	}{
		{"success", `{"results":[{"repoName":"r","commit":"abc"}]}`, "", false},
		{"repo error", `{"results":[{"repoName":"r","error":"冲突"}]}`, "冲突", true},
		{"empty", `{"results":[]}`, "未返回", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tc.body))
			}))
			err := client.SubmitMergeLocal(context.Background(), 1, "msg")
			if tc.wantErr != (err != nil) {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v missing %q", err, tc.want)
			}
		})
	}
}

func TestCommitWorktreeAndDiffRange(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "init")

	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc a() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktree(context.Background(), dir, "xkb round 1"); err != nil {
		t.Fatal(err)
	}
	sha1, err := HeadSHA(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktree(context.Background(), dir, "xkb round 2"); err != nil {
		t.Fatal(err)
	}
	sha2, _ := HeadSHA(context.Background(), dir)
	if sha1 == sha2 {
		t.Fatal("second commit did not advance HEAD")
	}
	diff, err := DiffRange(context.Background(), dir, sha1, sha2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "b.go") || strings.Contains(diff, "a.go") {
		t.Fatalf("incremental diff wrong:\n%s", diff)
	}

	// Clean worktree is a no-op.
	if err := CommitWorktree(context.Background(), dir, "xkb round 3"); err != nil {
		t.Fatal(err)
	}
	sha3, _ := HeadSHA(context.Background(), dir)
	if sha3 != sha2 {
		t.Fatal("clean worktree must not create a commit")
	}
	status, _ := git(context.Background(), dir, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("worktree not clean: %s", status)
	}
}

func TestGetRunChangesFallback(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, workdir, "init", "-b", "main")
	os.WriteFile(filepath.Join(workdir, "main.go"), []byte("package main\n"), 0o644)
	gitRun(t, workdir, "add", ".")
	gitRun(t, workdir, "commit", "-m", "init")
	os.WriteFile(filepath.Join(workdir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	os.WriteFile(filepath.Join(workdir, "new.go"), []byte("package main\n"), 0o644)

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runs/21/changes":
			json.NewEncoder(w).Encode([]RepoChange{{RepoID: 7, RepoName: "repo", Branch: "xkb-1"}})
		case "/api/runs/21":
			json.NewEncoder(w).Encode(RunInfo{ID: 21, Workdir: filepath.Dir(workdir), TargetBranch: "main"})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	changes, err := client.GetRunChanges(context.Background(), 21)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes: %+v", changes)
	}
	ch := changes[0]
	if ch.WorktreePath != workdir {
		t.Fatalf("worktree path %q want %q", ch.WorktreePath, workdir)
	}
	if !strings.Contains(ch.Patch, "func main()") || !strings.Contains(ch.Patch, "new.go") || !strings.Contains(ch.Patch, "diff --git") {
		t.Fatalf("patch missing changes:\n%s", ch.Patch)
	}
}
