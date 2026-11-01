// Package reviewer runs the heterogeneous read-only AI review stage.
package reviewer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/store"
)

// Finding is one review issue.
type Finding struct {
	Severity   string `json:"severity"`
	Mechanical bool   `json:"mechanical"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Issue      string `json:"issue"`
	Fix        string `json:"fix"`
}

// Result is the reviewer JSON verdict.
type Result struct {
	Verdict  string    `json:"verdict"`
	Findings []Finding `json:"findings"`
}

// Failing reports whether any non-mechanical blocking/high/medium finding
// exists.
func (r *Result) Failing() bool {
	for _, f := range r.Findings {
		if f.Mechanical {
			continue
		}
		switch strings.ToLower(f.Severity) {
		case "blocking", "high", "medium":
			return true
		}
	}
	return false
}

// MechanicalOnly reports whether every finding is mechanical.
func (r *Result) MechanicalOnly() bool {
	if len(r.Findings) == 0 {
		return false
	}
	for _, f := range r.Findings {
		if !f.Mechanical {
			return false
		}
	}
	return true
}

// Request describes one review pass.
type Request struct {
	Card         *store.Card
	Reviewer     config.AgentModel
	Diff         string
	WorktreePath string
	Incremental  bool
	PrevFindings []Finding
}

// ExecFunc runs the reviewer CLI; tests substitute it.
type ExecFunc func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error)

// Reviewer executes stateless read-only model reviews.
type Reviewer struct {
	Timeout time.Duration
	Exec    ExecFunc
}

// New returns a reviewer with the default exec implementation.
func New(timeout time.Duration) *Reviewer {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &Reviewer{Timeout: timeout, Exec: defaultExec}
}

func defaultExec(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// ShouldSkipAIReview implements the token-saving whitelist.
func (r *Reviewer) ShouldSkipAIReview(card *store.Card, changedFiles []string, totalLines int) bool {
	if card.SecuritySensitive || card.Priority == "P0" {
		return false
	}
	nonDoc := 0
	allDocs := true
	for _, f := range changedFiles {
		if strings.HasSuffix(strings.ToLower(f), ".md") {
			continue
		}
		nonDoc++
		allDocs = false
	}
	if len(changedFiles) > 0 && allDocs {
		return true
	}
	return nonDoc <= 1 && totalLines <= 10
}

// RunReadOnlyReview executes the reviewer and returns its structured verdict.
func (r *Reviewer) RunReadOnlyReview(ctx context.Context, req Request) (*Result, error) {
	if req.Card == nil {
		return nil, fmt.Errorf("review request has no card")
	}
	prompt := buildPrompt(req)
	name, args, env := commandFor(req.Reviewer, prompt)

	// Devin has no read-only switch; run it against a read-only export of the
	// worktree so it cannot touch the real files at all.
	reviewDir, cleanup, err := prepareReviewDir(ctx, req.Reviewer.Agent, req.WorktreePath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	beforeWorktree := repoState(ctx, req.WorktreePath)
	beforeProject := repoState(ctx, req.Card.ProjectPath)
	cctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	out, err := r.Exec(cctx, reviewDir, env, name, args...)
	if err != nil {
		return nil, err
	}
	afterWorktree := repoState(ctx, req.WorktreePath)
	afterProject := repoState(ctx, req.Card.ProjectPath)
	// shortcut: writes outside the worktree and project repo are not detected;
	// add an OS sandbox if reviewers must be fully confined.
	if beforeWorktree != afterWorktree || beforeProject != afterProject {
		return nil, fmt.Errorf("reviewer modified the repository (git state changed)")
	}
	return parseResult(out)
}

// prepareReviewDir returns a read-only copy of the committed worktree for
// reviewers without a hard read-only mode. Other agents get the real worktree.
func prepareReviewDir(ctx context.Context, agent, worktree string) (string, func(), error) {
	noop := func() {}
	if worktree == "" {
		return worktree, noop, nil
	}
	switch strings.ToLower(agent) {
	case "omp", "opencode":
		return worktree, noop, nil
	default:
		// devin and unknown agents have no hard read-only switch.
	}
	tmp, err := os.MkdirTemp("", "xkb-review-*")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() {
		_ = chmodTree(tmp, 0o755, 0o644)
		_ = os.RemoveAll(tmp)
	}
	tarPath := tmp + ".tar"
	if out, err := runCmd(ctx, worktree, "git", "archive", "--format=tar", "-o", tarPath, "HEAD"); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("git archive: %w: %s", err, out)
	}
	if out, err := runCmd(ctx, "", "tar", "-xf", tarPath, "-C", tmp); err != nil {
		os.Remove(tarPath)
		cleanup()
		return "", noop, fmt.Errorf("extract review tree: %w: %s", err, out)
	}
	os.Remove(tarPath)
	if err := stripSymlinks(tmp); err != nil {
		cleanup()
		return "", noop, err
	}
	if err := chmodTree(tmp, 0o555, 0o444); err != nil {
		cleanup()
		return "", noop, err
	}
	return tmp, cleanup, nil
}

func runCmd(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func stripSymlinks(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return os.Remove(path)
		}
		return nil
	})
}

func chmodTree(root string, dirMode, fileMode os.FileMode) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		mode := fileMode
		if info.IsDir() {
			mode = dirMode
		}
		return os.Chmod(path, mode)
	})
}

// repoState fingerprints HEAD and working-tree status so committed changes are
// detected too.
func repoState(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = dir
	status, serr := statusCmd.Output()
	if serr != nil {
		return ""
	}
	headCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	headCmd.Dir = dir
	head, herr := headCmd.Output()
	if herr != nil {
		return ""
	}
	return strings.TrimSpace(string(head)) + "\n" + string(status)
}

var readOnlyEnv = []string{
	`OPENCODE_PERMISSION={"edit":"deny","write":"deny","bash":"deny"}`,
}

func commandFor(agent config.AgentModel, prompt string) (string, []string, []string) {
	env := append([]string{}, readOnlyEnv...)
	switch strings.ToLower(agent.Agent) {
	case "omp":
		args := []string{"-p", "--no-session", "--no-tools"}
		if agent.Model != "" {
			args = append(args, "--model", agent.Model)
		}
		return "omp", append(args, prompt), env
	case "opencode":
		args := []string{"run"}
		if agent.Model != "" {
			args = append(args, "--model", agent.Model)
		}
		return "opencode", append(args, prompt), env
	case "devin", "":
		args := []string{"-p", "--permission-mode", "auto", "--respect-workspace-trust", "false"}
		if agent.Model != "" {
			args = append(args, "--model", agent.Model)
		}
		return "devin", append(args, prompt), env
	default:
		return agent.Agent, []string{"-p", prompt}, env
	}
}

func buildPrompt(req Request) string {
	var b strings.Builder
	b.WriteString("你是只读交叉审查官。禁止修改文件或运行写命令。只输出一个 JSON 对象，不要代码块或解释：\n")
	b.WriteString(`{"verdict":"PASS|FAIL","findings":[{"severity":"blocking|high|medium|low","mechanical":false,"file":"","line":0,"issue":"","fix":""}]}`)
	b.WriteString("\n规则：只审查本卡目标与对应 diff；越界改动、安全事故、数据损坏、错误处理缺失记为 blocking/high；")
	b.WriteString("注释、命名、死代码等机械问题标记 mechanical=true，不阻塞合入。\n")
	fmt.Fprintf(&b, "卡片: %s %s\n目标: %s\n禁区: %s\n验收: %s\n", req.Card.ID, req.Card.Title, req.Card.Goal, req.Card.OutOfScope, req.Card.Acceptance)
	if req.Incremental && len(req.PrevFindings) > 0 {
		prev, _ := json.Marshal(req.PrevFindings)
		fmt.Fprintf(&b, "上一轮未关闭问题: %s\n", prev)
	}
	b.WriteString("Diff:\n")
	b.WriteString(req.Diff)
	return b.String()
}

func parseResult(out string) (*Result, error) {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("reviewer returned no JSON object: %.300s", out)
	}
	var res Result
	if err := json.Unmarshal([]byte(out[start:end+1]), &res); err != nil {
		return nil, fmt.Errorf("decode reviewer JSON: %w", err)
	}
	res.Verdict = strings.ToUpper(strings.TrimSpace(res.Verdict))
	if res.Verdict != "PASS" && res.Verdict != "FAIL" {
		return nil, fmt.Errorf("invalid review verdict %q", res.Verdict)
	}
	return &res, nil
}
