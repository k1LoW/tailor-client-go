package tailorclient

import (
	"context"
	"errors"
	"testing"

	tailorv1 "buf.build/gen/go/tailor-inc/tailor/protocolbuffers/go/tailor/v1"
	"connectrpc.com/connect"
)

func TestNew_withTokens(t *testing.T) {
	c, err := New(context.Background(), WithTokens("at", "rt"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.OperatorServiceClient == nil {
		t.Errorf("OperatorServiceClient is nil")
	}
	if c.PlatformURL() != DefaultPlatformURL {
		t.Errorf("PlatformURL = %q, want %q", c.PlatformURL(), DefaultPlatformURL)
	}
	if c.HTTPClient() == nil {
		t.Errorf("HTTPClient is nil")
	}
}

func TestNew_withTokens_emptyAccessToken(t *testing.T) {
	_, err := New(context.Background(), WithTokens("", ""))
	if err == nil {
		t.Fatal("expected error for empty access token")
	}
}

func TestNew_withPlatformURL(t *testing.T) {
	want := "https://api.dev.tailor.tech"
	c, err := New(context.Background(),
		WithTokens("at", "rt"),
		WithPlatformURL(want),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.PlatformURL() != want {
		t.Errorf("PlatformURL = %q, want %q", c.PlatformURL(), want)
	}
}

func TestAutoRefreshInterceptor_attachesBearer(t *testing.T) {
	i := newAutoRefreshInterceptor("https://example.com", "tok-123", "rt", nil)

	var gotAuth string
	next := connect.UnaryFunc(func(_ context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		gotAuth = req.Header().Get("Authorization")
		return connect.NewResponse(&tailorv1.PingResponse{}), nil
	})

	wrapped := i.WrapUnary(next)
	_, err := wrapped(context.Background(), connect.NewRequest(&tailorv1.PingRequest{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "Bearer tok-123"; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
}

func TestAutoRefreshInterceptor_unauthenticatedWithoutRefreshToken(t *testing.T) {
	i := newAutoRefreshInterceptor("https://example.com", "tok-123", "", nil)

	authErr := errors.New("unauthenticated")
	next := connect.UnaryFunc(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, authErr
	})

	wrapped := i.WrapUnary(next)
	_, err := wrapped(context.Background(), connect.NewRequest(&tailorv1.PingRequest{}))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, authErr) {
		t.Errorf("expected wrapped authErr, got %v", err)
	}
}

func TestAutoRefreshInterceptor_passesThroughNonAuthErrors(t *testing.T) {
	i := newAutoRefreshInterceptor("https://example.com", "tok-123", "rt", nil)

	otherErr := errors.New("not found")
	calls := 0
	next := connect.UnaryFunc(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		calls++
		return nil, otherErr
	})

	wrapped := i.WrapUnary(next)
	_, err := wrapped(context.Background(), connect.NewRequest(&tailorv1.PingRequest{}))
	if !errors.Is(err, otherErr) {
		t.Errorf("expected otherErr, got %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry for non-auth errors)", calls)
	}
}

func TestAutoRefreshInterceptor_isUnauthenticated(t *testing.T) {
	i := &autoRefreshInterceptor{}
	tests := []struct {
		err  error
		want bool
	}{
		{errors.New("rpc error: unauthenticated"), true},
		{errors.New("Unauthenticated"), true},
		{errors.New("not found"), false},
		{errors.New(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			if got := i.isUnauthenticated(tt.err); got != tt.want {
				t.Errorf("isUnauthenticated(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
