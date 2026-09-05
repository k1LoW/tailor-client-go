package tailorclient_test

import (
	"context"
	"fmt"
	"log"
	"os"

	tailorv1 "buf.build/gen/go/tailor-inc/tailor/protocolbuffers/go/tailor/v1"
	"connectrpc.com/connect"

	tailorclient "github.com/k1LoW/tailor-client-go"
)

// Example builds an authenticated client from the SDK config and calls an
// OperatorService RPC. The RPC method is promoted from the embedded
// tailorv1connect.OperatorServiceClient, so it is callable directly on *Client.
func Example() {
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

// ExampleNew_explicitTokens passes tokens explicitly instead of reading the SDK config.
func ExampleNew_explicitTokens() {
	ctx := context.Background()
	_, err := tailorclient.New(ctx,
		tailorclient.WithTokens("access-token", "refresh-token"),
		tailorclient.WithPlatformURL(tailorclient.DefaultPlatformURL),
	)
	if err != nil {
		log.Fatal(err)
	}
}

// ExampleNew_clientCredentials authenticates as a platform machine user via
// the OAuth2 client_credentials grant. This is the option for CI and other
// unattended callers: it needs no `npx tailor-sdk login` and never touches the
// SDK config.
func ExampleNew_clientCredentials() { //nostyle:repetition
	ctx := context.Background()
	_, err := tailorclient.New(ctx,
		tailorclient.WithClientCredentials(os.Getenv("TAILOR_CLIENT_ID"), os.Getenv("TAILOR_CLIENT_SECRET")),
	)
	if err != nil {
		log.Fatal(err)
	}
}

// ExampleNew_persistTokens enables SDK config writeback on token refresh.
// Disabled by default.
func ExampleNew_persistTokens() {
	ctx := context.Background()
	_, err := tailorclient.New(ctx, tailorclient.WithTokenPersist())
	if err != nil {
		log.Fatal(err)
	}
}
