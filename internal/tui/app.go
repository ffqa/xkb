// Package tui renders the xkb kanban board.
package tui

import (
	"context"
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
}

// New builds the board model.
func New(st *store.Store, eng *engine.Engine, cfg *config.Config) Model {
	m := Model{Store: st, Engine: eng, Config: cfg, cols: map[string][]*store.Card{}}
	m.refresh(context.Background())
	return m
}

// Init starts the background engine loop.
func (m Model) Init() tea.Cmd {
	return m.tickCmd()
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
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
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
