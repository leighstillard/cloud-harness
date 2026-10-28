package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leighstillard/cloud-harness/internal/memory"
	"github.com/leighstillard/cloud-harness/internal/repo"
)

const (
	rememberPrefix       = "Remember:"
	rememberGlobalPrefix = "Remember global:"
	ledgerMax            = 500
	globalMax            = 50
	pendingBatchMax      = 4000 // Claude Code collapses hook output over 10,000 chars
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
		unlock := lockLedger(ctx)
		l := loadLedger()
		l.noteGlobal(id.String(), text)
		if err := l.save(); err != nil {
			warnf("ledger: %v", err)
		}
		unlock()
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

// runRememberAuto stores `Remember:` / `Remember global:` lines from the last
// assistant message and tells the user about global stores. Claude Code shows
// a Stop hook's systemMessage; PreCompact discards it, so global stores wait
// in the ledger's Pending list for the next Stop. The whole transaction runs
// under the ledger lock: hooks from other sessions and manual --global calls
// share the file.
func runRememberAuto() int {
	in := readHookInput(2 * time.Second)
	msg := in.LastAssistantMessage
	if msg == "" && in.TranscriptPath != "" {
		msg = lastAssistantText(in.TranscriptPath)
	}
	facts := extractRemembers(msg)
	// One deadline covers lock wait, provider calls and the save: the hook
	// itself is killed at 30 s, which would leave a stored fact unannounced.
	ctx, cancel := background(25 * time.Second)
	defer cancel()
	unlock := lockLedger(ctx)
	defer unlock()
	ledger := loadLedger()
	// A lost claim means the twin hook stores the facts; it never skips the
	// announcement, which may be older than this message.
	if len(facts) > 0 && claim("auto-"+claimKey(sessionID(in), msg), dedupeWindow) {
		ledger.store(ctx, in, facts)
	}
	if in.HookEventName != "PreCompact" {
		if batch := ledger.takePending(); len(batch) > 0 {
			out, _ := json.Marshal(map[string]string{"systemMessage": "harness: stored to global memory:\n- " + strings.Join(batch, "\n- ")})
			fmt.Println(string(out))
			if err := ledger.save(); err != nil {
				warnf("ledger: %v", err)
			}
		}
	}
	return 0
}

// store sends facts not already in the ledger to the provider.
func (l *ledger) store(ctx context.Context, in hookInput, facts []fact) {
	cfg, err := loadConfig()
	if err != nil {
		warnf("%v", err)
		return
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	id := repo.Resolve(cwd)
	prov := newProvider(cfg, 10*time.Second)
	stored := 0
	for _, f := range facts {
		if ctx.Err() != nil {
			warnf("auto remember: out of time, %d fact(s) left for the next hook", len(facts)-stored)
			break
		}
		key := hash(f.global, id.String(), f.text)
		if l.has(key) {
			continue
		}
		scope := memory.Scope{UserID: cfg.UserID}
		if !f.global {
			scope.AgentID = id.String()
		}
		if err := prov.Add(ctx, addRequest(scope, f.text, sessionID(in), id.String())); err != nil {
			warnf("auto remember: %v", err)
			continue
		}
		l.add(key)
		if f.global {
			l.noteGlobal(id.String(), f.text)
		}
		stored++
	}
	if stored > 0 {
		if err := l.save(); err != nil {
			warnf("ledger: %v", err)
		}
		warnf("auto-remembered %d fact(s)", stored)
	}
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
	path    string
	Keys    []string `json:"keys"`
	Global  []string `json:"global,omitempty"`  // recent global stores, for `harness status`
	Pending []string `json:"pending,omitempty"` // global stores not yet shown to the user
}

func (l *ledger) noteGlobal(repoName, text string) {
	if len(text) > 1000 {
		text = text[:1000] + "…"
	}
	l.Global = append(l.Global, time.Now().UTC().Format("2006-01-02 15:04")+" "+repoName+": "+text)
	if len(l.Global) > globalMax {
		l.Global = l.Global[len(l.Global)-globalMax:]
	}
	// ponytail: Pending shares the Global cap; a repo that elicits more than
	// 50 global lines before a Stop loses the oldest announcements, and
	// `harness status` still shows the record.
	l.Pending = append(l.Pending, text)
	if len(l.Pending) > globalMax {
		l.Pending = l.Pending[len(l.Pending)-globalMax:]
	}
}

// takePending removes and returns the pending announcements that fit one
// hook response; the rest wait for the next Stop.
func (l *ledger) takePending() []string {
	var batch []string
	n := 0
	for len(l.Pending) > 0 && n+len(l.Pending[0])+3 <= pendingBatchMax {
		n += len(l.Pending[0]) + 3
		batch = append(batch, l.Pending[0])
		l.Pending = l.Pending[1:]
	}
	return batch
}

// lockLedger serialises ledger transactions across processes, waiting at
// most until ctx's deadline. A lock that cannot be taken degrades to no lock
// rather than disabling the hook.
func lockLedger(ctx context.Context) func() {
	_ = os.MkdirAll(claudeDir(), 0o755)
	f, err := os.OpenFile(ledgerPath()+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
		}
		if err != syscall.EWOULDBLOCK || ctx.Err() != nil {
			warnf("ledger lock: %v; proceeding without it", err)
			f.Close()
			return func() {}
		}
		time.Sleep(50 * time.Millisecond)
	}
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

// save writes the ledger atomically (temp file + rename).
func (l *ledger) save() error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(l.path), 0o755)
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

func hash(global bool, repoName, text string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%v|%s|%s", global, repoName, text)))
	return hex.EncodeToString(h[:8])
}
