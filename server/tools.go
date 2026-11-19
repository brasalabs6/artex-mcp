package main

// toolDefs returns the MCP tool list with JSON schemas. Descriptions are
// written for the consuming LLM agent: they explain the task lifecycle and
// the template-first creation flow so the agent uses them correctly on
// first contact. Scope-expanding and graph-writing endpoints are
// deliberately absent (scope expansion and graph writes stay with the
// platform's own agents; the MCP is a consumer and dispatcher).
func toolDefs() []map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	intp := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	boolp := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}

	return []map[string]any{
		{
			"name": "artex_task_create",
			"description": "Create (and launch) a pentest task on the ARTEX platform. " +
				"Preferred flow: pass template_id (from artex_templates_list) — the template supplies description, goal, category and its saved scope rules; " +
				"explicit arguments override the template. " +
				"The task starts immediately: ARTEX seeds the target asset, decomposes the goal, and runs planner + workers. " +
				"Only create tasks against targets the user explicitly authorized. " +
				"Use scope_rules (action=allow) to pin exactly what the task may test.",
			"inputSchema": obj(map[string]any{
				"template_id":            str("Task template id (see artex_templates_list)."),
				"description":            str("Short task title. Optional when template_id is given."),
				"goal":                   str("Detailed objective the agents work toward. Optional when template_id is given."),
				"name":                   str("Optional task name."),
				"llm_profile_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Ordered LLM profile chain (first is active; failover follows)."},
				"company_ids":            map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Company scopes to associate (assets snapshot)."},
				"source_task_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Ids of tasks whose explored assets/conclusions this task READ-ONLY inherits as a starting point (e.g. retests)."},
				"timeout_seconds":        intp("Task-level timeout in seconds (0 = unlimited)."),
				"plan_heartbeat_seconds": intp("Planner heartbeat interval in seconds (min 600; default 600)."),
				"coverage_enabled":       boolp("Asset coverage tracking (default true)."),
				"seed_first_intent":      boolp("true = workers start immediately without waiting for the first planner round (one-shot/CTF style)."),
				"scope_rules": map[string]any{"type": "array", "description": "Asset rules pinned to this task. action=allow turns the list into the task's whitelist — the task may only test what matches an allow rule.",
					"items": obj(map[string]any{
						"action":  map[string]any{"type": "string", "enum": []string{"allow", "block"}},
						"kind":    map[string]any{"type": "string", "enum": []string{"exact_domain", "exact_ip", "exact_url", "fuzzy_domain", "fuzzy_ip", "fuzzy_url", "cidr"}},
						"pattern": str("Domain / IP / URL / CIDR to match."),
						"note":    str("Optional note."),
					}, "action", "kind", "pattern")},
			}),
		},
		{
			"name":        "artex_templates_list",
			"description": "List the ARTEX task templates (e.g. a pentest template library) with ids, names and goal previews. Use an id as artex_task_create's template_id.",
			"inputSchema": obj(map[string]any{}),
		},
		{
			"name":        "artex_tasks_list",
			"description": "List ARTEX tasks with status (created/running/paused/done/failed/timeout), paused/queued flags and the active task id. Paginated: pass next_cursor as cursor for more.",
			"inputSchema": obj(map[string]any{
				"cursor": str("Pagination cursor from a previous call next_cursor."),
				"limit":  intp("Page size (default 20)."),
			}),
		},
		{
			"name":        "artex_task_control",
			"description": "Pause or resume one or more ARTEX tasks. Paused tasks keep their state and resume exactly where they stopped.",
			"inputSchema": obj(map[string]any{
				"task_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Task ids."},
				"action":   map[string]any{"type": "string", "enum": []string{"pause", "resume"}},
			}, "task_ids", "action"),
		},
		{
			"name": "artex_task_graph",
			"description": "Overview of a task's exploration graph: node/edge counts, counts per kind (fact/intent/finding/goal/hint/digest) and a compact node list. " +
				"Filter with kind (e.g. finding, intent, goal) and cap with limit. Drill into a single node with artex_task_node_detail.",
			"inputSchema": obj(map[string]any{
				"task_id": str("Task id."),
				"kind":    str("Optional node kind filter (fact|intent|finding|goal|hint|digest)."),
				"limit":   intp("Page size (default 30, max 200)."),
				"cursor":  str("Pagination cursor from a previous call next_cursor."),
			}, "task_id"),
		},
		{
			"name": "artex_task_findings",
			"description": "List a task's findings (vulnerabilities/proofs) in COMPACT triage form: node_id, name, severity, vulnclass, status, assets. " +
				"Drill into node_id with artex_task_node_detail for the full evidence/PoC and summary. Pass full=true for the raw complete records.",
			"inputSchema": obj(map[string]any{
				"task_id": str("Task id."),
				"page":    intp("Page number (default 1)."),
				"limit":   intp("Page size (default 20)."),
				"cursor":  str("Pagination cursor from a previous call next_cursor."),
				"full":    boolp("Return the complete raw records (large)."),
			}, "task_id"),
		},
		{
			"name": "artex_task_node_detail",
			"description": "Full content of one exploration-graph node (finding evidence/PoC, fact details, intent, goal) plus the edges connecting it. " +
				"node_id comes from artex_task_findings or artex_task_graph output.",
			"inputSchema": obj(map[string]any{
				"task_id": str("Task id."),
				"node_id": str("Exploration node id (not an asset id) — from artex_task_findings or artex_task_graph."),
			}, "task_id", "node_id"),
		},
		{
			"name": "artex_task_activity",
			"description": "What the task's agents did. Two modes: " +
				"(1) session mode (recommended for latest activity): pass session=main (mainagent), plan (planner) or intent:<id> — returns the NEWEST entries first, page older ones with the before cursor; " +
				"(2) stream mode (no session): ALL workers, oldest-first from `since` — use the returned cursor to poll for new entries. " +
				"Pass seq to fetch the full detail of one activity entry.",
			"inputSchema": obj(map[string]any{
				"task_id": str("Task id."),
				"session": str("main | plan | intent:<id> — newest-first history of that session."),
				"before":  intp("Session mode: cursor from the previous page's meta (earliest_cursor) to page older entries."),
				"worker":  str("Stream mode only: worker substring filter (e.g. 'planner', 'work', 'mainagent')."),
				"since":   intp("Stream mode only: only entries after this seq."),
				"limit":   intp("Max entries (session default 30, stream default 100)."),
				"seq":     intp("Optional: fetch full detail of this single activity seq."),
			}, "task_id"),
		},
		{
			"name": "artex_chat_send",
			"description": "Send a message to the task's main agent (human-in-the-loop channel). Use it to steer a running task, ask for a status summary, " +
				"or request deeper analysis of findings. Async by default (mode llm): read replies with artex_chat_read.",
			"inputSchema": obj(map[string]any{
				"task_id": str("Task id."),
				"message": str("Message for the main agent."),
			}, "task_id", "message"),
		},
		{
			"name":        "artex_chat_read",
			"description": "Read the main agent's latest replies for a task (oldest→newest). Poll after artex_chat_send; page older messages with the before cursor.",
			"inputSchema": obj(map[string]any{
				"task_id": str("Task id."),
				"limit":   intp("Max messages returned (default 10)."),
				"before":  intp("Cursor from the previous read's meta (earliest_cursor) to page older messages."),
			}, "task_id"),
		},
		{
			"name":        "artex_traffic_list",
			"description": "HTTP(S) exchanges recorded by the ARTEX MITM proxy (what the agents actually sent to targets). Server-side filters + paging: matched is the filtered total; pass next_cursor as cursor for the next page.",
			"inputSchema": obj(map[string]any{
				"host":   str("Host substring filter."),
				"method": str("HTTP method filter (GET, POST...)."),
				"q":      str("Free-text query (URL/headers)."),
				"path":   str("URL path filter."),
				"status": str("HTTP status filter."),
				"limit":  intp("Page size (default 20, max 100)."),
				"cursor": str("Pagination cursor from next_cursor (0-based page)."),
			}),
		},
		{
			"name": "artex_conversations_list",
			"description": "List the top-level chat conversations (the cross-task mainagent surface — the same conversations shown in the ARTEX UI chat tab). " +
				"Use artex_conversation_send/read on one of these ids to talk to the agent that can read findings across all tasks. Paginated.",
			"inputSchema": obj(map[string]any{
				"cursor": str("Pagination cursor from a previous call next_cursor."),
				"limit":  intp("Page size (default 20)."),
			}),
		},
		{
			"name":        "artex_conversation_create",
			"description": "Create a new top-level chat conversation. Default agent is the mainagent (cross-task view with orchestration tools).",
			"inputSchema": obj(map[string]any{
				"agent_key": str("Agent key (default mainagent)."),
				"title":     str("Optional title (auto-titled from the first message)."),
			}),
		},
		{
			"name":        "artex_conversation_send",
			"description": "Send a message to a top-level conversation (async, 202 accepted; the agent answers in the background). Poll with artex_conversation_read.",
			"inputSchema": obj(map[string]any{
				"conversation_id": str("Conversation id (from artex_conversations_list or artex_conversation_create)."),
				"message":         str("Message text."),
			}, "conversation_id", "message"),
		},
		{
			"name": "artex_conversation_read",
			"description": "Read a conversation's messages (oldest→newest). Default returns the latest page; pass since (cursor from a previous read) to tail new messages. " +
				"running=true means the agent is still processing.",
			"inputSchema": obj(map[string]any{
				"conversation_id": str("Conversation id."),
				"limit":           intp("Max messages (default 20)."),
				"since":           intp("Only messages after this cursor (tail polling)."),
			}, "conversation_id"),
		},
		{
			"name":        "artex_traffic_exchange",
			"description": "Full request/response of one recorded traffic exchange (headers and bodies). exchange_id comes from artex_traffic_list output.",
			"inputSchema": obj(map[string]any{
				"exchange_id": str("Exchange id from artex_traffic_list."),
			}, "exchange_id"),
		},
		{
			"name":        "artex_assets_list",
			"description": "The global asset graph (domains, IPs, services) across all tasks, with which tasks touched each asset. NOTE: without a type filter the API lists root_domain only. Paginated server-side; pass next_cursor as cursor for more.",
			"inputSchema": obj(map[string]any{
				"type":       str("Asset type filter (root_domain, ip, url, endpoint, service...). Default root_domain."),
				"company_id": str("Filter by company scope."),
				"task_id":    str("Filter by task."),
				"dsl":        str("Advanced: asset DSL query (validated by the API)."),
				"limit":      intp("Page size (default 50, max 200)."),
				"cursor":     str("Pagination cursor from a previous call next_cursor."),
			}),
		},
	}
}
