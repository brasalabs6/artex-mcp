package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// --- fake ARTEX API (real DTO shapes: string node ids, payload as JSON
// string, edges src/dst, newest-first activity/history) ---

type fakeArtex struct {
	t        *testing.T
	logins   int
	stale401 bool
	lastTask map[string]any
}

func newFakeArtex(t *testing.T) (*fakeArtex, *httptest.Server) {
	fa := &fakeArtex{t: t}
	mux := http.NewServeMux()
	guard := func(w http.ResponseWriter, r *http.Request) bool {
		h := r.Header.Get("Authorization")
		valid := strings.HasPrefix(h, "Bearer tok-")
		if valid && fa.stale401 {
			fa.stale401 = false
			w.WriteHeader(401)
			return false
		}
		if !valid {
			w.WriteHeader(401)
			return false
		}
		return true
	}
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Username, Password string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Username != "admin" || req.Password != "secret" {
			w.WriteHeader(401)
			return
		}
		fa.logins++
		fmt.Fprintf(w, `{"token":"tok-%d"}`, fa.logins)
	})
	mux.HandleFunc("GET /api/task-templates", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		fmt.Fprint(w, `{"templates":[{"id":1,"name":"Full Engagement Template","description":"End-to-end offensive chain","goal":"Get root","category_id":1,"intercept_rules":[{"enabled":true,"action":"allow","kind":"exact_domain","pattern":"example.com","note":""}]}]}`)
	})
	mux.HandleFunc("POST /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		fa.lastTask = req
		w.WriteHeader(201)
		// real API shape: id is a string, exploration_id is a number
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "9", "status": "created", "exploration_id": 9, "description": req["description"], "goal": req["goal"]})
	})
	mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		fmt.Fprint(w, `{"active":"4","tasks":[{"id":"4","name":"","description":"target pentest","goal":"root it","status":"created","paused":true,"queued":false,"exploration_id":4}]}`)
	})
	mux.HandleFunc("POST /api/tasks/control/batch", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		fmt.Fprint(w, `{"items":[{"id":4,"ok":true}]}`)
	})
	mux.HandleFunc("GET /api/exploration/graph", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		if r.URL.Query().Get("task") != "9" {
			w.WriteHeader(404)
			return
		}
		// real shapes: id string, kind key is "type", payload is a JSON string
		fmt.Fprint(w, `{"nodes":[{"id":"11","type":"finding","state":"confirmed","ts":"2026-11-19T01:00:00Z","payload":"{\"summary\":\"SQLi in /login\",\"severity\":\"high\"}"},{"id":"12","type":"intent","state":"done","payload":"{\"summary\":\"test login\"}"}],"edges":[{"src":"12","dst":"11","rel":"produced"}]}`)
	})
	mux.HandleFunc("GET /api/exploration/findings", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		fmt.Fprint(w, `[{"id":"11","finding_id":"171","name":"SQLi in /login","severity":"high","vulnclass":"sqli","status":"pending","ts":"2026-11-19T01:00:00Z","summary":"long summary text","evidence":"huge evidence blob","assets":[{"id":"2857","label":"POST https://x/login","type":"endpoint"}]}]`)
	})
	mux.HandleFunc("GET /api/exploration/activity", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		long := strings.Repeat("x", 300) + "ENDMARKER"
		fmt.Fprintf(w, `{"items":[{"seq":1,"kind":"text","worker":"planner","summary":%q,"ts":"2026-11-19T01:00:01Z"},{"seq":2,"kind":"text","worker":"work#1","summary":"running nmap","ts":"2026-11-19T01:00:02Z"}],"cursor":2}`, long)
	})
	mux.HandleFunc("GET /api/exploration/activity/history", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		if r.URL.Query().Get("task") != "9" {
			w.WriteHeader(404)
			return
		}
		sess := r.URL.Query().Get("session")
		if sess != "main" && sess != "plan" && !strings.HasPrefix(sess, "intent:") {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"bad session"}`)
			return
		}
		// newest-first
		fmt.Fprint(w, `{"items":[{"seq":5,"kind":"text","worker":"mainagent","summary":"found SQLi","ts":"2026-11-19T01:00:05Z"},{"seq":4,"kind":"text","worker":"mainagent","summary":"analyzing","ts":"2026-11-19T01:00:04Z"}],"snapshot_cursor":5,"earliest_cursor":4,"has_more":true}`)
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		if r.URL.Query().Get("task") != "9" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(202)
		fmt.Fprint(w, `{"status":"accepted","mode":"llm"}`)
	})
	mux.HandleFunc("GET /api/traffic", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		// native paging: page/size + method filter; 3 POSTs + 2 GETs total
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		size, _ := strconv.Atoi(q.Get("size"))
		if size <= 0 {
			size = 100
		}
		all := []map[string]any{
			{"id": "e1", "host": "example.com", "method": "GET", "url": "https://example.com/", "status": 200},
			{"id": "e2", "host": "example.com", "method": "POST", "url": "https://example.com/x", "status": 403},
			{"id": "e3", "host": "example.com", "method": "POST", "url": "https://example.com/y", "status": 200},
		}
		if m := q.Get("method"); m != "" {
			var f []map[string]any
			for _, e := range all {
				if e["method"] == m {
					f = append(f, e)
				}
			}
			all = f
		}
		start := page * size
		if start > len(all) {
			start = len(all)
		}
		end := start + size
		if end > len(all) {
			end = len(all)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count": 42, "total": len(all), "enabled": true, "exchanges": all[start:end],
		})
	})
	mux.HandleFunc("GET /api/traffic/exchange", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		fmt.Fprint(w, `{"id":"e1","request":{"method":"GET"},"response":{"status":200}}`)
	})
	mux.HandleFunc("GET /api/assets", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		// native paging: limit/offset + total
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		if limit <= 0 {
			limit = 50
		}
		all := []map[string]any{}
		for i := 1; i <= 5; i++ {
			all = append(all, map[string]any{"id": i, "type": "root_domain", "domain": fmt.Sprintf("d%d.com", i), "task_ids": []int{9}})
		}
		if offset > len(all) {
			offset = len(all)
		}
		end := offset + limit
		if end > len(all) {
			end = len(all)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count": end - offset, "total": len(all), "assets": all[offset:end],
		})
	})
	mux.HandleFunc("GET /api/conversations", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		fmt.Fprint(w, `{"conversations":[{"id":106,"agent_key":"mainagent","title":"cross-task review","pinned":false,"running":false,"updated_at":"2026-11-19T02:00:00Z"}]}`)
	})
	mux.HandleFunc("POST /api/conversations", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		fmt.Fprint(w, `{"id":107,"agent_key":"mainagent","title":"ops","pinned":false,"created_at":"2026-11-19T02:00:00Z","updated_at":"2026-11-19T02:00:00Z"}`)
	})
	mux.HandleFunc("POST /api/conversations/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		w.WriteHeader(202)
		fmt.Fprint(w, `{"status":"accepted"}`)
	})
	mux.HandleFunc("GET /api/conversations/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		// latest page: newest-first
		fmt.Fprint(w, `{"items":[{"seq":2,"kind":"text","worker":"mainagent","summary":"second reply","ts":"2026-11-19T02:00:02Z"},{"seq":1,"kind":"user","worker":"mainagent","summary":"first reply","ts":"2026-11-19T02:00:01Z"}],"cursor":2,"running":false,"hasMore":false}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return fa, srv
}

