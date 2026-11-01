package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xpier/xkb/internal/store"
)

func newServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st)
}

func call(t *testing.T, s *Server, method string, params any) (map[string]any, *rpcError) {
	t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		req["params"] = params
	}
	line, _ := json.Marshal(req)
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(string(line)+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result map[string]any `json:"result"`
		Error  *rpcError      `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("response %q: %v", out.String(), err)
	}
	return resp.Result, resp.Error
}

func toolText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content: %v", result)
	}
	return content[0].(map[string]any)["text"].(string)
}

func TestToolsList(t *testing.T) {
	s := newServer(t)
	res, err := call(t, s, "tools/list", nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := res["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("tools: %d", len(tools))
	}
}

func TestCreateSpecCardsPromoteAndBoard(t *testing.T) {
	s := newServer(t)
	project := t.TempDir()

	res, rpcErr := call(t, s, "tools/call", map[string]any{
		"name": "xkb_create_spec",
		"arguments": map[string]any{
			"project_path": project, "title": "spec", "body": "# spec",
		},
	})
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	var specOut struct {
		SpecID string `json:"spec_id"`
	}
	json.Unmarshal([]byte(toolText(t, res)), &specOut)
	if specOut.SpecID == "" {
		t.Fatal("missing spec id")
	}

	res, rpcErr = call(t, s, "tools/call", map[string]any{
		"name": "xkb_create_cards",
		"arguments": map[string]any{
			"spec_id":      specOut.SpecID,
			"project_path": project,
			"cards": []any{
				map[string]any{
					"title": "ready", "priority": "P1", "touches": []string{"internal/a/"},
					"verify_cmd": "true", "goal": "g", "out_of_scope": "o", "acceptance": "a",
					"auto_ready": true,
				},
				map[string]any{"title": "later", "touches": []string{"internal/b/"}},
			},
		},
	})
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	var cardsOut struct {
		IDs      []string `json:"ids"`
		Problems []string `json:"problems"`
	}
	json.Unmarshal([]byte(toolText(t, res)), &cardsOut)
	if len(cardsOut.IDs) != 2 || len(cardsOut.Problems) != 0 {
		t.Fatalf("cards: %+v", cardsOut)
	}

	res, rpcErr = call(t, s, "tools/call", map[string]any{
		"name":      "xkb_promote_todo",
		"arguments": map[string]any{"ids": []string{cardsOut.IDs[1]}},
	})
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	var promoteOut struct {
		Promoted []string `json:"promoted"`
		Problems []string `json:"problems"`
	}
	json.Unmarshal([]byte(toolText(t, res)), &promoteOut)
	// "later" lacks verify/goal/scope, so the gate rejects it.
	if len(promoteOut.Promoted) != 0 || len(promoteOut.Problems) != 1 {
		t.Fatalf("promote: %+v", promoteOut)
	}

	res, rpcErr = call(t, s, "tools/call", map[string]any{"name": "xkb_board_status", "arguments": map[string]any{}})
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	var boardOut struct {
		Counts map[string]int   `json:"counts"`
		Specs  []map[string]any `json:"specs"`
	}
	json.Unmarshal([]byte(toolText(t, res)), &boardOut)
	if boardOut.Counts[store.StatusTodo] != 1 || boardOut.Counts[store.StatusBacklog] != 1 {
		t.Fatalf("counts: %+v", boardOut.Counts)
	}
	if len(boardOut.Specs) != 1 || boardOut.Specs[0]["total"].(float64) != 2 {
		t.Fatalf("specs: %+v", boardOut.Specs)
	}
}

func TestInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	devinPath := filepath.Join(home, ".config", "devin", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(devinPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devinPath, []byte(`{"mcpServers":{"other":{"command":"x"}},"keep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := Install()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths: %v", paths)
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"xkb"`) || !strings.Contains(string(data), "mcp") {
			t.Fatalf("%s missing xkb entry: %s", p, data)
		}
	}
	data, _ := os.ReadFile(devinPath)
	if !strings.Contains(string(data), `"other"`) || !strings.Contains(string(data), `"keep": true`) {
		t.Fatalf("existing devin config not preserved: %s", data)
	}
}
