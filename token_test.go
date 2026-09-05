package tailorclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestIsTokenExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt string
		want      bool
	}{
		{"empty string", "", true},
		{"malformed", "not-a-date", true},
		{"future RFC3339", time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339), false},
		{"past RFC3339", time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339), true},
		{"future alternate format", time.Now().Add(1 * time.Hour).Format("2006-01-02T15:04:05-07:00"), false},
		{"past alternate format", time.Now().Add(-1 * time.Hour).Format("2006-01-02T15:04:05-07:00"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTokenExpired(tt.expiresAt)
			if got != tt.want {
				t.Errorf("IsTokenExpired(%q) = %v, want %v", tt.expiresAt, got, tt.want)
			}
		})
	}
}

func TestResolveAuthURL(t *testing.T) {
	tests := []struct {
		name        string
		platformURL string
		want        string
	}{
		{"dev platform", "https://api.dev.tailor.tech", "https://api.dev.tailor.tech/oauth2/platform"},
		{"prod platform", "https://api.tailor.tech", defaultAuthURL},
		{"other URL", "https://custom.example.com", "https://custom.example.com/oauth2/platform"},
		{"empty falls back to prod", "", defaultAuthURL},
		{"trailing slash on prod", "https://api.tailor.tech/", defaultAuthURL},
		{"trailing slash on dev", "https://api.dev.tailor.tech/", "https://api.dev.tailor.tech/oauth2/platform"},
		{"trailing slash on custom", "https://custom.example.com/", "https://custom.example.com/oauth2/platform"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveAuthURL(tt.platformURL)
			if got != tt.want {
				t.Errorf("resolveAuthURL(%q) = %q, want %q", tt.platformURL, got, tt.want)
			}
		})
	}
}

// The refresh_token grant must post the SDK's own client_id, and must work
// against any platform URL: the SDK does not keep a table of known hosts.
func TestRefreshAccessToken_postsSDKClientID(t *testing.T) {
	tests := []struct {
		name           string
		oauth2ClientID string
		env            string
		wantClientID   string
	}{
		{"defaults to the SDK client_id", "", "", DefaultOAuth2ClientID},
		{"explicit client_id is used", "cpoc_explicit", "", "cpoc_explicit"},
		{"environment overrides the default", "", "cpoc_env", "cpoc_env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TAILOR_PLATFORM_OAUTH2_CLIENT_ID", tt.env)
			t.Setenv("PLATFORM_OAUTH2_CLIENT_ID", "")

			var (
				gotPath string
				gotForm url.Values
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				if err := r.ParseForm(); err != nil {
					t.Fatalf("ParseForm: %v", err)
				}
				gotForm = r.PostForm
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "new-at",
					"refresh_token": "new-rt",
					"expires_in":    3600,
				}); err != nil {
					t.Errorf("encode response: %v", err)
				}
			}))
			defer srv.Close()

			tr, err := RefreshAccessToken(context.Background(), nil, srv.URL, tt.oauth2ClientID, "rt-xxx")
			if err != nil {
				t.Fatalf("RefreshAccessToken: %v", err)
			}
			if tr.AccessToken != "new-at" || tr.RefreshToken != "new-rt" {
				t.Errorf("got at=%q rt=%q, want new-at/new-rt", tr.AccessToken, tr.RefreshToken)
			}
			if gotPath != "/oauth2/platform/token" {
				t.Errorf("path = %q, want /oauth2/platform/token", gotPath)
			}
			if gotForm.Get("grant_type") != "refresh_token" {
				t.Errorf("grant_type = %q, want refresh_token", gotForm.Get("grant_type"))
			}
			if gotForm.Get("refresh_token") != "rt-xxx" {
				t.Errorf("refresh_token = %q, want rt-xxx", gotForm.Get("refresh_token"))
			}
			if gotForm.Get("client_id") != tt.wantClientID {
				t.Errorf("client_id = %q, want %q", gotForm.Get("client_id"), tt.wantClientID)
			}
		})
	}
}

