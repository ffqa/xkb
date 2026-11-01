package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/engine"
	"github.com/xpier/xkb/internal/server"
	"github.com/xpier/xkb/internal/store"
	"github.com/xpier/xkb/internal/xissue"
)

// blockedTransport ignores request cancellation, so an engine cycle stays in
// flight past ctx cancellation: shutdown must wait for it.
type blockedTransport struct {
	started chan struct{}
	delay   time.Duration
	once    sync.Once
}

func (b *blockedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	b.once.Do(func() { close(b.started) })
	time.Sleep(b.delay)
	return nil, errors.New("stub: unreachable")
}

func TestServeSchedulesAndShutsDownOnCancel(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := config.Default()
	eng := engine.New(st, xissue.New("http://127.0.0.1:1", "", t.TempDir()), cfg)
	eng.Log = nil
	tr := &blockedTransport{started: make(chan struct{}), delay: 400 * time.Millisecond}
	eng.Client.HTTP = &http.Client{Transport: tr}

	srv := server.New(eng)
	ln, err := net.Listen("tcp", "127.0.0.1:0") // kernel-assigned, never 7789
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, eng, srv, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/api/health")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var health map[string]any
	err = json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || health["status"] != "ok" {
		cancel()
		t.Fatalf("health: %d %v", resp.StatusCode, health)
	}

	// Cancel while the scheduler is inside a cycle that ignores ctx.
	select {
	case <-tr.started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("engine scheduler did not run under server")
	}
	cancel()
	start := time.Now()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after cancel")
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("serve returned in %v: scheduler cycle was still in flight", elapsed)
	}
}
