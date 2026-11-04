package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xpier/xkb/internal/gate"
	"github.com/xpier/xkb/internal/store"
)

// reconcileTodos explains why every todo card is not running and persists the
// answer. Cards with a permanent defect (missing dependency or dependency
// cycle) are blocked instead of silently dying in todo; capacity, path and
// upstream waits stay queued but carry a visible reason and age.
func (e *Engine) reconcileTodos(ctx context.Context, all []*store.Card, active []*store.Card) error {
	if len(all) == 0 {
		return nil
	}
	byID := make(map[string]*store.Card, len(all))
	for _, c := range all {
		byID[c.ID] = c
	}
	max := e.maxConcurrent()
	free := max - len(active)
	now := e.now().UTC()

	for _, card := range all {
		if card.Status != store.StatusTodo {
			continue
		}
		// A todo card must not carry run state from a dead dispatch; otherwise
		// xkb show/logs point at a run that no longer exists.
		cleaned := false
		if card.XIssueRunID != 0 {
			card.XIssueRunID = 0
			cleaned = true
		}
		if card.StartedAt != nil {
			card.StartedAt = nil
			cleaned = true
		}

		reason, block := todoWaitReason(card, byID, active, free, max)
		if block {
			card.Status = store.StatusBlocked
			card.LastError = reason
			card.WaitReason = reason
			card.WaitSince = nil
			if err := e.Store.UpdateCard(ctx, card); err != nil {
				return err
			}
			e.eventf("card %s blocked: %s", card.ID, reason)
			continue
		}
		changed := setWait(card, reason, now) || cleaned
		if !changed {
			continue
		}
		if err := e.Store.UpdateCard(ctx, card); err != nil {
			return err
		}
		if reason != "" {
			e.eventf("card %s waiting: %s", card.ID, reason)
		}
	}
	return nil
}

// todoWaitReason returns a human-readable reason and whether the card is
// permanently stuck. A permanent reason is blocked; a transient one is only
// displayed so the operator can see the queue move.
func todoWaitReason(card *store.Card, byID map[string]*store.Card, active []*store.Card, free, max int) (string, bool) {
	var missing, blocked, pending []string
	for _, id := range card.DependsOn {
		dep, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		switch dep.Status {
		case store.StatusDone, store.StatusArchived:
		case store.StatusBlocked:
			blocked = append(blocked, id)
		default:
			pending = append(pending, id)
		}
	}
	if len(missing) > 0 {
		return "依赖卡片不存在: " + strings.Join(missing, ", ") + "；请修正 depends_on", true
	}
	if cycle := dependencyCycle(card.ID, byID); len(cycle) > 0 {
		return "依赖环: " + strings.Join(cycle, " -> "), true
	}
	if len(blocked) > 0 {
		return "等待上游阻塞卡: " + strings.Join(blocked, ", ") + "（先 xkb retry " + blocked[0] + "）", false
	}
	if len(pending) > 0 {
		return "等待依赖完成: " + strings.Join(pending, ", "), false
	}
	if ids := conflictIDs(card, active); len(ids) > 0 {
		return "路径冲突: " + strings.Join(ids, ", "), false
	}
	if free <= 0 {
		return fmt.Sprintf("等待槽位 (%d/%d)", len(active), max), false
	}
	if card.LastError != "" {
		return "上次派发失败，等待重试", false
	}
	return "", false
}

// dependencyCycle returns the cycle path starting at id, or nil. Dependencies
// that do not exist are handled separately so a typo is reported as missing.
func dependencyCycle(id string, byID map[string]*store.Card) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var dfs func(string) []string
	dfs = func(cur string) []string {
		card, ok := byID[cur]
		if !ok {
			return nil
		}
		if card.Status == store.StatusDone || card.Status == store.StatusArchived {
			return nil
		}
		color[cur] = gray
		stack = append(stack, cur)
		for _, dep := range card.DependsOn {
			if _, ok := byID[dep]; !ok {
				continue
			}
			switch color[dep] {
			case gray:
				for i, v := range stack {
					if v == dep {
						return append([]string(nil), stack[i:]...)
					}
				}
			case white:
				if cyc := dfs(dep); len(cyc) > 0 {
					return cyc
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[cur] = black
		return nil
	}
	return dfs(id)
}

func conflictIDs(card *store.Card, active []*store.Card) []string {
	var ids []string
	for _, other := range active {
		if other.ProjectPath == card.ProjectPath && gate.PathsOverlap(card.Touches, other.Touches) {
			ids = append(ids, other.ID)
		}
	}
	return ids
}

// setWait records a reason once and keeps the original timestamp while the
// card stays queued, so wait age is continuous across reason changes.
func setWait(card *store.Card, reason string, now time.Time) bool {
	if reason == "" {
		if card.WaitReason == "" && card.WaitSince == nil {
			return false
		}
		card.WaitReason = ""
		card.WaitSince = nil
		return true
	}
	changed := false
	if card.WaitReason != reason {
		card.WaitReason = reason
		changed = true
	}
	if card.WaitSince == nil {
		card.WaitSince = &now
		changed = true
	}
	return changed
}

func clearWait(card *store.Card) {
	card.WaitReason = ""
	card.WaitSince = nil
}

func (e *Engine) maxConcurrent() int {
	if e.Config != nil && e.Config.MaxConcurrent > 0 {
		return e.Config.MaxConcurrent
	}
	return 5
}

func (e *Engine) todoStallMinutes() int {
	if e.Config != nil && e.Config.TodoStallMinutes > 0 {
		return e.Config.TodoStallMinutes
	}
	return 15
}
