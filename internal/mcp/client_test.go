package mcp_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// The official SDK client can initialize, list tools and call one: the server
// speaks real streamable HTTP, not only the raw requests of the other tests.
func TestOfficialClientRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-agent", Version: "1"}, nil)
	sess, err := client.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint:   e.http.URL,
		HTTPClient: &http.Client{Transport: bearerTransport{token: e.token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()
	tools, err := sess.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Fatalf("list tools: %v %v", tools, err)
	}
	res, err := sess.CallTool(ctx, &sdk.CallToolParams{Name: "list_repos"})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if tc, ok := res.Content[0].(*sdk.TextContent); !ok || !strings.Contains(tc.Text, "shop-api") {
		t.Fatalf("content %+v", res.Content)
	}
	// A wrong token never connects.
	bad := sdk.NewClient(&sdk.Implementation{Name: "x", Version: "1"}, nil)
	if s, err := bad.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint: e.http.URL, HTTPClient: &http.Client{Transport: bearerTransport{token: "nope"}},
	}, nil); err == nil {
		s.Close()
		t.Fatal("connected with a wrong token")
	}
}
