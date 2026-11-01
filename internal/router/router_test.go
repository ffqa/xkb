package router

import (
	"testing"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/store"
)

func TestResolveCoderAndReviewer(t *testing.T) {
	r := New(&config.Config{
		SmallExecAgent: config.AgentModel{Agent: "omp"},
		LargeExecAgent: config.AgentModel{Agent: "devin", Model: "swe-2-high"},
	})
	cases := []struct {
		card  store.Card
		agent string
	}{
		{store.Card{Priority: "P3", Size: "small"}, "omp"},
		{store.Card{Priority: "P2", Size: "small"}, "omp"},
		{store.Card{Priority: "P1", Size: "small"}, "devin"},
		{store.Card{Priority: "P4", Size: "large"}, "devin"},
		{store.Card{Priority: "P4", Size: "small", ExecAgent: "codex"}, "codex"},
	}
	for _, tc := range cases {
		if got := r.ResolveCoder(&tc.card).Agent; got != tc.agent {
			t.Errorf("%+v -> %s want %s", tc.card, got, tc.agent)
		}
	}
	if got := r.ResolveReviewer("omp").Agent; got != "devin" {
		t.Fatalf("reviewer for omp: %s", got)
	}
	if got := r.ResolveReviewer("devin").Agent; got != "omp" {
		t.Fatalf("reviewer for devin: %s", got)
	}
}
