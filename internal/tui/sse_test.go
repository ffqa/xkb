package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/store"
)

func TestEventsURL(t *testing.T) {
	cases := []struct {
		cfg  *config.Config
		want string
	}{
		{&config.Config{ServerPort: 7790}, "http://127.0.0.1:7790/api/events"},
		{&config.Config{ServerURL: "http://127.0.0.1:9000/"}, "http://127.0.0.1:9000/api/events"},
		{&config.Config{}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := eventsURL(c.cfg); got != c.want {
			t.Fatalf("eventsURL(%+v) = %q, want %q", c.cfg, got, c.want)
		}
	}
}

func TestWaitEventReadsServerStream(t *testing.T) {
	sse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": connected\n\nevent: card_done\ndata: {\"type\":\"card_done\"}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer sse.Close()

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer ok.Close()

	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	down.Close()

	m := Model{sseURL: sse.URL}
	if msg := m.waitEventCmd()(); msg != (eventMsg{}) {
		t.Fatalf("stream msg = %T, want eventMsg", msg)
	}
	m.sseURL = ok.URL
	if msg := m.waitEventCmd()(); msg != (sseDownMsg{}) {
		t.Fatalf("bad status msg = %T, want sseDownMsg", msg)
	}
	m.sseURL = down.URL
	if msg := m.waitEventCmd()(); msg != (sseDownMsg{}) {
		t.Fatalf("refused msg = %T, want sseDownMsg", msg)
	}
}

func TestEventRefreshesBoardImmediately(t *testing.T) {
	m, st, card := newTestModel(t)
	m.sseURL = "http://127.0.0.1:1/api/events"
	if len(m.cols[store.StatusBacklog]) != 1 {
		t.Fatalf("backlog = %d, want 1", len(m.cols[store.StatusBacklog]))
	}

	card.Status = store.StatusRunning
	if err := st.UpdateCard(context.Background(), card); err != nil {
		t.Fatal(err)
	}
	if len(m.cols[store.StatusRunning]) != 0 {
		t.Fatal("model refreshed without an event")
	}

	updated, cmd := m.Update(eventMsg{})
	m = updated.(Model)
	if len(m.cols[store.StatusRunning]) != 1 || len(m.cols[store.StatusBacklog]) != 0 {
		t.Fatalf("event did not refresh: running=%d backlog=%d",
			len(m.cols[store.StatusRunning]), len(m.cols[store.StatusBacklog]))
	}
	if cmd == nil {
		t.Fatal("event must re-arm the stream")
	}
}

func TestSSEDownRetriesAndTickKeepsRunning(t *testing.T) {
	m, _, _ := newTestModel(t)
	if m.waitEventCmd() != nil {
		t.Fatal("no server URL must not start a stream")
	}

	m.sseURL = "http://127.0.0.1:1/api/events"
	updated, cmd := m.Update(sseDownMsg{})
	if cmd == nil {
		t.Fatal("stream failure must schedule a retry")
	}
	if _, cmd := updated.(Model).Update(sseRetryMsg{}); cmd == nil {
		t.Fatal("retry must re-open the stream")
	}
	if _, cmd := updated.(Model).Update(tickMsg{}); cmd == nil {
		t.Fatal("tick refresh must continue")
	}
}
