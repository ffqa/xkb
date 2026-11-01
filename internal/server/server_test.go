package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/engine"
	"github.com/xpier/xkb/internal/store"
)

func newServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	return New(engine.New(st, nil, cfg))
}

func createCard(t *testing.T, s *Server, id, status string) *store.Card {
	t.Helper()
	card, err := s.Engine.Store.CreateCard(context.Background(),
		&store.Card{ID: id, Title: "card " + id, Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func setStatus(t *testing.T, s *Server, id, status string) {
	t.Helper()
	card, err := s.Engine.Store.GetCard(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	card.Status = status
	if err := s.Engine.Store.UpdateCard(context.Background(), card); err != nil {
		t.Fatal(err)
	}
}

func getJSON(t *testing.T, url string, into any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatal(err)
	}
}

func TestHealth(t *testing.T) {
	ts := httptest.NewServer(newServer(t).Handler())
	defer ts.Close()

	var got map[string]any
	getJSON(t, ts.URL+"/api/health", &got)
	if got["status"] != "ok" || got["online"] != false {
		t.Fatalf("health: %v", got)
	}
}

func TestCards(t *testing.T) {
	s := newServer(t)
	createCard(t, s, "xkb-001", store.StatusTodo)
	createCard(t, s, "xkb-002", store.StatusBlocked)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	var cards []store.Card
	getJSON(t, ts.URL+"/api/cards", &cards)
	if len(cards) != 2 {
		t.Fatalf("cards: %v", cards)
	}
	seen := map[string]string{}
	for _, c := range cards {
		seen[c.ID] = c.Status
	}
	if seen["xkb-001"] != store.StatusTodo || seen["xkb-002"] != store.StatusBlocked {
		t.Fatalf("cards: %v", cards)
	}
}

func TestEventsBroadcastTransitions(t *testing.T) {
	s := newServer(t)
	createCard(t, s, "xkb-001", store.StatusTodo)
	if err := s.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	br, closeEvents := openEvents(t, ts.URL)
	defer closeEvents()

	next := func(status, want string) {
		t.Helper()
		setStatus(t, s, "xkb-001", status)
		if err := s.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := readEvent(t, br)
		if got.Type != want || got.Card.ID != "xkb-001" {
			t.Fatalf("after %s: got %+v, want %s", status, got, want)
		}
	}
	next(store.StatusRunning, EventDispatched)
	next(store.StatusReview, EventReviewing)
	next(store.StatusBlocked, EventBlocked)
	next(store.StatusDone, EventDone)
}

func TestPublishBroadcasts(t *testing.T) {
	s := newServer(t)
	card := createCard(t, s, "xkb-001", store.StatusTodo)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	br, closeEvents := openEvents(t, ts.URL)
	defer closeEvents()

	s.Publish(Event{Type: EventBlocked, Card: card})
	if got := readEvent(t, br); got.Type != EventBlocked || got.Card.ID != "xkb-001" {
		t.Fatalf("published event: %+v", got)
	}
}

// openEvents subscribes and returns the reader plus a closer that must run
// before httptest.Server.Close: the SSE handler only returns once the client
// body is closed, and Close waits for outstanding handlers.
func openEvents(t *testing.T, baseURL string) (*bufio.Reader, func()) {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		resp.Body.Close()
		t.Fatalf("content type: %s", ct)
	}
	br := bufio.NewReader(resp.Body)
	waitForComment(t, br)
	return br, func() { resp.Body.Close() }
}

func TestServeStopsOnCancel(t *testing.T) {
	s := newServer(t)
	s.Config.ServerPort = 0 // kernel-assigned, never the fixed 7790
	ln, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	var health map[string]any
	getJSON(t, "http://"+ln.Addr().String()+"/api/health", &health)
	if health["status"] != "ok" {
		t.Fatalf("health: %v", health)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop on cancel")
	}
}

// waitForComment consumes the connection comment sent once subscribed.
func waitForComment(t *testing.T, br *bufio.Reader) {
	t.Helper()
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read comment: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			return
		}
	}
}

// readEvent returns the next SSE event, skipping comments and blank lines.
func readEvent(t *testing.T, br *bufio.Reader) Event {
	t.Helper()
	var ev Event
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			ev.Type = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatalf("bad data line %q: %v", line, err)
			}
		case strings.HasPrefix(line, ": "), line == "":
			continue
		}
		if ev.Type != "" && ev.Card != nil {
			return ev
		}
	}
}
