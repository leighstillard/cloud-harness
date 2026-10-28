package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/leighstillard/cloud-harness/internal/config"
	hctx "github.com/leighstillard/cloud-harness/internal/context"
	"github.com/leighstillard/cloud-harness/internal/memory"
	"github.com/leighstillard/cloud-harness/internal/plugins"
	"github.com/leighstillard/cloud-harness/internal/settings"
)

// runStatus implements `harness status`.
func runStatus(args []string) int {
	mode := "local"
	if isCloud() {
		mode = "cloud"
	}
	fmt.Printf("mode:      %s\n", mode)
	fmt.Printf("version:   %s\n", version)
	cp, _ := config.Path()
	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("config:    %s (error: %v)\n", cp, err)
		return 1
	}
	fmt.Printf("config:    %s (user_id=%s)\n", cp, cfg.UserID)
	fmt.Printf("provider:  %s %s\n", cfg.Memory.Provider, cfg.Memory.BaseURL)

	switch {
	case isCloud():
		fmt.Printf("key:       proxy-expected (network secret for %s; %s is never read in the cloud)\n", hostOf(cfg.Memory.BaseURL), cfg.Memory.KeyEnv)
	case apiKey(cfg) != "":
		src := "env " + cfg.Memory.KeyEnv
		if os.Getenv(cfg.Memory.KeyEnv) == "" {
			d, _ := config.Dir()
			src = d + "/.env"
		}
		fmt.Printf("key:       present (%s)\n", src)
	default:
		d, _ := config.Dir()
		fmt.Printf("key:       missing (set %s in %s/.env)\n", cfg.Memory.KeyEnv, d)
	}

	pingCtx, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPing()
	start := time.Now()
	err = newProvider(cfg, 5*time.Second).Ping(pingCtx)
	switch {
	case err == nil:
		fmt.Printf("reach:     ok (%s)\n", time.Since(start).Round(time.Millisecond))
	case errors.Is(err, memory.ErrUnauthorized):
		fmt.Printf("reach:     unauthorized (%v)\n", err)
	default:
		fmt.Printf("reach:     failed (%v)\n", err)
	}

	cache := hctx.Cache{Path: cachePath()}
	if _, age, ok := cache.Read(); ok {
		fmt.Printf("cache:     %s (%s old)\n", cache.Path, age.Round(time.Second))
	} else {
		fmt.Printf("cache:     none at %s\n", cache.Path)
	}

	sp, _ := settings.Path()
	fmt.Printf("settings:  %s\n", sp)

	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Println("plugins:   claude CLI not on PATH")
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	have, err := plugins.New(nil).List(ctx)
	if err != nil {
		fmt.Printf("plugins:   %v\n", err)
		return 0
	}
	printPlugins := func(label string, want []config.Plugin) {
		for _, p := range want {
			got, ok := have[p.ID()]
			switch {
			case !ok:
				fmt.Printf("plugin:    %-28s %s  missing (pinned %s)\n", p.ID(), label, p.Version)
			case got.Version != p.Version && p.Version != "":
				fmt.Printf("plugin:    %-28s %s  installed %s (pinned %s)\n", p.ID(), label, got.Version, p.Version)
			default:
				fmt.Printf("plugin:    %-28s %s  installed %s\n", p.ID(), label, got.Version)
			}
		}
	}
	printPlugins("setup  ", cfg.Plugins.Setup)
	printPlugins("session", cfg.Plugins.Session)
	return 0
}

func hostOf(url string) string {
	for _, p := range []string{"https://", "http://"} {
		if len(url) > len(p) && url[:len(p)] == p {
			url = url[len(p):]
		}
	}
	for i, c := range url {
		if c == '/' || c == ':' {
			return url[:i]
		}
	}
	return url
}
