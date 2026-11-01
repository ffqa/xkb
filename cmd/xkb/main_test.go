package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/engine"
	"github.com/xpier/xkb/internal/server"
	"github.com/xpier/xkb/internal/store"
	"github.com/xpier/xkb/internal/xissue"
)

func TestServeSchedulesAndShutsDownOnCancel(t *testing.T) {
	// Health-only stub: the engine's RunOnce marks xissue reachable once the
	// scheduler loop actually runs.
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer daemon.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := config.Default()
	eng := engine.New(st, xissue.New(daemon.URL, "", t.TempDir()), cfg)
	srv := server.New(eng)
	ln, err := net.Listen("tcp", "127.0.0.1:0") // kernel-assigned, never 7789
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, eng, srv, ln) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if online, _ := eng.Status(); online {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("engine scheduler did not run under server")
		}
		time.Sleep(10 * time.Millisecond)
	}

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

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after cancel")
	}
}
