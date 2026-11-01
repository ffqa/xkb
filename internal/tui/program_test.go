package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xpier/xkb/internal/cli"
)

func TestProgramExecutesCommand(t *testing.T) {
	m, _, _ := newTestModel(t)
	m.SetCommand(func(ctx context.Context, args []string) (string, error) {
		var out bytes.Buffer
		err := cli.Run(ctx, m.Store, nil, args, &out)
		return out.String(), err
	})
	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(strings.NewReader(":list\rq")), tea.WithOutput(&out))
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "card one") {
		t.Fatalf("program output missing list overlay:\n%s", out.String())
	}
}