// --- helpers ---

func newTestMCP(t *testing.T, apiKey, baseURL string) *httptest.Server {
	cfg := config{apiKey: apiKey, baseURL: baseURL, maxResponse: 65536}
	client := newArtexClient(cfg)
	client.user, client.pass = "admin", "secret"
	mcp := newMCP(cfg, client)
	mux := http.NewServeMux()
	mux.HandleFunc("/", mcp.handle)
	srv := httptest.NewServer(withAuth(cfg, mux))
	t.Cleanup(srv.Close)
	return srv
}

func rpcCall(t *testing.T, srv *httptest.Server, key string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func callTool(t *testing.T, srv *httptest.Server, name string, args map[string]any) (string, bool) {
	_, out := rpcCall(t, srv, "k", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	res, _ := out["result"].(map[string]any)
	if res == nil {
		t.Fatalf("no result: %v", out)
	}
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text, res["isError"] == true
}

// --- tests ---

func TestAuthRejectsMissingAndWrongKey(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	for _, key := range []string{"", "wrong"} {
		code, out := rpcCall(t, srv, key, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
		if code != 401 {
			t.Fatalf("key %q: want 401, got %d", key, code)
		}
		if out["error"] == nil {
			t.Fatal("expected JSON-RPC error body")
		}
	}
}

func TestInitializeAndToolsList(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	_, out := rpcCall(t, srv, "k", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}})
	res := out["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocol echo failed: %v", res)
	}
	if res["serverInfo"].(map[string]any)["name"] != "artex-mcp" {
		t.Fatalf("serverInfo wrong: %v", res)
	}
	_, out = rpcCall(t, srv, "k", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 17 {
		t.Fatalf("want 17 tools, got %d", len(tools))
	}
}

func TestNotificationGets202(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	req, _ := http.NewRequest("POST", srv.URL, bytes.NewReader(b))
	req.Header.Set("X-API-Key", "k")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
}

func TestUnknownMethodAndTool(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	_, out := rpcCall(t, srv, "k", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "nope"})
	if out["error"] == nil {
		t.Fatal("want error for unknown method")
	}
	text, isErr := callTool(t, srv, "artex_nope", nil)
	if !isErr || !strings.Contains(text, "unknown tool") {
		t.Fatalf("unknown tool handling wrong: %q", text)
	}
}

