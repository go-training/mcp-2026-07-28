package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type workflowArgs struct {
	WorkspaceURI string `json:"workspaceURI" jsonschema:"explicit workspace URI; replaces implicit roots"`
	StateHandle  string `json:"stateHandle,omitempty" jsonschema:"server-minted handle returned by the previous call"`
	Instruction  string `json:"instruction" jsonschema:"work requested by the client or model"`
}

type workflowState struct {
	Step int
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Keep application logs off stdout so this pattern remains safe if the
	// example is moved to the STDIO transport, where stdout carries MCP frames.
	if err := run(ctx, os.Stderr, func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	}); err != nil {
		panic(err)
	}
}

func run(ctx context.Context, logOutput io.Writer, printf func(string, ...any)) error {
	var mu sync.Mutex
	states := make(map[string]*workflowState)
	logger := slog.New(slog.NewTextHandler(logOutput, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		},
	}))

	server := mcp.NewServer(&mcp.Implementation{Name: "modern-server", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "workflow_step", Description: "Explicit, sessionless workflow"},
		func(_ context.Context, _ *mcp.CallToolRequest, args workflowArgs) (*mcp.CallToolResult, any, error) {
			mu.Lock()
			defer mu.Unlock()
			handle := args.StateHandle
			if handle == "" {
				handle = "job-42"
				states[handle] = &workflowState{}
			}
			state, ok := states[handle]
			if !ok {
				return nil, nil, fmt.Errorf("unknown state handle %q", handle)
			}
			state.Step++
			logger.Info("workflow step", "handle", handle, "step", state.Step,
				"workspace", args.WorkspaceURI, "instruction", args.Instruction)
			text := fmt.Sprintf("handle=%s step=%d workspace=%s", handle, state.Step, args.WorkspaceURI)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
		})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return err
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "modern-client", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return err
	}
	defer clientSession.Close()

	call := func(handle, instruction string) (string, error) {
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
			Name: "workflow_step",
			Arguments: map[string]any{
				"workspaceURI": "file:///workspace",
				"stateHandle":  handle,
				"instruction":  instruction,
			},
		})
		if err != nil {
			return "", err
		}
		return result.Content[0].(*mcp.TextContent).Text, nil
	}

	first, err := call("", "scan files")
	if err != nil {
		return err
	}
	printf("first call: %s", first)
	second, err := call("job-42", "apply change")
	if err != nil {
		return err
	}
	printf("second call: %s", second)
	printf("roots replacement: workspaceURI is an explicit tool argument")
	printf("sampling replacement: the client/application owns model calls")
	printf("logging replacement: use slog/stderr or OpenTelemetry outside MCP")
	printf("feature lifecycle: Active -> Deprecated -> Removed (minimum 12-month deprecation window)")
	printf("transport migration: legacy HTTP+SSE -> stateless Streamable HTTP")
	if !strings.Contains(second, "step=2") {
		return fmt.Errorf("state handle was not continued")
	}
	return nil
}
