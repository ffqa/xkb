package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/xpier/xkb/internal/store"
)

const (
	minTermWidth  = 40
	minTermHeight = 10
)

var (
	headerStyle   = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	columnStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	accentColumn  = columnStyle.BorderForeground(lipgloss.Color("6"))
	overlayStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	dimStyle      = lipgloss.NewStyle().Faint(true)
)

// View renders the board or an overlay. The output always fits the terminal.
func (m Model) View() string {
	width, height := m.width, m.height
	if width <= 0 {
		width = 100
	}
	if height <= 0 {
		height = 30
	}
	if m.width > 0 && m.width < minTermWidth || m.height > 0 && m.height < minTermHeight {
		return fmt.Sprintf("terminal too small: %dx%d (need %dx%d)", m.width, m.height, minTermWidth, minTermHeight)
	}
	_, _, contentW, bodyH := m.dimensions()
	body := m.bodyView(width, contentW, bodyH)
	lines := []string{
		m.headerLine(width),
		m.statusLine(width),
		body,
		m.helpLine(width),
		m.inputLine(width),
	}
	return strings.Join(lines, "\n")
}

// dimensions returns the usable board geometry.
func (m Model) dimensions() (width, colOuter, contentW, bodyH int) {
	width = m.width
	if width <= 0 {
		width = 100
	}
	height := m.height
	if height <= 0 {
		height = 30
	}
	colOuter = width / len(columnStatuses)
	if colOuter < 8 {
		colOuter = 8
	}
	contentW = colOuter - 4 // border (2) + horizontal padding (2)
	if contentW < 1 {
		contentW = 1
	}
	bodyH = height - 4 // header (2) + footer (2)
	if bodyH < 3 {
		bodyH = 3
	}
	return width, colOuter, contentW, bodyH
}

func (m Model) overlayContentHeight() int {
	_, _, _, bodyH := m.dimensions()
	h := bodyH - 3 // border (2) + title (1)
	if h < 1 {
		h = 1
	}
	return h
}

func (m Model) bodyView(width, contentW, bodyH int) string {
	if m.detail {
		return m.overlayView("Card "+m.cardID(), m.detailLines(), m.detailScroll, width, bodyH)
	}
	if m.output {
		return m.overlayView(m.outputTitle, m.outputLines, m.outputScroll, width, bodyH)
	}
	if m.archivedView {
		title := fmt.Sprintf("Archived (%d)", len(m.cols[store.StatusArchived]))
		return m.overlayView(title, m.archivedLines(), m.archivedScroll, width, bodyH)
	}
	return m.columnsView(contentW, bodyH)
}

// archivedLines lists archived cards newest first with project, id, title,
// priority, finish/archive times and the xissue issue id.
func (m Model) archivedLines() []string {
	cards := append([]*store.Card(nil), m.cols[store.StatusArchived]...)
	if len(cards) == 0 {
		return []string{"暂无归档卡片"}
	}
	sort.SliceStable(cards, func(i, j int) bool { return archivedTime(cards[i]).After(archivedTime(cards[j])) })
	lines := make([]string, 0, len(cards))
	for _, c := range cards {
		lines = append(lines, fmt.Sprintf("%s  %s  %s  %s  完成 %s  归档 %s  issue %d",
			projectName(c.ProjectPath), c.ID, c.Title, c.Priority,
			stamp(c.FinishedAt), stamp(c.ArchivedAt), c.XIssueIssueID))
	}
	return lines
}

func archivedTime(c *store.Card) time.Time {
	if c.ArchivedAt != nil {
		return *c.ArchivedAt
	}
	return time.Time{}
}

func stamp(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Local().Format("01-02 15:04")
}

func (m Model) cardID() string {
	if c := m.selected(); c != nil {
		return c.ID
	}
	return ""
}

func (m Model) headerLine(width int) string {
	active, max := m.slots()
	head := fmt.Sprintf("xkb  Slots: %d/%d  Blocked: %d  Waiting: %d  Stalled: %d  Archived: %d", active, max, m.blocked, m.waiting, m.stalled, m.archived)
	if m.Config != nil {
		head += "  " + m.Config.XIssueURL
	}
	if m.readOnly {
		head += "  [READ-ONLY]"
	}
	return headerStyle.Render(truncate(head, width))
}

