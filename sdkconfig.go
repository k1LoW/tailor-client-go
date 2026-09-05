package tailorclient

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"
	"github.com/zalando/go-keyring"
)

const keyringServiceName = "tailor-platform-cli"

// SDKConfig represents the Tailor SDK config.yaml (v1, v2 and v3 formats).
type SDKConfig struct {
	Version             int                       `yaml:"version"`
	MinSDKVersion       string                    `yaml:"min_sdk_version,omitempty"`
	LatestVersion       *int                      `yaml:"latest_version,omitempty"`
	LatestMinSDKVersion string                    `yaml:"latest_min_sdk_version,omitempty"`
	Users               map[string]*SDKUserTokens `yaml:"users"`
	Profiles            yaml.MapSlice             `yaml:"profiles,omitempty"`
	CurrentUser         *string                   `yaml:"current_user"`
}

type SDKUserTokens struct {
	AccessToken    string  `yaml:"access_token,omitempty"`
	RefreshToken   string  `yaml:"refresh_token,omitempty"`
	TokenExpiresAt string  `yaml:"token_expires_at"`
	Storage        *string `yaml:"storage,omitempty"`
	// Email is carried so that a writeback does not strip the field the v3
	// SDK relies on to match a user across platforms.
	Email string `yaml:"email,omitempty"`
}

// SDKTokens holds the credentials resolved from the Tailor SDK config for one
// user, together with the platform they were issued for.
type SDKTokens struct {
	AccessToken    string
	RefreshToken   string
	TokenExpiresAt string
	// PlatformURL is the platform the tokens belong to, either the one the
	// caller asked for or the one recovered from the config user key.
	PlatformURL string
	// UserKey is the key the entry lives under in the config users map. It is
	// also the OS keyring account name, and the handle WriteSDKTokens takes.
	UserKey string
}

type keyringTokenData struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

var sdkConfigMu sync.Mutex

func sdkConfigFilePath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "tailor-platform", "config.yaml")
	}
	home, _ := os.UserHomeDir() //nostyle:handlerrors
	return filepath.Join(home, ".config", "tailor-platform", "config.yaml")
}

func isKeyringStorage(user *SDKUserTokens) bool {
	return user.Storage != nil && *user.Storage == "keyring"
}

// sdkUserKey mirrors the SDK's platformUserKey: the default platform keeps the
// bare user ID so pre-v3 configs stay readable, while every other platform is
// namespaced so a dev or self-hosted login cannot collide with a production one.
func sdkUserKey(user, platformURL string) string {
	p := normalizePlatformURL(platformURL)
	if p == "" || p == DefaultPlatformURL {
		return user
	}
	return p + "|" + user
}

// findSDKUser locates the config entry for user and reports which platform it
// belongs to.
//
// With platformURL set, the key is derived the way the SDK derives it, falling
// back to the bare key that pre-v3 SDKs wrote. With platformURL empty the
// platform is recovered from the key that carries the entry, so a caller that
// never names a platform still talks to the one the user actually logged into.
func findSDKUser(cfg *SDKConfig, user, platformURL string) (userKey string, entry *SDKUserTokens, resolved string, err error) {
	if platformURL != "" {
		platformURL = normalizePlatformURL(platformURL)
		key := sdkUserKey(user, platformURL)
		if e := cfg.Users[key]; e != nil {
			return key, e, platformURL, nil
		}
		if key != user {
			if e := cfg.Users[user]; e != nil {
				return user, e, platformURL, nil
			}
		}
		return "", nil, "", fmt.Errorf("user %q not found for platform %s in %s (looked up key %q)", user, platformURL, sdkConfigFilePath(), key)
	}

	if e := cfg.Users[user]; e != nil {
		return user, e, DefaultPlatformURL, nil
	}

	suffix := "|" + user
	var matched []string
	for k, e := range cfg.Users {
		if e != nil && strings.HasSuffix(k, suffix) {
			matched = append(matched, k)
		}
	}
	sort.Strings(matched)
	switch len(matched) {
	case 0:
		return "", nil, "", fmt.Errorf("user %q not found in %s", user, sdkConfigFilePath())
	case 1:
		k := matched[0]
		return k, cfg.Users[k], strings.TrimSuffix(k, suffix), nil
	default:
		urls := make([]string, 0, len(matched))
		for _, k := range matched {
			urls = append(urls, strings.TrimSuffix(k, suffix))
		}
		return "", nil, "", fmt.Errorf("user %q is registered for multiple platforms in %s (%s); select one with WithPlatformURL", user, sdkConfigFilePath(), strings.Join(urls, ", "))
	}
}

