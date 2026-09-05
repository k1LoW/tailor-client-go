package tailorclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const sampleSDKConfig = `version: 1
current_user: alice
users:
  alice:
    access_token: my-at
    refresh_token: my-rt
    token_expires_at: 2099-12-31T00:00:00Z
`

const v3DevSDKConfig = `version: 3
min_sdk_version: 2.0.0
current_user: alice
users:
  https://api.dev.tailor.tech|alice:
    storage: file
    access_token: dev-at
    refresh_token: dev-rt
    token_expires_at: 2099-12-31T00:00:00Z
    email: alice@example.com
  bob:
    storage: file
    access_token: bob-at
    token_expires_at: 2099-12-31T00:00:00Z
    email: bob@example.com
profiles: {}
`

const v3MultiPlatformSDKConfig = `version: 3
current_user: alice
users:
  alice:
    storage: file
    access_token: prod-at
    token_expires_at: 2099-12-31T00:00:00Z
  https://api.dev.tailor.tech|alice:
    storage: file
    access_token: dev-at
    token_expires_at: 2099-12-31T00:00:00Z
`

func writeSDKConfigForTest(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "tailor-platform")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSDKConfigFilePath_xdgConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	got := sdkConfigFilePath()
	if want := "/tmp/xdg/tailor-platform/config.yaml"; got != want {
		t.Errorf("sdkConfigFilePath() = %q, want %q", got, want)
	}
}

func TestReadSDKTokens_fileMode(t *testing.T) {
	writeSDKConfigForTest(t, sampleSDKConfig)

	tokens, err := ReadSDKTokens("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.AccessToken != "my-at" {
		t.Errorf("access token = %q, want %q", tokens.AccessToken, "my-at")
	}
	if tokens.RefreshToken != "my-rt" {
		t.Errorf("refresh token = %q, want %q", tokens.RefreshToken, "my-rt")
	}
	if want := "2099-12-31T00:00:00Z"; tokens.TokenExpiresAt != want {
		t.Errorf("token_expires_at = %q, want %q", tokens.TokenExpiresAt, want)
	}
	if tokens.PlatformURL != DefaultPlatformURL {
		t.Errorf("platform URL = %q, want %q", tokens.PlatformURL, DefaultPlatformURL)
	}
	if tokens.UserKey != "alice" {
		t.Errorf("user key = %q, want %q", tokens.UserKey, "alice")
	}
}

// A v3 config namespaces every non-production login as "<platformURL>|<user>"
// while current_user keeps the bare ID, so the entry has to be found through
// the composite key rather than by indexing the map with current_user.
func TestReadSDKTokens_v3PlatformScopedKey(t *testing.T) {
	writeSDKConfigForTest(t, v3DevSDKConfig)

	tokens, err := ReadSDKTokens("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.AccessToken != "dev-at" || tokens.RefreshToken != "dev-rt" {
		t.Errorf("got at=%q rt=%q, want dev-at/dev-rt", tokens.AccessToken, tokens.RefreshToken)
	}
	if want := "https://api.dev.tailor.tech"; tokens.PlatformURL != want {
		t.Errorf("platform URL = %q, want %q", tokens.PlatformURL, want)
	}
	if want := "https://api.dev.tailor.tech|alice"; tokens.UserKey != want {
		t.Errorf("user key = %q, want %q", tokens.UserKey, want)
	}
}

