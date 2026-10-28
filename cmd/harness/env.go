package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leighstillard/cloud-harness/internal/config"
	"github.com/leighstillard/cloud-harness/internal/memory"
	"github.com/leighstillard/cloud-harness/internal/settings"
)

// isCloud reports whether this is a Claude Code cloud session.
func isCloud() bool { return os.Getenv("CLAUDE_CODE_REMOTE") == "true" }

// apiKey returns the provider key for local use. In the cloud it always
// returns "": the agent proxy attaches the key as a network secret and the
// binary never sees one.
func apiKey(cfg *config.Config) string {
	if isCloud() {
		return ""
	}
	if v := os.Getenv(cfg.Memory.KeyEnv); v != "" {
		return v
	}
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	return dotenv(filepath.Join(dir, ".env"))[cfg.Memory.KeyEnv]
}

// dotenv parses KEY=value lines; missing file yields an empty map.
func dotenv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

func newProvider(cfg *config.Config, timeout time.Duration) memory.Provider {
	return memory.NewMem0(cfg.Memory.BaseURL, apiKey(cfg), timeout)
}

// hookInput is the subset of hook stdin JSON harness uses.
type hookInput struct {
	SessionID            string `json:"session_id"`
	TranscriptPath       string `json:"transcript_path"`
	CWD                  string `json:"cwd"`
	HookEventName        string `json:"hook_event_name"`
	Source               string `json:"source"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// readHookInput parses hook JSON from stdin when stdin is a pipe or file.
// It never blocks for more than wait.
func readHookInput(wait time.Duration) hookInput {
	var in hookInput
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice != 0 {
		return in
	}
	ch := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
		ch <- b
	}()
	select {
	case b := <-ch:
		_ = json.Unmarshal(b, &in)
	case <-time.After(wait):
	}
	return in
}

// sessionID resolves the session id: cloud env var, hook input, then a random id.
func sessionID(in hookInput) string {
	if v := os.Getenv("CLAUDE_CODE_REMOTE_SESSION_ID"); v != "" {
		return v
	}
	if in.SessionID != "" {
		return in.SessionID
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("local-%d", time.Now().UnixNano())
	}
	return "local-" + hex.EncodeToString(b)
}

func claudeDir() string {
	d, err := settings.Dir()
	if err != nil {
		return ".claude"
	}
	return d
}

func cachePath() string { return filepath.Join(claudeDir(), "harness-last.md") }

func warnf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "harness: "+format+"\n", a...)
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		p, _ := config.Path()
		return nil, fmt.Errorf("%w (expected at %s)", err, p)
	}
	return cfg, nil
}

func background(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
