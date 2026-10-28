package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriteClaudeMD(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude", "CLAUDE.md")
	changed, err := writeClaudeMD(p, "# harness\n- rule one\n")
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), claudeMDBegin+"\n# harness\n- rule one\n"+claudeMDEnd+"\n") {
		t.Errorf("created:\n%s", b)
	}
	// Idempotent.
	changed, err = writeClaudeMD(p, "# harness\n- rule one\n")
	if err != nil || changed {
		t.Error("expected no change", err, changed)
	}
	// Replace only the marked section; keep user content on both sides.
	if err := os.WriteFile(p, []byte("# mine\nkeep me\n\n"+claudeMDBegin+"\nold\n"+claudeMDEnd+"\n\n# after\nalso keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err = writeClaudeMD(p, "new text")
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	b, _ = os.ReadFile(p)
	s := string(b)
	if !strings.Contains(s, "# mine\nkeep me") || !strings.Contains(s, "# after\nalso keep") || strings.Contains(s, "old") || !strings.Contains(s, claudeMDBegin+"\nnew text\n"+claudeMDEnd) {
		t.Errorf("replaced:\n%s", s)
	}
	// Append to a file without markers.
	if err := os.WriteFile(p, []byte("# personal\n- style\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeClaudeMD(p, "section"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if string(b) != "# personal\n- style\n\n"+claudeMDBegin+"\nsection\n"+claudeMDEnd+"\n" {
		t.Errorf("appended:\n%s", b)
	}
}

func TestExtractRemembers(t *testing.T) {
	msg := `Done with the change.

- Remember: use make test before pushing.
**Remember:** the db needs docker compose up first
Remember global: prefer tabs in Go.
Remember: no
remember: lowercase is ignored
Some text that mentions Remember: mid-sentence is fine too? no, must start the line.
`
	got := extractRemembers(msg)
	want := []fact{
		{text: "use make test before pushing."},
		{text: "the db needs docker compose up first"},
		{text: "prefer tabs in Go.", global: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v want %+v", i, got[i], want[i])
		}
	}
	if extractRemembers("") != nil {
		t.Error("empty")
	}
}

func TestLastAssistantText(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}`,
		`not json`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"x"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Remember: last one"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result"}]}}`,
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lastAssistantText(p); got != "Remember: last one" {
		t.Errorf("got %q", got)
	}
	if lastAssistantText(filepath.Join(t.TempDir(), "missing")) != "" {
		t.Error("missing file should yield empty")
	}
}

func TestDotenvAndSessionID(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte("# c\nexport A=1\nB=\"two words\"\nC='x'\nbad line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := dotenv(p)
	if m["A"] != "1" || m["B"] != "two words" || m["C"] != "x" || len(m) != 3 {
		t.Errorf("dotenv = %v", m)
	}
	t.Setenv("CLAUDE_CODE_REMOTE_SESSION_ID", "")
	if got := sessionID(hookInput{SessionID: "from-hook"}); got != "from-hook" {
		t.Errorf("got %q", got)
	}
	if got := sessionID(hookInput{}); !strings.HasPrefix(got, "local-") || len(got) < 20 {
		t.Errorf("got %q", got)
	}
	t.Setenv("CLAUDE_CODE_REMOTE_SESSION_ID", "cse_1")
	if got := sessionID(hookInput{SessionID: "from-hook"}); got != "cse_1" {
		t.Errorf("env should win: %q", got)
	}
}

func TestClaim(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if !claim("boot-a", time.Minute) {
		t.Fatal("first claim should win")
	}
	if claim("boot-a", time.Minute) {
		t.Fatal("second claim should lose")
	}
	if !claim("boot-b", time.Minute) {
		t.Fatal("other name should win")
	}
	// After the window passes (resumed session) the name can be taken again.
	if !claim("boot-c", 50*time.Millisecond) {
		t.Fatal("boot-c first")
	}
	time.Sleep(60 * time.Millisecond)
	if !claim("boot-c", 50*time.Millisecond) {
		t.Fatal("claim should be free again after the window")
	}
	// Parallel hooks, including ones that straddle a bucket boundary: one winner.
	for round := 0; round < 20; round++ {
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		name := fmt.Sprintf("boot-par-%d", round)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if claim(name, 5*time.Millisecond) {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if wins != 1 {
			t.Errorf("parallel claims round %d: %d winners", round, wins)
		}
	}
	// Storage failure is not a duplicate: the hook still runs.
	t.Setenv("CLAUDE_CONFIG_DIR", "/proc/no-such-dir")
	if !claim("boot-a", time.Minute) {
		t.Fatal("unwritable claim dir should not suppress the hook")
	}
	if claimKey("a", "b") == claimKey("ab") || len(claimKey("x")) != 16 {
		t.Error("claimKey")
	}
}

func TestLedger(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	l := loadLedger()
	if l.has("k") {
		t.Fatal("fresh ledger has key")
	}
	l.add("k")
	l.save()
	if !loadLedger().has("k") {
		t.Error("ledger did not persist")
	}
	for i := 0; i < ledgerMax+10; i++ {
		l.add("x")
	}
	if len(l.Keys) != ledgerMax {
		t.Errorf("ledger not capped: %d", len(l.Keys))
	}
}

func TestLedgerGlobalNote(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	l := loadLedger()
	for i := 0; i < globalMax+5; i++ {
		l.noteGlobal("acme/widgets", fmt.Sprintf("fact %d", i))
	}
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	l = loadLedger()
	if len(l.Global) != globalMax || !strings.HasSuffix(l.Global[len(l.Global)-1], "acme/widgets: fact 54") {
		t.Fatalf("global = %d entries, last %q", len(l.Global), l.Global[len(l.Global)-1])
	}
	if len(l.Pending) != globalMax {
		t.Fatalf("pending = %d", len(l.Pending))
	}
	// One hook response carries at most pendingBatchMax bytes; the rest waits.
	l = &ledger{}
	for i := 0; i < 20; i++ {
		l.noteGlobal("r", strings.Repeat("x", 300))
	}
	batch := l.takePending()
	if len(batch) != 13 || len(l.Pending) != 7 {
		t.Fatalf("batch = %d, left = %d", len(batch), len(l.Pending))
	}
	if second := l.takePending(); len(second) != 7 || len(l.Pending) != 0 {
		t.Fatalf("second batch = %d, left = %d", len(second), len(l.Pending))
	}
}

func TestLedgerLockSerialisesWriters(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			unlock := lockLedger()
			defer unlock()
			l := loadLedger()
			l.noteGlobal("r", fmt.Sprintf("fact %d", i))
			if err := l.save(); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if l := loadLedger(); len(l.Pending) != 16 {
		t.Fatalf("lost writes: pending = %d", len(l.Pending))
	}
}
