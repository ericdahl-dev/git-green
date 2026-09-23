package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// HasRepo reports whether owner/name is already configured, ignoring case
// (GitHub names are case-insensitive) and skipping index skip (-1 for none).
func (c *Config) HasRepo(owner, name string, skip int) bool {
	for i, r := range c.Repos {
		if i != skip && strings.EqualFold(r.Owner, owner) && strings.EqualFold(r.Name, name) {
			return true
		}
	}
	return false
}

func duplicateErr(r Repo) error {
	return fmt.Errorf("%s/%s is already configured", r.Owner, r.Name)
}

// AddRepo appends a new repo and saves. It rejects a repo already configured.
func (c *Config) AddRepo(r Repo) error {
	if c.HasRepo(r.Owner, r.Name, -1) {
		return duplicateErr(r)
	}
	c.Repos = append(c.Repos, r)
	return c.Save()
}

// UpdateRepo replaces the repo at index i and saves.
func (c *Config) UpdateRepo(i int, r Repo) error {
	if i < 0 || i >= len(c.Repos) {
		return fmt.Errorf("repo index %d out of range", i)
	}
	if c.HasRepo(r.Owner, r.Name, i) {
		return duplicateErr(r)
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

var repoSegment = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ParseRepoRef extracts owner and name from "owner/name" or any GitHub URL
// pointing at a repo: https, ssh, git@ form, with or without ".git" and with
// any trailing path such as /pull/42 or /tree/main.
func ParseRepoRef(s string) (owner, name string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("enter owner/name or a GitHub URL")
	}

	path := s
	if rest, ok := strings.CutPrefix(s, "git@"); ok {
		host, p, found := strings.Cut(rest, ":")
		if !found || !isGitHubHost(host) {
			return "", "", fmt.Errorf("not a GitHub repo: %s", s)
		}
		path = p
	} else if strings.Contains(s, "://") || strings.HasPrefix(s, "github.com/") || strings.HasPrefix(s, "www.github.com/") {
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		u, perr := url.Parse(s)
		if perr != nil || !isGitHubHost(u.Hostname()) {
			return "", "", fmt.Errorf("not a GitHub repo URL: %s", s)
		}
		path = u.Path
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("expected owner/name, got %q", strings.TrimSpace(path))
	}
	owner, name = parts[0], strings.TrimSuffix(parts[1], ".git")
	if !repoSegment.MatchString(owner) || !repoSegment.MatchString(name) {
		return "", "", fmt.Errorf("invalid owner/name: %s/%s", owner, name)
	}
	return owner, name, nil
}

func isGitHubHost(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || host == "www.github.com"
}