func (m Model) statusLine(width int) string {
	var parts []string
	if m.Engine != nil {
		online, event := m.Engine.Status()
		state := "offline"
		if online {
			state = "online"
		}
		parts = append(parts, "xissue "+state)
		if event != "" {
			parts = append(parts, event)
		}
	}
	if m.message != "" {
		parts = append(parts, m.message)
	}
	line := strings.ReplaceAll(strings.Join(parts, "  |  "), "\n", " / ")
	return dimStyle.Render(truncate(line, width))
}

func (m Model) helpLine(width int) string {
	help := "h/l 切列  j/k 选卡  Enter 详情  t 待办  p 优先级  r 重试  A 归档  d 删除  a 归档Done  v 归档视图  : 命令  q 退出"
	return dimStyle.Render(truncate(help, width))
}

func (m Model) inputLine(width int) string {
	if m.inputMode {
		prefix := "› "
		text := m.inputText + "█"
		if lipgloss.Width(prefix+text) > width {
			return prefix + truncateLeft(text, width-lipgloss.Width(prefix))
		}
		return headerStyle.Render(prefix + text)
	}
	hint := ": 输入命令（help 查看可用命令）"
	if m.readOnly {
		hint = ": 只读模式（list/show/help 可用）"
	}
	return dimStyle.Render(truncate(hint, width))
}

func (m Model) columnsView(contentW, bodyH int) string {
	contentH := bodyH - 2 // border
	if contentH < 1 {
		contentH = 1
	}
	visible := contentH - 1 // column title
	if visible < 1 {
		visible = 1
	}
	var cols []string
	for i, status := range columnStatuses {
		cards := m.cols[status]
		title := fmt.Sprintf("%s (%d)", columnTitles[i], len(cards))
		if status == store.StatusRunning {
			_, max := m.slots()
			title = fmt.Sprintf("%s (%d/%d)", columnTitles[i], len(cards), max)
		}
		titleLine, colStyle := dimStyle.Render(truncate(title, contentW)), columnStyle
		if i == m.col {
			titleLine, colStyle = accentStyle.Render(truncate("▸ "+title, contentW)), accentColumn
		}
		lines := []string{titleLine}
		// Rich cards use four lines; fall back to one line on very short screens.
		const blockHeight = 4
		avail := contentH - 1
		compact := avail < blockHeight
		visible := avail
		if !compact {
			visible = avail / blockHeight
		}
		if visible < 1 {
			visible = 1
		}
		start := 0
		if i == m.col {
			if m.row >= visible {
				start = m.row - visible + 1
			}
			if maxStart := len(cards) - visible; start > maxStart {
				start = maxStart
			}
			if start < 0 {
				start = 0
			}
		}
		for j := start; j < len(cards) && j < start+visible; j++ {
			selected := i == m.col && j == m.row
			if compact {
				lines = append(lines, m.compactCardLine(cards[j], contentW, selected))
			} else {
				lines = append(lines, m.cardBlock(cards[j], contentW, selected)...)
			}
		}
		if len(cards) == 0 {
			lines = append(lines, dimStyle.Render("-"))
		}
		cols = append(cols, colStyle.Width(contentW+2).Height(contentH).Render(strings.Join(lines, "\n")))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols...)
}

func (m Model) compactCardLine(c *store.Card, width int, selected bool) string {
	text := fmt.Sprintf("%s %s %s", c.ID, c.Priority, c.Title)
	return styledLine(text, width, priorityStyle(c.Priority), selected)
}

// cardBlock renders project, id, title, summary, level and completion time.
func (m Model) cardBlock(c *store.Card, width int, selected bool) []string {
	meta := fmt.Sprintf("%s %s %s", c.ID, c.Priority, projectName(c.ProjectPath))
	if c.RetryCount > 0 {
		meta += fmt.Sprintf(" r%d", c.RetryCount)
	}
	if c.ExecAgent != "" {
		meta += " " + c.ExecAgent
	}
	return []string{
		styledLine(meta, width, priorityStyle(c.Priority), selected),
		styledLine(c.Title, width, headerStyle, selected),
		styledLine(summaryOf(c), width, dimStyle, selected),
		styledLine(finishLabel(c), width, dimStyle, selected),
	}
}

func styledLine(text string, width int, base lipgloss.Style, selected bool) string {
	text = truncate(text, width)
	if selected {
		return selectedStyle.Width(width).Render(text)
	}
	return base.Render(text)
}

