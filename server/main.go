// Package main implements artex-mcp: a thin MCP server that exposes the
// ARTEX pentest platform's HTTP API as MCP tools, so external agents can
// create tasks from templates, control them, and consume findings, graphs,
// worker traces and captured traffic.
//
// Design constraints:
//   - stdlib only: no third-party dependencies, no supply-chain surface;
//   - stateless MCP Streamable HTTP (POST JSON-RPC, JSON responses,
//     notifications answered with 202, no session id);
//   - auth: X-API-Key with constant-time compare; binds inside the docker
//     network / loopback, reached only through the SSH tunnel;
//   - upstream auth: ARTEX admin login, Bearer token cached, automatic
//     re-login on 401 (jwt.key rotates on container restart);
//   - tools are reads and dispatches only — no long-running work, responses
//     capped, scope-expanding and graph-writing endpoints deliberately
//     excluded (the platform's own agents own those).
package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

const serverVersion = "0.3.0"

type config struct {
	addr        string
	apiKey      string
	baseURL     string
	username    string
	password    string
	maxResponse int
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func loadConfig() config {
	c := config{
		addr:        envOr("ARTEX_MCP_ADDR", ":8790"),
		apiKey:      os.Getenv("ARTEX_MCP_API_KEY"),
		baseURL:     envOr("ARTEX_BASE_URL", "http://artex:8787"),
		username:    os.Getenv("ARTEX_USERNAME"),
		password:    os.Getenv("ARTEX_PASSWORD"),
		maxResponse: envInt("ARTEX_MCP_MAX_RESPONSE", 16384),
	}
	if c.apiKey == "" {
		log.Fatal("[config] ARTEX_MCP_API_KEY is required (refusing to start unauthenticated)")
	}
	if c.username == "" || c.password == "" {
		log.Fatal("[config] ARTEX_USERNAME and ARTEX_PASSWORD are required")
	}
	return c
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := parseInt(v); err == nil && n > 0 {
			return n
		}
		log.Printf("[config] ignoring invalid %s=%q", name, v)
	}
	return def
}

func main() {
	cfg := loadConfig()
	client := newArtexClient(cfg)
	mcp := newMCP(cfg, client)

	mux := http.NewServeMux()
	mux.HandleFunc("/", mcp.handle)

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           withAuth(cfg, mux),
		ReadHeaderTimeout: 15 * time.Second,
	}
	log.Printf("[artex-mcp] listening on %s -> %s (version %s)", cfg.addr, cfg.baseURL, serverVersion)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
