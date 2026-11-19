# artex-mcp

MCP server (Streamable HTTP) that exposes the [ARTEX](https://github.com/Autumn-27/artex) autonomous pentest platform's HTTP API as MCP tools — so external AI agents (Grok Build, Claude Code, any MCP client) can create tasks from templates, control their lifecycle, and consume findings, exploration graphs, worker traces and recorded traffic as native tools.

**No ARTEX modifications required.** This is a standalone API client (Go stdlib only, zero dependencies) that works against any stock ARTEX deployment.

## Tools (17)

| Tool | What it does |
|---|---|
| `artex_task_create` | Create and launch a task — resolves a `template_id` (description/goal/category/scope rules) with explicit overrides; accepts LLM profile chain, timeout, `seed_first_intent`, `scope_rules` (allow/block by domain/IP/URL/CIDR), `source_task_ids` (read-only inheritance from other tasks) |
| `artex_templates_list` | List the platform's task templates |
| `artex_tasks_list` | All tasks with status, paused/queued flags, active task |
| `artex_task_control` | Pause/resume (batch) |
| `artex_task_graph` | Exploration-graph overview: counts per kind + compact node list (paginated) |
| `artex_task_findings` | Findings in compact triage form (node_id, name, severity, vulnclass, status, assets); `full=true` for raw records |
| `artex_task_node_detail` | Full content of one graph node (evidence/PoC) plus its edges |
| `artex_task_activity` | What planner/workers/mainagent did — session mode (main/plan/intent) newest-first, or all-worker stream with a polling cursor |
| `artex_chat_send` / `artex_chat_read` | Talk to a task's main agent (human-in-the-loop steering) |
| `artex_conversations_list` / `artex_conversation_create` / `artex_conversation_send` / `artex_conversation_read` | The top-level cross-task chat surface |
| `artex_traffic_list` / `artex_traffic_exchange` | HTTP(S) exchanges recorded by the ARTEX MITM proxy — server-side filters (host, method, query, path, status) |
| `artex_assets_list` | Global asset graph (paginated; type/company/task filters, advanced DSL) |

**Pagination is uniform**: every list tool takes `cursor` and returns `next_cursor` (only when more pages exist) plus `total`. Cursors are opaque — just pass them back.

## Security design

- **Auth**: every request must carry `X-API-Key` (constant-time compare); the server refuses to start without one. Bind it to loopback or a private network — never expose it publicly.
- **Upstream auth**: ARTEX admin credentials via env; the Bearer token is cached and re-acquired automatically on 401.
- **Deliberate exclusions** — the MCP is a consumer and dispatcher, not a superuser: no scope expansion (`insert_assets`/`add_task_scope`/`add_company_scope`), no exploration-graph writes (`add_intent`/`record_fact`/`prove_goal`/`kill_work`/`steer_work`), no platform administration, no finding writes. Scope is a human decision at task creation; steering goes through the chat channels.
- **Bounded responses**: payloads capped (default 16 KB, configurable) so a client never receives a mid-JSON cut.

## Authorized use

ARTEX is an autonomous security-testing platform. Use this server — and the tasks it creates — only against systems you are explicitly authorized to test. The tool descriptions instruct agents accordingly; the operator remains responsible for scope.

## Configuration (env)

| Var | Default | |
|---|---|---|
| `ARTEX_MCP_ADDR` | `:8790` | bind address |
| `ARTEX_MCP_API_KEY` | — | **required** (refuses to start without it) |
| `ARTEX_BASE_URL` | `http://artex:8787` | ARTEX API base URL |
| `ARTEX_USERNAME` / `ARTEX_PASSWORD` | — | **required** (ARTEX admin API credentials) |
| `ARTEX_MCP_MAX_RESPONSE` | `16384` | response cap in bytes |

## Quick start

```bash
# next to your ARTEX deployment (same docker network)
git clone <this repo> && cd artex-mcp
cat > .env <<EOF   # chmod 600
ARTEX_MCP_API_KEY=<generate one>
ARTEX_USERNAME=<artex admin user>
ARTEX_PASSWORD=<artex admin password>
EOF
docker compose build && docker compose up -d

# validate
curl -s -H 'X-API-Key: ...' -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' http://127.0.0.1:8790/
```

Then register it with your MCP client, e.g. Grok Build (`~/.grok/config.toml`):

```toml
[mcp_servers.artex]
url = "http://localhost:8790/"
enabled = true
headers = { "X-API-Key" = "<your key>" }
```

If your ARTEX runs on a remote host, reach the MCP through an SSH tunnel
(`ssh -N -L 8790:127.0.0.1:8790 user@host`) — the compose file already binds
`127.0.0.1:8790` on the host only.

## Development

```bash
cd server && go test ./...   # fake ARTEX: auth, handshake, all tools, pagination, re-login on 401
```

## License

[MIT](LICENSE)
