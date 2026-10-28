// Package repo resolves the identity of the current repository.
package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Identity is owner/name when the origin remote is known, else the cwd basename.
type Identity struct {
	Owner string
	Name  string
	// FromRemote is true when the identity came from `git remote get-url origin`.
	FromRemote bool
}

// String returns "owner/name" or just "name".
func (i Identity) String() string {
	if i.Owner == "" {
		return i.Name
	}
	return i.Owner + "/" + i.Name
}

// Resolve identifies the repository at dir ("" means cwd).
func Resolve(dir string) Identity {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		if id, ok := ParseRemote(strings.TrimSpace(string(out))); ok {
			return id
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return Identity{Name: filepath.Base(abs)}
}

var remoteRe = regexp.MustCompile(`(?i)^(?:(?:https?|ssh|git)://(?:[^@/]+@)?[^/]+/|[^@/]+@[^:]+:|/)?(?:.*/)?([^/]+)/([^/]+?)(?:\.git)?/?$`)

// ParseRemote extracts owner/name from common git remote URL forms:
// https://github.com/o/n.git, git@github.com:o/n.git, ssh://git@host/o/n, /local/path/o/n.
func ParseRemote(url string) (Identity, bool) {
	url = strings.TrimSpace(url)
	if url == "" {
		return Identity{}, false
	}
	m := remoteRe.FindStringSubmatch(url)
	if m == nil || m[1] == "" || m[2] == "" {
		return Identity{}, false
	}
	return Identity{Owner: m[1], Name: m[2], FromRemote: true}, true
}
