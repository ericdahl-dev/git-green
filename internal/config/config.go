package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const DefaultPollInterval = 15
const DefaultStuckThresholdMinutes = 30

type Settings struct {
	PollInterval          int `toml:"poll_interval_seconds"`
	StuckThresholdMinutes int `toml:"stuck_threshold_minutes"`
}

type Org struct {
	Name     string `toml:"name"`
	Token    string `toml:"token"`
	TokenEnv string `toml:"token_env"`
}

type Repo struct {
	Owner     string   `toml:"owner"`
	Name      string   `toml:"name"`
	Branch    string   `toml:"branch"`
	Workflows []string `toml:"workflows"`
	Enabled   *bool    `toml:"enabled"`
}

// IsEnabled returns true unless explicitly set to false.
func (r Repo) IsEnabled() bool {
	return r.Enabled == nil || *r.Enabled
}

type Webhook struct {
	URL    string `toml:"url"`
	Secret string `toml:"secret"`
}

type Config struct {
	Settings Settings  `toml:"settings"`
	Orgs     []Org     `toml:"orgs"`
	Repos    []Repo    `toml:"repos"`
	Webhooks []Webhook `toml:"webhooks"`

	resolvedTokens map[string]string
	path           string
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found at %s — create one to get started", path)
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Settings.PollInterval == 0 {
		cfg.Settings.PollInterval = DefaultPollInterval
	}
	if cfg.Settings.PollInterval < 1 {
		return nil, fmt.Errorf("poll_interval_seconds must be at least 1 second")
	}

	if cfg.Settings.StuckThresholdMinutes == 0 {
		cfg.Settings.StuckThresholdMinutes = DefaultStuckThresholdMinutes
	}

	if len(cfg.Repos) == 0 {
		return nil, fmt.Errorf("config must include at least one [[repos]] entry")
	}

	for i, wh := range cfg.Webhooks {
		if wh.URL == "" {
			return nil, fmt.Errorf("webhooks[%d]: url is required", i)
		}
		u, err := url.ParseRequestURI(wh.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("webhooks[%d]: invalid url %q (must be http or https)", i, wh.URL)
		}
	}

	cfg.path = path
	cfg.resolvedTokens = make(map[string]string)
	for _, org := range cfg.Orgs {
		token, err := resolveToken(org)
		if err != nil {
			return nil, err
		}
		cfg.resolvedTokens[org.Name] = token
	}

	return &cfg, nil
}

// Path returns the file path this config was loaded from.
func (c *Config) Path() string { return c.path }

// EnabledRepos returns only repos that are enabled.
func (c *Config) EnabledRepos() []Repo {
	var out []Repo
	for _, r := range c.Repos {
		if r.IsEnabled() {
			out = append(out, r)
		}
	}
	return out
}

// Save writes the config back to the file it was loaded from.
func (c *Config) Save() error {
	if c.path == "" {
		return fmt.Errorf("config has no path set")
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(c.path, buf.Bytes(), 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// AddRepo appends a new repo and saves.
func (c *Config) AddRepo(r Repo) error {
	c.Repos = append(c.Repos, r)
	return c.Save()
}

// UpdateRepo replaces the repo at index i and saves.
func (c *Config) UpdateRepo(i int, r Repo) error {
	if i < 0 || i >= len(c.Repos) {
		return fmt.Errorf("repo index %d out of range", i)
	}
	c.Repos[i] = r
	return c.Save()
}

// RemoveRepo removes the repo at index i and saves.
func (c *Config) RemoveRepo(i int) error {
	if i < 0 || i >= len(c.Repos) {
		return fmt.Errorf("repo index %d out of range", i)
	}
	c.Repos = append(c.Repos[:i], c.Repos[i+1:]...)
	return c.Save()
}

// ToggleRepo flips the enabled state of repo i and saves.
func (c *Config) ToggleRepo(i int) error {
	if i < 0 || i >= len(c.Repos) {
		return fmt.Errorf("repo index %d out of range", i)
	}
	enabled := !c.Repos[i].IsEnabled()
	c.Repos[i].Enabled = &enabled
	return c.Save()
}

func (c *Config) TokenForOrg(owner string) (string, error) {
	if token, ok := c.resolvedTokens[owner]; ok {
		return token, nil
	}
	return ghAuthToken()
}

func resolveToken(org Org) (string, error) {
	if org.Token != "" {
		return org.Token, nil
	}
	if org.TokenEnv != "" {
		val := os.Getenv(org.TokenEnv)
		if val == "" {
			return "", fmt.Errorf("org %q: token_env %q is set but the environment variable is empty or unset", org.Name, org.TokenEnv)
		}
		return val, nil
	}
	return ghAuthToken()
}

func ghAuthToken() (string, error) {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("no token configured and 'gh auth token' failed — run 'gh auth login' or set a token in the config: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

type starterConfig struct {
	Settings Settings `toml:"settings"`
	Repos    []Repo   `toml:"repos"`
}

// WriteStarter writes a minimal valid config with one [[repos]] entry.
func WriteStarter(path, owner, name, branch string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	sc := starterConfig{
		Settings: Settings{
			PollInterval:          DefaultPollInterval,
			StuckThresholdMinutes: DefaultStuckThresholdMinutes,
		},
		Repos: []Repo{{
			Owner:  strings.TrimSpace(owner),
			Name:   strings.TrimSpace(name),
			Branch: strings.TrimSpace(branch),
		}},
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(sc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
