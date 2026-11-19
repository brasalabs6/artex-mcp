package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

func trimSpace(s string) string { return strings.TrimSpace(s) }

// --- pagination: uniform opaque-cursor contract ---
// Every list tool accepts `cursor` and returns `next_cursor` (only when more
// pages exist) plus `total`. Cursor semantics are opaque to the agent: for
// traffic it encodes the 0-based PAGE (native API paging); everywhere else
// it encodes the row OFFSET. The agent just passes next_cursor back.

func cursorOffset(cursor string) int {
	if n, err := strconv.Atoi(trimSpace(cursor)); err == nil && n > 0 {
		return n
	}
	return 0
}

func nextCursor(cur, step, total int) string {
	if cur+step < total {
		return fmt.Sprintf("%d", cur+step)
	}
	return ""
}

// flexString accepts a JSON string or number and normalizes to string — the
// ARTEX API mixes both for ids (tasks.id is a string, exploration_id a number).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexString(n.String())
		return nil
	}
	return fmt.Errorf("value is neither string nor number")
}

// --- graph cache: node_detail drills re-use a recent graph fetch (20s TTL) ---

type graphCacheEntry struct {
	graph   map[string]any
	fetched time.Time
}

var (
	graphCacheMu sync.Mutex
	graphCache   = map[string]graphCacheEntry{}
)

const graphCacheTTL = 20 * time.Second

func (s *mcpServer) fetchGraph(task string) (map[string]any, error) {
	graphCacheMu.Lock()
	if e, ok := graphCache[task]; ok && time.Since(e.fetched) < graphCacheTTL {
		graphCacheMu.Unlock()
		return e.graph, nil
	}
	graphCacheMu.Unlock()
	q := url.Values{"task": {task}}
	var out map[string]any
	if err := s.client.getJSON("/api/exploration/graph", q, &out); err != nil {
		return nil, err
	}
	graphCacheMu.Lock()
	graphCache[task] = graphCacheEntry{graph: out, fetched: time.Now()}
	graphCacheMu.Unlock()
	return out, nil
}

// nodePayload parses the node's payload. The real API returns payload as a
// JSON-encoded STRING (not an object), so decode defensively either way.
func nodePayload(node map[string]any) map[string]any {
	raw, ok := node["payload"]
	if !ok {
		return nil
	}
	if m, ok := raw.(map[string]any); ok {
		return m
	}
	if s, ok := raw.(string); ok && s != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			return m
		}
	}
	return nil
}

// nodeSummary extracts a one-line summary from a node's payload.
func nodeSummary(node map[string]any) string {
	if p := nodePayload(node); p != nil {
		for _, key := range []string{"summary", "text", "description", "goal", "name", "title"} {
			if v, ok := p[key].(string); ok && trimSpace(v) != "" {
				return truncate(trimSpace(v), 200)
			}
		}
	}
	if v, ok := node["summary"].(string); ok {
		return truncate(v, 200)
	}
	return ""
}

// nodeIDString normalizes the node id (API returns it as a string).
func nodeIDString(node map[string]any) string {
	switch v := node["id"].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%d", int64(v))
	}
	return ""
}

// --- templates ---

type templateDTO struct {
	ID             int64           `json:"id"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Goal           string          `json:"goal"`
	CategoryID     *int64          `json:"category_id"`
	InterceptRules json.RawMessage `json:"intercept_rules"`
}

func (s *mcpServer) toolTemplatesList(_ map[string]any) (string, bool) {
	var out struct {
		Templates []templateDTO `json:"templates"`
	}
	if err := s.client.getJSON("/api/task-templates", nil, &out); err != nil {
		return s.errText(err)
	}
	type row struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		CategoryID  *int64 `json:"category_id"`
		GoalPreview string `json:"goal_preview"`
	}
	rows := make([]row, 0, len(out.Templates))
	for _, t := range out.Templates {
		rows = append(rows, row{t.ID, t.Name, t.Description, t.CategoryID, truncate(t.Goal, 160)})
	}
	return s.toolText(map[string]any{"templates": rows, "count": len(rows)})
}

// --- tasks ---

type taskDTO struct {
	ID            flexString `json:"id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Goal          string     `json:"goal"`
	Status        string     `json:"status"`
	Paused        bool       `json:"paused"`
	Queued        bool       `json:"queued"`
	ExplorationID flexString `json:"exploration_id"`
}

