package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xpier/xkb/internal/store"
)

func newServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "xkb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, nil)
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
	if len(tools) != 5 {
		t.Fatalf("tools: %d", len(tools))
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	if !names["xkb_reopen_card"] {
		t.Fatalf("tools missing xkb_reopen_card: %v", names)
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

func TestBoardReportsTodoWaitHealth(t *testing.T) {
	s := newServer(t)
	old := time.Now().UTC().Add(-20 * time.Minute)
	_, err := s.Store.CreateCard(context.Background(), &store.Card{
		ProjectPath: t.TempDir(), Title: "waiting", Status: store.StatusTodo,
		Touches: []string{"internal/"}, WaitReason: "等待槽位 (5/5)", WaitSince: &old,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, rpcErr := call(t, s, "tools/call", map[string]any{"name": "xkb_board_status", "arguments": map[string]any{}})
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	var out struct {
		Waiting []map[string]any `json:"waiting"`
		Stalled int              `json:"stalled"`
	}
	json.Unmarshal([]byte(toolText(t, res)), &out)
	if len(out.Waiting) != 1 || out.Stalled != 1 {
		t.Fatalf("waiting=%v stalled=%d", out.Waiting, out.Stalled)
	}
	if out.Waiting[0]["reason"] != "等待槽位 (5/5)" {
		t.Fatalf("reason: %v", out.Waiting[0])
	}
	if out.Waiting[0]["stalled"] != true {
		t.Fatalf("stalled flag: %v", out.Waiting[0])
	}
}

func TestReopenCard(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	card, err := s.Store.CreateCard(ctx, &store.Card{
		ProjectPath: t.TempDir(), Title: "done card", Touches: []string{"internal/"},
		Status: store.StatusDone, RetryCount: 2, LastReviewedSHA: "sha-1", XIssueRunID: 7,
	})
	if err != nil {
		t.Fatal(err)
	}

	res, rpcErr := call(t, s, "tools/call", map[string]any{
		"name":      "xkb_reopen_card",
		"arguments": map[string]any{"id": card.ID, "feedback": "fix the drift"},
	})
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("reopen failed: %s", toolText(t, res))
	}
	var out struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Feedback string `json:"feedback"`
	}
	if err := json.Unmarshal([]byte(toolText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != card.ID || out.Status != store.StatusTodo || out.Feedback != "fix the drift" {
		t.Fatalf("reopen output: %+v", out)
	}

	got, err := s.Store.GetCard(ctx, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusTodo || got.RetryCount != 0 || got.LastReviewedSHA != "" ||
		got.XIssueRunID != 0 || got.FinishedAt != nil || got.LastError != "fix the drift" {
		t.Fatalf("reopened card: %+v", got)
	}

	// Only done cards can be reopened.
	res, _ = call(t, s, "tools/call", map[string]any{
		"name": "xkb_reopen_card", "arguments": map[string]any{"id": card.ID},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("reopen of todo card should fail: %v", res)
	}

	res, _ = call(t, s, "tools/call", map[string]any{
		"name": "xkb_reopen_card", "arguments": map[string]any{"id": "xkb-999"},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("reopen of unknown card should fail: %v", res)
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
