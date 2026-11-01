// Package engine contains the scheduler, verifier, and archiver.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/gate"
	"github.com/xpier/xkb/internal/reviewer"
	"github.com/xpier/xkb/internal/router"
	"github.com/xpier/xkb/internal/store"
	"github.com/xpier/xkb/internal/xissue"
)

// Engine wires the scheduler, verifier, and archiver together.
type Engine struct {
	Store    *store.Store
	Client   *xissue.Client
	Router   *router.Router
	Reviewer *reviewer.Reviewer
	Config   *config.Config
	Log      func(format string, args ...any)

	tickMu    sync.Mutex
	verifyMu  sync.Mutex
	verifying map[string]bool

	stateMu   sync.Mutex
	online    bool
	lastEvent string

	// Now is injectable for timeout tests.
	Now func() time.Time
}

// New builds an engine.
func New(st *store.Store, client *xissue.Client, cfg *config.Config) *Engine {
	return &Engine{
		Store:     st,
		Client:    client,
		Router:    router.New(cfg),
		Reviewer:  reviewer.New(time.Duration(cfg.ReviewTimeoutSeconds) * time.Second),
		Config:    cfg,
		Log:       log.Printf,
		verifying: map[string]bool{},
		Now:       time.Now,
	}
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

// eventf records the latest event for the TUI and forwards it to the logger.
func (e *Engine) eventf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	e.stateMu.Lock()
	e.lastEvent = msg
	e.stateMu.Unlock()
	e.logf("%s", msg)
}

// Status returns the last observed xissue reachability and event message.
func (e *Engine) Status() (online bool, lastEvent string) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	return e.online, e.lastEvent
}

// RunOnce performs one scheduler + archiver cycle.
func (e *Engine) RunOnce(ctx context.Context) {
	ok := e.Client.Health(ctx) == nil
	e.stateMu.Lock()
	if e.online != ok && e.lastEvent != "" {
		state := "offline"
		if ok {
			state = "online"
		}
		e.lastEvent = "xissue " + state
	}
	e.online = ok
	e.stateMu.Unlock()
	if err := e.Tick(ctx); err != nil {
		e.eventf("scheduler: %v", err)
	}
	if _, err := e.Archive(ctx); err != nil {
		e.eventf("archiver: %v", err)
	}
}

// Run loops until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	e.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.RunOnce(ctx)
		}
	}
}

// Tick performs one scheduling pass.
func (e *Engine) Tick(ctx context.Context) error {
	if !e.tickMu.TryLock() {
		return nil
	}
	defer e.tickMu.Unlock()

	all, err := e.Store.ListCards(ctx, store.CardFilter{})
	if err != nil {
		return err
	}
	status := make(map[string]string, len(all))
	for _, c := range all {
		status[c.ID] = c.Status
	}
	// Freeze the candidate set before syncing runs: a card that just failed or
	// timed out must not be re-dispatched in the same tick.
	var candidates []*store.Card
	for _, c := range all {
		if c.Status == store.StatusTodo {
			candidates = append(candidates, c)
		}
	}
	sortCards(candidates)

	active, err := e.syncActive(ctx, all)
	if err != nil {
		return err
	}
	if len(active) >= e.Config.MaxConcurrent {
		return nil
	}

	for _, card := range candidates {
		if len(active) >= e.Config.MaxConcurrent {
			break
		}
		if !dependsSatisfied(card, status) {
			continue
		}
		if conflicts(card, active) {
			continue
		}
		dispatched, err := e.dispatch(ctx, card)
		if err != nil {
			if !errors.Is(err, errAlreadyClaimed) {
				e.eventf("dispatch %s: %v", card.ID, err)
			}
			continue
		}
		status[card.ID] = store.StatusRunning
		active = append(active, dispatched)
	}
	return nil
}

const awaitingPermissionMsg = "xissue 等待权限确认（awaiting_permission），请在 xissue 中处理"

// syncActive polls active runs, advances completed ones to review, schedules
// verification, and handles failed, submitted, or timed-out runs.
func (e *Engine) syncActive(ctx context.Context, all []*store.Card) ([]*store.Card, error) {
	var active []*store.Card
	for _, card := range all {
		if card.Status != store.StatusRunning && card.Status != store.StatusReview {
			continue
		}
		if card.Status == store.StatusRunning && card.XIssueRunID > 0 {
			run, err := e.Client.GetRun(ctx, card.XIssueRunID)
			if err != nil {
				e.eventf("get run %d for %s: %v", card.XIssueRunID, card.ID, err)
			} else {
				switch run.Status {
				case "completed":
					card.Status = store.StatusReview
					card.LastError = ""
					if err := e.Store.UpdateCard(ctx, card); err != nil {
						return nil, err
					}
				case "submitted":
					// Already merged/submitted outside xkb; never submit twice.
					card.Status = store.StatusDone
					card.LastError = ""
					now := e.now()
					card.FinishedAt = &now
					if err := e.Store.UpdateCard(ctx, card); err != nil {
						return nil, err
					}
					e.eventf("card %s already submitted in xissue; marked done", card.ID)
				case "failed", "cancelled", "discarded":
					e.handleDeadRun(ctx, card, run)
				case "awaiting_permission":
					if card.LastError != awaitingPermissionMsg {
						card.LastError = awaitingPermissionMsg
						if err := e.Store.UpdateCard(ctx, card); err != nil {
							return nil, err
						}
					}
				}
				if card.Status == store.StatusRunning && e.runTimedOut(card) {
					_ = e.Client.CancelRun(ctx, card.XIssueRunID)
					e.handleDeadRun(ctx, card, &xissue.RunInfo{Status: "cancelled", Error: "运行超时，已自动取消"})
				}
			}
		}
		switch card.Status {
		case store.StatusReview:
			active = append(active, card)
			e.startVerify(card)
		case store.StatusRunning:
			active = append(active, card)
		}
	}
	return active, nil
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) runTimedOut(card *store.Card) bool {
	if card.StartedAt == nil {
		return false
	}
	minutes := 30
	if e.Config != nil && e.Config.RunTimeoutMinutes > 0 {
		minutes = e.Config.RunTimeoutMinutes
	}
	return e.now().Sub(*card.StartedAt) > time.Duration(minutes)*time.Minute
}

