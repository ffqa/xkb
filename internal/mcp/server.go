// Package mcp serves xkb tools over stdio JSON-RPC 2.0.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/xpier/xkb/internal/gate"
	"github.com/xpier/xkb/internal/store"
)

// Server exposes xkb tools over a line-delimited JSON-RPC stream.
type Server struct {
	Store *store.Store
}

// New returns a server backed by st.
func New(st *store.Store) *Server { return &Server{Store: st} }

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads requests until EOF.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: err.Error()}})
			continue
		}
		resp, hasResp := s.handle(ctx, req)
		if hasResp {
			if err := enc.Encode(resp); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

func (s *Server) handle(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	notify := len(req.ID) == 0
	result, err := s.dispatch(ctx, req.Method, req.Params)
	if notify {
		return resp, false
	}
	if err != nil {
		resp.Error = &rpcError{Code: -32603, Message: err.Error()}
		return resp, true
	}
	resp.Result = result
	return resp, true
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "xkb", "version": "0.1.0"},
		}, nil
	case "notifications/initialized":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefs()}, nil
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, err
		}
		text, err := s.call(ctx, call.Name, call.Arguments)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(text, false), nil
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func toolDefs() []map[string]any {
	return []map[string]any{
		{
			"name":        "xkb_create_spec",
			"description": "保存需求 Spec 文档并返回 spec_id",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"project_path", "title", "body"},
				"properties": map[string]any{
					"project_path": map[string]any{"type": "string"},
					"title":        map[string]any{"type": "string"},
					"body":         map[string]any{"type": "string"},
					"xissue_proj":  map[string]any{"type": "integer"},
				},
			},
		},
		{
			"name":        "xkb_create_cards",
			"description": "批量创建拆解后的原子任务卡；auto_ready 时通过硬门禁直接进入 todo",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"project_path", "cards"},
				"properties": map[string]any{
					"spec_id":      map[string]any{"type": "string"},
					"project_path": map[string]any{"type": "string"},
					"cards": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type":     "object",
							"required": []string{"title"},
							"properties": map[string]any{
								"title":              map[string]any{"type": "string"},
								"priority":           map[string]any{"type": "string"},
								"size":               map[string]any{"type": "string"},
								"security_sensitive": map[string]any{"type": "boolean"},
								"depends_on":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
								"touches":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
								"verify_cmd":         map[string]any{"type": "string"},
								"goal":               map[string]any{"type": "string"},
								"out_of_scope":       map[string]any{"type": "string"},
								"acceptance":         map[string]any{"type": "string"},
								"auto_ready":         map[string]any{"type": "boolean"},
							},
						},
					},
				},
			},
		},
		{
			"name":        "xkb_promote_todo",
			"description": "校验 backlog 卡片并提升到 todo",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"ids"},
				"properties": map[string]any{
					"ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
			},
		},
		{
			"name":        "xkb_board_status",
			"description": "查询看板各列任务、阻塞原因及 Spec 完成度",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

func (s *Server) call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "xkb_create_spec":
		var in struct {
			ProjectPath string `json:"project_path"`
			Title       string `json:"title"`
			Body        string `json:"body"`
			XIssueProj  int64  `json:"xissue_proj"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return "", err
		}
		sp, err := s.Store.CreateSpec(ctx, &store.Spec{
			ProjectPath: in.ProjectPath, Title: in.Title, Body: in.Body, XIssueProj: in.XIssueProj,
		})
		if err != nil {
			return "", err
		}
		return marshal(map[string]any{"spec_id": sp.ID}), nil
	case "xkb_create_cards":
		return s.createCards(ctx, args)
	case "xkb_promote_todo":
		return s.promote(ctx, args)
	case "xkb_board_status":
		return s.board(ctx)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

type cardInput struct {
	Title             string   `json:"title"`
	Priority          string   `json:"priority"`
	Size              string   `json:"size"`
	SecuritySensitive bool     `json:"security_sensitive"`
	DependsOn         []string `json:"depends_on"`
	Touches           []string `json:"touches"`
	VerifyCmd         string   `json:"verify_cmd"`
	Goal              string   `json:"goal"`
	OutOfScope        string   `json:"out_of_scope"`
	Acceptance        string   `json:"acceptance"`
	AutoReady         bool     `json:"auto_ready"`
}

func (s *Server) createCards(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		SpecID      string      `json:"spec_id"`
		ProjectPath string      `json:"project_path"`
		Cards       []cardInput `json:"cards"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if in.ProjectPath == "" && in.SpecID != "" {
		sp, err := s.Store.GetSpec(ctx, in.SpecID)
		if err != nil {
			return "", err
		}
		in.ProjectPath = sp.ProjectPath
	}
	if in.ProjectPath == "" {
		return "", fmt.Errorf("project_path is required")
	}
	if len(in.Cards) == 0 {
		return "", fmt.Errorf("cards must not be empty")
	}
	created := []string{}
	problems := []string{}
	for _, ci := range in.Cards {
		if len(ci.Touches) == 0 {
			problems = append(problems, fmt.Sprintf("%s: touches must not be empty", ci.Title))
			continue
		}
		var missing []string
		for _, dep := range ci.DependsOn {
			if _, err := s.Store.GetCard(ctx, dep); err != nil {
				missing = append(missing, dep)
			}
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("%s: unknown depends_on %s", ci.Title, strings.Join(missing, ", ")))
			continue
		}
		card := &store.Card{
			SpecID: in.SpecID, ProjectPath: in.ProjectPath, Title: ci.Title,
			Priority: ci.Priority, Size: ci.Size, SecuritySensitive: ci.SecuritySensitive,
			DependsOn: ci.DependsOn, Touches: ci.Touches, VerifyCmd: ci.VerifyCmd,
			Goal: ci.Goal, OutOfScope: ci.OutOfScope, Acceptance: ci.Acceptance,
		}
		saved, err := s.Store.CreateCard(ctx, card)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", ci.Title, err))
			continue
		}
		if ci.AutoReady {
			if err := gate.ValidateTodoGate(saved); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", saved.ID, err))
			} else {
				saved.Status = store.StatusTodo
				if err := s.Store.UpdateCard(ctx, saved); err != nil {
					problems = append(problems, fmt.Sprintf("%s: %v", saved.ID, err))
				}
			}
		}
		created = append(created, saved.ID)
	}
	return marshal(map[string]any{"ids": created, "problems": problems}), nil
}

