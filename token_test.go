package tailorclient

import (
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
		{"other URL", "https://custom.example.com", defaultAuthURL},
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

func TestResolveClientID(t *testing.T) {
	tests := []struct {
		name        string
		platformURL string
		want        string
	}{
		{"dev platform", devPlatformURL, devClientID},
		{"prod platform", "https://api.tailor.tech", prodClientID},
		{"other URL", "https://custom.example.com", prodClientID},
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