// ReadSDKTokens reads the current_user's credentials from the SDK config.
//
// platformURL selects which of the current_user's platform entries to read;
// pass "" to let the platform be inferred from the config. Both file-based
// (v1) and keyring-based (v2, v3) storage are supported.
func ReadSDKTokens(platformURL string) (*SDKTokens, error) {
	cfg, err := readSDKConfig()
	if err != nil {
		return nil, err
	}
	if cfg.CurrentUser == nil || *cfg.CurrentUser == "" {
		return nil, fmt.Errorf("current_user is not set in %s", sdkConfigFilePath())
	}
	currentUser := *cfg.CurrentUser

	userKey, user, resolved, err := findSDKUser(cfg, currentUser, platformURL)
	if err != nil {
		return nil, err
	}

	tokens := &SDKTokens{
		TokenExpiresAt: user.TokenExpiresAt,
		PlatformURL:    resolved,
		UserKey:        userKey,
	}
	if isKeyringStorage(user) {
		at, rt, err := loadKeyringTokens(userKey)
		if err != nil {
			return nil, fmt.Errorf("failed to read keyring tokens for %q: %w", userKey, err)
		}
		tokens.AccessToken, tokens.RefreshToken = at, rt
		slog.Info("Using SDK config tokens (keyring)", "user", userKey, "platformURL", resolved)
		return tokens, nil
	}

	tokens.AccessToken, tokens.RefreshToken = user.AccessToken, user.RefreshToken
	slog.Info("Using SDK config tokens (file)", "user", userKey, "platformURL", resolved, "configPath", sdkConfigFilePath())
	return tokens, nil
}

// WriteSDKTokens updates the tokens stored under userKey, which is the value
// SDKTokens.UserKey carries. Taking the resolved key rather than re-deriving
// one keeps a writeback on a legacy bare-key entry from silently creating a
// second, platform-scoped entry beside it.
//
// Tokens go to the keyring or the config file depending on the entry's storage
// mode.
func WriteSDKTokens(userKey, accessToken, refreshToken, tokenExpiresAt string) error {
	sdkConfigMu.Lock()
	defer sdkConfigMu.Unlock()

	if userKey == "" {
		return fmt.Errorf("user key is empty")
	}
	cfg, err := readSDKConfig()
	if err != nil {
		return err
	}
	user, ok := cfg.Users[userKey]
	if !ok || user == nil {
		return fmt.Errorf("user %q not found in %s", userKey, sdkConfigFilePath())
	}

	if isKeyringStorage(user) {
		if err := saveKeyringTokens(userKey, accessToken, refreshToken); err != nil {
			return fmt.Errorf("failed to save keyring tokens for %q: %w", userKey, err)
		}
		user.TokenExpiresAt = tokenExpiresAt
		slog.Info("SDK keyring tokens updated", "user", userKey)
	} else {
		user.AccessToken = accessToken
		user.RefreshToken = refreshToken
		user.TokenExpiresAt = tokenExpiresAt
		slog.Info("SDK config tokens updated (file)", "user", userKey, "path", sdkConfigFilePath())
	}

	return writeSDKConfig(cfg)
}

func loadKeyringTokens(account string) (accessToken, refreshToken string, err error) {
	raw, err := keyring.Get(keyringServiceName, account)
	if err != nil {
		return "", "", fmt.Errorf("keyring get: %w (service=%q, account=%q)", err, keyringServiceName, account)
	}
	var data keyringTokenData
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return "", "", fmt.Errorf("keyring data parse: %w", err)
	}
	return data.AccessToken, data.RefreshToken, nil
}

func saveKeyringTokens(account, accessToken, refreshToken string) error {
	data := keyringTokenData{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}
	b, err := json.Marshal(data) //nolint:gosec // Token data is intentionally serialized for keyring storage
	if err != nil {
		return err
	}
	return keyring.Set(keyringServiceName, account, string(b))
}

func readSDKConfig() (*SDKConfig, error) {
	path := sdkConfigFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read SDK config %s: %w", path, err)
	}
	var cfg SDKConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse SDK config: %w", err)
	}
	return &cfg, nil
}

func writeSDKConfig(cfg *SDKConfig) error {
	path := sdkConfigFilePath()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal SDK config: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write SDK config %s: %w", path, err)
	}
	return nil
}
