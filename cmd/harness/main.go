// Command harness bootstraps Claude Code cloud sessions.
package main

import (
	"fmt"
	"os"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "setup":
		os.Exit(runSetup(args))
	case "boot":
		os.Exit(runBoot(args))
	case "remember":
		os.Exit(runRemember(args))
	case "status":
		os.Exit(runStatus(args))
	case "version", "--version", "-v":
		fmt.Println("harness", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "harness: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: harness <command>

  setup      install pinned plugins, merge hooks into ~/.claude/settings.json, write ~/.claude/CLAUDE.md
  boot       print the memory block for this session (SessionStart hook)
  remember   store a memory: harness remember "<fact>" [--global]; --auto reads hook JSON on stdin
  status     show mode, key presence, provider reachability, cache age, plugin versions
  version    print the version
`)
}