func (s *mcpServer) toolTasksList(args map[string]any) (string, bool) {
	var out struct {
		Active string    `json:"active"`
		Tasks  []taskDTO `json:"tasks"`
	}
	if err := s.client.getJSON("/api/tasks", nil, &out); err != nil {
		return s.errText(err)
	}
	type row struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		Paused      bool   `json:"paused"`
		Queued      bool   `json:"queued"`
		Name        string `json:"name"`
		Description string `json:"description"`
		GoalPreview string `json:"goal_preview"`
		Active      bool   `json:"active"`
	}
	rows := make([]row, 0, len(out.Tasks))
	for _, t := range out.Tasks {
		rows = append(rows, row{
			ID: string(t.ID), Status: t.Status, Paused: t.Paused, Queued: t.Queued,
			Name: t.Name, Description: truncate(t.Description, 120),
			GoalPreview: truncate(t.Goal, 200), Active: string(t.ID) == out.Active,
		})
	}
	offset := cursorOffset(argString(args, "cursor"))
	limit := argInt(args, "limit", 20)
	if limit > 100 {
		limit = 100
	}
	resp := map[string]any{"active_task": out.Active, "total": len(rows)}
	if offset < len(rows) {
		end := offset + limit
		if end > len(rows) {
			end = len(rows)
		}
		resp["tasks"] = rows[offset:end]
	} else {
		resp["tasks"] = []row{}
	}
	if nc := nextCursor(offset, limit, len(rows)); nc != "" {
		resp["next_cursor"] = nc
	}
	return s.toolText(resp)
}

func (s *mcpServer) toolTaskCreate(args map[string]any) (string, bool) {
	description := argString(args, "description")
	goal := argString(args, "goal")
	payload := map[string]any{
		"description":     description,
		"goal":            goal,
		"timeout_seconds": argInt(args, "timeout_seconds", 0),
	}
	if v := argString(args, "name"); v != "" {
		payload["name"] = v
	}
	if v := argInt(args, "plan_heartbeat_seconds", 0); v > 0 {
		payload["plan_heartbeat_seconds"] = v
	}
	if ids := argInt64Slice(args, "llm_profile_ids"); len(ids) > 0 {
		payload["llm_profile_ids"] = ids
	}
	if ids := argInt64Slice(args, "company_ids"); len(ids) > 0 {
		payload["company_ids"] = ids
	}
	if src := argStringSlice(args, "source_task_ids"); len(src) > 0 {
		payload["source_task_ids"] = src
	}
	if v, ok := args["seed_first_intent"].(bool); ok {
		payload["seed_first_intent"] = v
	}
	if v, ok := args["coverage_enabled"].(bool); ok {
		payload["coverage_enabled"] = v
	}

	var interceptRules json.RawMessage
	// Template resolution: template supplies description/goal/category and its
	// saved intercept rules; explicit arguments override. template_id accepts
	// a number or numeric string.
	if tpl := argID(args, "template_id"); tpl != "" {
		var list struct {
			Templates []templateDTO `json:"templates"`
		}
		if err := s.client.getJSON("/api/task-templates", nil, &list); err != nil {
			return s.errText(err)
		}
		var found *templateDTO
		for i := range list.Templates {
			if fmt.Sprintf("%d", list.Templates[i].ID) == tpl {
				found = &list.Templates[i]
				break
			}
		}
		if found == nil {
			return fmt.Sprintf("template_id %s not found", tpl), true
		}
		if description == "" {
			description = found.Description
			payload["description"] = description
		}
		if goal == "" {
			goal = found.Goal
			payload["goal"] = goal
		}
		if found.CategoryID != nil {
			payload["category_id"] = *found.CategoryID
		}
		interceptRules = found.InterceptRules
	}
	if description == "" || goal == "" {
		return "description and goal are required (directly or via template_id)", true
	}

	// scope_rules -> task intercept rules (asset block/allow; allow-lists pin
	// what the task may test). Explicit scope wins over template rules.
	if raw, ok := args["scope_rules"].([]any); ok && len(raw) > 0 {
		rules := make([]map[string]any, 0, len(raw))
		for _, item := range raw {
			r, ok := item.(map[string]any)
			if !ok {
				continue
			}
			rule := map[string]any{"enabled": true}
			if a, ok := r["action"].(string); ok && (a == "block" || a == "allow") {
				rule["action"] = a
			} else {
				return "scope_rules[].action must be block|allow", true
			}
			if k, ok := r["kind"].(string); ok {
				rule["kind"] = k
			}
			if p, ok := r["pattern"].(string); ok {
				rule["pattern"] = p
			}
			if n, ok := r["note"].(string); ok {
				rule["note"] = n
			}
			rules = append(rules, rule)
		}
		b, _ := json.Marshal(rules)
		interceptRules = b
	}
	if len(interceptRules) > 0 {
		payload["intercept_rules"] = json.RawMessage(interceptRules)
	}

	var created taskDTO
	status, raw, err := s.client.do(http.MethodPost, "/api/tasks", payload, nil)
	if err != nil {
		return s.errText(err)
	}
	if status != http.StatusCreated {
		return fmt.Sprintf("create failed: %d %s", status, truncate(string(raw), 512)), true
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return fmt.Sprintf("created (unparsed response): %s", truncate(string(raw), 512)), false
	}
	return s.toolText(map[string]any{
		"created": true, "id": created.ID, "status": created.Status,
		"exploration_id": created.ExplorationID,
		"description":    created.Description,
		"goal_preview":   truncate(created.Goal, 400),
	})
}

