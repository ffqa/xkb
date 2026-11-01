package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xpier/xkb/internal/config"
	"github.com/xpier/xkb/internal/engine"
	"github.com/xpier/xkb/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func engineFor(st *store.Store, serverURL string) *engine.Engine {
	cfg := config.Default()
	cfg.ServerURL = serverURL
	eng := engine.New(st, nil, cfg)
	eng.Log = nil
	return eng
}

func healthOK(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func TestListUsesHTTPServer(t *testing.T) {
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", healthOK)
	mux.HandleFunc("GET /api/cards", func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode([]*store.Card{
			{ID: "xkb-900", Title: "from-server", Status: store.StatusBacklog},
			{ID: "xkb-901", Title: "todo-on-server", Status: store.StatusTodo},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	st := openStore(t)
	if _, err := st.CreateCard(context.Background(), &store.Card{Title: "local-only"}); err != nil {
		t.Fatal(err)
	}
	eng := engineFor(st, ts.URL)

	var out bytes.Buffer
	if err := Run(context.Background(), st, eng, []string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "from-server") || strings.Contains(out.String(), "local-only") {
		t.Fatalf("list did not read the server: %s", out.String())
	}
	if hits != 1 {
		t.Fatalf("GET /api/cards hits = %d", hits)
	}

	out.Reset()
	if err := Run(context.Background(), st, eng, []string{"list", "--status", "todo"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "todo-on-server") || strings.Contains(out.String(), "from-server") {
		t.Fatalf("status filter not applied: %s", out.String())
	}
}

func TestAddUsesHTTPServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", healthOK)
	mux.HandleFunc("POST /api/cards", func(w http.ResponseWriter, r *http.Request) {
		var card store.Card
		if err := json.NewDecoder(r.Body).Decode(&card); err != nil {
			t.Errorf("decode card: %v", err)
		}
		if card.Title != "remote" {
			t.Errorf("title = %q", card.Title)
		}
		card.ID = "xkb-777"
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(card)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	st := openStore(t)
	var out bytes.Buffer
	err := Run(context.Background(), st, engineFor(st, ts.URL), []string{"add", "--project", t.TempDir(), "--title", "remote"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "xkb-777" {
		t.Fatalf("add output: %q", out.String())
	}
	cards, err := st.ListCards(context.Background(), store.CardFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Fatalf("add wrote SQLite despite online server: %d card(s)", len(cards))
	}
}

func TestAddFallsBackWhenServerLacksWriteRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", healthOK)
	mux.HandleFunc("GET /api/cards", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	st := openStore(t)
	var out bytes.Buffer
	err := Run(context.Background(), st, engineFor(st, ts.URL), []string{"add", "--project", t.TempDir(), "--title", "fallback"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "xkb-001" {
		t.Fatalf("add output: %q", out.String())
	}
	cards, err := st.ListCards(context.Background(), store.CardFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].Title != "fallback" {
		t.Fatalf("cards: %d", len(cards))
	}
}

func TestOfflineServerFallsBackToSQLite(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close() // nothing is listening on that address any more

	st := openStore(t)
	var out bytes.Buffer
	if err := Run(context.Background(), st, engineFor(st, url), []string{"add", "--project", t.TempDir(), "--title", "solo"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "xkb-001" {
		t.Fatalf("add output: %q", out.String())
	}
	cards, err := st.ListCards(context.Background(), store.CardFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards: %d", len(cards))
	}
}

func TestReadyUsesHTTPServer(t *testing.T) {
	var path string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", healthOK)
	mux.HandleFunc("POST /api/cards/{id}/ready", func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]string{"status": store.StatusTodo})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	st := openStore(t)
	card, err := st.CreateCard(context.Background(), &store.Card{Title: "card"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(context.Background(), st, engineFor(st, ts.URL), []string{"ready", card.ID}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != card.ID+": todo\n" {
		t.Fatalf("ready output: %q", out.String())
	}
	if path != "/api/cards/"+card.ID+"/ready" {
		t.Fatalf("server path: %q", path)
	}
	got, err := st.GetCard(context.Background(), card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusBacklog {
		t.Fatalf("ready also wrote SQLite: status = %s", got.Status)
	}
}

func TestReadyReportsServerRejection(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", healthOK)
	mux.HandleFunc("POST /api/cards/{id}/ready", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "priority must be P0..P4", http.StatusBadRequest)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	st := openStore(t)
	card, err := st.CreateCard(context.Background(), &store.Card{Title: "card"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = Run(context.Background(), st, engineFor(st, ts.URL), []string{"ready", card.ID}, &out)
	if err == nil {
		t.Fatal("want rejection error")
	}
	if !strings.Contains(out.String(), card.ID+": priority must be P0..P4") {
		t.Fatalf("ready output: %q", out.String())
	}
}
