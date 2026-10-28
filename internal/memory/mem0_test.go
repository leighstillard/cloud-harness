package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type call struct {
	path  string
	query string
	auth  string
	body  map[string]any
}

func fake(t *testing.T, handler func(c call, w http.ResponseWriter)) (*httptest.Server, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		c := call{path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization"), body: body}
		calls = append(calls, c)
		handler(c, w)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func rows(w http.ResponseWriter, rs ...map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"count": len(rs), "results": rs})
}

func TestSearchGlobalListsAndFiltersClientSide(t *testing.T) {
	srv, calls := fake(t, func(c call, w http.ResponseWriter) {
		rows(w,
			map[string]any{"id": "1", "memory": "global one", "user_id": "u", "created_at": "2026-01-01T00:00:00Z"},
			map[string]any{"id": "2", "memory": "repo one", "user_id": "u", "agent_id": "o/r", "session_id": "s1"},
			map[string]any{"id": "3", "memory": "global two", "user_id": "u"},
		)
	})
	m := NewMem0(srv.URL+"/", "secret", time.Second)
	got, err := m.Search(context.Background(), Query{Scope: Scope{UserID: "u"}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "global one" || got[1].Text != "global two" {
		t.Errorf("got %+v", got)
	}
	c := (*calls)[0]
	if c.path != "/v3/memories/" || c.query != "page=1&page_size=100" {
		t.Errorf("path %q query %q", c.path, c.query)
	}
	if c.auth != "Token secret" {
		t.Errorf("auth %q", c.auth)
	}
	f := c.body["filters"].(map[string]any)
	if f["user_id"] != "u" || len(f) != 1 {
		t.Errorf("filters %v", f)
	}
}

func TestSearchRepoScopeFilterAndNoAuthHeader(t *testing.T) {
	srv, calls := fake(t, func(c call, w http.ResponseWriter) {
		rows(w, map[string]any{"id": "2", "memory": "repo one", "agent_id": "o/r", "session_id": "s1", "metadata": map[string]any{"source": "harness"}})
	})
	m := NewMem0(srv.URL, "", time.Second)
	got, err := m.Search(context.Background(), Query{Scope: Scope{UserID: "u", AgentID: "o/r"}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RunID != "s1" || got[0].Metadata["source"] != "harness" {
		t.Errorf("got %+v", got)
	}
	c := (*calls)[0]
	if c.auth != "" {
		t.Errorf("expected no Authorization header, got %q", c.auth)
	}
	if c.query != "page=1&page_size=20" {
		t.Errorf("query %q", c.query)
	}
	and := c.body["filters"].(map[string]any)["AND"].([]any)
	if len(and) != 2 || and[0].(map[string]any)["user_id"] != "u" || and[1].(map[string]any)["agent_id"] != "o/r" {
		t.Errorf("filters %v", c.body["filters"])
	}
}

func TestSearchWithTextUsesSearchEndpoint(t *testing.T) {
	srv, calls := fake(t, func(c call, w http.ResponseWriter) {
		rows(w, map[string]any{"id": "9", "memory": "hit", "run_id": "r9"})
	})
	m := NewMem0(srv.URL, "k", time.Second)
	got, err := m.Search(context.Background(), Query{Scope: Scope{UserID: "u", AgentID: "o/r"}, Text: "deploy", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RunID != "r9" {
		t.Errorf("got %+v", got)
	}
	c := (*calls)[0]
	if c.path != "/v3/memories/search/" || c.body["query"] != "deploy" || c.body["top_k"].(float64) != 5 {
		t.Errorf("call %+v", c)
	}
}

func TestAddBody(t *testing.T) {
	srv, calls := fake(t, func(c call, w http.ResponseWriter) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"event_id":"e","status":"PENDING"}`))
	})
	m := NewMem0(srv.URL, "k", time.Second)
	err := m.Add(context.Background(), AddRequest{
		Scope: Scope{UserID: "u", AgentID: "o/r"}, Text: "use make test", RunID: "sess-1",
		Metadata: map[string]any{"source": "harness", "repo": "o/r"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.path != "/v3/memories/add/" {
		t.Errorf("path %q", c.path)
	}
	msgs := c.body["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "use make test" || msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("messages %v", msgs)
	}
	if c.body["user_id"] != "u" || c.body["agent_id"] != "o/r" || c.body["run_id"] != "sess-1" || c.body["infer"] != false {
		t.Errorf("body %v", c.body)
	}
	if c.body["metadata"].(map[string]any)["source"] != "harness" {
		t.Errorf("metadata %v", c.body["metadata"])
	}
}

func TestAddGlobalOmitsAgent(t *testing.T) {
	srv, calls := fake(t, func(c call, w http.ResponseWriter) { w.WriteHeader(200) })
	m := NewMem0(srv.URL, "k", time.Second)
	if err := m.Add(context.Background(), AddRequest{Scope: Scope{UserID: "u"}, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := (*calls)[0].body["agent_id"]; ok {
		t.Error("agent_id should be absent for global scope")
	}
	if err := m.Add(context.Background(), AddRequest{Scope: Scope{UserID: "u"}, Text: "  "}); err == nil {
		t.Error("expected error for empty text")
	}
}

func TestErrorsAndPing(t *testing.T) {
	status := http.StatusUnauthorized
	srv, _ := fake(t, func(c call, w http.ResponseWriter) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"detail":"nope"}`))
	})
	m := NewMem0(srv.URL, "", time.Second)
	err := m.Ping(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
	status = http.StatusInternalServerError
	if err := m.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("expected HTTP 500 error, got %v", err)
	}
	status = http.StatusOK
	if err := m.Ping(context.Background()); err != nil {
		t.Errorf("ping: %v", err)
	}
	if _, err := m.Search(context.Background(), Query{}); err == nil {
		t.Error("expected error without user id")
	}
}

func TestTimeoutHonoursContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	m := NewMem0(srv.URL, "", 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := m.Search(ctx, Query{Scope: Scope{UserID: "u"}}); err == nil {
		t.Error("expected timeout error")
	}
	if time.Since(start) > time.Second {
		t.Error("search did not honour context deadline")
	}
}