func (s *Server) promote(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	promoted := []string{}
	problems := []string{}
	for _, id := range in.IDs {
		card, err := s.Store.GetCard(ctx, id)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		if card.Status != store.StatusBacklog {
			problems = append(problems, fmt.Sprintf("%s: status is %s, want backlog", id, card.Status))
			continue
		}
		if err := gate.ValidateTodoGate(card); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		card.Status = store.StatusTodo
		if err := s.Store.UpdateCard(ctx, card); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		promoted = append(promoted, id)
	}
	return marshal(map[string]any{"promoted": promoted, "problems": problems}), nil
}

func (s *Server) board(ctx context.Context) (string, error) {
	cards, err := s.Store.ListCards(ctx, store.CardFilter{})
	if err != nil {
		return "", err
	}
	specs, err := s.Store.ListSpecs(ctx)
	if err != nil {
		return "", err
	}
	columns := map[string][]map[string]any{}
	counts := map[string]int{}
	for _, c := range cards {
		counts[c.Status]++
		entry := map[string]any{
			"id": c.ID, "title": c.Title, "priority": c.Priority,
			"retry": c.RetryCount, "agent": c.ExecAgent,
		}
		if c.LastError != "" {
			entry["lastError"] = c.LastError
		}
		columns[c.Status] = append(columns[c.Status], entry)
	}
	completion := []map[string]any{}
	for _, sp := range specs {
		total, done := 0, 0
		for _, c := range cards {
			if c.SpecID != sp.ID {
				continue
			}
			total++
			if c.Status == store.StatusDone || c.Status == store.StatusArchived {
				done++
			}
		}
		completion = append(completion, map[string]any{"specId": sp.ID, "title": sp.Title, "total": total, "done": done})
	}
	return marshal(map[string]any{"columns": columns, "counts": counts, "specs": completion}), nil
}

func marshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