// An explicit platform selects among the current_user's entries.
func TestReadSDKTokens_explicitPlatformSelectsEntry(t *testing.T) {
	writeSDKConfigForTest(t, v3MultiPlatformSDKConfig)

	tokens, err := ReadSDKTokens("https://api.dev.tailor.tech")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.AccessToken != "dev-at" {
		t.Errorf("access token = %q, want %q", tokens.AccessToken, "dev-at")
	}

	tokens, err = ReadSDKTokens(DefaultPlatformURL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.AccessToken != "prod-at" {
		t.Errorf("access token = %q, want %q", tokens.AccessToken, "prod-at")
	}
	if tokens.UserKey != "alice" {
		t.Errorf("user key = %q, want %q", tokens.UserKey, "alice")
	}
}

// A bare production entry is unambiguous even when other platforms are present,
// so inference must not report a conflict for it.
func TestReadSDKTokens_inferencePrefersDefaultPlatform(t *testing.T) {
	writeSDKConfigForTest(t, v3MultiPlatformSDKConfig)

	tokens, err := ReadSDKTokens("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.PlatformURL != DefaultPlatformURL {
		t.Errorf("platform URL = %q, want %q", tokens.PlatformURL, DefaultPlatformURL)
	}
	if tokens.AccessToken != "prod-at" {
		t.Errorf("access token = %q, want %q", tokens.AccessToken, "prod-at")
	}
}

// Without a production entry to break the tie, two non-default platforms cannot
// be resolved without the caller naming one.
func TestReadSDKTokens_ambiguousPlatformRejected(t *testing.T) {
	writeSDKConfigForTest(t, `version: 3
current_user: alice
users:
  https://api.dev.tailor.tech|alice:
    storage: file
    access_token: dev-at
    token_expires_at: 2099-12-31T00:00:00Z
  https://api.staging.example.com|alice:
    storage: file
    access_token: staging-at
    token_expires_at: 2099-12-31T00:00:00Z
`)

	_, err := ReadSDKTokens("")
	if err == nil {
		t.Fatal("expected error when current_user is registered for multiple platforms")
	}
}

// A pre-v3 config keyed by the bare user ID stays readable when a platform is
// named explicitly.
func TestReadSDKTokens_legacyKeyFallback(t *testing.T) {
	writeSDKConfigForTest(t, sampleSDKConfig)

	tokens, err := ReadSDKTokens("https://api.dev.tailor.tech")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.UserKey != "alice" {
		t.Errorf("user key = %q, want %q", tokens.UserKey, "alice")
	}
	if want := "https://api.dev.tailor.tech"; tokens.PlatformURL != want {
		t.Errorf("platform URL = %q, want %q", tokens.PlatformURL, want)
	}
}

func TestReadSDKTokens_missingCurrentUser(t *testing.T) {
	writeSDKConfigForTest(t, `version: 1
users:
  alice:
    access_token: my-at
`)
	_, err := ReadSDKTokens("")
	if err == nil {
		t.Error("expected error for missing current_user")
	}
}

func TestReadSDKTokens_unknownCurrentUser(t *testing.T) {
	writeSDKConfigForTest(t, `version: 1
current_user: bob
users:
  alice:
    access_token: my-at
`)
	_, err := ReadSDKTokens("")
	if err == nil {
		t.Error("expected error for unknown current_user")
	}
}

func TestReadSDKTokens_missingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	_, err := ReadSDKTokens("")
	if err == nil {
		t.Error("expected error when config file is missing")
	}
}

func TestSDKUserKey(t *testing.T) {
	tests := []struct {
		name        string
		user        string
		platformURL string
		want        string
	}{
		{"default platform keeps the bare ID", "alice", DefaultPlatformURL, "alice"},
		{"empty platform keeps the bare ID", "alice", "", "alice"},
		{"trailing slash still counts as default", "alice", DefaultPlatformURL + "/", "alice"},
		{"dev platform is namespaced", "alice", "https://api.dev.tailor.tech", "https://api.dev.tailor.tech|alice"},
		{"trailing slash is normalized before namespacing", "alice", "https://api.dev.tailor.tech/", "https://api.dev.tailor.tech|alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sdkUserKey(tt.user, tt.platformURL); got != tt.want {
				t.Errorf("sdkUserKey(%q, %q) = %q, want %q", tt.user, tt.platformURL, got, tt.want)
			}
		})
	}
}

