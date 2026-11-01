package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xpier/xkb/internal/engine"
	"github.com/xpier/xkb/internal/store"
)

// serverTimeout bounds one request to the local server so a wedged server never
// stalls the CLI: the probe fails and the command falls back to SQLite.
const serverTimeout = 2 * time.Second

// errUnrouted means the server build does not serve this endpoint, so the
// caller applies the change to SQLite itself. Only 404/405 count: a transport
// error or a 5xx might have written already, and retrying it locally would
// duplicate the card.
var errUnrouted = errors.New("server does not route this endpoint")

// client mirrors add/ready/list onto a running `xkb server`, so a card is never
// written by two processes at once. A nil client is always offline.
type client struct {
	base string
	hc   *http.Client
	up   bool // cached health probe, see online
}

// newClient returns nil when the CLI has no server URL to probe.
func newClient(eng *engine.Engine) *client {
	if eng == nil || eng.Config == nil || eng.Config.ServerURL == "" {
		return nil
	}
	return &client{
		base: strings.TrimRight(eng.Config.ServerURL, "/"),
		hc:   &http.Client{Timeout: serverTimeout},
	}
}

// online probes GET /api/health once per command.
func (c *client) online(ctx context.Context) bool {
	if c == nil {
		return false
	}
	if c.up {
		return true
	}
	code, _, err := c.do(ctx, http.MethodGet, "/api/health", nil)
	c.up = err == nil && code == http.StatusOK
	return c.up
}

func (c *client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

func unrouted(code int) bool {
	return code == http.StatusNotFound || code == http.StatusMethodNotAllowed
}

// statusErr turns a non-2xx reply into the error text the terminal prints, using
// the server's body when it has one.
func statusErr(code int, data []byte) error {
	if msg := strings.TrimSpace(string(data)); msg != "" {
		return errors.New(msg)
	}
	return fmt.Errorf("server returned %s", http.StatusText(code))
}

// listCards returns every card the server knows, or ok=false to read SQLite.
// A read is safe to retry locally, so any failure falls back.
func (c *client) listCards(ctx context.Context) ([]*store.Card, bool) {
	code, data, err := c.do(ctx, http.MethodGet, "/api/cards", nil)
	if err != nil || code != http.StatusOK {
		return nil, false
	}
	var cards []*store.Card
	if err := json.Unmarshal(data, &cards); err != nil {
		return nil, false
	}
	return cards, true
}

// createCard POSTs a card and returns the stored one (with its id).
func (c *client) createCard(ctx context.Context, card *store.Card) (*store.Card, error) {
	code, data, err := c.do(ctx, http.MethodPost, "/api/cards", card)
	if err != nil {
		return nil, err
	}
	if unrouted(code) {
		return nil, errUnrouted
	}
	if code < 200 || code > 299 {
		return nil, statusErr(code, data)
	}
	// The write is committed from here on, so a missing echo must never turn
	// into an error a user would retry: look the card up instead.
	if len(bytes.TrimSpace(data)) > 0 {
		var created store.Card
		if err := json.Unmarshal(data, &created); err == nil && created.ID != "" {
			return &created, nil
		}
	}
	return c.findCreated(ctx, card)
}

// findCreated recovers the id of a card the server stored without echoing it.
func (c *client) findCreated(ctx context.Context, want *store.Card) (*store.Card, error) {
	known, ok := c.listCards(ctx)
	if ok {
		var found *store.Card
		for _, card := range known {
			if card.ProjectPath != want.ProjectPath || card.Title != want.Title {
				continue
			}
			if found == nil || card.CreatedAt.After(found.CreatedAt) ||
				(card.CreatedAt.Equal(found.CreatedAt) && card.ID > found.ID) {
				found = card
			}
		}
		if found != nil {
			return found, nil
		}
	}
	return nil, fmt.Errorf("card was created on the server but its id was not returned; check `xkb list` before adding it again")
}

// readyCard asks the server to run the todo gate and move the card to todo.
func (c *client) readyCard(ctx context.Context, id string) error {
	path := "/api/cards/" + url.PathEscape(id) + "/ready"
	code, data, err := c.do(ctx, http.MethodPost, path, struct{}{})
	if err != nil {
		return err
	}
	if unrouted(code) {
		return errUnrouted
	}
	if code < 200 || code > 299 {
		return statusErr(code, data)
	}
	return nil
}
