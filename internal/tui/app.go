// Package tui renders the xkb kanban board.
package tui

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/engine"
	"github.com/xpier/xkb/internal/store"
)

var columnStatuses = []string{
	store.StatusBacklog,
	store.StatusTodo,
	store.StatusRunning,
	store.StatusReview,
	store.StatusDone,
}

var columnTitles = []string{"Backlog", "Todo", "Running", "Review", "Done"}

type tickMsg time.Time

// eventMsg reports one server SSE event; sseDownMsg means the stream ended or
// could not connect; sseRetryMsg re-arms the listener after a delay.
type (
	eventMsg    struct{}
	sseDownMsg  struct{}
	sseRetryMsg struct{}
)

// sseRetryDelay is how long the board waits before reconnecting the event stream.
const sseRetryDelay = 5 * time.Second

// Model is the bubbletea board model.
type Model struct {
	Store  *store.Store
	Engine *engine.Engine
	Config *config.Config

	cols     map[string][]*store.Card
	blocked  int
	archived int
	reviews  []*store.ReviewRun
	col      int
	row      int
	detail   bool
	readOnly bool

	detailScroll int
	output       bool
	outputTitle  string
	outputLines  []string
	outputScroll int

	inputMode bool
	inputText string
	command   func(ctx context.Context, args []string) (string, error)

	width   int
	height  int
	message string
	sseURL  string
}

// New builds the board model.
func New(st *store.Store, eng *engine.Engine, cfg *config.Config) Model {
	m := Model{Store: st, Engine: eng, Config: cfg, cols: map[string][]*store.Card{}, sseURL: eventsURL(cfg)}
	m.refresh(context.Background())
	return m
}

// Init starts the background engine loop and listens for server events.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.tickCmd(), m.waitEventCmd())
}

func (m Model) tickCmd() tea.Cmd {
	return func() tea.Msg {
		if m.Engine != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			m.Engine.RunOnce(ctx)
			cancel()
		}
		return tickMsg(time.Now())
	}
}

func tickLater() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		m.refresh(context.Background())
		return m, tickLater()
	case eventMsg:
		// Server said something changed: redraw now instead of waiting for the tick.
		m.refresh(context.Background())
		return m, m.waitEventCmd()
	case sseDownMsg:
		// No server (daemon-only mode): the 2s tick keeps the board fresh.
		return m, tea.Tick(sseRetryDelay, func(time.Time) tea.Msg { return sseRetryMsg{} })
	case sseRetryMsg:
		return m, m.waitEventCmd()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// waitEventCmd blocks until the server sends one SSE event, then disconnects.
// Unreachable servers fall back to the tick refresh.
// shortcut: one connection per event; upgrade to a persistent reader if
// transitions ever become frequent.
func (m Model) waitEventCmd() tea.Cmd {
	url := m.sseURL
	if url == "" {
		return nil
	}
	return func() tea.Msg {
		resp, err := http.Get(url)
		if err != nil {
			return sseDownMsg{}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return sseDownMsg{}
		}
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data:") {
				return eventMsg{}
			}
		}
		return sseDownMsg{}
	}
}

// eventsURL builds the SSE endpoint from the configured server address.
func eventsURL(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	base := strings.TrimRight(cfg.ServerURL, "/")
	if base == "" && cfg.ServerPort > 0 {
		base = fmt.Sprintf("http://127.0.0.1:%d", cfg.ServerPort)
	}
	if base == "" {
		return ""
	}
	return base + "/api/events"
}

// SetMessage sets the status line shown above the board.
func (m *Model) SetMessage(message string) { m.message = message }

// SetReadOnly disables board mutations while another process owns the engine.
func (m *Model) SetReadOnly(readOnly bool) { m.readOnly = readOnly }

// SetCommand installs the executor used by the ":" command bar.
func (m *Model) SetCommand(fn func(ctx context.Context, args []string) (string, error)) {
	m.command = fn
}

func (m *Model) refresh(ctx context.Context) {
	cards, err := m.Store.ListCards(ctx, store.CardFilter{})
	if err != nil {
		m.message = err.Error()
		return
	}
	cols := map[string][]*store.Card{}
	m.blocked, m.archived = 0, 0
	for _, c := range cards {
		cols[c.Status] = append(cols[c.Status], c)
		switch c.Status {
		case store.StatusBlocked:
			m.blocked++
		case store.StatusArchived:
			m.archived++
		}
	}
	m.cols = cols
	m.clamp()
}

func (m *Model) clamp() {
	if m.col > len(columnStatuses)-1 {
		m.col = len(columnStatuses) - 1
	}
	if m.col < 0 {
		m.col = 0
	}
	rows := m.cols[columnStatuses[m.col]]
	if m.row >= len(rows) {
		m.row = len(rows) - 1
	}
	if m.row < 0 {
		m.row = 0
	}
}

func (m Model) selected() *store.Card {
	rows := m.cols[columnStatuses[m.col]]
	if m.row < 0 || m.row >= len(rows) {
		return nil
	}
	return rows[m.row]
}

func (m *Model) updateSelected(fn func(*store.Card) error) {
	card := m.selected()
	if card == nil {
		return
	}
	if err := fn(card); err != nil {
		m.message = err.Error()
		return
	}
	if err := m.Store.UpdateCard(context.Background(), card); err != nil {
		m.message = err.Error()
		return
	}
	m.refresh(context.Background())
}

func (m Model) slots() (int, int) {
	active := len(m.cols[store.StatusRunning]) + len(m.cols[store.StatusReview])
	max := 5
	if m.Config != nil && m.Config.MaxConcurrent > 0 {
		max = m.Config.MaxConcurrent
	}
	return active, max
}
