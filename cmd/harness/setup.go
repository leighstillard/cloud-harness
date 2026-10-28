package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leighstillard/cloud-harness/cloud"
	"github.com/leighstillard/cloud-harness/internal/plugins"
	"github.com/leighstillard/cloud-harness/internal/settings"
)

const (
	claudeMDBegin = "<!-- cloud-harness:begin -->"
	claudeMDEnd   = "<!-- cloud-harness:end -->"
)

// harnessHooks are merged into ~/.claude/settings.json. They mirror
// plugins/harness/hooks/hooks.json so the hooks fire even when plugin hooks
// are not loaded; `harness boot` de-duplicates a double fire per session.
var harnessHooks = map[string][]settings.Hook{
	"SessionStart": {{Type: "command", Command: "command -v harness >/dev/null 2>&1 && harness boot || true", Timeout: 120}},
	"Stop":         {{Type: "command", Command: "command -v harness >/dev/null 2>&1 && harness remember --auto || true", Timeout: 30}},
	"PreCompact":   {{Type: "command", Command: "command -v harness >/dev/null 2>&1 && harness remember --auto || true", Timeout: 30}},
}

// runSetup implements `harness setup`. It runs in the cloud setup script,
// before the proxy and env vars exist, so it only touches public sources.
func runSetup(args []string) int {
	cfg, err := loadConfig()
	if err != nil {
		warnf("%v", err)
		return 1
	}
	failed := false

	// 1. Plugins first: `claude plugin` rewrites settings.json itself, and the
	//    hook merge below must see the result.
	ctx, cancel := background(4 * time.Minute)
	defer cancel()
	inst := plugins.New(os.Stderr)
	for _, r := range inst.Ensure(ctx, cfg.Plugins.Setup) {
		switch {
		case r.Err != nil:
			warnf("plugin %s: %v", r.Plugin.ID(), r.Err)
			failed = true
		case r.Skipped:
			fmt.Printf("plugin %s: already installed\n", r.Plugin.ID())
		default:
			fmt.Printf("plugin %s: installed\n", r.Plugin.ID())
		}
	}

	// 2. Hooks: merge, never overwrite.
	sp, err := settings.Path()
	if err != nil {
		warnf("settings path: %v", err)
		return 1
	}
	changed, err := settings.MergeFile(sp, harnessHooks)
	if err != nil {
		warnf("merge hooks into %s: %v", sp, err)
		failed = true
	} else if changed {
		fmt.Printf("hooks: merged into %s\n", sp)
	} else {
		fmt.Printf("hooks: already present in %s\n", sp)
	}

	// 3. CLAUDE.md.
	text := cloud.DefaultClaudeMD
	if strings.TrimSpace(cfg.ClaudeMD) != "" {
		text = cfg.ClaudeMD
	}
	mdPath := filepath.Join(claudeDir(), "CLAUDE.md")
	changed, err = writeClaudeMD(mdPath, text)
	if err != nil {
		warnf("write %s: %v", mdPath, err)
		failed = true
	} else if changed {
		fmt.Printf("CLAUDE.md: written to %s\n", mdPath)
	} else {
		fmt.Printf("CLAUDE.md: up to date at %s\n", mdPath)
	}

	if failed {
		return 1
	}
	return 0
}

// writeClaudeMD installs text as the cloud-harness section of path. A file
// without the markers keeps its content and gains the section at the end; a
// file with the markers has only that section replaced.
func writeClaudeMD(path, text string) (changed bool, err error) {
	section := strings.TrimSpace(text)
	if !strings.HasPrefix(section, claudeMDBegin) {
		section = claudeMDBegin + "\n" + section + "\n" + claudeMDEnd
	}
	section += "\n"
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	var out string
	cur := string(existing)
	if i := strings.Index(cur, claudeMDBegin); i >= 0 {
		j := strings.Index(cur[i:], claudeMDEnd)
		if j < 0 {
			out = cur[:i] + section
		} else {
			rest := strings.TrimLeft(cur[i+j+len(claudeMDEnd):], "\n")
			out = cur[:i] + section
			if rest != "" {
				out += rest
			}
		}
	} else if strings.TrimSpace(cur) == "" {
		out = section
	} else {
		out = strings.TrimRight(cur, "\n") + "\n\n" + section
	}
	if out == cur {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(out), 0o644)
}
