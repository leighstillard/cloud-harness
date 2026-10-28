// Package context renders the `## harness` block printed at session start and
// manages its on-disk cache.
package context

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/leighstillard/cloud-harness/internal/memory"
)

// MaxLines is the hard cap on the rendered block.
const MaxLines = 40

// CacheFile is the cache file name inside Claude's config dir.
const CacheFile = "harness-last.md"

// Block is everything the renderer needs.
type Block struct {
	Repo      string
	SessionID string
	Global    []memory.Memory
	ThisRepo  []memory.Memory
	Beads     []string // pre-formatted lines, already capped
	BDH       string   // one line, or ""
	Note      string   // optional one-line note (e.g. memory unavailable)
}

// Reminder is the closing line of the block.
const Reminder = "Run `harness remember \"<fact>\"` for anything worth keeping; scoped to this repo unless --global."

// Render produces the block, never exceeding MaxLines lines.
func Render(b Block) string {
	var head, tail []string
	head = append(head, "## harness", "repo: "+orDash(b.Repo), "session: "+orDash(b.SessionID))
	if b.Note != "" {
		head = append(head, "note: "+b.Note)
	}
	if len(b.Beads) > 0 {
		tail = append(tail, "### Ready beads")
		tail = append(tail, b.Beads...)
	}
	if b.BDH != "" {
		tail = append(tail, "bdh: "+b.BDH)
	}
	tail = append(tail, Reminder)

	// Budget: each memory section costs 1 header line (+1 "none" line when empty).
	budget := MaxLines - len(head) - len(tail) - 2
	if budget < 2 {
		budget = 2
	}
	repoN, globalN := split(budget, len(b.ThisRepo), len(b.Global))

	var lines []string
	lines = append(lines, head...)
	lines = append(lines, section("### Global", b.Global, globalN)...)
	lines = append(lines, section("### This repo", b.ThisRepo, repoN)...)
	lines = append(lines, tail...)
	if len(lines) > MaxLines {
		lines = append(lines[:MaxLines-1], Reminder)
	}
	return strings.Join(lines, "\n") + "\n"
}

// split shares budget lines between repo (preferred) and global memories.
func split(budget, repo, global int) (repoN, globalN int) {
	half := budget / 2
	repoN = min(repo, max(half, budget-global))
	globalN = min(global, budget-repoN)
	return repoN, globalN
}

func section(title string, ms []memory.Memory, n int) []string {
	out := []string{title}
	if len(ms) == 0 {
		return append(out, "- (none)")
	}
	for i := 0; i < n && i < len(ms); i++ {
		out = append(out, "- "+oneLine(ms[i].Text))
	}
	if rest := len(ms) - n; rest > 0 {
		if n > 0 {
			out[len(out)-1] += fmt.Sprintf(" (+%d more)", rest)
		} else {
			out = append(out, fmt.Sprintf("- (%d more, no room)", rest))
		}
	}
	return out
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const maxLen = 200
	if len(s) > maxLen {
		s = s[:maxLen-1] + "…"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Cache persists the last rendered block.
type Cache struct{ Path string }

// Write stores block text.
func (c Cache) Write(text string) error {
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(c.Path, []byte(text), 0o600)
}

// Read returns the cached block, its age, and whether it exists.
func (c Cache) Read() (text string, age time.Duration, ok bool) {
	st, err := os.Stat(c.Path)
	if err != nil {
		return "", 0, false
	}
	b, err := os.ReadFile(c.Path)
	if err != nil {
		return "", 0, false
	}
	return string(b), time.Since(st.ModTime()), true
}

// MarkCached rewrites the header line of a cached block to say so.
func MarkCached(text string, age time.Duration, reason string) string {
	marker := fmt.Sprintf("## harness (cached %s ago", age.Round(time.Minute))
	if reason != "" {
		marker += "; " + reason
	}
	marker += ")"
	if strings.HasPrefix(text, "## harness") {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			return marker + text[i:]
		}
	}
	return marker + "\n" + text
}

// Fetcher loads memories for a scope.
type Fetcher func(ctx context.Context, scope memory.Scope) ([]memory.Memory, error)

// Load fetches global and repo memories concurrently within timeout. On
// timeout or error it returns err; callers fall back to the cache.
func Load(ctx context.Context, timeout time.Duration, fetch Fetcher, user, repo string) (global, this []memory.Memory, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type res struct {
		ms  []memory.Memory
		err error
	}
	gch, rch := make(chan res, 1), make(chan res, 1)
	go func() {
		ms, err := fetch(ctx, memory.Scope{UserID: user})
		gch <- res{ms, err}
	}()
	go func() {
		ms, err := fetch(ctx, memory.Scope{UserID: user, AgentID: repo})
		rch <- res{ms, err}
	}()
	g, r := <-gch, <-rch
	if g.err != nil {
		return nil, nil, g.err
	}
	if r.err != nil {
		return nil, nil, r.err
	}
	return g.ms, r.ms, nil
}

// Beads returns up to n ready beads as "- id: title" lines when `bd` is on
// PATH and .beads/ exists in dir. Any failure yields nil.
func Beads(ctx context.Context, dir string, n int) []string {
	if _, err := os.Stat(filepath.Join(dir, ".beads")); err != nil {
		return nil
	}
	if _, err := exec.LookPath("bd"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bd", "ready", "--json")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseBeads(out, n)
}

func parseBeads(out []byte, n int) []string {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil
	}
	var items []map[string]any
	if err := json.Unmarshal(out, &items); err != nil {
		// Some versions wrap the list.
		var wrapped map[string]any
		if json.Unmarshal(out, &wrapped) != nil {
			return nil
		}
		for _, k := range []string{"ready", "issues", "beads", "items", "results"} {
			if v, ok := wrapped[k].([]any); ok {
				for _, e := range v {
					if m, ok := e.(map[string]any); ok {
						items = append(items, m)
					}
				}
				break
			}
		}
	}
	var lines []string
	for _, it := range items {
		if len(lines) == n {
			break
		}
		id := str(it["id"])
		title := str(it["title"])
		if title == "" {
			title = str(it["summary"])
		}
		switch {
		case id != "" && title != "":
			lines = append(lines, "- "+id+": "+oneLine(title))
		case id != "":
			lines = append(lines, "- "+id)
		case title != "":
			lines = append(lines, "- "+oneLine(title))
		}
	}
	return lines
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// BDH returns the first line of `bdh status` when bdh is on PATH, else "".
func BDH(ctx context.Context, dir string) string {
	if _, err := exec.LookPath("bdh"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bdh", "status")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return oneLine(s)
}
