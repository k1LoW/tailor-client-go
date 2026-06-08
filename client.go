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
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	tailorv1 "buf.build/gen/go/tailor-inc/tailor/protocolbuffers/go/tailor/v1"
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
	platformURL               string
	accessToken               string
	refreshToken              string
	clientID                  string
	clientSecret              string
	tokensProvided            bool
	clientCredentialsProvided bool
	persistTokens             bool
	httpClient                connect.HTTPClient
	extraInterceptors         []connect.Interceptor
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
func WithHTTPClient(h connect.HTTPClient) Option { //nostyle:repetition
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

// WithClientCredentials configures OAuth2 client_credentials grant
// authentication using a platform machine user. New() will fetch an access
// token at construction time, and the interceptor re-fetches with the same
// credentials whenever the token is rejected as Unauthenticated (machine
// user grants do not issue refresh tokens, so the helper holds on to the
// client_id/client_secret rather than a refresh_token).
//
// Mutually exclusive with WithTokens and WithTokenPersist.
func WithClientCredentials(clientID, clientSecret string) Option { //nostyle:repetition
	return func(o *options) {
		o.clientID = clientID
		o.clientSecret = clientSecret
		o.clientCredentialsProvided = true
	}
}

// New builds an authenticated client.
//
// Authentication source is determined by which options are supplied:
//   - WithClientCredentials: fetch an access token via the OAuth2
//     client_credentials grant using a platform machine user.
//   - WithTokens: use the supplied access/refresh tokens directly.
//   - Otherwise: read the current user's tokens from the Tailor SDK config,
//     and proactively refresh them if expired.
//
// Token refresh on unauthenticated unary RPCs is always enabled (using the
// refresh_token for SDK-config / WithTokens flows, or re-fetching with the
// stored client_credentials for machine-user flows). SDK config writeback is
// opt-in via WithTokenPersist and only applies to refresh_token flows.
func New(ctx context.Context, opts ...Option) (*Client, error) {
	o := &options{
		platformURL: DefaultPlatformURL,
	}
	for _, opt := range opts {
		opt(o)
	}

	switch {
	case o.clientCredentialsProvided:
		if o.clientID == "" || o.clientSecret == "" {
			return nil, fmt.Errorf("client_credentials option requires both clientID and clientSecret")
		}
		if o.tokensProvided {
			return nil, fmt.Errorf("client_credentials option cannot be combined with WithTokens")
		}
		if o.persistTokens {
			return nil, fmt.Errorf("client_credentials option cannot be combined with WithTokenPersist")
		}
		tr, err := FetchClientCredentialsToken(o.platformURL, o.clientID, o.clientSecret)
		if err != nil {
			return nil, fmt.Errorf("fetch client_credentials token: %w", err)
		}
		o.accessToken = tr.AccessToken
		o.refreshToken = ""

	case !o.tokensProvided:
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

	interceptor := newAutoRefreshInterceptor(o.platformURL, o.accessToken, o.refreshToken, o.clientID, o.clientSecret, onRefresh)
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

// DefaultUploadChunkSize is the default chunk size used by
// Client.UploadFileFromReader when UploadFileParams.ChunkSize is not set.
const DefaultUploadChunkSize = 256 * 1024

// UploadFileParams configures Client.UploadFileFromReader.
type UploadFileParams struct {
	WorkspaceID  string
	DeploymentID string
	FilePath     string
	ContentType  string
	// ChunkSize controls the size in bytes of each ChunkData message. When
	// zero or negative, DefaultUploadChunkSize is used.
	ChunkSize int
}

// UploadFileFromReader streams r to the Tailor Platform as a single file. It
// sends one InitialMetadata message followed by ChunkData messages until r
// returns io.EOF, then closes the stream.
//
// This wraps the generated streaming UploadFile RPC so callers do not have to
// manage the metadata/chunk oneof, the chunk loop, or stream close themselves.
// The raw streaming RPC is still reachable as c.OperatorServiceClient.UploadFile.
func (c *Client) UploadFileFromReader(ctx context.Context, params UploadFileParams, r io.Reader) error {
	if r == nil {
		return fmt.Errorf("upload file: reader is nil")
	}
	chunkSize := params.ChunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultUploadChunkSize
	}

	stream := c.UploadFile(ctx)

	meta := &tailorv1.UploadFileRequest{}
	meta.SetInitialMetadata((&tailorv1.UploadFileRequest_InitialUploadMetadata_builder{
		WorkspaceId:  params.WorkspaceID,
		DeploymentId: params.DeploymentID,
		FilePath:     params.FilePath,
		ContentType:  params.ContentType,
	}).Build())
	if err := stream.Send(meta); err != nil {
		// Per connect-go's client-streaming contract, a server-side error
		// during Send is surfaced as an io.EOF-wrapped sentinel; the real
		// RPC error is only retrievable via CloseAndReceive.
		if _, closeErr := stream.CloseAndReceive(); closeErr != nil {
			return fmt.Errorf("upload file: send metadata: %w", closeErr)
		}
		return fmt.Errorf("upload file: send metadata: %w", err)
	}

	buf := make([]byte, chunkSize)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			chunk := &tailorv1.UploadFileRequest{}
			chunkBytes := make([]byte, n)
			copy(chunkBytes, buf[:n])
			chunk.SetChunkData(chunkBytes)
			if sendErr := stream.Send(chunk); sendErr != nil {
				if _, closeErr := stream.CloseAndReceive(); closeErr != nil {
					return fmt.Errorf("upload file: send chunk: %w", closeErr)
				}
				return fmt.Errorf("upload file: send chunk: %w", sendErr)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			// Close the stream so the server does not sit waiting for
			// more chunks after we abandon the upload.
			if _, closeErr := stream.CloseAndReceive(); closeErr != nil {
				slog.Warn("UploadFileFromReader: stream close after read error failed", "error", closeErr)
			}
			return fmt.Errorf("upload file: read: %w", readErr)
		}
	}

	if _, err := stream.CloseAndReceive(); err != nil {
		return fmt.Errorf("upload file: close: %w", err)
	}
	return nil
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
	clientID       string
	clientSecret   string
	onTokenRefresh onTokenRefreshFunc
	mu             sync.Mutex
}

func newAutoRefreshInterceptor(platformURL, token, refreshToken, clientID, clientSecret string, onRefresh onTokenRefreshFunc) *autoRefreshInterceptor {
	return &autoRefreshInterceptor{
		platformURL:    platformURL,
		token:          token,
		refreshToken:   refreshToken,
		clientID:       clientID,
		clientSecret:   clientSecret,
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
			if !i.canRefresh() {
				return nil, fmt.Errorf("%w (no refresh credentials available)", err)
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
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		i.mu.Lock()
		token := i.token
		i.mu.Unlock()
		conn.RequestHeader().Set("Authorization", "Bearer "+token)
		return conn
	}
}

func (i *autoRefreshInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (i *autoRefreshInterceptor) canRefresh() bool {
	return i.refreshToken != "" || (i.clientID != "" && i.clientSecret != "")
}

func (i *autoRefreshInterceptor) doRefresh() (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.clientID != "" && i.clientSecret != "" {
		tr, err := FetchClientCredentialsToken(i.platformURL, i.clientID, i.clientSecret)
		if err != nil {
			return "", err
		}
		i.token = tr.AccessToken
		slog.Info("Access token re-fetched via client_credentials")
		if i.onTokenRefresh != nil {
			expiresAt := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
			i.onTokenRefresh(tr.AccessToken, "", expiresAt)
		}
		return i.token, nil
	}

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
