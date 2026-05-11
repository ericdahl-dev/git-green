package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultPollInterval(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Settings.PollInterval != DefaultPollInterval {
		t.Errorf("expected %d, got %d", DefaultPollInterval, cfg.Settings.PollInterval)
	}
}

func TestExplicitPollInterval(t *testing.T) {
	path := writeTempConfig(t, `
[settings]
poll_interval_seconds = 60

[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Settings.PollInterval != 60 {
		t.Errorf("expected 60, got %d", cfg.Settings.PollInterval)
	}
}

func TestInvalidPollInterval(t *testing.T) {
	path := writeTempConfig(t, `
[settings]
poll_interval = 0

[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	// poll_interval_seconds = 0 triggers the default (15), not an error
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Settings.PollInterval != DefaultPollInterval {
		t.Errorf("expected default %d, got %d", DefaultPollInterval, cfg.Settings.PollInterval)
	}
}

func TestMissingConfigFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestNoRepos(t *testing.T) {
	path := writeTempConfig(t, `[settings]`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error when no repos configured")
	}
}

func TestRepoBranchDefaultsToEmpty(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Repos[0].Branch != "" {
		t.Errorf("expected empty branch, got %q", cfg.Repos[0].Branch)
	}
}

func TestRepoWorkflowsDefaultsToNil(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Repos[0].Workflows != nil {
		t.Errorf("expected nil workflows, got %v", cfg.Repos[0].Workflows)
	}
}

func TestExplicitToken(t *testing.T) {
	path := writeTempConfig(t, `
[[orgs]]
name = "ericdahl-dev"
token = "ghp_explicit"

[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok, err := cfg.TokenForOrg("ericdahl-dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_explicit" {
		t.Errorf("expected explicit token, got %q", tok)
	}
}

func TestTokenEnvResolution(t *testing.T) {
	t.Setenv("MY_TEST_TOKEN", "ghp_from_env")
	path := writeTempConfig(t, `
[[orgs]]
name = "ericdahl-dev"
token_env = "MY_TEST_TOKEN"

[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok, err := cfg.TokenForOrg("ericdahl-dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_from_env" {
		t.Errorf("expected env token, got %q", tok)
	}
}

func TestTokenEnvMissing(t *testing.T) {
	_ = os.Unsetenv("MISSING_TOKEN_ENV")
	path := writeTempConfig(t, `
[[orgs]]
name = "ericdahl-dev"
token_env = "MISSING_TOKEN_ENV"

[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing token_env variable")
	}
}
