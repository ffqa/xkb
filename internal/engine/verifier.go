package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xpier/xkb/internal/gate"
	"github.com/xpier/xkb/internal/reviewer"
	"github.com/xpier/xkb/internal/store"
	"github.com/xpier/xkb/internal/xissue"
)

// startVerify runs one verification in the background, deduplicating cards.
func (e *Engine) startVerify(card *store.Card) {
	e.verifyMu.Lock()
	if e.verifying[card.ID] {
		e.verifyMu.Unlock()
		return
	}
	e.verifying[card.ID] = true
	e.verifyMu.Unlock()

	id := card.ID
	go func() {
		defer func() {
			e.verifyMu.Lock()
			delete(e.verifying, id)
			e.verifyMu.Unlock()
		}()
		ctx := context.Background()
		if err := e.VerifyCard(ctx, id); err != nil {
			e.eventf("verify %s: %v", id, err)
		}
	}()
}

// VerifyCard runs the two-stage verification for a card in review.
func (e *Engine) VerifyCard(ctx context.Context, cardID string) error {
	card, err := e.Store.GetCard(ctx, cardID)
	if err != nil {
		return err
	}
	if card.Status != store.StatusReview || card.XIssueRunID == 0 {
		return nil
	}
	run, runErr := e.Client.GetRun(ctx, card.XIssueRunID)
	changes, err := e.Client.GetRunChanges(ctx, card.XIssueRunID)
	if err != nil {
		return fmt.Errorf("get changes: %w", err)
	}
	worktree := ""
	var files []string
	var patches []string
	totalLines := 0
	for _, ch := range changes {
		if worktree == "" && ch.WorktreePath != "" {
			worktree = ch.WorktreePath
		}
		for _, f := range ch.Files {
			files = append(files, f.Path)
			totalLines += f.Additions + f.Deletions
		}
		if ch.Patch != "" {
			patches = append(patches, ch.Patch)
		}
	}

	targetBranch := "main"
	if runErr == nil && run.TargetBranch != "" {
		targetBranch = run.TargetBranch
	}
	round := card.RetryCount + 1
	headSHA, baseSHA := "", card.LastReviewedSHA
	if worktree != "" {
		// The agent leaves edits uncommitted; commit them so incremental review
		// can use real commit ranges (lastReviewedSHA..HEAD) from round two on.
		if cerr := xissue.CommitWorktree(ctx, worktree, fmt.Sprintf("xkb round %d", round)); cerr != nil {
			reason := "提交工作区失败: " + cerr.Error()
			findings := findingsForProblems([]string{reason})
			e.recordReview(ctx, card, round, "gate_cmd", "gate", baseSHA, "", "FAIL", findings)
			card.Status = store.StatusBlocked
			card.LastError = reason
			card.WaitReason = reason
			card.WaitSince = nil
			if uerr := e.Store.UpdateCard(ctx, card); uerr != nil {
				return uerr
			}
			e.eventf("card %s blocked: %s", card.ID, reason)
			return nil
		}
		if sha, err := xissue.HeadSHA(ctx, worktree); err == nil {
			headSHA = sha
		}
		if baseSHA == "" {
			baseSHA = xissue.MergeBase(ctx, worktree, targetBranch)
		}
		// xissue's patch may omit untracked files; after the commit above the
		// local diff is authoritative for both the gate and the reviewer.
		if stats, total, serr := xissue.ChangedStats(ctx, worktree, targetBranch); serr == nil {
			files = files[:0]
			for _, f := range stats {
				files = append(files, f.Path)
			}
			totalLines = total
		}
		if d, derr := xissue.GitDiff(ctx, worktree, targetBranch); derr == nil && strings.TrimSpace(d) != "" {
			patches = []string{d}
		}
	}

	// Stage 1: zero-token gate.
	var gateProblems []string
	if v := gate.CheckWorktreeViolations(files, card.Touches); len(v) > 0 {
		gateProblems = append(gateProblems,
			fmt.Sprintf("越界修改了禁区外文件: %s，请立即 git checkout 还原该文件，仅允许修改: %s",
				strings.Join(v, ", "), strings.Join(card.Touches, ", ")))
	}
	if worktree == "" {
		gateProblems = append(gateProblems, "无法定位 worktree，不能执行验收命令")
	} else {
		maxFiles, maxLines := e.diffBudget(card)
		if len(files) > maxFiles || totalLines > maxLines {
			gateProblems = append(gateProblems,
				fmt.Sprintf("改动规模超出 %s 卡片预算: %d 文件 / %d 行 > %d 文件 / %d 行；请把本卡拆成更小的卡片，或把改动缩到最小",
					card.Size, len(files), totalLines, maxFiles, maxLines))
		}
		// Do not spend time running verify for an oversized diff: the card
		// already failed the zero-token scope contract.
		if len(gateProblems) == 0 {
			if out, err := e.runVerify(ctx, worktree, card.VerifyCmd); err != nil {
				gateProblems = append(gateProblems, fmt.Sprintf("验收命令失败: %v\n%s", err, out))
			}
		}
	}
	gateFindings := findingsForProblems(gateProblems)
	e.recordReview(ctx, card, round, "gate_cmd", "gate", baseSHA, headSHA, verdict(gateProblems), gateFindings)
	if len(gateProblems) > 0 {
		return e.failCard(ctx, card, strings.Join(gateProblems, "\n"), "gate_cmd", baseSHA, headSHA, gateFindings)
	}

	// Stage 2: heterogeneous read-only AI review.
	if e.skipAIReview(ctx, card, files, totalLines) {
		e.eventf("card %s passed gate; AI review skipped", card.ID)
		return e.submit(ctx, card, baseSHA, headSHA)
	}
	diff := strings.Join(patches, "\n")
	if card.LastReviewedSHA != "" && worktree != "" {
		if d, err := xissue.DiffRange(ctx, worktree, card.LastReviewedSHA, headSHA); err == nil {
			diff = d
		}
	}
	if diff == "" && worktree != "" {
		if d, err := xissue.GitDiff(ctx, worktree, targetBranch); err == nil {
			diff = d
		}
	}
	agent := e.Router.ResolveReviewer(card.ExecAgent)
	card.ReviewAgent = agent.Agent
	prev := e.previousFindings(ctx, card.ID)
	res, err := e.Reviewer.RunReadOnlyReview(ctx, reviewer.Request{
		Card:         card,
		Reviewer:     agent,
		Diff:         diff,
		WorktreePath: worktree,
		Incremental:  card.LastReviewedSHA != "",
		PrevFindings: prev,
	})
	if err != nil {
		// A crashed reviewer cannot be fixed by the coding agent, and retrying
		// would burn tokens: block immediately for human attention.
		reason := "审查进程失败: " + err.Error()
		findings := findingsForProblems([]string{reason})
		e.recordReview(ctx, card, round, "ai_review", agent.Agent, baseSHA, headSHA, "FAIL", findings)
		card.Status = store.StatusBlocked
		card.LastError = reason
		card.WaitReason = reason
		card.WaitSince = nil
		if uerr := e.Store.UpdateCard(ctx, card); uerr != nil {
			return uerr
		}
		e.eventf("card %s blocked: %s", card.ID, reason)
		return nil
	}
	recordVerdict := res.Verdict
	if res.Failing() || res.MechanicalOnly() {
		recordVerdict = "FAIL"
	}
	e.recordReview(ctx, card, round, "ai_review", agent.Agent, baseSHA, headSHA, recordVerdict, res.Findings)
	if res.Failing() {
		card.LastReviewedSHA = headSHA
		reason := findingsText(res.Findings)
		return e.failCard(ctx, card, reason, "ai_review", baseSHA, headSHA, res.Findings)
	}
	if res.MechanicalOnly() {
		// Mechanical findings are repaired through the warm session; the next
		// round then runs stage 1 only.
		card.LastReviewedSHA = headSHA
		return e.failCard(ctx, card, mechanicalText(res.Findings), "ai_review", baseSHA, headSHA, res.Findings)
	}
	card.LastReviewedSHA = headSHA
	return e.submit(ctx, card, baseSHA, headSHA)
}

