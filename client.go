// Package tailorclient is an UNOFFICIAL Go client library for the Tailor
// Platform.
//
// It does not implement its own login flow. Authentication piggybacks on the
// official Tailor SDK (https://github.com/tailor-platform/sdk): the user logs
// in once with `npx tailor-sdk login`, and this package reuses the access /
// refresh tokens that the SDK stores in
// ~/.config/tailor-platform/config.yaml (file or keyring).
//
// New returns a connect-go OperatorServiceClient already wired with
// bearer-token authentication and automatic refresh on Unauthenticated
// errors. Tokens can also be supplied explicitly via WithTokens.
package tailorclient

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"buf.build/gen/go/tailor-inc/tailor/connectrpc/go/tailor/v1/tailorv1connect"
	"connectrpc.com/connect"
)

// DefaultPlatformURL is the production Tailor Platform endpoint.
const DefaultPlatformURL = "https://api.tailor.tech"

// Client is an authenticated Tailor Platform client.
//
// It embeds OperatorServiceClient, so RPC methods can be called directly
// (e.g. c.GetApplication). The auto-refresh interceptor is wired into the
// embedded client at construction time.
type Client struct {
	tailorv1connect.OperatorServiceClient

	httpClient  connect.HTTPClient
	platformURL string
}

type options struct {
	platformURL       string
	accessToken       string
	refreshToken      string
	tokensProvided    bool
	persistTokens     bool
	httpClient        connect.HTTPClient
	extraInterceptors []connect.Interceptor
}

// Option configures New.
type Option func(*options)

// WithPlatformURL overrides the Tailor Platform endpoint.
func WithPlatformURL(u string) Option {
	return func(o *options) {
		if u != "" {
			o.platformURL = u
		}
	}
}

// WithTokens uses the supplied tokens instead of reading the SDK config.
func WithTokens(accessToken, refreshToken string) Option {
	return func(o *options) {
		o.accessToken = accessToken
		o.refreshToken = refreshToken
		o.tokensProvided = true
	}
}

// WithHTTPClient overrides the underlying HTTP client.
func WithHTTPClient(h connect.HTTPClient) Option {
	return func(o *options) {
		if h != nil {
			o.httpClient = h
		}
	}
}

// WithInterceptors appends additional connect interceptors. The auto-refresh
// interceptor is always installed first.
func WithInterceptors(ics ...connect.Interceptor) Option {
	return func(o *options) {
		o.extraInterceptors = append(o.extraInterceptors, ics...)
	}
}

// WithTokenPersist enables writing refreshed tokens back to the SDK config.
// Disabled by default.
func WithTokenPersist() Option {
	return func(o *options) {
		o.persistTokens = true
	}
}

// New builds an authenticated client.
//
// Without WithTokens, New reads the current user's tokens from the Tailor SDK
// config and proactively refreshes them if expired. Token refresh on
// unauthenticated RPC errors is always enabled; SDK config writeback is
// opt-in via WithTokenPersist.
func New(ctx context.Context, opts ...Option) (*Client, error) {
	o := &options{
		platformURL: DefaultPlatformURL,
	}
	for _, opt := range opts {
		opt(o)
	}

	if !o.tokensProvided {
		at, rt, expiresAt, err := ReadSDKTokens()
		if err != nil {
			return nil, fmt.Errorf("read SDK tokens: %w", err)
		}
		o.accessToken = at
		o.refreshToken = rt

		if IsTokenExpired(expiresAt) && rt != "" {
			slog.Info("SDK config token is expired, refreshing proactively")
			tr, err := RefreshAccessToken(o.platformURL, rt)
			if err != nil {
				return nil, fmt.Errorf("token expired and refresh failed: %w", err)
			}
			o.accessToken = tr.AccessToken
			if tr.RefreshToken != "" {
				o.refreshToken = tr.RefreshToken
			}
			if o.persistTokens {
				newExpiresAt := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
				if err := WriteSDKTokens(o.accessToken, o.refreshToken, newExpiresAt); err != nil {
					slog.Warn("failed to persist refreshed tokens", "error", err)
				}
			}
		}
	}

	if o.accessToken == "" {
		return nil, fmt.Errorf("no access token available")
	}

	if o.httpClient == nil {
		o.httpClient = &http.Client{}
	}

	var onRefresh onTokenRefreshFunc
	if o.persistTokens {
		onRefresh = func(at, rt, exp string) {
			if err := WriteSDKTokens(at, rt, exp); err != nil {
				slog.Warn("failed to persist refreshed tokens", "error", err)
			}
		}
	}

	interceptor := newAutoRefreshInterceptor(o.platformURL, o.accessToken, o.refreshToken, onRefresh)
	ics := make([]connect.Interceptor, 0, 1+len(o.extraInterceptors))
	ics = append(ics, interceptor)
	ics = append(ics, o.extraInterceptors...)

	op := tailorv1connect.NewOperatorServiceClient(
		o.httpClient,
		o.platformURL,
		connect.WithInterceptors(ics...),
	)

	_ = ctx
	return &Client{
		OperatorServiceClient: op,
		httpClient:            o.httpClient,
		platformURL:           o.platformURL,
	}, nil
}

// PlatformURL returns the configured Tailor Platform endpoint.
func (c *Client) PlatformURL() string {
	return c.platformURL
}

// HTTPClient returns the underlying HTTP client.
func (c *Client) HTTPClient() connect.HTTPClient {
	return c.httpClient
}

type onTokenRefreshFunc func(accessToken, refreshToken, expiresAt string)

type autoRefreshInterceptor struct {
	platformURL    string
	token          string
	refreshToken   string
	onTokenRefresh onTokenRefreshFunc
	mu             sync.Mutex
}

func newAutoRefreshInterceptor(platformURL, token, refreshToken string, onRefresh onTokenRefreshFunc) *autoRefreshInterceptor {
	return &autoRefreshInterceptor{
		platformURL:    platformURL,
		token:          token,
		refreshToken:   refreshToken,
		onTokenRefresh: onRefresh,
	}
}

func (i *autoRefreshInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		i.mu.Lock()
		token := i.token
		i.mu.Unlock()

		req.Header().Set("Authorization", "Bearer "+token)
		resp, err := next(ctx, req)
		if err != nil && i.isUnauthenticated(err) {
			if i.refreshToken == "" {
				return nil, fmt.Errorf("%w (no refresh token available)", err)
			}
			slog.Info("Token rejected, attempting refresh")
			newToken, refreshErr := i.doRefresh()
			if refreshErr != nil {
				return nil, fmt.Errorf("%w (token refresh also failed: %w)", err, refreshErr)
			}
			req.Header().Set("Authorization", "Bearer "+newToken)
			return next(ctx, req)
		}
		return resp, err
	}
}

func (i *autoRefreshInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *autoRefreshInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (i *autoRefreshInterceptor) doRefresh() (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	tr, err := RefreshAccessToken(i.platformURL, i.refreshToken)
	if err != nil {
		return "", err
	}
	i.token = tr.AccessToken
	if tr.RefreshToken != "" {
		i.refreshToken = tr.RefreshToken
	}
	slog.Info("Access token refreshed successfully")

	if i.onTokenRefresh != nil {
		expiresAt := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		i.onTokenRefresh(tr.AccessToken, i.refreshToken, expiresAt)
	}

	return i.token, nil
}

func (i *autoRefreshInterceptor) isUnauthenticated(err error) bool {
	s := err.Error()
	return strings.Contains(s, "unauthenticated") || strings.Contains(s, "Unauthenticated")
}
