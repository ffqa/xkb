package tui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xpier/xkb/internal/cli"
	"github.com/xpier/xkb/internal/gate"
	"github.com/xpier/xkb/internal/store"
)

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.inputMode {
		m.handleInputKey(msg)
		return m, nil
	}
	if m.confirmDelete != "" {
		id := m.confirmDelete
		m.confirmDelete = ""
		if key == "y" {
			m.deleteCard(id)
		} else {
			m.message = "已取消删除"
		}
		return m, nil
	}
	if m.detail || m.output || m.archivedView {
		switch key {
		case "esc", "enter", " ", "v":
			if key == "v" && !m.archivedView {
				break
			}
			m.detail = false
			m.output = false
			m.archivedView = false
			m.reviews = nil
			m.outputLines = nil
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			m.scrollOverlay(1)
		case "k", "up":
			m.scrollOverlay(-1)
		case "pgdown":
			m.scrollOverlay(m.overlayContentHeight())
		case "pgup":
			m.scrollOverlay(-m.overlayContentHeight())
		}
		return m, nil
	}
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case ":":
		m.inputMode = true
		m.inputText = ""
	case "h", "left":
		m.col--
		m.row = 0
		m.clamp()
	case "l", "right":
		m.col++
		m.row = 0
		m.clamp()
	case "j", "down":
		m.row++
		m.clamp()
	case "k", "up":
		m.row--
		m.clamp()
	case "enter", " ":
		if card := m.selected(); card != nil {
			m.detail = true
			m.output = false
			m.archivedView = false
			m.detailScroll = 0
			m.reviews, _ = m.Store.ListReviewRuns(context.Background(), card.ID)
		}
	case "v":
		m.archivedView = !m.archivedView
		m.archivedScroll = 0
		m.detail = false
		m.output = false
	case "t", "p", "r", "a", "A", "d":
		if m.readOnly {
			m.message = "只读模式：另一个 xkb 引擎正在运行"
			return m, nil
		}
		switch key {
		case "t":
			m.toggleTodo()
		case "p":
			m.cyclePriority()
		case "r":
			m.retryBlocked()
		case "a":
			m.archiveDone()
		case "A":
			m.archiveSelected()
		case "d":
			if card := m.selected(); card != nil {
				m.confirmDelete = card.ID
				m.message = fmt.Sprintf("删除 %s %s? (y/N)", card.ID, card.Title)
			}
		}
	}
	return m, nil
}

func (m *Model) handleInputKey(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyEnter:
		m.inputMode = false
		m.runCommand(strings.TrimSpace(m.inputText))
		m.inputText = ""
	case tea.KeyEsc, tea.KeyCtrlC:
		m.inputMode = false
		m.inputText = ""
	case tea.KeyBackspace:
		r := []rune(m.inputText)
		if len(r) > 0 {
			m.inputText = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		m.inputText = ""
	case tea.KeySpace:
		m.inputText += " "
	case tea.KeyRunes:
		m.inputText += string(msg.Runes)
	}
}

