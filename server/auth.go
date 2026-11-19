package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// withAuth rejects any request whose X-API-Key does not match the configured
// key. Comparison is constant-time. The empty-key case never happens: main
// refuses to start without ARTEX_MCP_API_KEY.
func withAuth(cfg config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(cfg.apiKey)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			// JSON-RPC-shaped error so MCP clients surface a readable message.
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":"unauthorized"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
