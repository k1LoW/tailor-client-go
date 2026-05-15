package tailorclient

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/goccy/go-yaml"
	"github.com/zalando/go-keyring"
)

const keyringServiceName = "tailor-platform-cli"

// SDKConfig represents the Tailor SDK config.yaml (v1/v2 format).
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

// ReadSDKTokens reads access_token, refresh_token, and token_expires_at from the SDK config for the current_user.
// Supports both file-based (v1) and keyring-based (v2) storage.
func ReadSDKTokens() (accessToken, refreshToken, tokenExpiresAt string, err error) {
	cfg, err := readSDKConfig()
	if err != nil {
		return "", "", "", err
	}
	if cfg.CurrentUser == nil || *cfg.CurrentUser == "" {
		return "", "", "", fmt.Errorf("current_user is not set in %s", sdkConfigFilePath())
	}
	currentUser := *cfg.CurrentUser
	user, ok := cfg.Users[currentUser]
	if !ok {
		return "", "", "", fmt.Errorf("user %q not found in %s", currentUser, sdkConfigFilePath())
	}

	if isKeyringStorage(user) {
		at, rt, err := loadKeyringTokens(currentUser)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to read keyring tokens for %q: %w", currentUser, err)
		}
		slog.Info("Using SDK config tokens (keyring)", "user", currentUser)
		return at, rt, user.TokenExpiresAt, nil
	}

	slog.Info("Using SDK config tokens (file)", "user", currentUser, "configPath", sdkConfigFilePath())
	return user.AccessToken, user.RefreshToken, user.TokenExpiresAt, nil
}

// WriteSDKTokens updates the tokens for the current_user.
// Writes to keyring or config file depending on the user's storage mode.
func WriteSDKTokens(accessToken, refreshToken, tokenExpiresAt string) error {
	sdkConfigMu.Lock()
	defer sdkConfigMu.Unlock()

	cfg, err := readSDKConfig()
	if err != nil {
		return err
	}
	if cfg.CurrentUser == nil || *cfg.CurrentUser == "" {
		return fmt.Errorf("current_user is not set")
	}
	currentUser := *cfg.CurrentUser
	user, ok := cfg.Users[currentUser]
	if !ok {
		return fmt.Errorf("user %q not found", currentUser)
	}

	if isKeyringStorage(user) {
		if err := saveKeyringTokens(currentUser, accessToken, refreshToken); err != nil {
			return fmt.Errorf("failed to save keyring tokens for %q: %w", currentUser, err)
		}
		user.TokenExpiresAt = tokenExpiresAt
		slog.Info("SDK keyring tokens updated", "user", currentUser)
	} else {
		user.AccessToken = accessToken
		user.RefreshToken = refreshToken
		user.TokenExpiresAt = tokenExpiresAt
		slog.Info("SDK config tokens updated (file)", "path", sdkConfigFilePath())
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