func (s *mcpServer) toolTaskControl(args map[string]any) (string, bool) {
	ids := argStringSlice(args, "task_ids")
	action := argString(args, "action")
	if len(ids) == 0 {
		return "task_ids is required", true
	}
	if action != "pause" && action != "resume" {
		return "action must be pause|resume", true
	}
	status, raw, err := s.client.do(http.MethodPost, "/api/tasks/control/batch",
		map[string]any{"task_ids": ids, "action": action}, nil)
	if err != nil {
		return s.errText(err)
	}
	if status != http.StatusOK {
		return fmt.Sprintf("control failed: %d %s", status, truncate(string(raw), 512)), true
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(raw, &out)
	return s.toolText(map[string]any{"action": action, "results": out.Items})
}

// --- graph / findings / nodes ---

func (s *mcpServer) toolTaskGraph(args map[string]any) (string, bool) {
	task := taskID(args)
	if task == "" {
		return "task_id is required", true
	}
	graph, err := s.fetchGraph(task)
	if err != nil {
		return s.errText(err)
	}
	nodes, _ := graph["nodes"].([]any)
	edges, _ := graph["edges"].([]any)
	kinds := map[string]int{}
	kindFilter := argString(args, "kind")
	limit := argInt(args, "limit", 30)
	if limit > 200 {
		limit = 200
	}
	type nrow struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		State   string `json:"state"`
		TS      string `json:"ts,omitempty"`
		Summary string `json:"summary"`
	}
	rows := make([]nrow, 0, len(nodes))
	for _, raw := range nodes {
		n, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := n["type"].(string)
		kinds[kind]++
		if kindFilter != "" && kind != kindFilter {
			continue
		}
		state, _ := n["state"].(string)
		ts, _ := n["ts"].(string)
		rows = append(rows, nrow{nodeIDString(n), kind, state, ts, nodeSummary(n)})
	}
	offset := cursorOffset(argString(args, "cursor"))
	resp := map[string]any{
		"task_id": task, "node_count": len(nodes), "edge_count": len(edges),
		"nodes_by_kind": kinds, "total_matching": len(rows),
	}
	if offset < len(rows) {
		end := offset + limit
		if end > len(rows) {
			end = len(rows)
		}
		resp["nodes"] = rows[offset:end]
	} else {
		resp["nodes"] = []nrow{}
	}
	if nc := nextCursor(offset, limit, len(rows)); nc != "" {
		resp["next_cursor"] = nc
	}
	return s.toolText(resp)
}

func (s *mcpServer) toolNodeDetail(args map[string]any) (string, bool) {
	task := taskID(args)
	if task == "" {
		return "task_id is required", true
	}
	want := argID(args, "node_id")
	if want == "" {
		return "node_id is required", true
	}
	graph, err := s.fetchGraph(task)
	if err != nil {
		return s.errText(err)
	}
	nodes, _ := graph["nodes"].([]any)
	edges, _ := graph["edges"].([]any)
	var found map[string]any
	for _, raw := range nodes {
		if n, ok := raw.(map[string]any); ok && nodeIDString(n) == want {
			found = n
			break
		}
	}
	if found == nil {
		return fmt.Sprintf("node %s not found in task %s", want, task), true
	}
	touching := make([]map[string]any, 0, 8)
	for _, raw := range edges {
		if e, ok := raw.(map[string]any); ok {
			src, _ := e["src"].(string)
			dst, _ := e["dst"].(string)
			if src == want || dst == want {
				touching = append(touching, e)
			}
		}
	}
	return s.toolText(map[string]any{"node": found, "edges": touching})
}

