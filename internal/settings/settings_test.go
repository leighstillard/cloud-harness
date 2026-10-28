package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var want = map[string][]Hook{
	"SessionStart": {{Type: "command", Command: "harness boot", Timeout: 120}},
	"Stop":         {{Type: "command", Command: "harness remember --auto", Timeout: 30}},
}

const existing = `{
  "permissions": {"allow": ["Bash(npm test)"], "deny": []},
  "model": "opus",
  "enabledPlugins": {"x@y": true},
  "hooks": {
    "SessionStart": [
      {"matcher": "startup", "hooks": [{"type": "command", "command": "echo hi"}]}
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "./check.sh", "timeout": 5}]}
    ]
  },
  "unknownFutureKey": {"deep": [1, 2, {"three": null}]}
}`

func TestMergePreservesEverything(t *testing.T) {
	out, changed, err := Merge([]byte(existing), want)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change")
	}
	var before, after map[string]any
	if err := json.Unmarshal([]byte(existing), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &after); err != nil {
		t.Fatal(err)
	}
	for k := range before {
		if k == "hooks" {
			continue
		}
		if !jsonEqual(before[k], after[k]) {
			t.Errorf("key %q changed: %v -> %v", k, before[k], after[k])
		}
	}
	hooks := after["hooks"].(map[string]any)
	if !jsonEqual(before["hooks"].(map[string]any)["PreToolUse"], hooks["PreToolUse"]) {
		t.Error("PreToolUse hooks changed")
	}
	ss := hooks["SessionStart"].([]any)
	if len(ss) != 2 {
		t.Fatalf("SessionStart groups = %d, want 2", len(ss))
	}
	if !jsonEqual(before["hooks"].(map[string]any)["SessionStart"].([]any)[0], ss[0]) {
		t.Error("existing SessionStart group changed")
	}
	added := ss[1].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if added["command"] != "harness boot" || added["timeout"].(float64) != 120 || added["type"] != "command" {
		t.Errorf("added hook = %v", added)
	}
	stop := hooks["Stop"].([]any)
	if len(stop) != 1 || stop[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"] != "harness remember --auto" {
		t.Errorf("Stop = %v", stop)
	}
	if !strings.HasPrefix(string(out), "{\n  ") {
		t.Error("output not pretty-printed")
	}
}

func TestMergeIdempotent(t *testing.T) {
	once, changed, err := Merge([]byte(existing), want)
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	twice, changed, err := Merge(once, want)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second merge reported a change")
	}
	if string(twice) != string(once) {
		t.Error("second merge altered bytes")
	}
}

func TestMergeEmptyAndMissingHooks(t *testing.T) {
	for _, in := range []string{"", "{}", `{"hooks": null}`, `{"hooks": {"SessionStart": null}}`} {
		out, changed, err := Merge([]byte(in), want)
		if err != nil || !changed {
			t.Fatalf("%q: %v %v", in, err, changed)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		if len(m["hooks"].(map[string]any)) != 2 {
			t.Errorf("%q -> %s", in, out)
		}
	}
	if _, _, err := Merge([]byte(`[]`), want); err == nil {
		t.Error("expected error for non-object")
	}
	if _, _, err := Merge([]byte(`{"hooks": {"Stop": {}}}`), want); err == nil {
		t.Error("expected error for non-array event")
	}
}

func TestMergeFileCreatesAndRewrites(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "settings.json")
	changed, err := MergeFile(p, want)
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	if err := os.WriteFile(p, []byte(`{"model":"x","hooks":{"Stop":[{"hooks":[{"type":"command","command":"harness remember --auto"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err = MergeFile(p, want)
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	changed, err = MergeFile(p, want)
	if err != nil || changed {
		t.Fatal("expected no change", err, changed)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"model": "x"`) || strings.Count(string(b), "harness remember --auto") != 1 {
		t.Errorf("file = %s", b)
	}
	// Exactly the file and its .bak: no temp file left behind.
	if entries, _ := os.ReadDir(filepath.Dir(p)); len(entries) != 2 {
		t.Errorf("unexpected files: %v", entries)
	}
	if bak, _ := os.ReadFile(p + ".bak"); !strings.HasPrefix(string(bak), `{"model":"x","hooks"`) {
		t.Errorf(".bak should hold the pre-merge file byte for byte: %s", bak)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestSwapRefusesConcurrentChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	orig := []byte(`{"a":1}`)
	if err := os.WriteFile(path, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := path + ".harness.tmp"
	if err := os.WriteFile(tmp, []byte(`{"a":1,"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Someone else wrote settings.json between read and rename.
	if err := os.WriteFile(path, []byte(`{"a":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := swap(path, tmp, orig, true); err == nil {
		t.Fatal("swap should refuse when the file changed underneath")
	}
	if got, _ := os.ReadFile(path); string(got) != `{"a":2}` {
		t.Fatalf("other writer's change lost: %s", got)
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Fatal("tmp file left behind")
	}
	// Unchanged: swap succeeds and keeps the old content in .bak.
	if err := os.WriteFile(tmp, []byte(`{"a":2,"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := swap(path, tmp, []byte(`{"a":2}`), true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path + ".bak"); string(got) != `{"a":2}` {
		t.Fatalf(".bak = %s", got)
	}
	if got, _ := os.ReadFile(path); string(got) != `{"a":2,"hooks":{}}` {
		t.Fatalf("settings = %s", got)
	}
}
