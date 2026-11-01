package gate

import (
	"strings"
	"testing"

	"github.com/xpier/xkb/internal/store"
)

func validCard(t *testing.T) *store.Card {
	t.Helper()
	dir := t.TempDir()
	return &store.Card{
		ProjectPath: dir, Priority: "P2", Size: "small",
		Touches: []string{"internal/a/"}, VerifyCmd: "go test ./...",
		Goal: "implement a", OutOfScope: "do not touch b", Acceptance: "- works",
	}
}

func TestValidateTodoGateAcceptsValidCard(t *testing.T) {
	if err := ValidateTodoGate(validCard(t)); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTodoGateRejects(t *testing.T) {
	cases := map[string]func(*store.Card){
		"priority":     func(c *store.Card) { c.Priority = "P9" },
		"size":         func(c *store.Card) { c.Size = "medium" },
		"project":      func(c *store.Card) { c.ProjectPath = "/definitely/not/here" },
		"no touches":   func(c *store.Card) { c.Touches = nil },
		"absolute":     func(c *store.Card) { c.Touches = []string{"/etc/passwd"} },
		"dotdot":       func(c *store.Card) { c.Touches = []string{"internal/../../etc"} },
		"empty verify": func(c *store.Card) { c.VerifyCmd = "  " },
		"goal":         func(c *store.Card) { c.Goal = "" },
		"placeholder":  func(c *store.Card) { c.Acceptance = "TODO: write tests" },
		"empty scope":  func(c *store.Card) { c.OutOfScope = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			card := validCard(t)
			mutate(card)
			if err := ValidateTodoGate(card); err == nil {
				t.Fatalf("expected rejection, got nil")
			}
		})
	}
}

func TestPathsOverlap(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{[]string{"internal/"}, []string{"internal/auth/a.go"}, true},
		{[]string{"internal/api"}, []string{"internal/api/handler.go"}, true},
		{[]string{"internal/api"}, []string{"internal/apix.go"}, false},
		{[]string{"cmd/main.go"}, []string{"cmd/other.go"}, false},
		{[]string{"internal/*.go"}, []string{"internal/main.go"}, true},
		{[]string{"internal/*.go"}, []string{"internal/deep/x.go"}, true},
		{[]string{"internal/auth/"}, []string{"web/app.ts"}, false},
	}
	for _, tc := range cases {
		if got := PathsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("PathsOverlap(%v,%v)=%v want %v", tc.a, tc.b, got, tc.want)
		}
		if got := PathsOverlap(tc.b, tc.a); got != tc.want {
			t.Errorf("PathsOverlap(%v,%v)=%v want %v", tc.b, tc.a, got, tc.want)
		}
	}
}

func TestCheckWorktreeViolations(t *testing.T) {
	allowed := []string{"internal/gate/", "AGENTS.md", "docs/*.md"}
	changed := []string{"internal/gate/gate.go", "AGENTS.md", "docs/spec.md", "web/app.ts", "internal/other/x.go"}
	got := CheckWorktreeViolations(changed, allowed)
	if len(got) != 2 || !strings.Contains(got[0], "web/app.ts") || !strings.Contains(got[1], "internal/other") {
		t.Fatalf("violations: %v", got)
	}
}