func (e *Engine) diffBudget(card *store.Card) (files, lines int) {
	if e.Config != nil {
		return e.Config.DiffBudget(card.Size)
	}
	if card.Size == "large" {
		return 4, 300
	}
	return 2, 120
}

func (e *Engine) runVerify(ctx context.Context, dir, command string) (string, error) {
	timeout := time.Duration(e.Config.VerifyTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	text := string(out)
	if cctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("超时 (%s)", timeout)
	}
	return tailLines(text, 80), err
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func verdict(problems []string) string {
	if len(problems) > 0 {
		return "FAIL"
	}
	return "PASS"
}

func findingsForProblems(problems []string) []reviewer.Finding {
	out := make([]reviewer.Finding, 0, len(problems))
	for _, p := range problems {
		out = append(out, reviewer.Finding{Severity: "blocking", Issue: p})
	}
	return out
}

func findingsText(findings []reviewer.Finding) string {
	var b strings.Builder
	for _, f := range findings {
		if f.Mechanical {
			continue
		}
		switch strings.ToLower(f.Severity) {
		case "blocking", "high", "medium":
			fmt.Fprintf(&b, "[%s] %s:%d %s", f.Severity, f.File, f.Line, f.Issue)
			if f.Fix != "" {
				fmt.Fprintf(&b, " (修复: %s)", f.Fix)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func mechanicalText(findings []reviewer.Finding) string {
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "[%s/mechanical] %s:%d %s\n", f.Severity, f.File, f.Line, f.Issue)
	}
	return strings.TrimSpace(b.String())
}

func (e *Engine) recordReview(ctx context.Context, card *store.Card, round int, stage, who, base, head, v string, findings any) {
	data, _ := json.Marshal(findings)
	rr := &store.ReviewRun{
		CardID: card.ID, Round: round, Stage: stage, Reviewer: who,
		BaseSHA: base, HeadSHA: head, Verdict: v, FindingsJSON: string(data),
	}
	if err := e.Store.RecordReviewRun(ctx, rr); err != nil {
		e.eventf("record review %s: %v", card.ID, err)
	}
}

// skipAIReview combines the whitelist with the mechanical-only rule from the
// previous round.
func (e *Engine) skipAIReview(ctx context.Context, card *store.Card, files []string, totalLines int) bool {
	runs, err := e.Store.ListReviewRuns(ctx, card.ID)
	if err == nil {
		for i := len(runs) - 1; i >= 0; i-- {
			if runs[i].Stage != "ai_review" {
				continue
			}
			var findings []reviewer.Finding
			if runs[i].Verdict == "FAIL" && json.Unmarshal([]byte(runs[i].FindingsJSON), &findings) == nil {
				res := reviewer.Result{Findings: findings}
				if res.MechanicalOnly() {
					return true
				}
			}
			break
		}
	}
	return e.Reviewer.ShouldSkipAIReview(card, files, totalLines)
}

func (e *Engine) previousFindings(ctx context.Context, cardID string) []reviewer.Finding {
	runs, err := e.Store.ListReviewRuns(ctx, cardID)
	if err != nil {
		return nil
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Stage != "ai_review" {
			continue
		}
		var out []reviewer.Finding
		_ = json.Unmarshal([]byte(runs[i].FindingsJSON), &out)
		return out
	}
	return nil
}

// failCard retries via the warm session, or blocks after two failures.
func (e *Engine) failCard(ctx context.Context, card *store.Card, reason, stage, base, head string, findings any) error {
	card.LastError = reason
	if card.RetryCount >= 2 {
		card.Status = store.StatusBlocked
		card.WaitReason = reason
		card.WaitSince = nil
		if err := e.Store.UpdateCard(ctx, card); err != nil {
			return err
		}
		if err := e.Client.CancelRun(ctx, card.XIssueRunID); err != nil {
			e.eventf("cancel run %d: %v", card.XIssueRunID, err)
		}
		e.eventf("card %s blocked after %d retries", card.ID, card.RetryCount)
		return nil
	}
	card.RetryCount++
	msg := fmt.Sprintf("验收失败（第 %d 次），请立即修复后重新提交：\n%s", card.RetryCount, reason)
	if err := e.Client.SendMessage(ctx, card.XIssueRunID, msg); err != nil {
		card.Status = store.StatusBlocked
		card.LastError = reason + "\n唤醒热会话失败: " + err.Error()
		card.WaitReason = card.LastError
		card.WaitSince = nil
		if uerr := e.Store.UpdateCard(ctx, card); uerr != nil {
			return uerr
		}
		e.eventf("card %s blocked: warm session unavailable: %v", card.ID, err)
		return nil
	}
	card.Status = store.StatusRunning
	clearWait(card)
	if err := e.Store.UpdateCard(ctx, card); err != nil {
		return err
	}
	e.eventf("card %s sent back for retry %d (%s)", card.ID, card.RetryCount, stage)
	return nil
}

// submit merges the branch and marks the card done.
func (e *Engine) submit(ctx context.Context, card *store.Card, base, head string) error {
	msg := fmt.Sprintf("feat(xkb): %s (%s)", card.Title, card.ID)
	if err := e.Client.SubmitMergeLocal(ctx, card.XIssueRunID, msg); err != nil {
		findings := findingsForProblems([]string{"合入失败: " + err.Error()})
		e.recordReview(ctx, card, card.RetryCount+1, "merge", "xissue", base, head, "FAIL", findings)
		return e.failCard(ctx, card, "合入失败: "+err.Error(), "merge", base, head, findings)
	}
	if head != "" && card.ProjectPath != "" {
		if err := verifyMergedLocally(ctx, card.ProjectPath, head); err != nil {
			findings := findingsForProblems([]string{"主干合入校验失败: " + err.Error()})
			e.recordReview(ctx, card, card.RetryCount+1, "merge", "local_verify", base, head, "FAIL", findings)
			return e.failCard(ctx, card, "主干合入校验失败: "+err.Error(), "merge", base, head, findings)
		}
	}
	now := time.Now().UTC()
	card.Status = store.StatusDone
	card.FinishedAt = &now
	card.LastError = ""
	clearWait(card)
	if err := e.Store.UpdateCard(ctx, card); err != nil {
		return err
	}
	e.eventf("card %s merged and done", card.ID)
	e.afterDone(ctx)
	return nil
}

func verifyMergedLocally(ctx context.Context, projectPath, head string) error {
	if _, err := os.Stat(filepath.Join(projectPath, ".git")); err != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", projectPath, "merge-base", "--is-ancestor", head, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		cmdMain := exec.CommandContext(ctx, "git", "-C", projectPath, "merge-base", "--is-ancestor", head, "main")
		if outMain, errMain := cmdMain.CombinedOutput(); errMain != nil {
			return fmt.Errorf("提交 %s 未合入本地主分支: %s", head, strings.TrimSpace(string(out)+"\n"+string(outMain)))
		}
	}
	return nil
}

// afterDone immediately pulls downstream cards and applies archive rules.
func (e *Engine) afterDone(ctx context.Context) {
	if err := e.Tick(ctx); err != nil {
		e.eventf("scheduler after done: %v", err)
	}
	if _, err := e.Archive(ctx); err != nil {
		e.eventf("archiver after done: %v", err)
	}
}
