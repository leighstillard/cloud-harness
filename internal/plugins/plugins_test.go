package plugins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/leighstillard/cloud-harness/internal/config"
)

type fakeCLI struct {
	mu        sync.Mutex
	calls     []string
	installed []string
	failAdd   map[string]int // source -> remaining failures
}

func (f *fakeCLI) run(ctx context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.Join(args, " ")
	f.calls = append(f.calls, line)
	switch {
	case line == "plugin list --json":
		var parts []string
		for _, id := range f.installed {
			parts = append(parts, `{"id":"`+id+`","version":"0.1.0","scope":"user","enabled":true}`)
		}
		return []byte("[" + strings.Join(parts, ",") + "]\n"), nil
	case strings.HasPrefix(line, "plugin marketplace add "):
		src := args[3]
		if n := f.failAdd[src]; n > 0 {
			f.failAdd[src] = n - 1
			return []byte("boom\nclone failed"), errors.New("exit 1")
		}
		return []byte(`{"outcome":"ok"}`), nil
	case strings.HasPrefix(line, "plugin install "):
		f.installed = append(f.installed, args[2])
		return []byte(`{"outcome":"ok"}`), nil
	}
	return nil, errors.New("unexpected: " + line)
}

func plugin(name, mkt, src string) config.Plugin {
	return config.Plugin{Name: name, Marketplace: mkt, Source: src, Version: "0.1.0"}
}

func TestEnsureSkipsInstalledAndInstallsMissing(t *testing.T) {
	cli := &fakeCLI{installed: []string{"a@ma"}}
	in := &Installer{Run: cli.run}
	res := in.Ensure(context.Background(), []config.Plugin{plugin("a", "ma", "o/a"), plugin("b", "mb", "o/b")})
	if !res[0].Skipped || res[0].Err != nil {
		t.Errorf("a: %+v", res[0])
	}
	if res[1].Skipped || res[1].Err != nil {
		t.Errorf("b: %+v", res[1])
	}
	joined := strings.Join(cli.calls, "\n")
	if strings.Contains(joined, "marketplace add o/a") {
		t.Error("should not add marketplace for installed plugin")
	}
	if !strings.Contains(joined, "plugin marketplace add o/b --scope user --json") || !strings.Contains(joined, "plugin install b@mb --scope user -y --json") {
		t.Errorf("calls:\n%s", joined)
	}
	// Second run: nothing to do.
	cli.calls = nil
	res = in.Ensure(context.Background(), []config.Plugin{plugin("a", "ma", "o/a"), plugin("b", "mb", "o/b")})
	if !res[0].Skipped || !res[1].Skipped || len(cli.calls) != 1 {
		t.Errorf("second run: %+v calls=%v", res, cli.calls)
	}
}

func TestEnsureRetriesMarketplaceOnce(t *testing.T) {
	cli := &fakeCLI{failAdd: map[string]int{"o/b": 1, "o/c": 5}}
	in := &Installer{Run: cli.run}
	res := in.Ensure(context.Background(), []config.Plugin{plugin("b", "mb", "o/b"), plugin("c", "mc", "o/c")})
	if res[0].Err != nil {
		t.Errorf("b should succeed after retry: %v", res[0].Err)
	}
	if res[1].Err == nil || !strings.Contains(res[1].Err.Error(), "clone failed") {
		t.Errorf("c should fail with last line: %v", res[1].Err)
	}
	adds := 0
	for _, c := range cli.calls {
		if strings.Contains(c, "marketplace add o/c") {
			adds++
		}
	}
	if adds != 2 {
		t.Errorf("o/c add attempts = %d, want 2", adds)
	}
}

func TestEnsureEmpty(t *testing.T) {
	cli := &fakeCLI{}
	in := &Installer{Run: cli.run}
	if res := in.Ensure(context.Background(), nil); len(res) != 0 || len(cli.calls) != 0 {
		t.Errorf("expected no work: %v %v", res, cli.calls)
	}
}