func (s *mcpServer) toolTaskFindings(args map[string]any) (string, bool) {
	task := taskID(args)
	if task == "" {
		return "task_id is required", true
	}
	q := url.Values{"task": {task}}
	if v := argInt(args, "page", 1); v > 1 {
		q.Set("page", fmt.Sprintf("%d", v))
	}
	if v := argInt(args, "limit", 20); v > 0 {
		q.Set("limit", fmt.Sprintf("%d", v))
	}
	status, raw, err := s.client.do(http.MethodGet, "/api/exploration/findings", nil, q)
	if err != nil {
		return s.errText(err)
	}
	if status != http.StatusOK {
		return fmt.Sprintf("findings failed: %d %s", status, truncate(string(raw), 512)), true
	}
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		// paged shape {items,total}: fall back to passthrough
		var out any
		_ = json.Unmarshal(raw, &out)
		return s.toolText(map[string]any{"task_id": task, "findings": out})
	}
	offset := cursorOffset(argString(args, "cursor"))
	page := list
	if offset < len(list) {
		end := offset + argInt(args, "limit", 20)
		if end > len(list) {
			end = len(list)
		}
		page = list[offset:end]
	} else {
		page = []map[string]any{}
	}
	if argBool(args, "full") {
		resp := map[string]any{"task_id": task, "findings": page, "total": len(list)}
		if nc := nextCursor(offset, argInt(args, "limit", 20), len(list)); nc != "" {
			resp["next_cursor"] = nc
		}
		return s.toolText(resp)
	}
	// Compact by default: triage fields only — drill into a node with
	// artex_task_node_detail (node_id = id) for evidence and full summary.
	type crow struct {
		NodeID    string   `json:"node_id"`
		FindingID string   `json:"finding_id,omitempty"`
		Name      string   `json:"name"`
		Severity  string   `json:"severity"`
		VulnClass string   `json:"vulnclass"`
		Status    string   `json:"status"`
		TS        string   `json:"ts,omitempty"`
		Assets    []string `json:"assets,omitempty"`
	}
	rows := make([]crow, 0, len(page))
	for _, f := range page {
		get := func(k string) string {
			if v, ok := f[k].(string); ok {
				return v
			}
			return ""
		}
		var assets []string
		if arr, ok := f["assets"].([]any); ok {
			for _, a := range arr {
				if m, ok := a.(map[string]any); ok {
					if l, ok := m["label"].(string); ok {
						assets = append(assets, truncate(l, 120))
					}
				}
			}
		}
		rows = append(rows, crow{
			NodeID: get("id"), FindingID: get("finding_id"), Name: truncate(get("name"), 160),
			Severity: get("severity"), VulnClass: get("vulnclass"), Status: get("status"),
			TS: get("ts"), Assets: assets,
		})
	}
	resp := map[string]any{"task_id": task, "total": len(list), "count": len(rows), "findings": rows,
		"hint": "drill into node_id with artex_task_node_detail for evidence/PoC"}
	if nc := nextCursor(offset, argInt(args, "limit", 20), len(list)); nc != "" {
		resp["next_cursor"] = nc
	}
	return s.toolText(resp)
}

// --- activity / chat ---

type activityItem struct {
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"`
	Worker  string `json:"worker"`
	Tool    string `json:"tool"`
	IsError bool   `json:"is_error"`
	TS      string `json:"ts"`
	Intent  string `json:"intent_id"`

	RawSummary string `json:"-"`
}

func (a activityItem) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Seq     int64  `json:"seq"`
		Kind    string `json:"kind"`
		Worker  string `json:"worker"`
		Tool    string `json:"tool"`
		IsError bool   `json:"is_error"`
		Summary string `json:"summary"`
		TS      string `json:"ts"`
		Intent  string `json:"intent_id"`
	}{a.Seq, a.Kind, a.Worker, a.Tool, a.IsError, truncate(a.RawSummary, 240), a.TS, a.Intent})
}

