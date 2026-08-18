package main

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const cacheTTL = 120 * time.Millisecond

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	}); err != nil {
		panic(err)
	}
}

func run(ctx context.Context, printf func(string, ...any)) error {
	server := mcp.NewServer(&mcp.Implementation{Name: "cache-server", Version: "1.0.0"}, nil)
	// Register in a deliberately non-sorted order. go-sdk v1.7.0's feature
	// registry returns list results in ascending unique-ID order, independently
	// of map iteration and registration order.
	for _, name := range []string{"zeta-tool", "alpha-tool", "stable-tool"} {
		mcp.AddTool(server, &mcp.Tool{Name: name},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{}, nil, nil
			})
	}

	var serverCalls atomic.Int32
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err == nil && method == "tools/list" {
				serverCalls.Add(1)
				list := result.(*mcp.ListToolsResult)
				list.TTLMs = int(cacheTTL.Milliseconds())
				list.CacheScope = "private"
			}
			return result, err
		}
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return err
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "cache-client", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return err
	}
	defer clientSession.Close()

	first, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return err
	}
	firstOrder := toolNames(first.Tools)
	printf("first list: server calls=%d ttlMs=%d scope=%s order=%s",
		serverCalls.Load(), first.TTLMs, first.CacheScope, firstOrder)

	insideTTL, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return err
	}
	printf("inside TTL: server calls=%d (cache hit; order=%s)", serverCalls.Load(), toolNames(insideTTL.Tools))

	time.Sleep(cacheTTL + 30*time.Millisecond)
	afterTTL, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return err
	}
	afterOrder := toolNames(afterTTL.Tools)
	printf("after TTL: server calls=%d (re-fetched; order=%s)", serverCalls.Load(), afterOrder)
	if firstOrder != afterOrder {
		return fmt.Errorf("tools/list order changed across re-fetch: first=%q after=%q", firstOrder, afterOrder)
	}
	return nil
}

func toolNames(tools []*mcp.Tool) string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	return strings.Join(names, ",")
}
