// Package cli implements xkb's non-interactive subcommands so both the binary
// and the TUI command bar share one implementation.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/xpier/xkb/internal/engine"
	"github.com/xpier/xkb/internal/gate"
	"github.com/xpier/xkb/internal/store"
)

// Run executes one subcommand (without the leading "xkb").
func Run(ctx context.Context, st *store.Store, eng *engine.Engine, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command")
	}
	switch args[0] {
	case "spec":
		if len(args) < 2 || args[1] != "new" {
			return fmt.Errorf("usage: xkb spec new --project <dir> --title <title> --file <spec.md>")
		}
		return SpecNew(ctx, st, args[2:], out)
	case "add":
		return Add(ctx, st, args[1:], out)
	case "ready":
		return Ready(ctx, st, args[1:], out)
	case "list":
		return List(ctx, st, args[1:], out)
	case "show":
		return Show(ctx, st, args[1:], out)
	case "retry":
		return Retry(ctx, st, args[1:], out)
	case "archive":
		return Archive(ctx, eng, args[1:], out)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// SplitList splits a comma-separated flag value.
func SplitList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func readBody(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// SpecNew creates a spec from a markdown file.
func SpecNew(ctx context.Context, st *store.Store, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("spec new", flag.ContinueOnError)
	fs.SetOutput(out)
	project := fs.String("project", "", "local git repository path")
	title := fs.String("title", "", "spec title")
	file := fs.String("file", "", "markdown file with the spec body")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *title == "" || *file == "" {
		return fmt.Errorf("usage: xkb spec new --project <dir> --title <title> --file <spec.md>")
	}
	body, err := readBody(*file)
	if err != nil {
		return err
	}
	sp, err := st.CreateSpec(ctx, &store.Spec{ProjectPath: *project, Title: *title, Body: body})
	if err != nil {
		return err
	}
	fmt.Fprintln(out, sp.ID)
	return nil
}

// Add creates a backlog card.
func Add(ctx context.Context, st *store.Store, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(out)
	project := fs.String("project", "", "local git repository path")
	title := fs.String("title", "", "card title")
	priority := fs.String("priority", "P2", "P0..P4")
	size := fs.String("size", "small", "small|large")
	touches := fs.String("touches", "", "comma separated allowed paths")
	verify := fs.String("verify", "", "verification command")
	depends := fs.String("depends", "", "comma separated card ids")
	goal := fs.String("goal", "", "goal text")
	scope := fs.String("scope", "", "out of scope text")
	accept := fs.String("accept", "", "acceptance checklist")
	spec := fs.String("spec", "", "spec id")
	file := fs.String("file", "", "markdown file used as goal when --goal is empty")
	security := fs.Bool("security", false, "security sensitive")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *title == "" {
		return fmt.Errorf("usage: xkb add --project <dir> --title <title> [flags]")
	}
	if *goal == "" && *file != "" {
		body, err := readBody(*file)
		if err != nil {
			return err
		}
		*goal = body
	}
	card, err := st.CreateCard(ctx, &store.Card{
		SpecID: *spec, ProjectPath: *project, Title: *title,
		Priority: *priority, Size: *size, SecuritySensitive: *security,
		Touches: SplitList(*touches), DependsOn: SplitList(*depends),
		VerifyCmd: *verify, Goal: *goal, OutOfScope: *scope, Acceptance: *accept,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(out, card.ID)
	return nil
}

// Ready validates cards and moves them to todo.
func Ready(ctx context.Context, st *store.Store, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: xkb ready <card-id>...")
	}
	var failed int
	for _, id := range args {
		card, err := st.GetCard(ctx, id)
		if err != nil {
			fmt.Fprintf(out, "%s: %v\n", id, err)
			failed++
			continue
		}
		if card.Status != store.StatusBacklog {
			fmt.Fprintf(out, "%s: status is %s, want backlog\n", id, card.Status)
			failed++
			continue
		}
		if err := gate.ValidateTodoGate(card); err != nil {
			fmt.Fprintf(out, "%s: %v\n", id, err)
			failed++
			continue
		}
		card.Status = store.StatusTodo
		if err := st.UpdateCard(ctx, card); err != nil {
			fmt.Fprintf(out, "%s: %v\n", id, err)
			failed++
			continue
		}
		fmt.Fprintf(out, "%s: todo\n", id)
	}
	if failed > 0 {
		return fmt.Errorf("%d card(s) rejected", failed)
	}
	return nil
}

// List prints cards.
func List(ctx context.Context, st *store.Store, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(out)
	status := fs.String("status", "", "filter by status")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	filter := store.CardFilter{}
	if *status != "" {
		filter.Statuses = SplitList(*status)
	}
	cards, err := st.ListCards(ctx, filter)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out, cards)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPRIO\tSTATUS\tAGENT\tRETRY\tTITLE")
	for _, c := range cards {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", c.ID, c.Priority, c.Status, c.ExecAgent, c.RetryCount, c.Title)
	}
	return w.Flush()
}

// Show prints one card and its review records.
func Show(ctx context.Context, st *store.Store, args []string, out io.Writer) error {
	asJSON := false
	var ids []string
	for _, a := range args {
		if a == "--json" || a == "-json" {
			asJSON = true
			continue
		}
		ids = append(ids, a)
	}
	if len(ids) != 1 {
		return fmt.Errorf("usage: xkb show <card-id> [--json]")
	}
	card, err := st.GetCard(ctx, ids[0])
	if err != nil {
		return err
	}
	reviews, err := st.ListReviewRuns(ctx, card.ID)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(out, map[string]any{"card": card, "reviews": reviews})
	}
	fmt.Fprintf(out, "%s  %s\n", card.ID, card.Title)
	fmt.Fprintf(out, "status=%s priority=%s size=%s security=%v retry=%d\n", card.Status, card.Priority, card.Size, card.SecuritySensitive, card.RetryCount)
	fmt.Fprintf(out, "project=%s spec=%s\n", card.ProjectPath, card.SpecID)
	fmt.Fprintf(out, "touches=%s depends_on=%s\n", strings.Join(card.Touches, ","), strings.Join(card.DependsOn, ","))
	fmt.Fprintf(out, "verify=%s\n", card.VerifyCmd)
	fmt.Fprintf(out, "exec=%s/%s reviewer=%s run=%d sha=%s\n", card.ExecAgent, card.ExecModel, card.ReviewAgent, card.XIssueRunID, card.LastReviewedSHA)
	fmt.Fprintf(out, "goal:\n%s\nout_of_scope:\n%s\nacceptance:\n%s\n", card.Goal, card.OutOfScope, card.Acceptance)
	if card.LastError != "" {
		fmt.Fprintf(out, "last_error:\n%s\n", card.LastError)
	}
	for _, rr := range reviews {
		fmt.Fprintf(out, "review #%d %s %s %s %s\n", rr.Round, rr.Stage, rr.Verdict, rr.Reviewer, rr.FindingsJSON)
	}
	return nil
}

// Retry resets cards to todo with a fresh retry budget.
func Retry(ctx context.Context, st *store.Store, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: xkb retry <card-id>...")
	}
	for _, id := range args {
		card, err := st.GetCard(ctx, id)
		if err != nil {
			return err
		}
		card.Status = store.StatusTodo
		card.RetryCount = 0
		card.LastError = ""
		card.LastReviewedSHA = ""
		card.XIssueRunID = 0
		if err := st.UpdateCard(ctx, card); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: todo\n", id)
	}
	return nil
}

// Archive archives done cards.
func Archive(ctx context.Context, eng *engine.Engine, args []string, out io.Writer) error {
	if eng == nil {
		return fmt.Errorf("归档引擎不可用")
	}
	fs := flag.NewFlagSet("archive", flag.ContinueOnError)
	fs.SetOutput(out)
	allDone := fs.Bool("all-done", false, "archive every done card now")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var n int
	var err error
	if *allDone {
		n, err = eng.ArchiveAllDone(ctx)
	} else {
		n, err = eng.Archive(ctx)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "archived %d card(s)\n", n)
	return nil
}
