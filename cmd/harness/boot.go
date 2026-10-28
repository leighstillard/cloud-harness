package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leighstillard/cloud-harness/internal/config"
	hctx "github.com/leighstillard/cloud-harness/internal/context"
	"github.com/leighstillard/cloud-harness/internal/memory"
	"github.com/leighstillard/cloud-harness/internal/plugins"
	"github.com/leighstillard/cloud-harness/internal/repo"
)

const (
	providerTimeout = 2 * time.Second
	sessionInstall  = 60 * time.Second
	dedupeWindow    = 60 * time.Second
)

// runBoot implements `harness boot`. It always exits 0: a session must never
// be blocked by memory.
func runBoot(args []string) int {
	in := readHookInput(500 * time.Millisecond)
	sid := sessionID(in)
	if !claim("boot-"+claimKey(sid), dedupeWindow) {
		return 0
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	id := repo.Resolve(cwd)
	block := hctx.Block{Repo: id.String(), SessionID: sid}
	cache := hctx.Cache{Path: cachePath()}

	cfg, err := loadConfig()
	if err != nil {
		block.Note = "no config: " + err.Error()
		block.Beads = hctx.Beads(context.Background(), cwd, 5)
		block.BDH = hctx.BDH(context.Background(), cwd)
		fmt.Print(hctx.Render(block))
		return 0
	}

	// 0. Session plugins (private sources are reachable now that the proxy is
	//    up). Setup plugins are included so a failed setup script self-heals;
	//    anything already installed is skipped.
	if want := append(append([]config.Plugin{}, cfg.Plugins.Setup...), cfg.Plugins.Session...); isCloud() && len(want) > 0 {
		ctx, cancel := background(sessionInstall)
		inst := plugins.New(os.Stderr)
		var installed []string
		for _, r := range inst.Ensure(ctx, want) {
			switch {
			case r.Err != nil:
				warnf("session plugin %s: %v", r.Plugin.ID(), r.Err)
			case !r.Skipped:
				installed = append(installed, r.Plugin.ID())
			}
		}
		cancel()
		if len(installed) > 0 {
			// Plugins are discovered before SessionStart hooks run, so a plugin
			// installed here is only usable after a reload.
			block.Note = "installed " + strings.Join(installed, ", ") + "; run /reload-plugins to use them in this session"
		}
	}

	// 2. Memory, with the cache as fallback.
	prov := newProvider(cfg, providerTimeout+time.Second)
	fetch := func(ctx context.Context, s memory.Scope) ([]memory.Memory, error) {
		return prov.Search(ctx, memory.Query{Scope: s, Limit: 20})
	}
	global, this, err := hctx.Load(context.Background(), providerTimeout, fetch, cfg.UserID, id.String())

	// 3. Local tools.
	block.Beads = hctx.Beads(context.Background(), cwd, 5)
	block.BDH = hctx.BDH(context.Background(), cwd)

	// 4. Print.
	if err != nil {
		reason := "memory unavailable"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "provider timeout"
		} else if errors.Is(err, memory.ErrUnauthorized) {
			reason = "memory unauthorized"
		}
		warnf("%s: %v", reason, err)
		// ponytail: one cache file for all repos, so a miss here just means
		// "another repo booted last"; per-repo cache files if that bites.
		if text, age, ok := cache.Read(); ok && strings.Contains(text, "\nrepo: "+id.String()+"\n") {
			fmt.Print(hctx.MarkCached(text, age, reason))
			return 0
		}
		block.Note = reason + "; no cache for this repo"
		fmt.Print(hctx.Render(block))
		return 0
	}
	block.Global, block.ThisRepo = global, this
	text := hctx.Render(block)
	fmt.Print(text)
	if err := cache.Write(text); err != nil {
		warnf("cache: %v", err)
	}
	return 0
}
