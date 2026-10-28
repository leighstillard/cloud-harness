package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Mem0 Platform REST shape (verified against docs.mem0.ai on 2026-10-28):
//
//	List:   POST {base}/v3/memories/?page=1&page_size=N
//	        body {"filters": {...}}                      -> {"count","next","previous","results":[...]}
//	Search: POST {base}/v3/memories/search/
//	        body {"query": "...", "filters": {...}, "top_k": N} -> {"results":[...]}
//	Add:    POST {base}/v3/memories/add/
//	        body {"messages":[{"role","content"}], "user_id", "agent_id", "run_id",
//	              "metadata", "infer": false}             -> 200 with results, or 202 {"event_id","status":"PENDING"}
//	Auth:   Authorization: Token <api-key>
//
// Entity ids (user_id, agent_id, run_id) go inside "filters"; top-level entity ids are
// rejected with 400. Filters support AND/OR/NOT and in/gte/lte/gt/lt/ne/icontains.
// The docs do not define a filter for "agent_id is unset", so the global scope lists
// by user_id and drops repo-scoped rows client-side.
//
// Each result row carries id, memory, user_id, agent_id, metadata, created_at, and
// the run id as "run_id" (search) or "session_id" (list).

const (
	mem0ListPath   = "/v3/memories/"
	mem0SearchPath = "/v3/memories/search/"
	mem0AddPath    = "/v3/memories/add/"
	// listOverfetch is how many rows a global list pulls before client-side filtering.
	listOverfetch = 100
)

// Mem0 talks to the Mem0 Platform API.
type Mem0 struct {
	BaseURL string
	// Key is the API key. When empty no Authorization header is sent: in the
	// cloud the agent proxy attaches it as a network secret.
	Key    string
	Client *http.Client
}

// NewMem0 returns a client. key may be "".
func NewMem0(baseURL, key string, timeout time.Duration) *Mem0 {
	return &Mem0{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Key:     key,
		Client:  &http.Client{Timeout: timeout},
	}
}

type mem0Row struct {
	ID        string         `json:"id"`
	Memory    string         `json:"memory"`
	UserID    string         `json:"user_id"`
	AgentID   string         `json:"agent_id"`
	RunID     string         `json:"run_id"`
	SessionID string         `json:"session_id"`
	Metadata  map[string]any `json:"metadata"`
	CreatedAt time.Time      `json:"created_at"`
}

func (r mem0Row) toMemory() Memory {
	run := r.RunID
	if run == "" {
		run = r.SessionID
	}
	return Memory{ID: r.ID, Text: r.Memory, UserID: r.UserID, AgentID: r.AgentID, RunID: run, CreatedAt: r.CreatedAt, Metadata: r.Metadata}
}

func filtersFor(s Scope) map[string]any {
	if s.Global() {
		return map[string]any{"user_id": s.UserID}
	}
	return map[string]any{"AND": []map[string]any{{"user_id": s.UserID}, {"agent_id": s.AgentID}}}
}

// Search implements Provider.
func (m *Mem0) Search(ctx context.Context, q Query) ([]Memory, error) {
	if q.UserID == "" {
		return nil, fmt.Errorf("memory: user id is required")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	var (
		path string
		body map[string]any
		url  string
	)
	fetch := limit
	if q.Global() {
		fetch = max(limit, listOverfetch)
	}
	if strings.TrimSpace(q.Text) == "" {
		path = mem0ListPath
		url = fmt.Sprintf("%s%s?page=1&page_size=%d", m.BaseURL, path, fetch)
		body = map[string]any{"filters": filtersFor(q.Scope)}
	} else {
		path = mem0SearchPath
		url = m.BaseURL + path
		body = map[string]any{"query": q.Text, "filters": filtersFor(q.Scope), "top_k": fetch}
	}
	var resp struct {
		Results []mem0Row `json:"results"`
	}
	if err := m.do(ctx, url, body, &resp); err != nil {
		return nil, err
	}
	out := make([]Memory, 0, len(resp.Results))
	for _, r := range resp.Results {
		if q.Global() && r.AgentID != "" {
			continue
		}
		out = append(out, r.toMemory())
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Add implements Provider. Facts are stored verbatim (infer=false).
func (m *Mem0) Add(ctx context.Context, r AddRequest) error {
	if r.UserID == "" {
		return fmt.Errorf("memory: user id is required")
	}
	if strings.TrimSpace(r.Text) == "" {
		return fmt.Errorf("memory: empty text")
	}
	body := map[string]any{
		"messages": []map[string]string{{"role": "user", "content": r.Text}},
		"user_id":  r.UserID,
		"infer":    false,
	}
	if r.AgentID != "" {
		body["agent_id"] = r.AgentID
	}
	if r.RunID != "" {
		body["run_id"] = r.RunID
	}
	if len(r.Metadata) > 0 {
		body["metadata"] = r.Metadata
	}
	return m.do(ctx, m.BaseURL+mem0AddPath, body, nil)
}

// Ping implements Provider with a one-row list for a scope that need not exist.
func (m *Mem0) Ping(ctx context.Context) error {
	url := fmt.Sprintf("%s%s?page=1&page_size=1", m.BaseURL, mem0ListPath)
	return m.do(ctx, url, map[string]any{"filters": map[string]any{"user_id": "harness-ping"}}, nil)
}

func (m *Mem0) do(ctx context.Context, url string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cloud-harness")
	if m.Key != "" {
		req.Header.Set("Authorization", "Token "+m.Key)
	}
	client := m.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d): %s", ErrUnauthorized, res.StatusCode, trim(data))
	case res.StatusCode < 200 || res.StatusCode >= 300:
		return fmt.Errorf("memory: HTTP %d from %s: %s", res.StatusCode, url, trim(data))
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("memory: decode response: %w", err)
		}
	}
	return nil
}

func trim(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
