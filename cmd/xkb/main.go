package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xpier/xkb/internal/cli"
	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/engine"
	"github.com/xpier/xkb/internal/mcp"
	"github.com/xpier/xkb/internal/store"
	"github.com/xpier/xkb/internal/tui"
	"github.com/xpier/xkb/internal/xissue"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "xkb:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if len(args) == 0 {
		return runTUI(st, cfg)
	}
	client := xissue.New(cfg.XIssueURL, cfg.XIssueToken, cfg.XIssueDataDir)
	eng := engine.New(st, client, cfg)
	ctx := context.Background()

	switch args[0] {
	case "daemon":
		lock, err := engine.AcquireLock(engineLockPath())
		if errors.Is(err, engine.ErrLocked) {
			return fmt.Errorf("engine.lock 已被占用：已有 daemon 或 TUI 在运行")
		}
		if err != nil {
			return err
		}
		defer lock.Close()
		sctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		fmt.Println("xkb daemon running; db:", cfg.DBPath)
		eng.Run(sctx)
		return nil
	case "spec", "add", "ready", "list", "show", "retry", "archive":
		return cli.Run(ctx, st, eng, args, os.Stdout)
	case "mcp":
		return cmdMCP(ctx, st, args[1:])
	default:
		return fmt.Errorf("unknown command %q (want daemon, spec, add, ready, list, show, retry, archive, mcp)", args[0])
	}
}

func engineLockPath() string {
	return filepath.Join(config.Home(), "engine.lock")
}

func runTUI(st *store.Store, cfg *config.Config) error {
	lock, err := engine.AcquireLock(engineLockPath())
	if err != nil && !errors.Is(err, engine.ErrLocked) {
		return err
	}
	defer lock.Close()

	var eng *engine.Engine
	if lock != nil {
		client := xissue.New(cfg.XIssueURL, cfg.XIssueToken, cfg.XIssueDataDir)
		eng = engine.New(st, client, cfg)
		eng.Log = nil // stderr output would corrupt the alt screen; events show in the header.
	}
	m := tui.New(st, eng, cfg)
	m.SetCommand(func(ctx context.Context, args []string) (string, error) {
		var buf bytes.Buffer
		err := cli.Run(ctx, st, eng, args, &buf)
		return buf.String(), err
	})
	if lock == nil {
		m.SetReadOnly(true)
		m.SetMessage("daemon 正在运行：TUI 只读，不参与调度/验收")
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func cmdMCP(ctx context.Context, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	install := fs.Bool("install", false, "register xkb mcp in local agent configs")
	fs.Parse(args)
	if *install {
		paths, err := mcp.Install()
		for _, p := range paths {
			fmt.Println("registered:", p)
		}
		return err
	}
	return mcp.New(st).Serve(ctx, os.Stdin, os.Stdout)
}
