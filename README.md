> [!IMPORTANT]
> This is an **unofficial** library.

# tailor-client-go

`tailor-client-go` is an **unofficial** Go client library for the [Tailor Platform](https://docs.tailor.tech/) Controlplane.

It does **not** implement its own login flow. Authentication piggybacks on the official [Tailor SDK](https://github.com/tailor-platform/sdk): you log in once with `npx tailor-sdk login`, and `tailor-client-go` reuses the access / refresh tokens stored by the SDK to authenticate Controlplane RPCs. Token refresh and (optionally) writeback are kept compatible with the SDK config so other Tailor tools stay in sync.

In short, `tailorclient.New(ctx)` returns the [buf.build](https://buf.build/tailor-inc/tailor) generated connect-go `OperatorServiceClient` already wired with bearer-token auth and auto-refresh.

## Features

- Piggybacks on the [Tailor SDK](https://github.com/tailor-platform/sdk) authentication: tokens are sourced from `~/.config/tailor-platform/config.yaml` (file and keyring storage both supported)
- One-call constructor: `tailorclient.New(ctx)` returns a ready-to-use authenticated client
- Automatic token refresh on `Unauthenticated` RPC errors
- Optional SDK config writeback on token refresh (off by default) so the SDK and other tools see the new tokens
- Embeds `tailorv1connect.OperatorServiceClient`, so RPC methods are callable directly on the client

## Install

```console
$ go get github.com/k1LoW/tailor-client-go
```

## Authentication model

`tailor-client-go` does not handle the OAuth2 login flow itself. The expected workflow is:

1. The user (or operator) logs in once via the Tailor SDK:

   ```console
   $ npx tailor-sdk login
   ```

   This writes `access_token`, `refresh_token`, and `token_expires_at` for the current user to `~/.config/tailor-platform/config.yaml` (or, when the user is configured for `storage: keyring`, into the OS keyring).

2. Any Go program using `tailor-client-go` calls `tailorclient.New(ctx)` and the library transparently:

   - reads the current user's tokens from the SDK config (or keyring),
   - refreshes the access token if `token_expires_at` is in the past,
   - retries once with a refreshed token if a Controlplane RPC returns `Unauthenticated`.

If you prefer to manage tokens yourself (e.g. in CI, or against a service account), pass them in explicitly with `WithTokens` and the SDK config is not touched.

## Usage

```go
package main

import (
	"context"
	"fmt"
	"log"

	tailorv1 "buf.build/gen/go/tailor-inc/tailor/protocolbuffers/go/tailor/v1"
	"connectrpc.com/connect"

	tailorclient "github.com/k1LoW/tailor-client-go"
)

func main() {
	ctx := context.Background()

	c, err := tailorclient.New(ctx)
	if err != nil {
		log.Fatal(err)
	}

	res, err := c.GetApplication(ctx, connect.NewRequest(&tailorv1.GetApplicationRequest{
		WorkspaceId:     "ws_xxx",
		ApplicationName: "my-app",
	}))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Msg.GetApplication().GetName())
}
```

Without any options, `New` reads the current user's tokens from the Tailor SDK config. When `token_expires_at` indicates the access token is stale, it is refreshed proactively before the client is returned.

### Explicit tokens

To bypass the SDK config and supply tokens directly:

```go
c, err := tailorclient.New(ctx,
	tailorclient.WithTokens(accessToken, refreshToken),
	tailorclient.WithPlatformURL(tailorclient.DefaultPlatformURL),
)
```

### Persisting refreshed tokens

By default, tokens refreshed during a session are kept in-memory only. Pass `WithTokenPersist()` to write them back to the SDK config (or keyring, if the user is configured for keyring storage) so other tools stay in sync:

```go
c, err := tailorclient.New(ctx, tailorclient.WithTokenPersist())
```

### Available options

| Option | Description |
|--------|-------------|
| `WithPlatformURL(url string)` | Override the Tailor Platform endpoint (default: `https://api.tailor.tech`) |
| `WithTokens(access, refresh string)` | Use supplied tokens instead of reading the SDK config |
| `WithTokenPersist()` | Write refreshed tokens back to the SDK config (default: off) |
| `WithHTTPClient(h connect.HTTPClient)` | Override the underlying HTTP client |
| `WithInterceptors(ics ...connect.Interceptor)` | Append additional connect interceptors |

## How it works

1. Resolves access / refresh tokens (explicit values via `WithTokens`, or the Tailor SDK config)
2. If the SDK config token is expired, refreshes it proactively against the OAuth2 token endpoint
3. Builds a connect-go `OperatorServiceClient` wrapped with an interceptor that:
   - Attaches the `Authorization: Bearer <token>` header to every request
   - On an `Unauthenticated` error, exchanges the refresh token for a new access token and retries once
4. When `WithTokenPersist()` is set, refreshed tokens are written back to the SDK config (or keyring)

## License

[MIT License](LICENSE)
