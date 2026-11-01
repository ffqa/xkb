package xissue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxPatchBytes = 256 << 10

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	msg := strings.TrimSpace(errb.String())
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("git not found in PATH")
		}
		if msg != "" {
			return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return out.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out.String(), nil
}

// HeadSHA returns the worktree HEAD commit.
func HeadSHA(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// MergeBase returns merge-base(base, HEAD), falling back to base or HEAD.
func MergeBase(ctx context.Context, dir, base string) string {
	refs := []string{base}
	if base != "" {
		refs = append([]string{"origin/" + base}, refs...)
	}
	if out, err := git(ctx, dir, "rev-parse", "--verify", "HEAD"); err == nil {
		head := strings.TrimSpace(out)
		for _, ref := range refs {
			if out, err := git(ctx, dir, "merge-base", ref, "HEAD"); err == nil {
				if mb := strings.TrimSpace(out); mb != "" {
					return mb
				}
			}
		}
		return head
	}
	return ""
}

// CommitWorktree stages and commits all worktree changes so incremental review
// can diff commit ranges. It is a no-op when the worktree is clean.
func CommitWorktree(ctx context.Context, dir, message string) error {
	status, err := git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}
	if _, err := git(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	if _, err := git(ctx, dir, "-c", "user.name=xkb", "-c", "user.email=xkb@localhost",
		"-c", "commit.gpgsign=false", "commit", "--no-verify", "-m", message); err != nil {
		return err
	}
	return nil
}

// DiffRange returns git diff from..to, capped at 256KB.
func DiffRange(ctx context.Context, dir, from, to string) (string, error) {
	out, err := git(ctx, dir, "diff", "--no-color", "--no-ext-diff", from+".."+to)
	if err != nil {
		return "", err
	}
	if len(out) > maxPatchBytes {
		return out[:maxPatchBytes] + "\n... (patch truncated at 256KB)\n", nil
	}
	return out, nil
}

// GitDiff returns a unified diff from merge-base(base, HEAD) through the
// working tree, including untracked files, capped at 256KB.
func GitDiff(ctx context.Context, dir, base string) (string, error) {
	mb := MergeBase(ctx, dir, base)
	if mb == "" {
		return "", fmt.Errorf("cannot resolve diff base in %s", dir)
	}
	diff, err := git(ctx, dir, "diff", "--no-color", "--no-ext-diff", mb)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	buf.WriteString(diff)

	untracked, err := git(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err == nil {
		for _, rel := range strings.Split(untracked, "\x00") {
			if rel == "" || buf.Len() >= maxPatchBytes {
				continue
			}
			// git diff --no-index exits 1 when files differ; that is expected.
			out, derr := git(ctx, dir, "diff", "--no-color", "--no-index", "--", "/dev/null", filepath.ToSlash(rel))
			if derr != nil && out == "" {
				continue
			}
			buf.WriteString(out)
		}
	}
	if buf.Len() > maxPatchBytes {
		return buf.String()[:maxPatchBytes] + "\n... (patch truncated at 256KB)\n", nil
	}
	return buf.String(), nil
}
