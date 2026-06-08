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

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"shorter than n", "abc", 5, "abc"},
		{"equal to n", "abcde", 5, "abcde"},
		{"longer than n", "abcdef", 5, "abcde..."},
		{"empty string", "", 5, ""},
		{"zero n", "abc", 0, "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.s, tt.n)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
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
		{"dev platform", devPlatformURL, devPlatformURL + "/oauth2/platform"},
		{"prod platform", "https://api.tailor.tech", defaultAuthURL},
		{"other URL", "https://custom.example.com", "https://custom.example.com/oauth2/platform"},
		{"empty falls back to prod", "", defaultAuthURL},
		{"trailing slash on prod", "https://api.tailor.tech/", defaultAuthURL},
		{"trailing slash on dev", devPlatformURL + "/", devPlatformURL + "/oauth2/platform"},
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

func TestRefreshAccessToken_unknownPlatformRejected(t *testing.T) {
	_, err := RefreshAccessToken(context.Background(), "https://self-hosted.example.com", "rt-xxx")
	if err == nil {
		t.Fatal("expected error when refreshing against an unknown platform URL")
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

	tr, err := FetchClientCredentialsToken(context.Background(), srv.URL, "cid", "csecret")
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

	_, err := FetchClientCredentialsToken(context.Background(), srv.URL, "cid", "bad")
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
	_, err := FetchClientCredentialsToken(ctx, srv.URL, "cid", "csecret")
	if err == nil {
		t.Fatal("expected error when context is already canceled")
	}
}

func TestResolveClientID(t *testing.T) {
	tests := []struct {
		name        string
		platformURL string
		want        string
	}{
		{"dev platform", devPlatformURL, devClientID},
		{"prod platform", "https://api.tailor.tech", prodClientID},
		{"empty falls back to prod", "", prodClientID},
		{"other URL returns empty (caller must use WithClientCredentials)", "https://custom.example.com", ""},
		{"trailing slash on prod still matches", "https://api.tailor.tech/", prodClientID},
		{"trailing slash on dev still matches", devPlatformURL + "/", devClientID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveClientID(tt.platformURL)
			if got != tt.want {
				t.Errorf("resolveClientID(%q) = %q, want %q", tt.platformURL, got, tt.want)
			}
		})
	}
}