func TestWriteSDKTokens_roundTrip(t *testing.T) {
	writeSDKConfigForTest(t, `version: 1
current_user: alice
users:
  alice:
    access_token: old-at
    refresh_token: old-rt
    token_expires_at: 2020-01-01T00:00:00Z
`)

	if err := WriteSDKTokens("alice", "new-at", "new-rt", "2099-12-31T00:00:00Z"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tokens, err := ReadSDKTokens("")
	if err != nil {
		t.Fatalf("read after write: %v", err)
	}
	if tokens.AccessToken != "new-at" || tokens.RefreshToken != "new-rt" || tokens.TokenExpiresAt != "2099-12-31T00:00:00Z" {
		t.Errorf("got at=%q rt=%q exp=%q", tokens.AccessToken, tokens.RefreshToken, tokens.TokenExpiresAt)
	}
}

// A writeback updates the platform-scoped entry in place and leaves every other
// entry, and the v3 fields the SDK depends on, untouched.
func TestWriteSDKTokens_v3PreservesConfig(t *testing.T) {
	writeSDKConfigForTest(t, v3DevSDKConfig)

	tokens, err := ReadSDKTokens("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := WriteSDKTokens(tokens.UserKey, "new-at", "new-rt", "2099-12-31T00:00:00Z"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := readSDKConfig()
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if cfg.Version != 3 {
		t.Errorf("version = %d, want 3", cfg.Version)
	}
	if len(cfg.Users) != 2 {
		t.Errorf("users = %d, want 2", len(cfg.Users))
	}
	updated := cfg.Users["https://api.dev.tailor.tech|alice"]
	if updated == nil {
		t.Fatal("dev user entry is missing after writeback")
	}
	if updated.AccessToken != "new-at" || updated.RefreshToken != "new-rt" {
		t.Errorf("got at=%q rt=%q, want new-at/new-rt", updated.AccessToken, updated.RefreshToken)
	}
	if updated.Email != "alice@example.com" {
		t.Errorf("email = %q, want %q", updated.Email, "alice@example.com")
	}
	if other := cfg.Users["bob"]; other == nil || other.AccessToken != "bob-at" {
		t.Error("unrelated user entry was modified by the writeback")
	}
}

func TestWriteSDKTokens_unknownUserKey(t *testing.T) {
	writeSDKConfigForTest(t, v3DevSDKConfig)

	if err := WriteSDKTokens("alice", "new-at", "new-rt", "2099-12-31T00:00:00Z"); err == nil {
		t.Error("expected error when writing to a key that does not exist")
	}
}

func TestNew_readsSDKConfig(t *testing.T) {
	writeSDKConfigForTest(t, sampleSDKConfig)

	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.OperatorServiceClient == nil {
		t.Error("OperatorServiceClient is nil")
	}
	if c.PlatformURL() != DefaultPlatformURL {
		t.Errorf("platform URL = %q, want %q", c.PlatformURL(), DefaultPlatformURL)
	}
}

// A caller that names no platform must end up on the one the SDK recorded at
// login, otherwise a dev token would be refreshed against production.
func TestNew_infersPlatformFromSDKConfig(t *testing.T) {
	writeSDKConfigForTest(t, v3DevSDKConfig)

	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://api.dev.tailor.tech"; c.PlatformURL() != want {
		t.Errorf("platform URL = %q, want %q", c.PlatformURL(), want)
	}
}

func TestNew_platformURLOptionOverridesInference(t *testing.T) {
	writeSDKConfigForTest(t, v3MultiPlatformSDKConfig)

	c, err := New(context.Background(), WithPlatformURL("https://api.dev.tailor.tech"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://api.dev.tailor.tech"; c.PlatformURL() != want {
		t.Errorf("platform URL = %q, want %q", c.PlatformURL(), want)
	}
}

func TestNew_platformURLEnvOverridesInference(t *testing.T) {
	writeSDKConfigForTest(t, v3MultiPlatformSDKConfig)
	t.Setenv("TAILOR_PLATFORM_URL", "https://api.dev.tailor.tech")

	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://api.dev.tailor.tech"; c.PlatformURL() != want {
		t.Errorf("platform URL = %q, want %q", c.PlatformURL(), want)
	}
}
