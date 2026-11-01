// Package router picks the coding and review agents for a card.
package router

import (
	"strings"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/store"
)

// Router maps cards to heterogeneous coder/reviewer agents.
type Router struct {
	Small config.AgentModel
	Large config.AgentModel
}

// New builds a router from config.
func New(cfg *config.Config) *Router {
	return &Router{Small: cfg.SmallExecAgent, Large: cfg.LargeExecAgent}
}

// ResolveCoder honours an explicit card agent, otherwise routes by size and
// priority.
func (r *Router) ResolveCoder(card *store.Card) config.AgentModel {
	if card.ExecAgent != "" {
		return config.AgentModel{Agent: card.ExecAgent, Model: card.ExecModel}
	}
	if card.Size == "large" || card.Priority == "P0" || card.Priority == "P1" {
		return r.Large
	}
	return r.Small
}

// ResolveReviewer returns an agent different from the coder.
func (r *Router) ResolveReviewer(coderAgent string) config.AgentModel {
	if strings.EqualFold(coderAgent, "omp") {
		return r.Large
	}
	return r.Small
}
