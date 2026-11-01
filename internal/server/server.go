// Package server exposes the xkb engine over HTTP: health, cards, and an SSE
// stream of card status transitions.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/engine"
	"github.com/xpier/xkb/internal/store"
)

// SSE event names.
const (
	EventDispatched = "card_dispatched"
	EventReviewing  = "card_reviewing"
	EventDone       = "card_done"
	EventBlocked    = "card_blocked"
)

// pollInterval is how often the server diffs card statuses for the SSE stream.
const pollInterval = time.Second

// Event is one card status transition sent to SSE subscribers.
type Event struct {
	Type string      `json:"type"`
	Card *store.Card `json:"card"`
}

// Server serves the engine over HTTP.
type Server struct {
	Engine *engine.Engine
	Config *config.Config

	mu     sync.Mutex
	subs   map[chan Event]struct{}
	status map[string]string
}

// New builds a server backed by eng.
func New(eng *engine.Engine) *Server {
	return &Server{Engine: eng, Config: eng.Config, subs: map[chan Event]struct{}{}}
}

// Handler returns the HTTP routes. Cards are read from the engine's store and
// events are derived from card status transitions.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/cards", s.handleCards)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	online, lastEvent := s.Engine.Status()
	writeJSON(w, map[string]any{"status": "ok", "online": online, "lastEvent": lastEvent})
}

func (s *Server) handleCards(w http.ResponseWriter, r *http.Request) {
	cards, err := s.Engine.Store.ListCards(r.Context(), store.CardFilter{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if cards == nil {
		cards = []*store.Card{}
	}
	writeJSON(w, cards)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	events := s.subscribe()
	defer s.unsubscribe(events)
	// Subscribe before flushing so a client that saw the comment cannot miss
	// a subsequent transition.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-events:
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			flusher.Flush()
		}
	}
}

func (s *Server) subscribe() chan Event {
	ch := make(chan Event, 16)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan Event) {
	s.mu.Lock()
	delete(s.subs, ch)
	s.mu.Unlock()
}

func (s *Server) publish(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
			// Drop for slow clients instead of stalling the poller.
		}
	}
}

// Poll reads the cards and broadcasts the transitions since the previous poll.
// Cards seen for the first time are recorded without an event.
func (s *Server) Poll(ctx context.Context) error {
	cards, err := s.Engine.Store.ListCards(ctx, store.CardFilter{})
	if err != nil {
		return err
	}
	var events []Event
	s.mu.Lock()
	prev := s.status
	s.status = make(map[string]string, len(cards))
	for _, card := range cards {
		s.status[card.ID] = card.Status
		if before, seen := prev[card.ID]; seen {
			if name := eventName(before, card.Status); name != "" {
				events = append(events, Event{Type: name, Card: card})
			}
		}
	}
	s.mu.Unlock()
	for _, ev := range events {
		s.publish(ev)
	}
	return nil
}

func eventName(before, after string) string {
	if before == after {
		return ""
	}
	switch after {
	case store.StatusRunning:
		if before == store.StatusTodo {
			return EventDispatched
		}
	case store.StatusReview:
		return EventReviewing
	case store.StatusDone:
		return EventDone
	case store.StatusBlocked:
		return EventBlocked
	}
	return ""
}

// Listen binds the loopback port from Config.
func (s *Server) Listen() (net.Listener, error) {
	port := s.Config.ServerPort
	if port < 0 {
		return nil, fmt.Errorf("bad serverPort %d", port)
	}
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// Run listens and serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	ln, err := s.Listen()
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve serves HTTP on ln and polls card transitions until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	go s.watch(ctx)

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		<-errc
		return nil
	}
}

func (s *Server) watch(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if err := s.Poll(ctx); err != nil && ctx.Err() == nil {
			s.logf("server poll: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.Engine != nil && s.Engine.Log != nil {
		s.Engine.Log(format, args...)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
