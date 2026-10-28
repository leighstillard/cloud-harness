// Package memory defines the persistent-memory provider used by harness.
package memory

import (
	"context"
	"errors"
	"time"
)

// Scope identifies whose memory and, optionally, which repo it belongs to.
// AgentID is "" for global (cross-repo) memories and "owner/name" for repo memories.
type Scope struct {
	UserID  string
	AgentID string
}

// Global reports whether the scope has no repo.
func (s Scope) Global() bool { return s.AgentID == "" }

// Query is a memory lookup. Text may be empty, meaning "list the most recent".
type Query struct {
	Scope
	Text  string
	Limit int
}

// Memory is one stored fact.
type Memory struct {
	ID        string
	Text      string
	UserID    string
	AgentID   string
	RunID     string
	CreatedAt time.Time
	Metadata  map[string]any
}

// AddRequest is one fact to store.
type AddRequest struct {
	Scope
	Text     string
	RunID    string
	Metadata map[string]any
}

// Provider is a memory backend.
type Provider interface {
	// Search returns up to q.Limit memories in q.Scope. A global scope returns
	// only memories with no agent id; a repo scope returns only that repo's.
	Search(ctx context.Context, q Query) ([]Memory, error)
	// Add stores one fact.
	Add(ctx context.Context, r AddRequest) error
	// Ping checks reachability and authentication without changing anything.
	Ping(ctx context.Context) error
}

// ErrUnauthorized is returned by Ping when the provider rejects the credential
// (or, in the cloud, when the agent proxy did not inject one).
var ErrUnauthorized = errors.New("memory: unauthorized")