func (a *activityItem) UnmarshalJSON(b []byte) error {
	var raw struct {
		Seq     int64  `json:"seq"`
		Kind    string `json:"kind"`
		Worker  string `json:"worker"`
		Tool    string `json:"tool"`
		IsError bool   `json:"is_error"`
		Summary string `json:"summary"`
		TS      string `json:"ts"`
		Intent  string `json:"intent_id"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*a = activityItem{Seq: raw.Seq, Kind: raw.Kind, Worker: raw.Worker, Tool: raw.Tool,
		IsError: raw.IsError, TS: raw.TS, Intent: raw.Intent, RawSummary: raw.Summary}
	return nil
}

// fetchActivityHistory uses the newest-first /activity/history endpoint with a
// session filter (""/"main" = mainagent current segment, "plan" = planner,
// "intent:<id>" = one intent's session). Returns items newest-first.
func (s *mcpServer) fetchActivityHistory(task, session string, before, limit int) ([]activityItem, map[string]any, error) {
	q := url.Values{"task": {task}, "session": {session}}
	if before > 0 {
		q.Set("before", fmt.Sprintf("%d", before))
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	var out struct {
		Items          []activityItem `json:"items"`
		SnapshotCursor int64          `json:"snapshot_cursor"`
		EarliestCursor int64          `json:"earliest_cursor"`
		HasMore        bool           `json:"has_more"`
	}
	if err := s.client.getJSON("/api/exploration/activity/history", q, &out); err != nil {
		return nil, nil, err
	}
	meta := map[string]any{"snapshot_cursor": out.SnapshotCursor, "earliest_cursor": out.EarliestCursor, "has_more": out.HasMore}
	return out.Items, meta, nil
}

func (s *mcpServer) toolTaskActivity(args map[string]any) (string, bool) {
	task := taskID(args)
	if task == "" {
		return "task_id is required", true
	}
	// Drill-down: full detail of one activity seq.
	if seq := argInt(args, "seq", 0); seq > 0 {
		var detail map[string]any
		q := url.Values{"task": {task}}
		if err := s.client.getJSON(fmt.Sprintf("/api/exploration/activity/%d", seq), q, &detail); err != nil {
			return s.errText(err)
		}
		return s.toolText(map[string]any{"task_id": task, "detail": detail})
	}
	// Session mode: newest-first page from /activity/history.
	if sess := argString(args, "session"); sess != "" {
		if sess != "main" && sess != "plan" && !strings.HasPrefix(sess, "intent:") {
			return "session must be main, plan or intent:<id>", true
		}
		limit := argInt(args, "limit", 30)
		if limit > 200 {
			limit = 200
		}
		items, meta, err := s.fetchActivityHistory(task, sess, argInt(args, "before", 0), limit)
		if err != nil {
			return s.errText(err)
		}
		return s.toolText(map[string]any{"task_id": task, "session": sess,
			"order": "newest-first", "activity": items, "meta": meta})
	}
	// Stream mode: all workers, ascending from `since` (poll with the cursor).
	workerFilter := argString(args, "worker")
	q := url.Values{"task": {task}}
	if v := argInt(args, "since", 0); v > 0 {
		q.Set("since", fmt.Sprintf("%d", v))
	}
	if v := argInt(args, "limit", 100); v > 0 {
		q.Set("limit", fmt.Sprintf("%d", v))
	}
	var out struct {
		Items  []activityItem `json:"items"`
		Cursor int64          `json:"cursor"`
	}
	if err := s.client.getJSON("/api/exploration/activity", q, &out); err != nil {
		return s.errText(err)
	}
	rows := make([]activityItem, 0, len(out.Items))
	for _, it := range out.Items {
		if workerFilter != "" && !strings.Contains(it.Worker, workerFilter) {
			continue
		}
		rows = append(rows, it)
	}
	return s.toolText(map[string]any{"task_id": task,
		"order": "oldest-first (stream from since)", "cursor": out.Cursor, "activity": rows})
}

func (s *mcpServer) toolChatSend(args map[string]any) (string, bool) {
	task := taskID(args)
	if task == "" {
		return "task_id is required", true
	}
	msg := argString(args, "message")
	if msg == "" {
		return "message is required", true
	}
	q := url.Values{"task": {task}}
	status, raw, err := s.client.do(http.MethodPost, "/api/chat", map[string]any{"message": msg}, q)
	if err != nil {
		return s.errText(err)
	}
	if status != http.StatusAccepted && status != http.StatusOK {
		return fmt.Sprintf("chat failed: %d %s", status, truncate(string(raw), 512)), true
	}
	var out struct {
		Status string `json:"status"`
		Mode   string `json:"mode"`
		Reply  string `json:"reply"`
	}
	_ = json.Unmarshal(raw, &out)
	resp := map[string]any{"task_id": task, "mode": out.Mode, "http_status": status}
	if out.Reply != "" {
		resp["reply"] = out.Reply
	} else {
		resp["status"] = out.Status
		resp["note"] = "async: read replies with artex_chat_read"
	}
	return s.toolText(resp)
}

func (s *mcpServer) toolChatRead(args map[string]any) (string, bool) {
	task := taskID(args)
	if task == "" {
		return "task_id is required", true
	}
	limit := argInt(args, "limit", 10)
	if limit > 100 {
		limit = 100
	}
	items, meta, err := s.fetchActivityHistory(task, "main", argInt(args, "before", 0), limit)
	if err != nil {
		return s.errText(err)
	}
	// history is newest-first; return oldest→newest for readability
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return s.toolText(map[string]any{"task_id": task, "mainagent_messages": items, "meta": meta})
}

// --- conversations (top-level chat: the cross-task mainagent surface) ---

type conversationDTO struct {
	ID        int64  `json:"id"`
	AgentKey  string `json:"agent_key"`
	Title     string `json:"title"`
	Pinned    bool   `json:"pinned"`
	Running   bool   `json:"running"`
	UpdatedAt string `json:"updated_at"`
}

func (s *mcpServer) toolConversationsList(args map[string]any) (string, bool) {
	var out struct {
		Conversations []conversationDTO `json:"conversations"`
	}
	if err := s.client.getJSON("/api/conversations", nil, &out); err != nil {
		return s.errText(err)
	}
	offset := cursorOffset(argString(args, "cursor"))
	limit := argInt(args, "limit", 20)
	if limit > 100 {
		limit = 100
	}
	resp := map[string]any{"total": len(out.Conversations)}
	if offset < len(out.Conversations) {
		end := offset + limit
		if end > len(out.Conversations) {
			end = len(out.Conversations)
		}
		resp["conversations"] = out.Conversations[offset:end]
	} else {
		resp["conversations"] = []conversationDTO{}
	}
	if nc := nextCursor(offset, limit, len(out.Conversations)); nc != "" {
		resp["next_cursor"] = nc
	}
	return s.toolText(resp)
}

func (s *mcpServer) toolConversationCreate(args map[string]any) (string, bool) {
	agentKey := argString(args, "agent_key")
	if agentKey == "" {
		agentKey = "mainagent"
	}
	payload := map[string]any{"agent_key": agentKey}
	if v := argString(args, "title"); v != "" {
		payload["title"] = v
	}
	var created conversationDTO
	status, raw, err := s.client.do(http.MethodPost, "/api/conversations", payload, nil)
	if err != nil {
		return s.errText(err)
	}
	if status != http.StatusOK {
		return fmt.Sprintf("create conversation failed: %d %s", status, truncate(string(raw), 512)), true
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return fmt.Sprintf("created (unparsed): %s", truncate(string(raw), 256)), false
	}
	return s.toolText(map[string]any{"created": true, "id": created.ID,
		"agent_key": created.AgentKey, "title": created.Title})
}

func (s *mcpServer) toolConversationSend(args map[string]any) (string, bool) {
	id := argID(args, "conversation_id")
	if id == "" {
		return "conversation_id is required", true
	}
	msg := argString(args, "message")
	if msg == "" {
		return "message is required", true
	}
	status, raw, err := s.client.do(http.MethodPost,
		fmt.Sprintf("/api/conversations/%s/messages", id), map[string]any{"message": msg}, nil)
	if err != nil {
		return s.errText(err)
	}
	switch status {
	case http.StatusAccepted:
		return s.toolText(map[string]any{"conversation_id": id, "status": "accepted",
			"note": "async: poll with artex_conversation_read"})
	case http.StatusConflict:
		return "conversation busy: a previous message is still being processed", true
	default:
		return fmt.Sprintf("send failed: %d %s", status, truncate(string(raw), 512)), true
	}
}

func (s *mcpServer) toolConversationRead(args map[string]any) (string, bool) {
	id := argID(args, "conversation_id")
	if id == "" {
		return "conversation_id is required", true
	}
	limit := argInt(args, "limit", 20)
	if limit > 200 {
		limit = 200
	}
	q := url.Values{"limit": {fmt.Sprintf("%d", limit)}}
	if v := argInt(args, "since", 0); v > 0 {
		q.Set("since", fmt.Sprintf("%d", v))
	}
	var out struct {
		Items   []activityItem `json:"items"`
		Cursor  int64          `json:"cursor"`
		Running bool           `json:"running"`
		HasMore bool           `json:"hasMore"`
	}
	if err := s.client.getJSON(fmt.Sprintf("/api/conversations/%s/messages", id), q, &out); err != nil {
		return s.errText(err)
	}
	// latest page is newest-first; tail (since) is ascending — normalize to
	// oldest→newest either way.
	if argInt(args, "since", 0) == 0 {
		for i, j := 0, len(out.Items)-1; i < j; i, j = i+1, j-1 {
			out.Items[i], out.Items[j] = out.Items[j], out.Items[i]
		}
	}
	return s.toolText(map[string]any{"conversation_id": id, "messages": out.Items,
		"cursor": out.Cursor, "running": out.Running, "has_more": out.HasMore})
}

// --- traffic ---

func (s *mcpServer) toolTrafficList(args map[string]any) (string, bool) {
	page := cursorOffset(argString(args, "cursor")) // 0-based page (native API paging)
	limit := argInt(args, "limit", 20)
	if limit > 100 {
		limit = 100
	}
	q := url.Values{
		"page": {fmt.Sprintf("%d", page)},
		"size": {fmt.Sprintf("%d", limit)},
	}
	for _, key := range []string{"host", "method", "q", "path", "status"} {
		if v := argString(args, key); v != "" {
			q.Set(key, v)
		}
	}
	var out struct {
		Count     int              `json:"count"`
		Total     int              `json:"total"`
		Enabled   bool             `json:"enabled"`
		Exchanges []map[string]any `json:"exchanges"`
	}
	if err := s.client.getJSON("/api/traffic", q, &out); err != nil {
		return s.errText(err)
	}
	resp := map[string]any{"total_recorded": out.Count, "matched": out.Total,
		"capture_enabled": out.Enabled, "exchanges": out.Exchanges}
	// cursor is a 0-based PAGE: next page exists while the next page's first
	// row index ((page+1)*limit) is still below the matched total.
	if (page+1)*limit < out.Total {
		resp["next_cursor"] = fmt.Sprintf("%d", page+1)
	}
	return s.toolText(resp)
}

func (s *mcpServer) toolTrafficExchange(args map[string]any) (string, bool) {
	id := argString(args, "exchange_id")
	if id == "" {
		return "exchange_id is required", true
	}
	q := url.Values{"id": {id}}
	status, raw, err := s.client.do(http.MethodGet, "/api/traffic/exchange", nil, q)
	if err != nil {
		return s.errText(err)
	}
	if status != http.StatusOK {
		return fmt.Sprintf("exchange fetch failed: %d %s", status, truncate(string(raw), 512)), true
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return s.toolText(out)
}

// --- assets ---

func (s *mcpServer) toolAssetsList(args map[string]any) (string, bool) {
	offset := cursorOffset(argString(args, "cursor"))
	limit := argInt(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	q := url.Values{
		"limit":  {fmt.Sprintf("%d", limit)},
		"offset": {fmt.Sprintf("%d", offset)},
	}
	if v := argString(args, "type"); v != "" {
		q.Set("type", v)
	}
	if v := argID(args, "company_id"); v != "" {
		q.Set("company_id", v)
	}
	if v := argID(args, "task_id"); v != "" {
		q.Set("task_id", v)
	}
	if v := argString(args, "dsl"); v != "" {
		q.Set("dsl", v)
	}
	var out struct {
		Count  int              `json:"count"`
		Total  int              `json:"total"`
		Assets []map[string]any `json:"assets"`
	}
	if err := s.client.getJSON("/api/assets", q, &out); err != nil {
		return s.errText(err)
	}
	resp := map[string]any{"assets": out.Assets, "shown": len(out.Assets), "total": out.Total}
	if nc := nextCursor(offset, limit, out.Total); nc != "" {
		resp["next_cursor"] = nc
	}
	return s.toolText(resp)
}
