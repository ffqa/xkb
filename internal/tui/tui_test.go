package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xpier/xkb/internal/cli"
	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/store"
)

func TestMain(m *testing.M) {
	// Force ANSI so column styling is observable in a non-TTY test run.
	os.Setenv("NO_COLOR", "")
	os.Setenv("CLICOLOR_FORCE", "1")
	os.Exit(m.Run())
}

func newTestModel(t *testing.T) (Model, *store.Store, *store.Card) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	card, err := st.CreateCard(context.Background(), &store.Card{
		ProjectPath: t.TempDir(), Title: "card one", Priority: "P2",
		Touches: []string{"internal/"}, VerifyCmd: "true",
		Goal: "goal text", OutOfScope: "scope", Acceptance: "acceptance",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := New(st, nil, &config.Config{MaxConcurrent: 5})
	m.width = 120
	return m, st, card
}

func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

func TestViewRendersColumns(t *testing.T) {
	m, _, _ := newTestModel(t)
	view := m.View()
	for _, want := range []string{"Backlog", "Todo", "Running", "Review", "Done", "card one", "Slots: 0/5"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestActiveColumnHighlight(t *testing.T) {
	m, _, _ := newTestModel(t)
	view := m.View()
	if strings.Count(view, "▸") != 1 || !strings.Contains(view, "\x1b[1;36m▸ Backlog (1)\x1b[0m") {
		t.Fatalf("active column marker/style:\n%q", view)
	}
	if !strings.Contains(view, "\x1b[2mTodo (0)\x1b[0m") {
		t.Fatalf("inactive column title not dim:\n%q", view)
	}
	updated, _ := m.Update(key('l'))
	m = updated.(Model)
	view = m.View()
	if strings.Count(view, "▸") != 1 || !strings.Contains(view, "▸ Todo (0)") {
		t.Fatalf("marker did not follow active column:\n%q", view)
	}
}

func TestNavigationAndDetail(t *testing.T) {
	m, _, _ := newTestModel(t)
	updated, _ := m.Update(key('l'))
	m = updated.(Model)
	if m.col != 1 {
		t.Fatalf("col %d", m.col)
	}
	updated, _ = m.Update(key('h'))
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if !m.detail {
		t.Fatal("detail not opened")
	}
	if !strings.Contains(m.View(), "acceptance") {
		t.Fatalf("detail missing acceptance:\n%s", m.View())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.detail {
		t.Fatal("detail not closed")
	}
}

func TestTogglePromoteAndPriority(t *testing.T) {
	m, st, card := newTestModel(t)
	updated, _ := m.Update(key('t'))
	m = updated.(Model)
	got, _ := st.GetCard(context.Background(), card.ID)
	if got.Status != store.StatusTodo {
		t.Fatalf("status %s", got.Status)
	}
	// Column 0 is now empty; move to Todo and cycle priority.
	updated, _ = m.Update(key('l'))
	m = updated.(Model)
	updated, _ = m.Update(key('p'))
	m = updated.(Model)
	got, _ = st.GetCard(context.Background(), card.ID)
	if got.Priority != "P3" {
		t.Fatalf("priority %s", got.Priority)
	}
	// t in Todo sends the card back to backlog.
	updated, _ = m.Update(key('t'))
	m = updated.(Model)
	got, _ = st.GetCard(context.Background(), card.ID)
	if got.Status != store.StatusBacklog {
		t.Fatalf("status %s", got.Status)
	}
}

func TestReadOnlyBlocksMutations(t *testing.T) {
	m, st, card := newTestModel(t)
	m.SetReadOnly(true)
	updated, _ := m.Update(key('t'))
	m = updated.(Model)
	got, _ := st.GetCard(context.Background(), card.ID)
	if got.Status != store.StatusBacklog {
		t.Fatalf("read-only TUI mutated card: %s", got.Status)
	}
	if !strings.Contains(m.message, "只读") {
		t.Fatalf("message %q", m.message)
	}
}

func TestRetryBlocked(t *testing.T) {
	m, st, card := newTestModel(t)
	card.Status = store.StatusBlocked
	card.RetryCount = 2
	card.LastError = "boom"
	if err := st.UpdateCard(context.Background(), card); err != nil {
		t.Fatal(err)
	}
	m.refresh(context.Background())
	updated, _ := m.Update(key('r'))
	m = updated.(Model)
	got, _ := st.GetCard(context.Background(), card.ID)
	if got.Status != store.StatusTodo || got.RetryCount != 0 || got.LastError != "" {
		t.Fatalf("retry result: %+v", got)
	}
	if !strings.Contains(m.message, "重试 1") {
		t.Fatalf("message %q", m.message)
	}
}

func TestLayoutFitsTerminal(t *testing.T) {
	m, _, _ := newTestModel(t)
	m.width, m.height = 80, 24
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 24 {
		t.Fatalf("view has %d lines, want full 24", len(lines))
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w > 80 {
			t.Fatalf("line %d width %d > 80: %q", i, w, line)
		}
	}
	for _, want := range []string{"Backlog", "Done", "输入命令"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q", want)
		}
	}
}

func TestTooSmallTerminal(t *testing.T) {
	m, _, _ := newTestModel(t)
	m.width, m.height = 20, 5
	if !strings.Contains(m.View(), "terminal too small") {
		t.Fatalf("view: %s", m.View())
	}
}

func TestCommandInputAndOutput(t *testing.T) {
	m, _, _ := newTestModel(t)
	var got []string
	m.SetCommand(func(ctx context.Context, args []string) (string, error) {
		got = args
		return "line one\nline two", nil
	})
	updated, _ := m.Update(key(':'))
	m = updated.(Model)
	if !m.inputMode {
		t.Fatal("input mode not opened")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("list")})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if strings.Join(got, " ") != "list" {
		t.Fatalf("command args: %v", got)
	}
	if !m.output || !strings.Contains(m.View(), "line one") {
		t.Fatalf("output overlay missing: %+v", m.outputLines)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.output {
		t.Fatal("output overlay not closed")
	}
}

func TestCommandListIntegration(t *testing.T) {
	m, _, _ := newTestModel(t)
	m.SetCommand(func(ctx context.Context, args []string) (string, error) {
		var out bytes.Buffer
		err := cli.Run(ctx, m.Store, nil, args, &out)
		return out.String(), err
	})
	m.inputMode = true
	m.inputText = "list"
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if !m.output {
		t.Fatalf("command produced no output overlay: %q", m.message)
	}
	if !strings.Contains(strings.Join(m.outputLines, "\n"), "card one") {
		t.Fatalf("list output missing card: %v", m.outputLines)
	}
}

func TestReadOnlyBlocksCommandMutations(t *testing.T) {
	m, _, _ := newTestModel(t)
	called := false
	m.SetCommand(func(ctx context.Context, args []string) (string, error) {
		called = true
		return "", nil
	})
	m.SetReadOnly(true)
	m.inputMode = true
	m.inputText = "ready xkb-001"
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if called {
		t.Fatal("read-only TUI executed a mutating command")
	}
	if !strings.Contains(m.message, "只读") {
		t.Fatalf("message: %q", m.message)
	}
}

func TestShellSplit(t *testing.T) {
	got := shellSplit(`add --title "hello world" --project '/tmp/a b'`)
	want := []string{"add", "--title", "hello world", "--project", "/tmp/a b"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("split: %#v", got)
	}
}

func TestCardShowsProjectSummaryLevelAndFinish(t *testing.T) {
	m, st, _ := newTestModel(t)
	project := filepath.Join(t.TempDir(), "proj-demo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	fin := time.Date(2026, 11, 1, 10, 30, 0, 0, time.Local)
	_, err := st.CreateCard(context.Background(), &store.Card{
		ProjectPath: project, Title: "rich title", Priority: "P1",
		Status: store.StatusDone, FinishedAt: &fin,
		Touches: []string{"a/"}, VerifyCmd: "true",
		Goal: "summary line\nsecond line", OutOfScope: "o", Acceptance: "a",
	})
	if err != nil {
		t.Fatal(err)
	}
	m.refresh(context.Background())
	m.width, m.height = 140, 32
	view := m.View()
	for _, want := range []string{"proj-demo", "P1", "rich title", "summary line", "完成 11-01 10:30"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestChineseContentFitsWidth(t *testing.T) {
	m, st, _ := newTestModel(t)
	project := filepath.Join(t.TempDir(), "项目目录名称")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	fin := time.Date(2026, 11, 1, 10, 30, 0, 0, time.Local)
	_, err := st.CreateCard(context.Background(), &store.Card{
		ProjectPath: project, Title: strings.Repeat("中文标题", 10), Priority: "P0",
		Status: store.StatusDone, FinishedAt: &fin,
		Goal: strings.Repeat("中文摘要很长", 10), Touches: []string{"a/"},
		VerifyCmd: "true", OutOfScope: "o", Acceptance: "a",
	})
	if err != nil {
		t.Fatal(err)
	}
	m.refresh(context.Background())
	m.width, m.height = 80, 24
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 24 {
		t.Fatalf("view has %d lines", len(lines))
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w > 80 {
			t.Fatalf("line %d width %d > 80: %q", i, w, line)
		}
	}
}

func TestArchiveSelectedAnyStatusAndOverlay(t *testing.T) {
	m, st, card := newTestModel(t)
	fin := time.Date(2026, 11, 1, 10, 30, 0, 0, time.Local)
	card.FinishedAt = &fin
	card.XIssueIssueID = 42
	if err := st.UpdateCard(context.Background(), card); err != nil {
		t.Fatal(err)
	}
	m.refresh(context.Background())

	updated, _ := m.Update(key('A'))
	m = updated.(Model)
	got, err := st.GetCard(context.Background(), card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusArchived || got.ArchivedAt == nil {
		t.Fatalf("archive by key failed: %s %v", got.Status, got.ArchivedAt)
	}
	if !strings.Contains(m.message, "archived") {
		t.Fatalf("message %q", m.message)
	}

	updated, _ = m.Update(key('v'))
	m = updated.(Model)
	view := m.View()
	for _, want := range []string{"Archived (1)", card.ID, "card one", "P2", "完成 11-01 10:30", "归档 ", "issue 42"} {
		if !strings.Contains(view, want) {
			t.Fatalf("archived overlay missing %q:\n%s", want, view)
		}
	}
	updated, _ = m.Update(key('v'))
	m = updated.(Model)
	if m.archivedView || !strings.Contains(m.View(), "Backlog") {
		t.Fatalf("v did not close the archived overlay")
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	m, st, card := newTestModel(t)
	updated, _ := m.Update(key('d'))
	m = updated.(Model)
	if m.confirmDelete != card.ID {
		t.Fatalf("confirm state %q", m.confirmDelete)
	}
	if _, err := st.GetCard(context.Background(), card.ID); err != nil {
		t.Fatalf("card deleted before confirmation: %v", err)
	}
	updated, _ = m.Update(key('n'))
	m = updated.(Model)
	if m.confirmDelete != "" {
		t.Fatalf("confirm state survived n: %q", m.confirmDelete)
	}
	if _, err := st.GetCard(context.Background(), card.ID); err != nil {
		t.Fatalf("card deleted on n: %v", err)
	}

	updated, _ = m.Update(key('d'))
	m = updated.(Model)
	updated, _ = m.Update(key('y'))
	m = updated.(Model)
	if _, err := st.GetCard(context.Background(), card.ID); err == nil {
		t.Fatal("y did not delete the card")
	}
}

func TestReadOnlyBlocksArchiveAndDeleteKeys(t *testing.T) {
	m, st, card := newTestModel(t)
	m.SetReadOnly(true)
	updated, _ := m.Update(key('A'))
	m = updated.(Model)
	got, _ := st.GetCard(context.Background(), card.ID)
	if got.Status != store.StatusBacklog || got.ArchivedAt != nil {
		t.Fatalf("read-only archived card: %+v", got)
	}
	updated, _ = m.Update(key('d'))
	m = updated.(Model)
	if m.confirmDelete != "" {
		t.Fatalf("read-only opened delete confirm: %q", m.confirmDelete)
	}
	if _, err := st.GetCard(context.Background(), card.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.message, "只读") {
		t.Fatalf("message %q", m.message)
	}
}

func TestHelpLineListsArchiveDeleteKeys(t *testing.T) {
	m, _, _ := newTestModel(t)
	help := m.helpLine(400)
	for _, want := range []string{"A 归档", "d 删除", "a 归档Done", "v 归档视图"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help line missing %q: %q", want, help)
		}
	}
}