func priorityStyle(p string) lipgloss.Style {
	switch p {
	case "P0":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	case "P1":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	case "P2":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	case "P3":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	}
}

func projectName(path string) string {
	p := filepath.Clean(path)
	base := filepath.Base(p)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return path
	}
	return base
}

// summaryOf returns the first non-empty line of the goal (or acceptance).
func summaryOf(c *store.Card) string {
	for _, text := range []string{c.Goal, c.Acceptance} {
		for _, line := range strings.Split(text, "\n") {
			if s := strings.TrimSpace(line); s != "" {
				return s
			}
		}
	}
	return "-"
}

func finishLabel(c *store.Card) string {
	if c.WaitReason != "" {
		if c.WaitSince != nil {
			return fmt.Sprintf("等待 %s · %s", stamp(c.WaitSince), c.WaitReason)
		}
		return "原因 " + c.WaitReason
	}
	return "完成 " + stamp(c.FinishedAt)
}

func (m Model) overlayView(title string, lines []string, scroll, width, bodyH int) string {
	innerH := bodyH - 2
	if innerH < 1 {
		innerH = 1
	}
	contentH := m.overlayContentHeight()
	maxScroll := len(lines) - contentH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	window := []string{}
	for i := scroll; i < len(lines) && len(window) < contentH; i++ {
		window = append(window, truncate(lines[i], width-4))
	}
	content := headerStyle.Render(truncate(title, width-4)) + "\n" + strings.Join(window, "\n")
	return overlayStyle.Width(width - 2).Height(innerH).Render(content)
}

// detailLines renders the card contract and review evidence.
func (m Model) detailLines() []string {
	card := m.selected()
	if card == nil {
		return []string{"no card selected"}
	}
	lines := []string{
		fmt.Sprintf("%s  %s", card.ID, card.Title),
		fmt.Sprintf("Status: %s   Priority: %s   Size: %s   Security: %v", card.Status, card.Priority, card.Size, card.SecuritySensitive),
		fmt.Sprintf("Project: %s", card.ProjectPath),
		fmt.Sprintf("Spec: %s", card.SpecID),
		fmt.Sprintf("Touches: %s", strings.Join(card.Touches, ", ")),
		fmt.Sprintf("DependsOn: %s", strings.Join(card.DependsOn, ", ")),
		fmt.Sprintf("Verify: %s", card.VerifyCmd),
		fmt.Sprintf("Exec: %s/%s   Reviewer: %s   Retry: %d", card.ExecAgent, card.ExecModel, card.ReviewAgent, card.RetryCount),
		fmt.Sprintf("Run: %d   ReviewedSHA: %s", card.XIssueRunID, card.LastReviewedSHA),
	}
	if card.WaitReason != "" {
		lines = append(lines, fmt.Sprintf("Waiting: %s (since %s)", card.WaitReason, stamp(card.WaitSince)))
	}
	lines = append(lines, "")
	addBlock := func(label, text string) {
		lines = append(lines, label+":")
		for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			lines = append(lines, "  "+l)
		}
	}
	addBlock("Goal", card.Goal)
	addBlock("OutOfScope", card.OutOfScope)
	addBlock("Acceptance", card.Acceptance)
	if len(m.reviews) > 0 {
		lines = append(lines, "", "Review records:")
		start := 0
		if len(m.reviews) > 20 {
			start = len(m.reviews) - 20
		}
		for _, rr := range m.reviews[start:] {
			lines = append(lines, fmt.Sprintf("  #%d %s %s %s %s %s", rr.Round, rr.Stage, rr.Verdict, rr.Reviewer, shortSHA(rr.HeadSHA), rr.FindingsJSON))
		}
	}
	if card.LastError != "" {
		addBlock("LastError", card.LastError)
	}
	return lines
}

func truncate(s string, n int) string {
	if n < 1 {
		return ""
	}
	if runewidth.StringWidth(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return runewidth.Truncate(s, n, "…")
}

func truncateLeft(s string, n int) string {
	if n < 1 {
		return ""
	}
	if runewidth.StringWidth(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	r := []rune(s)
	width := 0
	var out []rune
	for i := len(r) - 1; i >= 0; i-- {
		w := runewidth.RuneWidth(r[i])
		if width+w > n-1 {
			break
		}
		width += w
		out = append([]rune{r[i]}, out...)
	}
	return "…" + string(out)
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
