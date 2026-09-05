package tailorclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
)

// DefaultOAuth2ClientID is the public OAuth2 client_id the Tailor SDK uses
// for the refresh_token grant. The SDK ships a single client_id for every
// platform and lets the environment override it, so this package must not
// invent per-platform IDs of its own.
const DefaultOAuth2ClientID = "cpoc_0Iudir72fqSpqC6GQ58ri1cLAqcq5vJl" //nostyle:repetition

const defaultAuthURL = DefaultPlatformURL + "/oauth2/platform"

// TokenResponse is the response from the OAuth2 token endpoint.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error,omitempty"`
}

// RefreshAccessToken exchanges a refresh_token for a new access_token.
//
// oauth2ClientID selects the OAuth2 client the grant is made against; pass ""
// to fall back to the environment and then to DefaultOAuth2ClientID, the same
// way the SDK resolves it.
//
// ctx is propagated to the underlying HTTP request so callers can apply
// deadlines and cancellation to the token round-trip. httpClient overrides
// the HTTP transport (e.g. for custom CAs or proxies on self-hosted
// platforms); pass nil to use http.DefaultClient.
func RefreshAccessToken(ctx context.Context, httpClient connect.HTTPClient, platformURL, oauth2ClientID, refreshToken string) (*TokenResponse, error) {
	clientID := ResolveOAuth2ClientID(oauth2ClientID)
	tokenEndpoint := resolveAuthURL(platformURL) + "/token"

	slog.Info("Refreshing access token", "endpoint", tokenEndpoint, "clientId", clientID)

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)

	resp, err := postForm(ctx, httpClient, tokenEndpoint, form)
	if err != nil {
		return nil, fmt.Errorf("refresh token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}
	slog.Info("Refresh response", "status", resp.StatusCode)

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
// ctx is propagated to the underlying HTTP request so callers can apply
// deadlines and cancellation to the token round-trip. httpClient overrides
// the HTTP transport (e.g. for custom CAs or proxies on self-hosted
// platforms); pass nil to use http.DefaultClient.
//
// Unlike RefreshAccessToken, the response does not carry a refresh_token —
// machine user tokens are short-lived and re-fetched with the same
// credentials when they expire or are rejected.
func FetchClientCredentialsToken(ctx context.Context, httpClient connect.HTTPClient, platformURL, clientID, clientSecret string) (*TokenResponse, error) { //nostyle:repetition
	tokenEndpoint := resolveAuthURL(platformURL) + "/token"

	slog.Info("Fetching client_credentials access token", "endpoint", tokenEndpoint, "clientId", clientID)

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)

	resp, err := postForm(ctx, httpClient, tokenEndpoint, form)
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

// postForm issues a context-aware application/x-www-form-urlencoded POST,
// using httpClient (or http.DefaultClient when nil). Sharing the caller's
// HTTP client matters for self-hosted platforms that need a custom CA
// bundle, proxy, or transport timeout configured via WithHTTPClient.
//
// The gosec suppression is intentional: tokenEndpoint is built from a
// caller-supplied platformURL (config-level trust, not external untrusted
// input), so flagging it as taint adds no signal here.
func postForm(ctx context.Context, httpClient connect.HTTPClient, tokenEndpoint string, form url.Values) (*http.Response, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode())) //nolint:gosec // Token endpoint is built from caller-supplied platformURL; trust boundary is the library caller's configuration
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return httpClient.Do(req)
}

func resolveAuthURL(platformURL string) string {
	platformURL = normalizePlatformURL(platformURL)
	if platformURL == "" {
		return defaultAuthURL
	}
	return platformURL + "/oauth2/platform"
}

// ResolveOAuth2ClientID mirrors the SDK's getOAuth2ClientId: an explicit value
// wins, then TAILOR_PLATFORM_OAUTH2_CLIENT_ID, then PLATFORM_OAUTH2_CLIENT_ID,
// then the SDK default. The client_id is not derived from the platform URL,
// because a self-hosted platform is configured through the same environment
// variables rather than through a table of known hosts.
func ResolveOAuth2ClientID(oauth2ClientID string) string { //nostyle:repetition
	if oauth2ClientID != "" {
		return oauth2ClientID
	}
	for _, key := range []string{"TAILOR_PLATFORM_OAUTH2_CLIENT_ID", "PLATFORM_OAUTH2_CLIENT_ID"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return DefaultOAuth2ClientID
}

// ResolvePlatformURL mirrors the SDK's getPlatformBaseUrl: an explicit value
// wins, then TAILOR_PLATFORM_URL, then PLATFORM_URL. It returns "" when none
// is set so callers can apply their own fallback.
func ResolvePlatformURL(platformURL string) string {
	if platformURL != "" {
		return normalizePlatformURL(platformURL)
	}
	for _, key := range []string{"TAILOR_PLATFORM_URL", "PLATFORM_URL"} {
		if v := os.Getenv(key); v != "" {
			return normalizePlatformURL(v)
		}
	}
	return ""
}

// normalizePlatformURL strips a trailing slash so that URL concatenation in
// resolveAuthURL and the exact-match switch in resolveClientID behave
// predictably when callers pass a config-style value such as
// "https://api.tailor.tech/".
func normalizePlatformURL(platformURL string) string {
	return strings.TrimRight(platformURL, "/")
}