func TestTaskCreateFromTemplate(t *testing.T) {
	fa, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	text, isErr := callTool(t, srv, "artex_task_create", map[string]any{"template_id": "1"})
	if isErr {
		t.Fatalf("create failed: %s", text)
	}
	if !strings.Contains(text, `"created": true`) || !strings.Contains(text, `"id": "9"`) {
		t.Fatalf("create result wrong: %s", text)
	}
	if !strings.Contains(text, "Get root") {
		t.Fatalf("template goal not applied: %s", text)
	}
	if fa.logins != 1 {
		t.Fatalf("want 1 login, got %d", fa.logins)
	}
}

func TestTaskCreateNewParamsPassthrough(t *testing.T) {
	fa, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	_, isErr := callTool(t, srv, "artex_task_create", map[string]any{
		"description": "retest", "goal": "verify",
		"source_task_ids": []string{"5"}, "plan_heartbeat_seconds": 900,
		"coverage_enabled": false,
	})
	if isErr {
		t.Fatal("create with new params failed")
	}
	if got := fa.lastTask["source_task_ids"]; fmt.Sprint(got) != "[5]" {
		t.Fatalf("source_task_ids not passed: %v", got)
	}
	if v, _ := fa.lastTask["plan_heartbeat_seconds"].(float64); v != 900 {
		t.Fatalf("plan_heartbeat_seconds not passed: %v", fa.lastTask["plan_heartbeat_seconds"])
	}
	if v, _ := fa.lastTask["coverage_enabled"].(bool); v != false {
		t.Fatalf("coverage_enabled not passed: %v", fa.lastTask["coverage_enabled"])
	}
}

func TestTaskCreateAcceptsNumericTemplateID(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	text, isErr := callTool(t, srv, "artex_task_create", map[string]any{"template_id": float64(1)})
	if isErr {
		t.Fatalf("numeric template_id failed: %s", text)
	}
}

func TestTaskCreateRequiresTemplateOrText(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	_, isErr := callTool(t, srv, "artex_task_create", map[string]any{})
	if !isErr {
		t.Fatal("empty create must fail")
	}
}

func TestTasksListAndControl(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	text, isErr := callTool(t, srv, "artex_tasks_list", nil)
	if isErr || !strings.Contains(text, `"active_task": "4"`) {
		t.Fatalf("tasks_list wrong: %s", text)
	}
	text, isErr = callTool(t, srv, "artex_task_control", map[string]any{"task_ids": []string{"4"}, "action": "pause"})
	if isErr || !strings.Contains(text, `"ok": true`) {
		t.Fatalf("control wrong: %s", text)
	}
	_, isErr = callTool(t, srv, "artex_task_control", map[string]any{"task_ids": []string{"4"}, "action": "explode"})
	if !isErr {
		t.Fatal("invalid action must fail")
	}
}

