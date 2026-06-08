package tailorclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	tailorv1 "buf.build/gen/go/tailor-inc/tailor/protocolbuffers/go/tailor/v1"
	"buf.build/gen/go/tailor-inc/tailor/connectrpc/go/tailor/v1/tailorv1connect"
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
	i := newAutoRefreshInterceptor("https://example.com", "tok-123", "rt", "", "", nil, nil)

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
	i := newAutoRefreshInterceptor("https://example.com", "tok-123", "", "", "", nil, nil)

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
	i := newAutoRefreshInterceptor("https://example.com", "tok-123", "rt", "", "", nil, nil)

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

type fakeStreamingClientConn struct {
	spec   connect.Spec
	header http.Header
}

func (f *fakeStreamingClientConn) Spec() connect.Spec             { return f.spec }
func (f *fakeStreamingClientConn) Peer() connect.Peer             { return connect.Peer{} }
func (f *fakeStreamingClientConn) Send(any) error                 { return nil }
func (f *fakeStreamingClientConn) RequestHeader() http.Header     { return f.header }
func (f *fakeStreamingClientConn) CloseRequest() error            { return nil }
func (f *fakeStreamingClientConn) Receive(any) error              { return io.EOF }
func (f *fakeStreamingClientConn) ResponseHeader() http.Header    { return http.Header{} }
func (f *fakeStreamingClientConn) ResponseTrailer() http.Header   { return http.Header{} }
func (f *fakeStreamingClientConn) CloseResponse() error           { return nil }

func TestAutoRefreshInterceptor_attachesBearerOnStreaming(t *testing.T) {
	i := newAutoRefreshInterceptor("https://example.com", "tok-456", "rt", "", "", nil, nil)

	fake := &fakeStreamingClientConn{header: http.Header{}}
	next := connect.StreamingClientFunc(func(_ context.Context, _ connect.Spec) connect.StreamingClientConn {
		return fake
	})

	wrapped := i.WrapStreamingClient(next)
	_ = wrapped(context.Background(), connect.Spec{})
	if want, got := "Bearer tok-456", fake.header.Get("Authorization"); got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
}

type fakeOperatorHandler struct {
	tailorv1connect.UnimplementedOperatorServiceHandler
	uploadFn func(context.Context, *connect.ClientStream[tailorv1.UploadFileRequest]) (*connect.Response[tailorv1.UploadFileResponse], error)
}

func (h *fakeOperatorHandler) UploadFile(ctx context.Context, stream *connect.ClientStream[tailorv1.UploadFileRequest]) (*connect.Response[tailorv1.UploadFileResponse], error) {
	return h.uploadFn(ctx, stream)
}

