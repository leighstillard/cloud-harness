package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseRemote(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/widgets.git":   "acme/widgets",
		"https://github.com/acme/widgets":       "acme/widgets",
		"git@github.com:acme/widgets.git":       "acme/widgets",
		"ssh://git@github.com/acme/widgets.git": "acme/widgets",
		"https://user@gitlab.com/grp/sub/proj":  "sub/proj",
		"/srv/git/acme/widgets.git":             "acme/widgets",
		"https://github.com/acme/widgets/":      "acme/widgets",
	}
	for in, want := range cases {
		id, ok := ParseRemote(in)
		if !ok || id.String() != want {
			t.Errorf("ParseRemote(%q) = %q, %v; want %q", in, id.String(), ok, want)
		}
	}
	for _, bad := range []string{"", "   ", "widgets"} {
		if id, ok := ParseRemote(bad); ok {
			t.Errorf("ParseRemote(%q) = %+v; want !ok", bad, id)
		}
	}
}

func TestResolveFallsBackToBasename(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myproj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := Resolve(dir)
	if id.String() != "myproj" {
		t.Errorf("got %+v", id)
	}
}

func TestResolveFromRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:acme/widgets.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	id := Resolve(dir)
	if id.String() != "acme/widgets" {
		t.Errorf("got %+v", id)
	}
}
