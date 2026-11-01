// Package xissue is a thin bridge to the local xissue daemon.
package xissue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Client talks to xissue over HTTP.
type Client struct {
	BaseURL string
	Token   string
	DataDir string
	HTTP    *http.Client
}

// New returns a client; dataDir is the xissue data root (used for local diff
// fallback when the daemon does not expose patch/worktree fields).
func New(baseURL, token, dataDir string) *Client {
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".xissue")
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		DataDir: dataDir,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Project mirrors xissue's project JSON.
type Project struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	DevMode string `json:"devMode"`
	Repos   []Repo `json:"repos"`
}

// Repo mirrors xissue's repo JSON. RemoteURL may be a local git directory.
type Repo struct {
	ID            int64  `json:"id"`
	ProjectID     int64  `json:"projectId"`
	Name          string `json:"name"`
	RemoteURL     string `json:"remoteUrl"`
	DefaultBranch string `json:"defaultBranch"`
}

// Issue mirrors xissue's issue JSON.
type Issue struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// RunInfo is the subset of xissue's run JSON used by xkb.
type RunInfo struct {
	ID           int64  `json:"id"`
	IssueID      int64  `json:"issueId"`
	Agent        string `json:"agent"`
	Model        string `json:"model"`
	Status       string `json:"status"`
	Branch       string `json:"branch"`
	TargetBranch string `json:"targetBranch"`
	Workdir      string `json:"workdir"`
	Summary      string `json:"summary"`
	Error        string `json:"error"`
}

// FileChange is one changed file in a worktree.
type FileChange struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// RepoChange is xissue's per-repo change record, extended by XISSUE-01.
type RepoChange struct {
	RepoID       int64        `json:"repoId"`
	RepoName     string       `json:"repoName"`
	Branch       string       `json:"branch"`
	WorktreePath string       `json:"worktreePath,omitempty"`
	Patch        string       `json:"patch,omitempty"`
	Files        []FileChange `json:"files"`
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-XIssue", "1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

var unsafeRepoName = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

func repoNameFor(projectPath string) string {
	name := unsafeRepoName.ReplaceAllString(filepath.Base(projectPath), "-")
	name = strings.Trim(name, " .")
	if name == "" {
		name = "repo"
	}
	return name
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	clean := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		return filepath.Clean(abs)
	}
	return clean(a) == clean(b)
}

// Health reports whether the daemon is reachable.
func (c *Client) Health(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/api/health", nil)
	return err
}

// EnsureProjectAndRepo finds or registers the local repository in xissue.
func (c *Client) EnsureProjectAndRepo(ctx context.Context, projectPath string) (projectID, repoID int64, err error) {
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return 0, 0, err
	}
	data, err := c.do(ctx, http.MethodGet, "/api/projects", nil)
	if err != nil {
		return 0, 0, err
	}
	var projects []Project
	if err := json.Unmarshal(data, &projects); err != nil {
		return 0, 0, fmt.Errorf("decode projects: %w", err)
	}
	for _, p := range projects {
		for _, r := range p.Repos {
			if samePath(r.RemoteURL, abs) {
				return p.ID, r.ID, nil
			}
		}
	}

	name := repoNameFor(abs)
	data, err = c.do(ctx, http.MethodPost, "/api/projects", map[string]any{
		"name":        name,
		"description": "xkb managed project",
		"devMode":     "single_main",
	})
	if err != nil {
		return 0, 0, fmt.Errorf("create project: %w", err)
	}
	var proj Project
	if err := json.Unmarshal(data, &proj); err != nil {
		return 0, 0, fmt.Errorf("decode project: %w", err)
	}
	data, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/api/projects/%d/repos", proj.ID), map[string]any{
		"name":          name,
		"remoteUrl":     abs,
		"defaultBranch": "main",
	})
	if err != nil {
		return 0, 0, fmt.Errorf("create repo: %w", err)
	}
	var repo Repo
	if err := json.Unmarshal(data, &repo); err != nil {
		return 0, 0, fmt.Errorf("decode repo: %w", err)
	}
	return proj.ID, repo.ID, nil
}

func issuePriority(p string) string {
	switch p {
	case "P0":
		return "urgent"
	case "P1":
		return "high"
	case "P3", "P4":
		return "low"
	default:
		return "normal"
	}
}

// CreateIssue creates the xissue issue for a card.
func (c *Client) CreateIssue(ctx context.Context, projectID, repoID int64, title, body, priority string) (int64, error) {
	data, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/projects/%d/issues", projectID), map[string]any{
		"title":    title,
		"body":     body,
		"priority": issuePriority(priority),
		"repoId":   repoID,
	})
	if err != nil {
		return 0, fmt.Errorf("create issue: %w", err)
	}
	var issue Issue
	if err := json.Unmarshal(data, &issue); err != nil {
		return 0, fmt.Errorf("decode issue: %w", err)
	}
	return issue.ID, nil
}

