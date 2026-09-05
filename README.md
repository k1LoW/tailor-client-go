> [!IMPORTANT]
> This is an **unofficial** library.

# tailor-client-go

[![Go Reference](https://pkg.go.dev/badge/github.com/k1LoW/tailor-client-go.svg)](https://pkg.go.dev/github.com/k1LoW/tailor-client-go) [![build](https://github.com/k1LoW/tailor-client-go/actions/workflows/ci.yml/badge.svg)](https://github.com/k1LoW/tailor-client-go/actions/workflows/ci.yml) ![Coverage](https://raw.githubusercontent.com/k1LoW/octocovs/main/badges/k1LoW/tailor-client-go/coverage.svg) ![Code to Test Ratio](https://raw.githubusercontent.com/k1LoW/octocovs/main/badges/k1LoW/tailor-client-go/ratio.svg) ![Test Execution Time](https://raw.githubusercontent.com/k1LoW/octocovs/main/badges/k1LoW/tailor-client-go/time.svg)

`tailor-client-go` is an **unofficial** Go client library for the [Tailor Platform](https://docs.tailor.tech/).

> [!IMPORTANT]
> `tailor-client-go` implements **no login flow of its own**. It runs on the access tokens the official [Tailor SDK](https://github.com/tailor-platform/sdk) already holds, so `npx tailor-sdk login` is a **prerequisite**, not a suggestion. For CI and other unattended callers, use `WithClientCredentials` instead. See [Authentication model](#authentication-model).

In short, `tailorclient.New(ctx)` returns the [buf.build](https://buf.build/tailor-inc/tailor) generated connect-go `OperatorServiceClient` already wired with bearer-token auth and auto-refresh.

## Features

- Piggybacks on [Tailor SDK](https://github.com/tailor-platform/sdk) authentication. Tokens are sourced from `~/.config/tailor-platform/config.yaml` (file and keyring storage both supported)
- OAuth2 `client_credentials` grant for platform machine users via `WithClientCredentials`, for CI and other unattended callers
- One-call constructor. `tailorclient.New(ctx)` returns a ready-to-use authenticated client
- Automatic token refresh on `Unauthenticated` RPC errors
- Optional SDK config writeback on token refresh (off by default) so the SDK and other tools see the new tokens
- Token handling follows the SDK. The config user key is platform-scoped the way the SDK scopes it, the OAuth2 `client_id` defaults to the SDK's own and honors the same environment variables, and the platform endpoint is taken from the SDK config when you name none
- Embeds `tailorv1connect.OperatorServiceClient`, so RPC methods are callable directly on the client

## Install

```console
$ go get github.com/k1LoW/tailor-client-go
```

## Authentication model

`tailor-client-go` does not handle the OAuth2 login flow itself. `New` picks its credentials from one of three sources, in this order.

| Source | Option | Use it for |
|--------|--------|-----------|
| OAuth2 `client_credentials` grant | `WithClientCredentials(clientID, clientSecret)` | CI, service accounts, any unattended caller. The SDK config is never touched |
| Caller-managed tokens | `WithTokens(access, refresh)` | You already hold tokens and want to manage their lifecycle yourself |
| Tailor SDK config | *(default)* | Local development, where a human has run `npx tailor-sdk login` |

### Machine user (CI)

Create a platform machine user, then pass its credentials. No SDK login and no SDK config are involved.

```go
c, err := tailorclient.New(ctx,
	tailorclient.WithClientCredentials(os.Getenv("TAILOR_CLIENT_ID"), os.Getenv("TAILOR_CLIENT_SECRET")),
)
```

Machine user grants do not issue a refresh token, so on an `Unauthenticated` error the client re-fetches with the same credentials. `WithClientCredentials` cannot be combined with `WithTokens` or `WithTokenPersist`.

### SDK config (local development)

1. Log in once via the Tailor SDK.

   ```console
   $ npx tailor-sdk login
   ```

   This writes the tokens for the current user to `~/.config/tailor-platform/config.yaml`, or into the OS keyring when the user is configured for `storage: keyring`.

2. Any Go program using `tailor-client-go` calls `tailorclient.New(ctx)` and the library transparently:

   - resolves the `current_user`'s entry in the SDK config,
   - reads the tokens from the config file or the OS keyring,
   - refreshes the access token if `token_expires_at` is in the past,
   - retries once with a refreshed token if an RPC returns `Unauthenticated`.

## Platform and client_id resolution

The SDK scopes each login to a platform, and `tailor-client-go` follows the same rules rather than assuming production.

**Platform endpoint**, highest precedence first:

1. `WithPlatformURL(url)`
2. `TAILOR_PLATFORM_URL`, then `PLATFORM_URL`
3. The platform recorded on the SDK config user key
4. `tailorclient.DefaultPlatformURL` (`https://api.tailor.tech`)

Step 3 is what keeps a dev login working. Since SDK config v3, a non-production login is stored under a platform-scoped key while `current_user` keeps the bare user ID:

```yaml
version: 3
users:
  https://api.dev.tailor.tech|ac354dd0-...:   # dev, platform-scoped
    storage: keyring
    token_expires_at: '2026-09-02T05:23:39.185Z'
  98f96ebb-...:                               # production, bare key
    storage: keyring
current_user: ac354dd0-...
```

With no platform named, `New` resolves `current_user` to the dev entry above and talks to `https://api.dev.tailor.tech`, so the dev refresh token is never posted to production. If `current_user` is registered for several non-production platforms, the lookup is ambiguous and `New` asks you to pick one with `WithPlatformURL`.

**OAuth2 `client_id`** for the `refresh_token` grant, highest precedence first:

1. `WithOAuth2ClientID(id)`
2. `TAILOR_PLATFORM_OAUTH2_CLIENT_ID`, then `PLATFORM_OAUTH2_CLIENT_ID`
3. `tailorclient.DefaultOAuth2ClientID`, which is the SDK's own public client ID

The `client_id` is deliberately **not** derived from the platform URL. The SDK ships one client ID for every platform and lets the environment override it, so a self-hosted platform is configured through these variables rather than through a table of known hosts.

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

Without any options, `New` reads the current user's tokens from the Tailor SDK config and infers the platform from the config user key. When `token_expires_at` indicates the access token is stale, it is refreshed proactively before the client is returned.

### Explicit tokens

To bypass the SDK config and supply tokens directly:

```go
c, err := tailorclient.New(ctx,
	tailorclient.WithTokens(accessToken, refreshToken),
	tailorclient.WithPlatformURL(tailorclient.DefaultPlatformURL),
)
```

### Persisting refreshed tokens

By default, tokens refreshed during a session are kept in-memory only. Pass `WithTokenPersist()` to write them back to the SDK config (or keyring, if the user is configured for keyring storage) so other tools stay in sync. Only SDK-config-sourced tokens have an entry to write back to, so this cannot be combined with `WithTokens` or `WithClientCredentials`:

```go
c, err := tailorclient.New(ctx, tailorclient.WithTokenPersist())
```

### Available options

| Option | Description |
|--------|-------------|
| `WithClientCredentials(clientID, clientSecret string)` | Authenticate as a platform machine user via the OAuth2 `client_credentials` grant. Mutually exclusive with `WithTokens` and `WithTokenPersist` |
| `WithTokens(access, refresh string)` | Use supplied tokens instead of reading the SDK config. Mutually exclusive with `WithTokenPersist` |
| `WithPlatformURL(url string)` | Override the Tailor Platform endpoint. See [Platform and client_id resolution](#platform-and-client_id-resolution) for the full precedence |
| `WithOAuth2ClientID(id string)` | Override the OAuth2 `client_id` used for the `refresh_token` grant |
| `WithTokenPersist()` | Write refreshed tokens back to the SDK config (default: off). Requires SDK-config-sourced tokens, so it cannot be combined with `WithTokens` or `WithClientCredentials` |
| `WithHTTPClient(h connect.HTTPClient)` | Override the underlying HTTP client |
| `WithInterceptors(ics ...connect.Interceptor)` | Append additional connect interceptors |

## How it works

1. Resolves the platform endpoint and the OAuth2 `client_id` as described in [Platform and client_id resolution](#platform-and-client_id-resolution)
2. Resolves credentials from the machine user grant (`WithClientCredentials`), the explicit values (`WithTokens`), or the Tailor SDK config
3. If an SDK config token is expired, refreshes it proactively against the OAuth2 token endpoint
4. Builds a connect-go `OperatorServiceClient` wrapped with an interceptor that:
   - Attaches the `Authorization: Bearer <token>` header to every request
   - On an `Unauthenticated` error, obtains a new access token (via the refresh token, or by re-fetching with the machine user credentials) and retries once
5. When `WithTokenPersist()` is set, refreshed tokens are written back to the SDK config entry they came from, or to the keyring

## License

[MIT License](LICENSE)
