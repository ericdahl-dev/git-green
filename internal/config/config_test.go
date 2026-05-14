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

func TestDefaultStuckThreshold(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Settings.StuckThresholdMinutes != DefaultStuckThresholdMinutes {
		t.Errorf("expected %d, got %d", DefaultStuckThresholdMinutes, cfg.Settings.StuckThresholdMinutes)
	}
}

func TestExplicitStuckThreshold(t *testing.T) {
	path := writeTempConfig(t, `
[settings]
stuck_threshold_minutes = 60

[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Settings.StuckThresholdMinutes != 60 {
		t.Errorf("expected 60, got %d", cfg.Settings.StuckThresholdMinutes)
	}
}

func TestWebhookValidURL(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "ericdahl-dev"
name = "git-green"

[[webhooks]]
url = "https://hooks.example.com/git-green"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Webhooks) != 1 || cfg.Webhooks[0].URL != "https://hooks.example.com/git-green" {
		t.Errorf("unexpected webhooks: %+v", cfg.Webhooks)
	}
}

func TestWebhookInvalidURL(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "ericdahl-dev"
name = "git-green"

[[webhooks]]
url = "not-a-url"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid webhook url")
	}
}

func TestWebhookEmptyURL(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "ericdahl-dev"
name = "git-green"

[[webhooks]]
url = ""
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty webhook url")
	}
}

func TestWebhookWithSecret(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "ericdahl-dev"
name = "git-green"

[[webhooks]]
url = "https://hooks.example.com/git-green"
secret = "mysecret"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Webhooks[0].Secret != "mysecret" {
		t.Errorf("expected secret, got %q", cfg.Webhooks[0].Secret)
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

func TestWriteStarterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "git-green", "config.toml")
	if err := WriteStarter(path, "acme", "widgets", "develop"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("repos: got %d", len(cfg.Repos))
	}
	r := cfg.Repos[0]
	if r.Owner != "acme" || r.Name != "widgets" || r.Branch != "develop" {
		t.Fatalf("repo: %+v", r)
	}
	if cfg.Settings.PollInterval != DefaultPollInterval {
		t.Errorf("poll interval: %d", cfg.Settings.PollInterval)
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

func TestIsEnabled(t *testing.T) {
	f := false
	tr := true
	if !(Repo{}.IsEnabled()) {
		t.Error("nil Enabled should be enabled")
	}
	if !(Repo{Enabled: &tr}.IsEnabled()) {
		t.Error("Enabled=true should be enabled")
	}
	if (Repo{Enabled: &f}.IsEnabled()) {
		t.Error("Enabled=false should be disabled")
	}
}

func TestEnabledRepos(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "a"
name = "enabled"

[[repos]]
owner = "b"
name = "disabled"
enabled = false
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.EnabledRepos()
	if len(got) != 1 || got[0].Name != "enabled" {
		t.Errorf("expected 1 enabled repo, got %+v", got)
	}
}

func TestToggleRepo(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "a"
name = "r"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ToggleRepo(0); err != nil {
		t.Fatal(err)
	}
	if cfg.Repos[0].IsEnabled() {
		t.Error("expected disabled after first toggle")
	}
	if err := cfg.ToggleRepo(0); err != nil {
		t.Fatal(err)
	}
	if !cfg.Repos[0].IsEnabled() {
		t.Error("expected enabled after second toggle")
	}
}

func TestAddAndRemoveRepo(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "a"
name = "first"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddRepo(Repo{Owner: "b", Name: "second"}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(cfg.Repos))
	}
	if err := cfg.RemoveRepo(0); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Name != "second" {
		t.Errorf("unexpected repos after remove: %+v", cfg.Repos)
	}
}

func TestUpdateRepo(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "a"
name = "old"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateRepo(0, Repo{Owner: "a", Name: "new", Branch: "develop"}); err != nil {
		t.Fatal(err)
	}
	if cfg.Repos[0].Name != "new" || cfg.Repos[0].Branch != "develop" {
		t.Errorf("unexpected repo after update: %+v", cfg.Repos[0])
	}
}

func TestSavePersists(t *testing.T) {
	path := writeTempConfig(t, `
[[repos]]
owner = "a"
name = "r"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = cfg.ToggleRepo(0)

	cfg2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Repos[0].IsEnabled() {
		t.Error("toggled state not persisted to disk")
	}
}
