package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const full = `
user_id: alice
memory:
  provider: mem0
  base_url: https://api.mem0.ai/
  key_env: MEM0_API_KEY
plugins:
  setup:
    - { name: harness, marketplace: cloud-harness, source: OWNER/cloud-harness, version: 0.1.0 }
  session:
    - { name: spec-butler, marketplace: spec-butler, source: OWNER/spec-butler, version: 0.1.0 }
claude_md: |
  custom text
`

func TestParseFull(t *testing.T) {
	c, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID != "alice" {
		t.Errorf("user_id = %q", c.UserID)
	}
	if c.Memory.BaseURL != "https://api.mem0.ai" {
		t.Errorf("base_url not trimmed: %q", c.Memory.BaseURL)
	}
	if len(c.Plugins.Setup) != 1 || c.Plugins.Setup[0].ID() != "harness@cloud-harness" {
		t.Errorf("setup plugins = %+v", c.Plugins.Setup)
	}
	if len(c.Plugins.Session) != 1 || c.Plugins.Session[0].Source != "OWNER/spec-butler" {
		t.Errorf("session plugins = %+v", c.Plugins.Session)
	}
	if strings.TrimSpace(c.ClaudeMD) != "custom text" {
		t.Errorf("claude_md = %q", c.ClaudeMD)
	}
}

func TestDefaults(t *testing.T) {
	c, err := Parse([]byte("user_id: bob\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Memory.Provider != "mem0" || c.Memory.BaseURL != "https://api.mem0.ai" || c.Memory.KeyEnv != "MEM0_API_KEY" {
		t.Errorf("defaults not applied: %+v", c.Memory)
	}
	if len(c.Plugins.Setup) != 0 || len(c.Plugins.Session) != 0 {
		t.Errorf("expected no plugins")
	}
}

func TestErrors(t *testing.T) {
	cases := map[string]string{
		"missing user":   "memory:\n  provider: mem0\n",
		"bad provider":   "user_id: a\nmemory:\n  provider: pinecone\n",
		"bad url":        "user_id: a\nmemory:\n  base_url: api.mem0.ai\n",
		"unknown field":  "user_id: a\nmemmory: {}\n",
		"plugin no name": "user_id: a\nplugins:\n  setup:\n    - { marketplace: m, source: s }\n",
		"plugin no mkt":  "user_id: a\nplugins:\n  session:\n    - { name: n, source: s }\n",
		"plugin no src":  "user_id: a\nplugins:\n  session:\n    - { name: n, marketplace: m }\n",
		"empty document": "",
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadFromDirEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLOUD_HARNESS_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("user_id: carol\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID != "carol" {
		t.Errorf("user_id = %q", c.UserID)
	}
	if _, err := LoadFile(filepath.Join(dir, "nope.yml")); err == nil {
		t.Error("expected error for missing file")
	}
}
