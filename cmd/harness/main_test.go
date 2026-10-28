package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
