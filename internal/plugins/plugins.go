// Package plugins installs pinned Claude Code plugins via the claude CLI.
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/leighstillard/cloud-harness/internal/config"
)

// Runner executes the claude CLI. Replaceable in tests.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// Installer installs plugins idempotently.
type Installer struct {
	Run Runner
	Log io.Writer
}

// New returns an Installer backed by the `claude` binary on PATH.
func New(log io.Writer) *Installer {
	return &Installer{Run: runClaude, Log: log}
}

func runClaude(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// Installed is one entry of `claude plugin list --json`.
type Installed struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Scope   string `json:"scope"`
	Enabled bool   `json:"enabled"`
}

// List returns installed plugins keyed by id (name@marketplace).
func (in *Installer) List(ctx context.Context) (map[string]Installed, error) {
	out, err := in.Run(ctx, "plugin", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("claude plugin list: %w: %s", err, firstLine(out))
	}
	var items []Installed
	if err := json.Unmarshal(bytes.TrimSpace(out), &items); err != nil {
		return nil, fmt.Errorf("claude plugin list: parse: %w", err)
	}
	m := make(map[string]Installed, len(items))
	for _, it := range items {
		m[it.ID] = it
	}
	return m, nil
}

// Result summarises one plugin's outcome.
type Result struct {
	Plugin  config.Plugin
	Skipped bool // already installed
	Err     error
}

// Ensure installs every plugin not already present. Marketplace adds (the
// network-heavy step) run in parallel; installs run serially because they all
// rewrite the same settings files. Missing marketplaces are retried serially
// once, in case parallel writes to the marketplace registry collided.
func (in *Installer) Ensure(ctx context.Context, want []config.Plugin) []Result {
	results := make([]Result, len(want))
	for i, p := range want {
		results[i].Plugin = p
	}
	if len(want) == 0 {
		return results
	}
	have, err := in.List(ctx)
	if err != nil {
		in.logf("warning: %v", err)
		have = map[string]Installed{}
	}
	var todo []int
	for i, p := range want {
		if _, ok := have[p.ID()]; ok {
			results[i].Skipped = true
			continue
		}
		todo = append(todo, i)
	}
	if len(todo) == 0 {
		return results
	}

	sources := map[string]struct{}{}
	for _, i := range todo {
		sources[want[i].Source] = struct{}{}
	}
	var wg sync.WaitGroup
	addErrs := make(map[string]error, len(sources))
	var mu sync.Mutex
	for src := range sources {
		wg.Add(1)
		go func(src string) {
			defer wg.Done()
			err := in.addMarketplace(ctx, src)
			mu.Lock()
			addErrs[src] = err
			mu.Unlock()
		}(src)
	}
	wg.Wait()
	for src, err := range addErrs {
		if err != nil {
			in.logf("retrying marketplace %s: %v", src, err)
			addErrs[src] = in.addMarketplace(ctx, src)
		}
	}
	for _, i := range todo {
		p := want[i]
		if err := addErrs[p.Source]; err != nil {
			results[i].Err = fmt.Errorf("marketplace add %s: %w", p.Source, err)
			continue
		}
		out, err := in.Run(ctx, "plugin", "install", p.ID(), "--scope", "user", "-y", "--json")
		if err != nil {
			results[i].Err = fmt.Errorf("plugin install %s: %w: %s", p.ID(), err, lastLine(out))
			continue
		}
		in.logf("installed %s", p.ID())
	}
	return results
}

func (in *Installer) addMarketplace(ctx context.Context, source string) error {
	out, err := in.Run(ctx, "plugin", "marketplace", "add", source, "--scope", "user", "--json")
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s", err, lastLine(out))
	}
	return nil
}

func (in *Installer) logf(format string, a ...any) {
	if in.Log != nil {
		fmt.Fprintf(in.Log, "harness: "+format+"\n", a...)
	}
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func lastLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// WithTimeout is a helper for callers that cap total install time.
func WithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
