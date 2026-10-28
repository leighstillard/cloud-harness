// Package settings merges hooks into Claude Code's user settings.json without
// disturbing anything else in the file.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Hook is one command hook entry.
type Hook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// Dir returns Claude Code's config directory: $CLAUDE_CONFIG_DIR or ~/.claude.
func Dir() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// Path returns the settings.json path.
func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "settings.json"), nil
}

// MergeFile merges hooks (event -> hooks to ensure) into the settings file at
// path, creating it if missing. It reports whether the file changed.
func MergeFile(path string, hooks map[string][]Hook) (changed bool, err error) {
	raw, err := os.ReadFile(path)
	existed := err == nil
	switch {
	case errors.Is(err, os.ErrNotExist):
		raw = []byte("{}")
	case err != nil:
		return false, err
	}
	merged, changed, err := Merge(raw, hooks)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if !changed {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".harness.tmp"
	if err := os.WriteFile(tmp, merged, 0o600); err != nil {
		return false, err
	}
	return true, swap(path, tmp, raw, existed)
}

// swap renames tmp over path, but only if path still holds what MergeFile
// read; a live Claude Code session writing settings.json in between would
// otherwise lose its change. The previous content is kept in path.bak.
// ponytail: a re-read, not a lock; flock the file if setup ever runs
// routinely beside a live session.
func swap(path, tmp string, raw []byte, existed bool) error {
	cur, err := os.ReadFile(path)
	if (existed && (err != nil || !bytes.Equal(cur, raw))) || (!existed && err == nil) {
		_ = os.Remove(tmp)
		return fmt.Errorf("%s changed while setup was running; re-run setup", path)
	}
	if existed {
		if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	return os.Rename(tmp, path)
}

// Merge adds each hook under its event in the settings JSON unless a hook with
// the same command is already registered for that event. Every other key, and
// every existing hook, is preserved. Output is pretty-printed with 2 spaces.
func Merge(raw []byte, hooks map[string][]Hook) (out []byte, changed bool, err error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, false, fmt.Errorf("settings.json is not a JSON object: %w", err)
	}
	if root == nil {
		root = map[string]json.RawMessage{}
	}
	events := map[string]json.RawMessage{}
	if h, ok := root["hooks"]; ok && len(bytes.TrimSpace(h)) > 0 && string(bytes.TrimSpace(h)) != "null" {
		if err := json.Unmarshal(h, &events); err != nil {
			return nil, false, fmt.Errorf("settings.json hooks is not an object: %w", err)
		}
	}
	for event, want := range hooks {
		var groups []map[string]json.RawMessage
		if g, ok := events[event]; ok && len(bytes.TrimSpace(g)) > 0 && string(bytes.TrimSpace(g)) != "null" {
			if err := json.Unmarshal(g, &groups); err != nil {
				return nil, false, fmt.Errorf("settings.json hooks.%s is not an array: %w", event, err)
			}
		}
		var missing []Hook
		for _, h := range want {
			if !hasCommand(groups, h.Command) {
				missing = append(missing, h)
			}
		}
		if len(missing) == 0 {
			continue
		}
		groupJSON, err := json.Marshal(map[string]any{"hooks": missing})
		if err != nil {
			return nil, false, err
		}
		groups = append(groups, map[string]json.RawMessage{})
		if err := json.Unmarshal(groupJSON, &groups[len(groups)-1]); err != nil {
			return nil, false, err
		}
		g, err := json.Marshal(groups)
		if err != nil {
			return nil, false, err
		}
		events[event] = g
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	h, err := json.Marshal(events)
	if err != nil {
		return nil, false, err
	}
	root["hooks"] = h
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), true, nil
}

func hasCommand(groups []map[string]json.RawMessage, command string) bool {
	for _, g := range groups {
		var hs []struct {
			Command string `json:"command"`
		}
		if raw, ok := g["hooks"]; ok {
			_ = json.Unmarshal(raw, &hs)
		}
		for _, h := range hs {
			if h.Command == command {
				return true
			}
		}
	}
	return false
}