func TestClient_UploadFileFromReader_sendsMetadataAndChunksWithAuth(t *testing.T) {
	var (
		gotAuth     string
		gotMetadata *tailorv1.UploadFileRequest_InitialUploadMetadata
		gotPayload  bytes.Buffer
		gotMsgCount int
	)
	handler := &fakeOperatorHandler{
		uploadFn: func(_ context.Context, stream *connect.ClientStream[tailorv1.UploadFileRequest]) (*connect.Response[tailorv1.UploadFileResponse], error) {
			gotAuth = stream.RequestHeader().Get("Authorization")
			for stream.Receive() {
				gotMsgCount++
				msg := stream.Msg()
				switch {
				case msg.HasInitialMetadata():
					gotMetadata = msg.GetInitialMetadata()
				case msg.HasChunkData():
					gotPayload.Write(msg.GetChunkData())
				}
			}
			if err := stream.Err(); err != nil {
				return nil, err
			}
			return connect.NewResponse(&tailorv1.UploadFileResponse{}), nil
		},
	}

	path, h := tailorv1connect.NewOperatorServiceHandler(handler)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	c, err := New(context.Background(),
		WithTokens("tok-stream", ""),
		WithPlatformURL(srv.URL),
		WithHTTPClient(srv.Client()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	want := bytes.Repeat([]byte("abcdefghij"), 100) // 1000 bytes
	err = c.UploadFileFromReader(context.Background(), UploadFileParams{
		WorkspaceID:  "ws_xxx",
		DeploymentID: "dp_xxx",
		FilePath:     "index.html",
		ContentType:  "text/html",
		ChunkSize:    256,
	}, bytes.NewReader(want))
	if err != nil {
		t.Fatalf("UploadFileFromReader: %v", err)
	}

	if want := "Bearer tok-stream"; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotMetadata == nil {
		t.Fatal("metadata message was not received")
	}
	if gotMetadata.GetWorkspaceId() != "ws_xxx" ||
		gotMetadata.GetDeploymentId() != "dp_xxx" ||
		gotMetadata.GetFilePath() != "index.html" ||
		gotMetadata.GetContentType() != "text/html" {
		t.Errorf("unexpected metadata: %+v", gotMetadata)
	}
	if !bytes.Equal(gotPayload.Bytes(), want) {
		t.Errorf("payload mismatch: got %d bytes, want %d bytes", gotPayload.Len(), len(want))
	}
	// 1 metadata + ceil(1000/256) = 1 + 4 = 5 messages.
	if gotMsgCount != 5 {
		t.Errorf("message count = %d, want 5", gotMsgCount)
	}
}

func TestClient_UploadFileFromReader_nilReader(t *testing.T) {
	c, err := New(context.Background(), WithTokens("at", "rt"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.UploadFileFromReader(context.Background(), UploadFileParams{}, nil); err == nil {
		t.Fatal("expected error for nil reader")
	}
}

func TestNew_clientCredentials_happyPath(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/platform/token" {
			t.Errorf("token endpoint path = %q, want /oauth2/platform/token", r.URL.Path)
		}
		hits++
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

	c, err := New(context.Background(),
		WithPlatformURL(srv.URL),
		WithClientCredentials("cid", "csecret"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if hits != 1 {
		t.Errorf("token endpoint hit %d times, want 1", hits)
	}
	if c.PlatformURL() != srv.URL {
		t.Errorf("PlatformURL = %q, want %q", c.PlatformURL(), srv.URL)
	}
}

func TestNew_clientCredentials_exclusiveWithTokens(t *testing.T) {
	_, err := New(context.Background(),
		WithTokens("at", "rt"),
		WithClientCredentials("cid", "csecret"),
	)
	if err == nil {
		t.Fatal("expected error when WithClientCredentials is combined with WithTokens")
	}
}

func TestNew_clientCredentials_exclusiveWithTokenPersist(t *testing.T) {
	_, err := New(context.Background(),
		WithClientCredentials("cid", "csecret"),
		WithTokenPersist(),
	)
	if err == nil {
		t.Fatal("expected error when WithClientCredentials is combined with WithTokenPersist")
	}
}

func TestNew_clientCredentials_partialOptions(t *testing.T) {
	_, err := New(context.Background(),
		WithClientCredentials("cid", ""),
	)
	if err == nil {
		t.Fatal("expected error when clientSecret is empty")
	}
}

func TestNew_clientCredentials_emptyValuesFailFast(t *testing.T) {
	// Both empty (e.g. unset env vars passed via os.Getenv) must NOT
	// silently fall back to the SDK config path. Once WithClientCredentials
	// has been supplied, that auth source is selected and validated.
	_, err := New(context.Background(),
		WithClientCredentials("", ""),
	)
	if err == nil {
		t.Fatal("expected error when both clientID and clientSecret are empty")
	}
}

func TestAutoRefreshInterceptor_clientCredentialsReFetch(t *testing.T) {
	var tokenHits int
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenHits++
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"token_type":   "Bearer",
			"access_token": "new-tok",
			"expires_in":   3600,
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer tokenSrv.Close()

	i := newAutoRefreshInterceptor(tokenSrv.URL, "old-token", "", "cid", "csecret", nil, nil)

	if !i.canRefresh() {
		t.Fatal("canRefresh should be true with client credentials")
	}

	authErr := errors.New("unauthenticated")
	var headers []string
	next := connect.UnaryFunc(func(_ context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		headers = append(headers, req.Header().Get("Authorization"))
		if len(headers) == 1 {
			return nil, authErr
		}
		return connect.NewResponse(&tailorv1.PingResponse{}), nil
	})

	wrapped := i.WrapUnary(next)
	_, err := wrapped(context.Background(), connect.NewRequest(&tailorv1.PingRequest{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokenHits != 1 {
		t.Errorf("token endpoint hit %d times, want 1", tokenHits)
	}
	if len(headers) != 2 {
		t.Fatalf("expected 2 RPC calls (initial + retry), got %d", len(headers))
	}
	if headers[0] != "Bearer old-token" {
		t.Errorf("first header = %q, want Bearer old-token", headers[0])
	}
	if headers[1] != "Bearer new-tok" {
		t.Errorf("retry header = %q, want Bearer new-tok", headers[1])
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
