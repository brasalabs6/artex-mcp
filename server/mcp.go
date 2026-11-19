package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// --- JSON-RPC / MCP wire types (matching the ARTEX mcphttp client and the
// rmcp streamable-http client used by grok build) ---

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpServer struct {
	cfg    config
	client *artexClient
}

func newMCP(cfg config, client *artexClient) *mcpServer {
	return &mcpServer{cfg: cfg, client: client}
}

// handle serves the MCP Streamable HTTP endpoint: every JSON-RPC request is
// POSTed; responses are plain JSON. Notifications get 202 with no body. The
// endpoint is stateless (no session id assigned).
func (s *mcpServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "MCP endpoint accepts POST only", http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "parse error: "+err.Error())
		return
	}
	if req.JSONRPC != "2.0" {
		writeRPCError(w, req.ID, -32600, "jsonrpc must be \"2.0\"")
		return
	}
	if req.ID == nil {
		// Notification (e.g. notifications/initialized): acknowledge, no body.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		writeRPCResult(w, req.ID, map[string]any{
			"protocolVersion": clientVersion(req.Params),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "artex-mcp", "version": serverVersion},
		})
	case "ping":
		writeRPCResult(w, req.ID, map[string]any{})
	case "tools/list":
		writeRPCResult(w, req.ID, map[string]any{"tools": toolDefs()})
	case "tools/call":
		s.handleToolCall(w, req)
	default:
		writeRPCError(w, req.ID, -32601, "unknown method: "+req.Method)
	}
}

// clientVersion echoes the protocol version the client asked for (bounded to
// sane values) so version negotiation never breaks on either side.
func clientVersion(params any) string {
	const def = "2025-06-18"
	if p, ok := params.(map[string]any); ok {
		if v, ok := p["protocolVersion"].(string); ok && len(v) <= 32 {
			return v
		}
	}
	return def
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *mcpServer) handleToolCall(w http.ResponseWriter, req rpcRequest) {
	var p toolCallParams
	if raw, err := json.Marshal(req.Params); err == nil {
		_ = json.Unmarshal(raw, &p)
	}
	text, isErr := s.runTool(p.Name, p.Arguments)
	writeRPCResult(w, req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	})
}

// runTool dispatches a tool call and returns the text payload (plus isError).
func (s *mcpServer) runTool(name string, args map[string]any) (string, bool) {
	switch name {
	case "artex_task_create":
		return s.toolTaskCreate(args)
	case "artex_templates_list":
		return s.toolTemplatesList(args)
	case "artex_tasks_list":
		return s.toolTasksList(args)
	case "artex_task_control":
		return s.toolTaskControl(args)
	case "artex_task_graph":
		return s.toolTaskGraph(args)
	case "artex_task_findings":
		return s.toolTaskFindings(args)
	case "artex_task_node_detail":
		return s.toolNodeDetail(args)
	case "artex_task_activity":
		return s.toolTaskActivity(args)
	case "artex_chat_send":
		return s.toolChatSend(args)
	case "artex_chat_read":
		return s.toolChatRead(args)
	case "artex_conversations_list":
		return s.toolConversationsList(args)
	case "artex_conversation_create":
		return s.toolConversationCreate(args)
	case "artex_conversation_send":
		return s.toolConversationSend(args)
	case "artex_conversation_read":
		return s.toolConversationRead(args)
	case "artex_traffic_list":
		return s.toolTrafficList(args)
	case "artex_traffic_exchange":
		return s.toolTrafficExchange(args)
	case "artex_assets_list":
		return s.toolAssetsList(args)
	default:
		return "unknown tool: " + name, true
	}
}

// --- argument helpers ---

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return trimSpace(v)
	}
	return ""
}

func argInt(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return def
}

func argBool(args map[string]any, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}

func argStringSlice(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		switch v := item.(type) {
		case string:
			if t := trimSpace(v); t != "" {
				out = append(out, t)
			}
		case float64:
			out = append(out, fmt.Sprintf("%d", int(v)))
		}
	}
	return out
}

func argInt64Slice(args map[string]any, key string) []int64 {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(raw))
	for _, item := range raw {
		if v, ok := item.(float64); ok {
			out = append(out, int64(v))
		}
	}
	return out
}

// argID normalizes an id argument (number or numeric string).
func argID(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case string:
		return trimSpace(v)
	case float64:
		return fmt.Sprintf("%d", int(v))
	}
	return ""
}

// taskID normalizes the task id argument (number or numeric string).
func taskID(args map[string]any) string { return argID(args, "task_id") }

// toolText marshals v as the tool payload, capped at cfg.maxResponse bytes.
// Pretty-print when it fits; fall back to compact JSON; only truncate (with a
// marker) when even compact JSON exceeds the cap.
func (s *mcpServer) toolText(v any) (string, bool) {
	compact, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v), true
	}
	if len(compact) > s.cfg.maxResponse {
		return string(compact[:s.cfg.maxResponse]) + "\n…[truncated]", false
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil || len(pretty) > s.cfg.maxResponse {
		return string(compact), false
	}
	return string(pretty), false
}

func (s *mcpServer) errText(err error) (string, bool) {
	return err.Error(), true
}

func writeRPCResult(w http.ResponseWriter, id any, result any) {
	writeJSONRPC(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeRPCError(w http.ResponseWriter, id any, code int, msg string) {
	writeJSONRPC(w, map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": rpcError{Code: code, Message: msg},
	})
}

func writeJSONRPC(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