func TestGraphNodeDetailFindings(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	// graph: real DTO (type + payload-as-string) parsed correctly
	text, isErr := callTool(t, srv, "artex_task_graph", map[string]any{"task_id": "9", "kind": "finding"})
	if isErr || !strings.Contains(text, "SQLi in /login") {
		t.Fatalf("graph wrong: %s", text)
	}
	if strings.Contains(text, "test login") {
		t.Fatal("kind filter not applied")
	}
	if !strings.Contains(text, `"finding": 1`) || !strings.Contains(text, `"intent": 1`) {
		t.Fatalf("kind counts wrong: %s", text)
	}
	// node detail: string id, src/dst edges
	text, isErr = callTool(t, srv, "artex_task_node_detail", map[string]any{"task_id": "9", "node_id": "11"})
	if isErr || !strings.Contains(text, `"produced"`) {
		t.Fatalf("node detail wrong: %s", text)
	}
	_, isErr = callTool(t, srv, "artex_task_node_detail", map[string]any{"task_id": "9", "node_id": "99"})
	if !isErr {
		t.Fatal("missing node must be an error")
	}
	// findings: compact by default (no evidence blob), full=true returns raw
	text, isErr = callTool(t, srv, "artex_task_findings", map[string]any{"task_id": "9"})
	if isErr || !strings.Contains(text, `"node_id": "11"`) || !strings.Contains(text, "SQLi in /login") {
		t.Fatalf("findings compact wrong: %s", text)
	}
	if strings.Contains(text, "huge evidence blob") {
		t.Fatal("compact findings must not include evidence")
	}
	text, isErr = callTool(t, srv, "artex_task_findings", map[string]any{"task_id": "9", "full": true})
	if isErr || !strings.Contains(text, "huge evidence blob") {
		t.Fatalf("findings full wrong: %s", text)
	}
}

func TestActivityModesAndChat(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	// session mode: newest-first history
	text, isErr := callTool(t, srv, "artex_task_activity", map[string]any{"task_id": "9", "session": "main"})
	if isErr || !strings.Contains(text, `"order": "newest-first"`) || !strings.Contains(text, "found SQLi") {
		t.Fatalf("activity session mode wrong: %s", text)
	}
	if !strings.Contains(text, `"has_more": true`) {
		t.Fatalf("history meta missing: %s", text)
	}
	_, isErr = callTool(t, srv, "artex_task_activity", map[string]any{"task_id": "9", "session": "bogus"})
	if !isErr {
		t.Fatal("invalid session must fail")
	}
	// stream mode: ascending, worker filter
	text, isErr = callTool(t, srv, "artex_task_activity", map[string]any{"task_id": "9", "worker": "planner"})
	if isErr || !strings.Contains(text, `"worker": "planner"`) || strings.Contains(text, "running nmap") {
		t.Fatalf("activity stream filter wrong: %s", text)
	}
	// chat: async send + read via history (reversed to oldest→newest)
	text, isErr = callTool(t, srv, "artex_chat_send", map[string]any{"task_id": "9", "message": "status?"})
	if isErr || !strings.Contains(text, `"mode": "llm"`) {
		t.Fatalf("chat_send wrong: %s", text)
	}
	text, isErr = callTool(t, srv, "artex_chat_read", map[string]any{"task_id": "9"})
	if isErr || !strings.Contains(text, "found SQLi") {
		t.Fatalf("chat_read wrong: %s", text)
	}
	if strings.Index(text, "analyzing") > strings.Index(text, "found SQLi") {
		t.Fatal("chat_read must be oldest→newest")
	}
}

func TestTrafficAndAssets(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	text, isErr := callTool(t, srv, "artex_traffic_list", map[string]any{"method": "POST"})
	if isErr || !strings.Contains(text, `"total_recorded": 42`) || !strings.Contains(text, `"matched": 2`) || strings.Contains(text, `"GET"`) {
		t.Fatalf("traffic_list filters wrong: %s", text)
	}
	text, isErr = callTool(t, srv, "artex_traffic_exchange", map[string]any{"exchange_id": "e1"})
	if isErr || !strings.Contains(text, `"status": 200`) {
		t.Fatalf("traffic_exchange wrong: %s", text)
	}
	text, isErr = callTool(t, srv, "artex_assets_list", map[string]any{"type": "root_domain"})
	if isErr || !strings.Contains(text, "d1.com") || !strings.Contains(text, `"total": 5`) {
		t.Fatalf("assets wrong: %s", text)
	}
}

func TestReloginOn401(t *testing.T) {
	fa, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	_, isErr := callTool(t, srv, "artex_tasks_list", nil)
	if isErr {
		t.Fatal("prime call failed")
	}
	fa.stale401 = true
	fa.logins = 0
	text, isErr := callTool(t, srv, "artex_tasks_list", nil)
	if isErr {
		t.Fatalf("relogin call failed: %s", text)
	}
	if fa.logins != 1 {
		t.Fatalf("relogin did not happen: %d", fa.logins)
	}
}

