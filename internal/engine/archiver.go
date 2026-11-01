package engine

import (
	"context"
	"sort"
	"time"

	"github.com/xpier/xkb/internal/store"
)

// Archive applies the three archive rules and returns how many cards moved.
func (e *Engine) Archive(ctx context.Context) (int, error) {
	cards, err := e.Store.ListCards(ctx, store.CardFilter{})
	if err != nil {
		return 0, err
	}
	var done []*store.Card
	for _, c := range cards {
		if c.Status == store.StatusDone {
			done = append(done, c)
		}
	}
	sort.SliceStable(done, func(i, j int) bool {
		return finishedAt(done[i]).Before(finishedAt(done[j]))
	})

	toArchive := map[string]*store.Card{}
	if extra := len(done) - e.Config.DoneMaxCount; extra > 0 {
		for _, c := range done[:extra] {
			toArchive[c.ID] = c
		}
	}
	ttl := time.Duration(e.Config.DoneTTLHours) * time.Hour
	now := time.Now().UTC()
	for _, c := range done {
		if now.Sub(finishedAt(c)) > ttl {
			toArchive[c.ID] = c
		}
	}

	// All cards of a spec finished: archive an hour after the last one.
	bySpec := map[string][]*store.Card{}
	for _, c := range cards {
		if c.SpecID != "" {
			bySpec[c.SpecID] = append(bySpec[c.SpecID], c)
		}
	}
	for _, specCards := range bySpec {
		allFinished := true
		var last time.Time
		var doneCards []*store.Card
		for _, c := range specCards {
			switch c.Status {
			case store.StatusDone:
				doneCards = append(doneCards, c)
				if f := finishedAt(c); f.After(last) {
					last = f
				}
			case store.StatusArchived:
			default:
				allFinished = false
			}
		}
		if allFinished && len(doneCards) > 0 && now.Sub(last) >= time.Hour {
			for _, c := range doneCards {
				toArchive[c.ID] = c
			}
		}
	}

	if len(toArchive) == 0 {
		return 0, nil
	}
	archived := 0
	for _, c := range toArchive {
		c.Status = store.StatusArchived
		at := now
		c.ArchivedAt = &at
		if err := e.Store.UpdateCard(ctx, c); err != nil {
			e.eventf("archive card %s: %v", c.ID, err)
			continue
		}
		if err := e.Client.ArchiveIssue(ctx, c.XIssueIssueID); err != nil {
			e.eventf("archive xissue issue %d: %v", c.XIssueIssueID, err)
		}
		archived++
	}
	return archived, nil
}

// ArchiveAllDone immediately archives every done card.
func (e *Engine) ArchiveAllDone(ctx context.Context) (int, error) {
	cards, err := e.Store.ListCards(ctx, store.CardFilter{Statuses: []string{store.StatusDone}})
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, c := range cards {
		c.Status = store.StatusArchived
		c.ArchivedAt = &now
		if err := e.Store.UpdateCard(ctx, c); err != nil {
			return 0, err
		}
		if err := e.Client.ArchiveIssue(ctx, c.XIssueIssueID); err != nil {
			e.eventf("archive xissue issue %d: %v", c.XIssueIssueID, err)
		}
	}
	return len(cards), nil
}

func finishedAt(c *store.Card) time.Time {
	if c.FinishedAt != nil {
		return *c.FinishedAt
	}
	return c.CreatedAt
}