func TestFetchClientCredentialsToken_success(t *testing.T) {
	var (
		gotMethod      string
		gotPath        string
		gotContentType string
		gotForm        url.Values
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"token_type":   "Bearer",
			"access_token": "mu-access",
			"expires_in":   3600,
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	tr, err := FetchClientCredentialsToken(context.Background(), nil, srv.URL, "cid", "csecret")
	if err != nil {
		t.Fatalf("FetchClientCredentialsToken: %v", err)
	}

	if tr.AccessToken != "mu-access" {
		t.Errorf("AccessToken = %q, want mu-access", tr.AccessToken)
	}
	if tr.ExpiresIn != 3600 {
		t.Errorf("ExpiresIn = %d, want 3600", tr.ExpiresIn)
	}
	if tr.RefreshToken != "" {
		t.Errorf("RefreshToken = %q, want empty (client_credentials does not issue one)", tr.RefreshToken)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/oauth2/platform/token" {
		t.Errorf("path = %q, want /oauth2/platform/token", gotPath)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", gotContentType)
	}
	if gotForm.Get("grant_type") != "client_credentials" {
		t.Errorf("grant_type = %q, want client_credentials", gotForm.Get("grant_type"))
	}
	if gotForm.Get("client_id") != "cid" {
		t.Errorf("client_id = %q, want cid", gotForm.Get("client_id"))
	}
	if gotForm.Get("client_secret") != "csecret" {
		t.Errorf("client_secret = %q, want csecret", gotForm.Get("client_secret"))
	}
}

func TestFetchClientCredentialsToken_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		if err := json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	_, err := FetchClientCredentialsToken(context.Background(), nil, srv.URL, "cid", "bad")
	if err == nil {
		t.Fatal("expected error for invalid_client response")
	}
}

func TestFetchClientCredentialsToken_contextCancellation(t *testing.T) {
	// Handler intentionally never responds; the request must abort because
	// the context is already canceled at call time.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := FetchClientCredentialsToken(ctx, nil, srv.URL, "cid", "csecret")
	if err == nil {
		t.Fatal("expected error when context is already canceled")
	}
}

// The client_id must not depend on the platform URL: the SDK ships one
// client_id for every platform and lets the environment override it.
func TestResolveOAuth2ClientID(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		env      map[string]string
		want     string
	}{
		{"defaults to the SDK client_id", "", nil, DefaultOAuth2ClientID},
		{"explicit value wins", "cpoc_explicit", map[string]string{"TAILOR_PLATFORM_OAUTH2_CLIENT_ID": "cpoc_env"}, "cpoc_explicit"},
		{"TAILOR_PLATFORM_OAUTH2_CLIENT_ID is honored", "", map[string]string{"TAILOR_PLATFORM_OAUTH2_CLIENT_ID": "cpoc_env"}, "cpoc_env"},
		{"PLATFORM_OAUTH2_CLIENT_ID is honored", "", map[string]string{"PLATFORM_OAUTH2_CLIENT_ID": "cpoc_legacy"}, "cpoc_legacy"},
		{"TAILOR_ prefix takes precedence", "", map[string]string{"TAILOR_PLATFORM_OAUTH2_CLIENT_ID": "cpoc_env", "PLATFORM_OAUTH2_CLIENT_ID": "cpoc_legacy"}, "cpoc_env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TAILOR_PLATFORM_OAUTH2_CLIENT_ID", "")
			t.Setenv("PLATFORM_OAUTH2_CLIENT_ID", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if got := ResolveOAuth2ClientID(tt.explicit); got != tt.want {
				t.Errorf("ResolveOAuth2ClientID(%q) = %q, want %q", tt.explicit, got, tt.want)
			}
		})
	}
}

func TestResolvePlatformURL(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		env      map[string]string
		want     string
	}{
		{"empty without env stays empty so callers can infer", "", nil, ""},
		{"explicit value wins", "https://api.dev.tailor.tech", map[string]string{"TAILOR_PLATFORM_URL": "https://env.example.com"}, "https://api.dev.tailor.tech"},
		{"trailing slash is normalized", "https://api.dev.tailor.tech/", nil, "https://api.dev.tailor.tech"},
		{"TAILOR_PLATFORM_URL is honored", "", map[string]string{"TAILOR_PLATFORM_URL": "https://env.example.com"}, "https://env.example.com"},
		{"PLATFORM_URL is honored", "", map[string]string{"PLATFORM_URL": "https://legacy.example.com"}, "https://legacy.example.com"},
		{"TAILOR_ prefix takes precedence", "", map[string]string{"TAILOR_PLATFORM_URL": "https://env.example.com", "PLATFORM_URL": "https://legacy.example.com"}, "https://env.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TAILOR_PLATFORM_URL", "")
			t.Setenv("PLATFORM_URL", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if got := ResolvePlatformURL(tt.explicit); got != tt.want {
				t.Errorf("ResolvePlatformURL(%q) = %q, want %q", tt.explicit, got, tt.want)
			}
		})
	}
}