func (m *Model) runCommand(line string) {
	if line == "" {
		return
	}
	args := shellSplit(line)
	if len(args) == 0 {
		return
	}
	if args[0] == "help" {
		m.showOutput("help", commandHelp())
		return
	}
	if args[0] == "quit" || args[0] == "q" {
		m.message = "使用 q 退出"
		return
	}
	if m.command == nil {
		m.message = "命令执行器未配置"
		return
	}
	if m.readOnly && isMutatingCommand(args[0]) {
		m.message = "只读模式：另一个 xkb 引擎正在运行"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := m.command(ctx, args)
	m.refresh(ctx)
	if err != nil {
		if strings.TrimSpace(out) != "" {
			lines := append(strings.Split(strings.TrimRight(out, "\n"), "\n"), "error: "+err.Error())
			m.showOutput(strings.Join(args, " "), lines)
		} else {
			m.message = "错误: " + err.Error()
		}
		return
	}
	out = strings.TrimRight(out, "\n")
	if out != "" {
		m.showOutput(strings.Join(args, " "), strings.Split(out, "\n"))
	} else {
		m.message = "ok: " + strings.Join(args, " ")
	}
}

func (m *Model) showOutput(title string, lines []string) {
	m.output = true
	m.detail = false
	m.archivedView = false
	m.outputTitle = title
	m.outputLines = lines
	m.outputScroll = 0
}

func isMutatingCommand(cmd string) bool {
	switch cmd {
	case "add", "ready", "retry", "archive", "spec":
		return true
	}
	return false
}

func commandHelp() []string {
	return []string{
		"可用命令:",
		"  list [--status todo|done|...] [--json]",
		"  show <card-id> [--json]",
		"  add --project <dir> --title <t> --touches <paths> --verify <cmd> ...",
		"  ready <card-id>...",
		"  retry <card-id>...",
		"  archive [--all-done]",
		"  logs <card-id> [--tail N]",
		"  cancel <card-id>",
		"  spec new --project <dir> --title <t> --file <spec.md>",
		"  help",
	}
}

func (m *Model) scrollOverlay(delta int) {
	var total int
	var cursor *int
	switch {
	case m.detail:
		total, cursor = len(m.detailLines()), &m.detailScroll
	case m.archivedView:
		total, cursor = len(m.archivedLines()), &m.archivedScroll
	default:
		total, cursor = len(m.outputLines), &m.outputScroll
	}
	max := total - m.overlayContentHeight()
	if max < 0 {
		max = 0
	}
	*cursor += delta
	if *cursor > max {
		*cursor = max
	}
	if *cursor < 0 {
		*cursor = 0
	}
}

// shellSplit splits a command line, honouring single/double quotes and
// backslash escapes.
func shellSplit(line string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range line {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

func (m *Model) toggleTodo() {
	card := m.selected()
	if card == nil {
		return
	}
	switch card.Status {
	case store.StatusBacklog:
		if err := gate.ValidateTodoGate(card); err != nil {
			m.message = "门禁拒绝: " + err.Error()
			return
		}
		card.Status = store.StatusTodo
		m.message = card.ID + " -> Todo"
	case store.StatusTodo:
		card.Status = store.StatusBacklog
		m.message = card.ID + " -> Backlog"
	default:
		m.message = "只能在 Backlog 与 Todo 之间切换"
		return
	}
	if err := m.Store.UpdateCard(context.Background(), card); err != nil {
		m.message = err.Error()
	}
	m.refresh(context.Background())
}

func (m *Model) cyclePriority() {
	next := map[string]string{"P0": "P1", "P1": "P2", "P2": "P3", "P3": "P4", "P4": "P0"}
	m.updateSelected(func(c *store.Card) error {
		c.Priority = next[c.Priority]
		if c.Priority == "" {
			c.Priority = "P2"
		}
		m.message = fmt.Sprintf("%s -> %s", c.ID, c.Priority)
		return nil
	})
}

func (m *Model) retryBlocked() {
	ctx := context.Background()
	blocked, err := m.Store.ListCards(ctx, store.CardFilter{Statuses: []string{store.StatusBlocked}})
	if err != nil {
		m.message = err.Error()
		return
	}
	for _, c := range blocked {
		c.Status = store.StatusTodo
		c.RetryCount = 0
		c.LastError = ""
		c.LastReviewedSHA = ""
		c.XIssueRunID = 0
		if err := m.Store.UpdateCard(ctx, c); err != nil {
			m.message = err.Error()
			return
		}
	}
	m.message = fmt.Sprintf("重试 %d 张 blocked 卡片", len(blocked))
	m.refresh(ctx)
}

func (m *Model) archiveDone() {
	if m.Engine == nil {
		m.message = "归档引擎不可用"
		return
	}
	n, err := m.Engine.ArchiveAllDone(context.Background())
	if err != nil {
		m.message = err.Error()
		return
	}
	m.message = fmt.Sprintf("已归档 %d 张 Done 卡片", n)
	m.refresh(context.Background())
}

// archiveSelected archives the selected card whatever its status, reusing the
// CLI path so the in-flight xissue run is cancelled first.
func (m *Model) archiveSelected() {
	card := m.selected()
	if card == nil {
		m.message = "没有选中的卡片"
		return
	}
	var out bytes.Buffer
	if err := cli.Archive(context.Background(), m.Store, m.Engine, []string{card.ID}, &out); err != nil {
		m.message = "归档失败: " + err.Error()
		return
	}
	m.message = strings.TrimSpace(out.String())
	m.refresh(context.Background())
}

// deleteCard removes the card and its review evidence after confirmation.
func (m *Model) deleteCard(id string) {
	var out bytes.Buffer
	if err := cli.Delete(context.Background(), m.Store, m.Engine, []string{id}, &out); err != nil {
		m.message = "删除失败: " + err.Error()
		return
	}
	m.message = strings.TrimSpace(out.String())
	m.refresh(context.Background())
}
