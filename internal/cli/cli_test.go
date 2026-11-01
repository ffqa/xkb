package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xpier/xkb/internal/store"
)

func TestShowAcceptsFlagAfterID(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	card, err := st.CreateCard(context.Background(), &store.Card{
		ProjectPath: t.TempDir(), Title: "card", Touches: []string{"a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(context.Background(), st, nil, []string{"show", card.ID, "--json"}, &out); err != nil {
		t.Fatalf("show with trailing --json: %v", err)
	}
	if !strings.Contains(out.String(), `"id": "`+card.ID+`"`) {
		t.Fatalf("output: %s", out.String())
	}
	out.Reset()
	if err := Run(context.Background(), st, nil, []string{"show", "--json", card.ID}, &out); err != nil {
		t.Fatalf("show with leading --json: %v", err)
	}
}

func TestAddAndList(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	project := t.TempDir()
	var out bytes.Buffer
	err = Run(context.Background(), st, nil, []string{
		"add", "--project", project, "--title", "hello", "--touches", "a/",
		"--verify", "true", "--goal", "g", "--scope", "o", "--accept", "a",
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "xkb-001" {
		t.Fatalf("add output: %q", out.String())
	}
	out.Reset()
	if err := Run(context.Background(), st, nil, []string{"list", "--status", "backlog"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Fatalf("list output: %s", out.String())
	}
}

func TestSplitList(t *testing.T) {
	got := SplitList(" a/ , b.go ,, ")
	if len(got) != 2 || got[0] != "a/" || got[1] != "b.go" {
		t.Fatalf("split: %#v", got)
	}
}
