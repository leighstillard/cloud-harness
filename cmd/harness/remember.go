package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leighstillard/cloud-harness/internal/memory"
	"github.com/leighstillard/cloud-harness/internal/repo"
)

const (
	rememberPrefix       = "Remember:"
	rememberGlobalPrefix = "Remember global:"
	ledgerMax            = 500
)

// runRemember implements `harness remember "<text>" [--global]` and
// `harness remember --auto`.
func runRemember(args []string) int {
	var (
		global bool
		auto   bool
		parts  []string
	)
	for _, a := range args {
		switch a {
		case "--global", "-g":
			global = true
		case "--auto":
			auto = true
		case "-h", "--help":
			fmt.Fprintln(os.Stderr, `usage: harness remember "<fact>" [--global]
       harness remember --auto   (Stop/PreCompact hook; reads hook JSON on stdin)`)
			return 0
		default:
			parts = append(parts, a)
		}
	}
	if auto {
		return runRememberAuto()
	}
	text := strings.TrimSpace(strings.Join(parts, " "))
	if text == "" {
		warnf("nothing to remember; usage: harness remember \"<fact>\" [--global]")
		return 2
	}
	cfg, err := loadConfig()
	if err != nil {
		warnf("%v", err)
		return 1
	}
	in := readHookInput(200 * time.Millisecond)
	cwd, _ := os.Getwd()
	id := repo.Resolve(cwd)
	scope := memory.Scope{UserID: cfg.UserID}
	if !global {
		scope.AgentID = id.String()
	}
	ctx, cancel := background(15 * time.Second)
	defer cancel()
	prov := newProvider(cfg, 15*time.Second)
	if err := prov.Add(ctx, addRequest(scope, text, sessionID(in), id.String())); err != nil {
		warnf("%v", err)
		return 1
	}
	if global {
		fmt.Println("remembered (global):", text)
	} else {
		fmt.Printf("remembered (%s): %s\n", id.String(), text)
	}
	return 0
}

func addRequest(scope memory.Scope, text, sid, repoName string) memory.AddRequest {
	return memory.AddRequest{
		Scope: scope,
		Text:  text,
		RunID: sid,
		Metadata: map[string]any{
			"source": "harness",
			"repo":   repoName,
			"ts":     time.Now().UTC().Format(time.RFC3339),
		},
	}
}

// runRememberAuto stores only `Remember:` lines from the last assistant
// message. Anything else is ignored and the hook exits 0 silently.
func runRememberAuto() int {
	in := readHookInput(2 * time.Second)
	msg := in.LastAssistantMessage
	if msg == "" && in.TranscriptPath != "" {
		msg = lastAssistantText(in.TranscriptPath)
	}
	facts := extractRemembers(msg)
	if len(facts) == 0 {
		return 0
	}
	cfg, err := loadConfig()
	if err != nil {
		warnf("%v", err)
		return 0
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	id := repo.Resolve(cwd)
	sid := sessionID(in)
	ledger := loadLedger()
	ctx, cancel := background(20 * time.Second)
	defer cancel()
	prov := newProvider(cfg, 10*time.Second)
	stored := 0
	for _, f := range facts {
		key := hash(f.global, id.String(), f.text)
		if ledger.has(key) {
			continue
		}
		scope := memory.Scope{UserID: cfg.UserID}
		if !f.global {
			scope.AgentID = id.String()
		}
		if err := prov.Add(ctx, addRequest(scope, f.text, sid, id.String())); err != nil {
			warnf("auto remember: %v", err)
			continue
		}
		ledger.add(key)
		stored++
	}
	if stored > 0 {
		ledger.save()
		warnf("auto-remembered %d fact(s)", stored)
	}
	return 0
}

type fact struct {
	text   string
	global bool
}

// extractRemembers pulls `Remember:` / `Remember global:` lines out of a message.
func extractRemembers(msg string) []fact {
	var out []fact
	sc := bufio.NewScanner(strings.NewReader(msg))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimLeft(line, "-*> ")
		line = strings.Trim(line, "*_`")
		var f fact
		switch {
		case strings.HasPrefix(line, rememberGlobalPrefix):
			f = fact{text: strings.TrimSpace(line[len(rememberGlobalPrefix):]), global: true}
		case strings.HasPrefix(line, rememberPrefix):
			f = fact{text: strings.TrimSpace(line[len(rememberPrefix):])}
		default:
			continue
		}
		f.text = strings.TrimSpace(strings.Trim(f.text, "*_`"))
		if len(f.text) < 4 || len(f.text) > 1000 {
			continue
		}
		out = append(out, f)
	}
	return out
}

// lastAssistantText reads a Claude Code transcript (JSONL) and returns the
// text of the last assistant message.
func lastAssistantText(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "assistant" {
			continue
		}
		if text := contentText(entry.Message.Content); text != "" {
			last = text
		}
	}
	return last
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// ledger remembers what --auto already stored so Stop and PreCompact do not
// store the same line twice.
type ledger struct {
	path string
	Keys []string `json:"keys"`
}

func ledgerPath() string { return filepath.Join(claudeDir(), "harness-remembered.json") }

func loadLedger() *ledger {
	l := &ledger{path: ledgerPath()}
	if b, err := os.ReadFile(l.path); err == nil {
		_ = json.Unmarshal(b, l)
	}
	return l
}

func (l *ledger) has(key string) bool {
	for _, k := range l.Keys {
		if k == key {
			return true
		}
	}
	return false
}

func (l *ledger) add(key string) {
	l.Keys = append(l.Keys, key)
	if len(l.Keys) > ledgerMax {
		l.Keys = l.Keys[len(l.Keys)-ledgerMax:]
	}
}

func (l *ledger) save() {
	b, err := json.Marshal(l)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(l.path), 0o755)
	if err := os.WriteFile(l.path, b, 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		warnf("ledger: %v", err)
	}
}

func hash(global bool, repoName, text string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%v|%s|%s", global, repoName, text)))
	return hex.EncodeToString(h[:8])
}
