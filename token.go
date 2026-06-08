package tailorclient

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultAuthURL = "https://api.tailor.tech/oauth2/platform"
	prodClientID   = "cpoc_6X8NTyohCX1PMRilxSsmJ9CVh8ZNmH5B"
	devClientID    = "cpoc_PttbVewKJUdpYXDEFVFQOjSDcQS3Cyo3"
	devPlatformURL = "https://api.dev.tailor.tech"
)

// TokenResponse is the response from the OAuth2 token endpoint.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error,omitempty"`
}

// RefreshAccessToken exchanges a refresh_token for a new access_token.
func RefreshAccessToken(platformURL, refreshToken string) (*TokenResponse, error) {
	authURL := resolveAuthURL(platformURL)
	clientID := resolveClientID(platformURL)
	tokenEndpoint := authURL + "/token"

	slog.Info("Refreshing access token", "endpoint", tokenEndpoint, "clientId", clientID, "refreshTokenPrefix", truncate(refreshToken, 10))

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)

	resp, err := http.Post(tokenEndpoint, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec // Token endpoint URL is constructed from known platform URL
	if err != nil {
		return nil, fmt.Errorf("refresh token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}
	slog.Info("Refresh response", "status", resp.StatusCode, "body", truncate(string(body), 200))

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("refresh token failed: %s", tr.Error)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("refresh token returned empty access_token")
	}
	return &tr, nil
}

// FetchClientCredentialsToken obtains an access token via the OAuth2
// client_credentials grant using a platform machine user's clientID and
// clientSecret.
//
// Unlike RefreshAccessToken, the response does not carry a refresh_token —
// machine user tokens are short-lived and re-fetched with the same
// credentials when they expire or are rejected.
func FetchClientCredentialsToken(platformURL, clientID, clientSecret string) (*TokenResponse, error) { //nostyle:repetition
	tokenEndpoint := resolveAuthURL(platformURL) + "/token"

	slog.Info("Fetching client_credentials access token", "endpoint", tokenEndpoint, "clientId", clientID)

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)

	resp, err := http.Post(tokenEndpoint, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec // Token endpoint URL is constructed from known platform URL
	if err != nil {
		return nil, fmt.Errorf("client_credentials request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read client_credentials response: %w", err)
	}

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decode client_credentials response: %w", err)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("client_credentials grant failed: %s", tr.Error)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("client_credentials grant returned empty access_token (HTTP %d)", resp.StatusCode)
	}
	return &tr, nil
}

// IsTokenExpired checks if a token_expires_at string indicates an expired token.
func IsTokenExpired(expiresAt string) bool {
	if expiresAt == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05-07:00", expiresAt)
		if err != nil {
			return true
		}
	}
	return time.Now().After(t)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func resolveAuthURL(platformURL string) string {
	if platformURL == "" {
		return defaultAuthURL
	}
	return platformURL + "/oauth2/platform"
}

func resolveClientID(platformURL string) string {
	if platformURL == devPlatformURL {
		return devClientID
	}
	return prodClientID
}
