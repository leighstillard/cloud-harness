// Package config loads ~/.config/cloud-harness/config.yml.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Plugin is one pinned plugin entry.
type Plugin struct {
	Name        string `yaml:"name"`        // plugin name inside the marketplace
	Marketplace string `yaml:"marketplace"` // marketplace name (as declared in its marketplace.json)
	Source      string `yaml:"source"`      // owner/repo, git URL, or local path handed to `claude plugin marketplace add`
	Version     string `yaml:"version"`     // informational pin; recorded by status, not enforced by the CLI
}

// ID is the plugin@marketplace identifier the claude CLI uses.
func (p Plugin) ID() string {
	if p.Marketplace == "" {
		return p.Name
	}
	return p.Name + "@" + p.Marketplace
}

// Memory configures the memory provider.
type Memory struct {
	Provider string `yaml:"provider"`
	BaseURL  string `yaml:"base_url"`
	KeyEnv   string `yaml:"key_env"`
}

// Plugins splits pinned plugins by when they are installed.
type Plugins struct {
	Setup   []Plugin `yaml:"setup"`   // installed by `harness setup` (pre-proxy: public sources only)
	Session []Plugin `yaml:"session"` // installed by `harness boot` once the GitHub proxy is up
}

// Config is the parsed config.yml.
type Config struct {
	UserID   string  `yaml:"user_id"`
	Memory   Memory  `yaml:"memory"`
	Plugins  Plugins `yaml:"plugins"`
	ClaudeMD string  `yaml:"claude_md"` // optional override of cloud/CLAUDE.md
}

// Defaults applied when a field is absent.
const (
	DefaultProvider = "mem0"
	DefaultBaseURL  = "https://api.mem0.ai"
	DefaultKeyEnv   = "MEM0_API_KEY"
)

// Dir returns the config directory, honouring $CLOUD_HARNESS_CONFIG_DIR for tests.
func Dir() (string, error) {
	if d := os.Getenv("CLOUD_HARNESS_CONFIG_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cloud-harness"), nil
}

// Path returns the config file path.
func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.yml"), nil
}

// Load reads and validates the config file at Path().
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return LoadFile(p)
}

// LoadFile reads and validates a config file.
func LoadFile(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(b)
}

// Parse parses YAML bytes, applies defaults and validates.
func Parse(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Memory.Provider == "" {
		c.Memory.Provider = DefaultProvider
	}
	if c.Memory.BaseURL == "" {
		c.Memory.BaseURL = DefaultBaseURL
	}
	if c.Memory.KeyEnv == "" {
		c.Memory.KeyEnv = DefaultKeyEnv
	}
	c.Memory.BaseURL = strings.TrimRight(c.Memory.BaseURL, "/")
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.UserID) == "" {
		return errors.New("config: user_id is required")
	}
	if c.Memory.Provider != "mem0" {
		return fmt.Errorf("config: unsupported memory.provider %q (supported: mem0)", c.Memory.Provider)
	}
	if !strings.HasPrefix(c.Memory.BaseURL, "https://") && !strings.HasPrefix(c.Memory.BaseURL, "http://") {
		return fmt.Errorf("config: memory.base_url must be an http(s) URL, got %q", c.Memory.BaseURL)
	}
	for i, p := range c.Plugins.Setup {
		if err := p.validate(); err != nil {
			return fmt.Errorf("config: plugins.setup[%d]: %w", i, err)
		}
	}
	for i, p := range c.Plugins.Session {
		if err := p.validate(); err != nil {
			return fmt.Errorf("config: plugins.session[%d]: %w", i, err)
		}
	}
	return nil
}

func (p Plugin) validate() error {
	if p.Name == "" {
		return errors.New("name is required")
	}
	if p.Marketplace == "" {
		return errors.New("marketplace is required")
	}
	if p.Source == "" {
		return errors.New("source is required")
	}
	return nil
}