func (e *Engine) handleDeadRun(ctx context.Context, card *store.Card, run *xissue.RunInfo) {
	reason := fmt.Sprintf("xissue run %s: %s", run.Status, strings.TrimSpace(run.Error))
	if card.RetryCount >= 2 {
		card.Status = store.StatusBlocked
		card.LastError = reason
	} else {
		card.RetryCount++
		card.Status = store.StatusTodo
		card.LastError = reason
		card.XIssueRunID = 0
	}
	if err := e.Store.UpdateCard(ctx, card); err != nil {
		e.eventf("update card %s: %v", card.ID, err)
	}
}

var errAlreadyClaimed = errors.New("card already claimed")

func (e *Engine) dispatch(ctx context.Context, card *store.Card) (*store.Card, error) {
	claimed, err := e.Store.ClaimTodo(ctx, card.ID)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, errAlreadyClaimed
	}
	projectID, repoID, err := e.Client.EnsureProjectAndRepo(ctx, card.ProjectPath)
	if err != nil {
		// Connectivity/registration errors are transient; keep the card queued
		// without burning its retry budget.
		e.failDispatch(ctx, card, "ensure project: "+err.Error(), false)
		return nil, err
	}
	coder := e.Router.ResolveCoder(card)
	// Reuse the issue created by a previous failed dispatch instead of leaving
	// a trail of empty issues in xissue.
	if card.XIssueIssueID == 0 {
		card.XIssueIssueID, err = e.Client.CreateIssue(ctx, projectID, repoID, card.Title, card.Goal, card.Priority)
		if err != nil {
			e.failDispatch(ctx, card, err.Error(), true)
			return nil, err
		}
	}
	runID, err := e.Client.StartRun(ctx, card.XIssueIssueID, coder.Agent, coder.Model, BuildPrompt(card))
	if err != nil {
		e.failDispatch(ctx, card, err.Error(), true)
		return nil, err
	}
	now := time.Now().UTC()
	card.XIssueProj = projectID
	card.XIssueRunID = runID
	card.ExecAgent = coder.Agent
	card.ExecModel = coder.Model
	card.Status = store.StatusRunning
	card.StartedAt = &now
	card.LastError = ""
	if err := e.Store.UpdateCard(ctx, card); err != nil {
		return nil, err
	}
	e.eventf("dispatched %s -> %s/%s (issue %d run %d)", card.ID, coder.Agent, coder.Model, card.XIssueIssueID, runID)
	return card, nil
}

// failDispatch records a failed attempt. count=false keeps the card in todo
// for transient connectivity errors; count=true blocks after two tries.
func (e *Engine) failDispatch(ctx context.Context, card *store.Card, reason string, count bool) {
	if count {
		card.RetryCount++
	}
	card.Status = store.StatusTodo
	card.StartedAt = nil
	if count && card.RetryCount >= 2 {
		card.Status = store.StatusBlocked
	}
	card.LastError = reason
	if err := e.Store.UpdateCard(ctx, card); err != nil {
		e.eventf("record dispatch failure %s: %v", card.ID, err)
	}
	e.eventf("dispatch %s failed (retry %d): %s", card.ID, card.RetryCount, reason)
}

// BuildPrompt renders the compact execution prompt (under 500 characters).
func BuildPrompt(card *store.Card) string {
	return fmt.Sprintf("目标: %s\n禁区: %s\n验收: %s\ntouches: %s\n只修改 touches 内文件；不要执行看板流转或合并命令。",
		card.Goal, card.OutOfScope, card.VerifyCmd, strings.Join(card.Touches, ", "))
}

func sortCards(cards []*store.Card) {
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].Priority != cards[j].Priority {
			return cards[i].Priority < cards[j].Priority
		}
		if !cards[i].CreatedAt.Equal(cards[j].CreatedAt) {
			return cards[i].CreatedAt.Before(cards[j].CreatedAt)
		}
		return cards[i].ID < cards[j].ID
	})
}

func dependsSatisfied(card *store.Card, status map[string]string) bool {
	for _, dep := range card.DependsOn {
		st := status[dep]
		if st != store.StatusDone && st != store.StatusArchived {
			return false
		}
	}
	return true
}

func conflicts(card *store.Card, active []*store.Card) bool {
	for _, other := range active {
		if other.ProjectPath == card.ProjectPath && gate.PathsOverlap(card.Touches, other.Touches) {
			return true
		}
	}
	return false
}
