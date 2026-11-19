package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// artexClient talks to the ARTEX HTTP API. It owns the admin session: login
// once, cache the Bearer token, and transparently re-login on 401 (the ARTEX
// jwt.key regenerates on container restart, invalidating tokens).
type artexClient struct {
	baseURL string
	user    string
	pass    string
	http    *http.Client

	mu    sync.Mutex
	token string
}

func newArtexClient(cfg config) *artexClient {
	return &artexClient{
		baseURL: cfg.baseURL,
		user:    cfg.username,
		pass:    cfg.password,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// login fetches a fresh Bearer token from POST /api/auth/login.
func (c *artexClient) login() error {
	body, _ := json.Marshal(map[string]string{"username": c.user, "password": c.pass})
	resp, err := c.http.Post(c.baseURL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("artex unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("artex login failed: %d %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" {
		return fmt.Errorf("artex login: no token in response")
	}
	c.mu.Lock()
	c.token = out.Token
	c.mu.Unlock()
	return nil
}

// do performs an authenticated API call; on 401 it re-logins once and retries.
// Returns status code and raw body.
func (c *artexClient) do(method, path string, body any, query url.Values) (int, []byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		token := c.token
		c.mu.Unlock()
		if token == "" {
			if err := c.login(); err != nil {
				return 0, nil, err
			}
			continue
		}
		u := c.baseURL + path
		if len(query) > 0 {
			u += "?" + query.Encode()
		}
		var rdr io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return 0, nil, err
			}
			rdr = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, u, rdr)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return 0, nil, fmt.Errorf("artex call failed: %w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			if err := c.login(); err != nil {
				return 0, nil, err
			}
			continue
		}
		return resp.StatusCode, raw, nil
	}
	return 0, nil, fmt.Errorf("unreachable")
}

// getJSON performs a GET and decodes the body into out. Non-200 is an error
// carrying the upstream body (truncated).
func (c *artexClient) getJSON(path string, query url.Values, out any) error {
	status, raw, err := c.do(http.MethodGet, path, nil, query)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("artex GET %s: %d %s", path, status, truncate(string(raw), 512))
	}
	return json.Unmarshal(raw, out)
}

// postJSON performs a POST; wantStatus is the expected code (201 or 200).
func (c *artexClient) postJSON(path string, body any, wantStatus int, out any) error {
	status, raw, err := c.do(http.MethodPost, path, body, nil)
	if err != nil {
		return err
	}
	if status != wantStatus {
		return fmt.Errorf("artex POST %s: %d %s", path, status, truncate(string(raw), 512))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func parseInt(s string) (int, error) {
	return strconv.Atoi(s)
}
