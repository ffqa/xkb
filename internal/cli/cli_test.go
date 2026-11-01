package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/engine"
	"github.com/xpier/xkb/internal/store"
	"github.com/xpier/xkb/internal/xissue"
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

func TestLogsPrintsAgentEvents(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	card, err := st.CreateCard(context.Background(), &store.Card{
		ProjectPath: t.TempDir(), Title: "card", Touches: []string{"a"},
		Status: store.StatusRunning, XIssueRunID: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "runId": 5, "seq": 1, "type": "tool_call", "data": map[string]any{"text": "go test ./..."}},
		})
	}))
	defer srv.Close()
	cfg := &config.Config{MaxConcurrent: 5, SmallExecAgent: config.AgentModel{Agent: "omp"}}
	eng := engine.New(st, xissue.New(srv.URL, "", t.TempDir()), cfg)
	var out bytes.Buffer
	if err := Run(context.Background(), st, eng, []string{"logs", card.ID, "--tail", "1"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "tool_call") || !strings.Contains(out.String(), "go test") {
		t.Fatalf("logs output: %s", out.String())
	}
}

func TestCancelStopsRunAndBlocks(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	card, err := st.CreateCard(context.Background(), &store.Card{
		ProjectPath: t.TempDir(), Title: "card", Touches: []string{"a"},
		Status: store.StatusRunning, XIssueRunID: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/runs/5/cancel" {
			called = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	cfg := &config.Config{MaxConcurrent: 5, SmallExecAgent: config.AgentModel{Agent: "omp"}}
	eng := engine.New(st, xissue.New(srv.URL, "", t.TempDir()), cfg)
	var out bytes.Buffer
	if err := Run(context.Background(), st, eng, []string{"cancel", card.ID}, &out); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("cancel API not called")
	}
	got, _ := st.GetCard(context.Background(), card.ID)
	if got.Status != store.StatusBlocked || got.LastError != "已手动取消" {
		t.Fatalf("card: %+v", got)
	}
}

func TestArchiveNamedCardCancelsRunAndArchivesIssue(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	card, err := st.CreateCard(context.Background(), &store.Card{
		ProjectPath: t.TempDir(), Title: "card", Touches: []string{"a"},
		Status: store.StatusRunning, XIssueRunID: 5, XIssueIssueID: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	var cancelled, issueArchived bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/runs/5/cancel":
			cancelled = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/issues/7":
			issueArchived = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := &config.Config{MaxConcurrent: 5, SmallExecAgent: config.AgentModel{Agent: "omp"}}
	eng := engine.New(st, xissue.New(srv.URL, "", t.TempDir()), cfg)
	var out bytes.Buffer
	if err := Run(context.Background(), st, eng, []string{"archive", card.ID}, &out); err != nil {
		t.Fatal(err)
	}
	if !cancelled || !issueArchived {
		t.Fatalf("cancelled=%v issueArchived=%v", cancelled, issueArchived)
	}
	got, err := st.GetCard(context.Background(), card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusArchived || got.ArchivedAt == nil {
		t.Fatalf("card: %+v", got)
	}
}

func TestArchiveWithoutEngineArchivesLocally(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	card, err := st.CreateCard(context.Background(), &store.Card{
		ProjectPath: t.TempDir(), Title: "card", Touches: []string{"a"}, Status: store.StatusBlocked,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(context.Background(), st, nil, []string{"archive", card.ID}, &out); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCard(context.Background(), card.ID)
	if got.Status != store.StatusArchived {
		t.Fatalf("card: %+v", got)
	}
}

func TestDeleteRemovesCardAndReviewRuns(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	card, err := st.CreateCard(context.Background(), &store.Card{
		ProjectPath: t.TempDir(), Title: "card", Touches: []string{"a"}, Status: store.StatusDone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordReviewRun(context.Background(), &store.ReviewRun{
		CardID: card.ID, Round: 1, Stage: "gate_cmd", Reviewer: "gate", Verdict: "PASS",
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(context.Background(), st, nil, []string{"delete", card.ID}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCard(context.Background(), card.ID); err == nil {
		t.Fatal("card still present")
	}
	if runs, err := st.ListReviewRuns(context.Background(), card.ID); err != nil || len(runs) != 0 {
		t.Fatalf("review runs: %v %+v", err, runs)
	}
	out.Reset()
	if err := Run(context.Background(), st, nil, []string{"delete", card.ID}, &out); err == nil {
		t.Fatal("deleting an unknown card must fail")
	}
}

func TestDeleteCancelsActiveRun(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	card, err := st.CreateCard(context.Background(), &store.Card{
		ProjectPath: t.TempDir(), Title: "card", Touches: []string{"a"},
		Status: store.StatusReview, XIssueRunID: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/runs/9/cancel" {
			cancelled = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	cfg := &config.Config{MaxConcurrent: 5, SmallExecAgent: config.AgentModel{Agent: "omp"}}
	eng := engine.New(st, xissue.New(srv.URL, "", t.TempDir()), cfg)
	var out bytes.Buffer
	if err := Run(context.Background(), st, eng, []string{"delete", card.ID}, &out); err != nil {
		t.Fatal(err)
	}
	if !cancelled {
		t.Fatal("cancel API not called for review card")
	}
	if _, err := st.GetCard(context.Background(), card.ID); err == nil {
		t.Fatal("card still present")
	}
}