// StartRun starts an agent run on an existing issue.
func (c *Client) StartRun(ctx context.Context, issueID int64, agent, model, prompt string) (int64, error) {
	data, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/issues/%d/runs", issueID), map[string]any{
		"agent":  agent,
		"model":  model,
		"prompt": prompt,
	})
	if err != nil {
		return 0, fmt.Errorf("start run: %w", err)
	}
	var run RunInfo
	if err := json.Unmarshal(data, &run); err != nil {
		return 0, fmt.Errorf("decode run: %w", err)
	}
	return run.ID, nil
}

// CreateAndStartRun creates the issue and starts an agent run on it. On a run
// failure the issueID is still returned so callers can reuse the issue.
func (c *Client) CreateAndStartRun(ctx context.Context, projectID, repoID int64, title, body, priority, agent, model, prompt string) (issueID, runID int64, err error) {
	issueID, err = c.CreateIssue(ctx, projectID, repoID, title, body, priority)
	if err != nil {
		return 0, 0, err
	}
	runID, err = c.StartRun(ctx, issueID, agent, model, prompt)
	if err != nil {
		return issueID, 0, err
	}
	return issueID, runID, nil
}

// GetRun loads a run.
func (c *Client) GetRun(ctx context.Context, runID int64) (*RunInfo, error) {
	data, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/runs/%d", runID), nil)
	if err != nil {
		return nil, err
	}
	var run RunInfo
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// GetRunChanges returns repo changes and fills in worktreePath/patch locally
// when the daemon predates XISSUE-01.
func (c *Client) GetRunChanges(ctx context.Context, runID int64) ([]RepoChange, error) {
	data, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/runs/%d/changes?patch=1", runID), nil)
	if err != nil {
		return nil, err
	}
	var changes []RepoChange
	if err := json.Unmarshal(data, &changes); err != nil {
		return nil, err
	}
	needsFallback := false
	for _, ch := range changes {
		if ch.WorktreePath == "" || ch.Patch == "" {
			needsFallback = true
		}
	}
	if needsFallback {
		c.fillLocalChanges(ctx, runID, changes)
	}
	if changes == nil {
		changes = []RepoChange{}
	}
	return changes, nil
}

func (c *Client) fillLocalChanges(ctx context.Context, runID int64, changes []RepoChange) {
	run, err := c.GetRun(ctx, runID)
	if err != nil {
		return
	}
	branch := run.TargetBranch
	if branch == "" {
		branch = "main"
	}
	for i := range changes {
		dir := ""
		if run.Workdir != "" && changes[i].RepoName != "" {
			candidate := filepath.Join(run.Workdir, changes[i].RepoName)
			if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
				dir = candidate
			}
		}
		if dir == "" {
			candidate := filepath.Join(c.DataDir, "runs", fmt.Sprintf("run-%d", runID), changes[i].RepoName)
			if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
				dir = candidate
			}
		}
		if dir == "" {
			continue
		}
		if changes[i].WorktreePath == "" {
			changes[i].WorktreePath = dir
		}
		if changes[i].Patch == "" {
			if patch, err := GitDiff(ctx, dir, branch); err == nil {
				changes[i].Patch = patch
			}
		}
	}
}

// SendMessage wakes the warm session for a run.
func (c *Client) SendMessage(ctx context.Context, runID int64, text string) error {
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/runs/%d/message", runID), map[string]any{"text": text})
	return err
}

// SubmitResult is xissue's per-repo merge outcome.
type SubmitResult struct {
	RepoID   int64  `json:"repoId"`
	RepoName string `json:"repoName"`
	Branch   string `json:"branch"`
	Commit   string `json:"commit"`
	PRURL    string `json:"prUrl"`
	Error    string `json:"error"`
}

// SubmitMergeLocal merges the run branch into the local target branch. xissue
// returns HTTP 200 even when individual repos fail, so the results are checked.
func (c *Client) SubmitMergeLocal(ctx context.Context, runID int64, commitMsg string) error {
	data, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/runs/%d/submit", runID), map[string]any{
		"mode":          "merge_local",
		"commitMessage": commitMsg,
	})
	if err != nil {
		return err
	}
	var out struct {
		Results []SubmitResult `json:"results"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("decode submit result: %w", err)
	}
	if len(out.Results) == 0 {
		return fmt.Errorf("xissue 未返回任何提交结果（Agent 可能没有产生改动）")
	}
	var failures []string
	for _, r := range out.Results {
		if r.Error != "" {
			failures = append(failures, fmt.Sprintf("%s: %s", r.RepoName, r.Error))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("xissue 合入失败: %s", strings.Join(failures, "; "))
	}
	return nil
}

// CancelRun stops an active run.
func (c *Client) CancelRun(ctx context.Context, runID int64) error {
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/runs/%d/cancel", runID), nil)
	return err
}

// ArchiveIssue marks the issue archived in xissue.
func (c *Client) ArchiveIssue(ctx context.Context, issueID int64) error {
	if issueID == 0 {
		return nil
	}
	_, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/api/issues/%d", issueID), map[string]any{"archived": true})
	return err
}