func TestConversationTools(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	text, isErr := callTool(t, srv, "artex_conversations_list", nil)
	if isErr || !strings.Contains(text, `"total": 1`) || !strings.Contains(text, "cross-task review") {
		t.Fatalf("conversations_list wrong: %s", text)
	}
	text, isErr = callTool(t, srv, "artex_conversation_create", map[string]any{"title": "ops"})
	if isErr || !strings.Contains(text, `"created": true`) || !strings.Contains(text, `"id": 107`) {
		t.Fatalf("conversation_create wrong: %s", text)
	}
	text, isErr = callTool(t, srv, "artex_conversation_send", map[string]any{"conversation_id": "106", "message": "hi"})
	if isErr || !strings.Contains(text, `"status": "accepted"`) {
		t.Fatalf("conversation_send wrong: %s", text)
	}
	// read: latest page is newest-first -> normalized oldest→newest
	text, isErr = callTool(t, srv, "artex_conversation_read", map[string]any{"conversation_id": "106"})
	if isErr || !strings.Contains(text, "second reply") || !strings.Contains(text, "first reply") {
		t.Fatalf("conversation_read wrong: %s", text)
	}
	if strings.Index(text, "first reply") > strings.Index(text, "second reply") {
		t.Fatal("conversation_read must be oldest→newest")
	}
}

func TestActivitySummaryTruncated(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	// long summary in the stream endpoint gets capped at 240 chars in output
	text, isErr := callTool(t, srv, "artex_task_activity", map[string]any{"task_id": "9", "worker": "planner"})
	if isErr {
		t.Fatalf("activity failed: %s", text)
	}
	if strings.Contains(text, "ENDMARKER") {
		t.Fatal("long summary must be truncated to 240 chars")
	}
}

func TestPagination(t *testing.T) {
	_, fake := newFakeArtex(t)
	srv := newTestMCP(t, "k", fake.URL)
	// assets: 5 total, page of 2 -> next_cursor 2 -> last page 3 items, no cursor
	text, isErr := callTool(t, srv, "artex_assets_list", map[string]any{"limit": 2})
	if isErr || !strings.Contains(text, `"total": 5`) || !strings.Contains(text, `"next_cursor": "2"`) {
		t.Fatalf("assets page 1 wrong: %s", text)
	}
	text, isErr = callTool(t, srv, "artex_assets_list", map[string]any{"limit": 2, "cursor": "2"})
	if isErr || !strings.Contains(text, `"next_cursor": "4"`) {
		t.Fatalf("assets middle page wrong: %s", text)
	}
	text, isErr = callTool(t, srv, "artex_assets_list", map[string]any{"limit": 2, "cursor": "4"})
	if isErr || strings.Contains(text, "next_cursor") || !strings.Contains(text, `"total": 5`) {
		t.Fatalf("assets last page wrong: %s", text)
	}
	// traffic: native page cursor (page 0 of size 2 -> next "1")
	text, isErr = callTool(t, srv, "artex_traffic_list", map[string]any{"limit": 2})
	if isErr || !strings.Contains(text, `"matched": 3`) || !strings.Contains(text, `"next_cursor": "1"`) {
		t.Fatalf("traffic page 1 wrong: %s", text)
	}
	text, isErr = callTool(t, srv, "artex_traffic_list", map[string]any{"limit": 2, "cursor": "1"})
	if isErr || strings.Contains(text, "next_cursor") {
		t.Fatalf("traffic last page wrong: %s", text)
	}
	// tasks_list: 1 task, no cursor expected
	text, isErr = callTool(t, srv, "artex_tasks_list", nil)
	if isErr || strings.Contains(text, "next_cursor") || !strings.Contains(text, `"total": 1`) {
		t.Fatalf("tasks pagination wrong: %s", text)
	}
	// findings: total + no cursor when all fit
	text, isErr = callTool(t, srv, "artex_task_findings", map[string]any{"task_id": "9"})
	if isErr || strings.Contains(text, "next_cursor") || !strings.Contains(text, `"total": 1`) {
		t.Fatalf("findings pagination wrong: %s", text)
	}
	// graph: 2 nodes, page of 1 -> cursor
	text, isErr = callTool(t, srv, "artex_task_graph", map[string]any{"task_id": "9", "limit": 1})
	if isErr || !strings.Contains(text, `"next_cursor": "1"`) || !strings.Contains(text, `"total_matching": 2`) {
		t.Fatalf("graph pagination wrong: %s", text)
	}
}
