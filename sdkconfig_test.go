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

	at, rt, exp, err := ReadSDKTokens()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if at != "my-at" {
		t.Errorf("access token = %q, want %q", at, "my-at")
	}
	if rt != "my-rt" {
		t.Errorf("refresh token = %q, want %q", rt, "my-rt")
	}
	if want := "2099-12-31T00:00:00Z"; exp != want {
		t.Errorf("token_expires_at = %q, want %q", exp, want)
	}
}

func TestReadSDKTokens_missingCurrentUser(t *testing.T) {
	writeSDKConfigForTest(t, `version: 1
users:
  alice:
    access_token: my-at
`)
	_, _, _, err := ReadSDKTokens()
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
	_, _, _, err := ReadSDKTokens()
	if err == nil {
		t.Error("expected error for unknown current_user")
	}
}

func TestReadSDKTokens_missingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	_, _, _, err := ReadSDKTokens()
	if err == nil {
		t.Error("expected error when config file is missing")
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

	if err := WriteSDKTokens("new-at", "new-rt", "2099-12-31T00:00:00Z"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	at, rt, exp, err := ReadSDKTokens()
	if err != nil {
		t.Fatalf("read after write: %v", err)
	}
	if at != "new-at" || rt != "new-rt" || exp != "2099-12-31T00:00:00Z" {
		t.Errorf("got at=%q rt=%q exp=%q", at, rt, exp)
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
}
