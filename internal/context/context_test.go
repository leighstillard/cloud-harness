package context

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leighstillard/cloud-harness/internal/memory"
)

func mems(prefix string, n int) []memory.Memory {
	var out []memory.Memory
	for i := 0; i < n; i++ {
		out = append(out, memory.Memory{Text: fmt.Sprintf("%s fact %d\nwith newline", prefix, i)})
	}
	return out
}

func lines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

func TestRenderBasic(t *testing.T) {
	out := Render(Block{Repo: "o/r", SessionID: "s1", Global: mems("g", 2), ThisRepo: mems("r", 1),
		Beads: []string{"- b-1: do it"}, BDH: "2 agents present"})
	ls := lines(out)
	if ls[0] != "## harness" || ls[1] != "repo: o/r" || ls[2] != "session: s1" {
		t.Errorf("header %v", ls[:3])
	}
	for _, want := range []string{"### Global", "- g fact 0 with newline", "### This repo", "- r fact 0 with newline", "### Ready beads", "- b-1: do it", "bdh: 2 agents present", Reminder} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if ls[len(ls)-1] != Reminder {
		t.Error("reminder must be last line")
	}
}

func TestRenderEmptyAndNote(t *testing.T) {
	out := Render(Block{Note: "memory unavailable: boom"})
	if !strings.Contains(out, "repo: -") || !strings.Contains(out, "note: memory unavailable: boom") || strings.Count(out, "- (none)") != 2 {
		t.Errorf("out:\n%s", out)
	}
	if strings.Contains(out, "Ready beads") || strings.Contains(out, "bdh:") {
		t.Error("empty sections should be omitted")
	}
}

func TestRenderNeverExceedsMaxLines(t *testing.T) {
	for _, g := range []int{0, 5, 20, 50} {
		for _, r := range []int{0, 5, 20, 50} {
			out := Render(Block{Repo: "o/r", SessionID: "s", Global: mems("g", g), ThisRepo: mems("r", r),
				Beads: []string{"- 1", "- 2", "- 3", "- 4", "- 5"}, BDH: "x", Note: "n"})
			ls := lines(out)
			if len(ls) > MaxLines {
				t.Errorf("g=%d r=%d: %d lines", g, r, len(ls))
			}
			if ls[len(ls)-1] != Reminder {
				t.Errorf("g=%d r=%d: last line %q", g, r, ls[len(ls)-1])
			}
			if g > 0 && r > 0 && (!strings.Contains(out, "- r fact 0") || !strings.Contains(out, "- g fact 0")) {
				t.Errorf("g=%d r=%d: both sections should show at least one item\n%s", g, r, out)
			}
		}
	}
	out := Render(Block{Global: mems("g", 20), ThisRepo: mems("r", 20)})
	if !strings.Contains(out, "more)") {
		t.Errorf("expected truncation marker:\n%s", out)
	}
}

func TestCacheRoundTripAndMark(t *testing.T) {
	c := Cache{Path: filepath.Join(t.TempDir(), "x", CacheFile)}
	if _, _, ok := c.Read(); ok {
		t.Fatal("unexpected cache")
	}
	text := Render(Block{Repo: "o/r"})
	if err := c.Write(text); err != nil {
		t.Fatal(err)
	}
	got, age, ok := c.Read()
	if !ok || got != text || age > time.Minute {
		t.Errorf("read: ok=%v age=%v eq=%v", ok, age, got == text)
	}
	marked := MarkCached(got, 90*time.Second, "provider timeout")
	if !strings.HasPrefix(marked, "## harness (cached 2m0s ago; provider timeout)\nrepo: o/r") {
		t.Errorf("marked:\n%s", marked)
	}
	if MarkCached("plain", time.Minute, "") != "## harness (cached 1m0s ago)\nplain" {
		t.Error("mark without header")
	}
}

func TestLoadTimeoutAndSuccess(t *testing.T) {
	slow := func(ctx context.Context, s memory.Scope) ([]memory.Memory, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
			return nil, nil
		}
	}
	start := time.Now()
	if _, _, err := Load(context.Background(), 30*time.Millisecond, slow, "u", "o/r"); err == nil {
		t.Error("expected timeout error")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Error("Load did not respect timeout")
	}
	fast := func(ctx context.Context, s memory.Scope) ([]memory.Memory, error) {
		if s.Global() {
			return mems("g", 1), nil
		}
		if s.AgentID != "o/r" {
			return nil, errors.New("wrong repo scope")
		}
		return mems("r", 2), nil
	}
	g, r, err := Load(context.Background(), time.Second, fast, "u", "o/r")
	if err != nil || len(g) != 1 || len(r) != 2 {
		t.Errorf("g=%d r=%d err=%v", len(g), len(r), err)
	}
}

func TestParseBeads(t *testing.T) {
	got := parseBeads([]byte(`[{"id":"b-1","title":"first"},{"id":"b-2"},{"title":"third"},{"id":"b-4","title":"x"}]`), 3)
	if len(got) != 3 || got[0] != "- b-1: first" || got[1] != "- b-2" || got[2] != "- third" {
		t.Errorf("got %v", got)
	}
	got = parseBeads([]byte(`{"ready":[{"id":"w-1","title":"wrapped"}]}`), 5)
	if len(got) != 1 || got[0] != "- w-1: wrapped" {
		t.Errorf("wrapped: %v", got)
	}
	if parseBeads([]byte("not json"), 5) != nil || parseBeads(nil, 5) != nil {
		t.Error("garbage should yield nil")
	}
}

func TestBeadsAbsent(t *testing.T) {
	if got := Beads(context.Background(), t.TempDir(), 5); got != nil {
		t.Errorf("expected nil without .beads: %v", got)
	}
}
