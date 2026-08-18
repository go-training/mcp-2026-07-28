package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mutationMethodMismatch = "method-mismatch"
	mutationMissingMethod  = "missing-method"
)

type errorObservation struct {
	Status  int
	Code    int
	Message string
	Body    string
}

type recordingTransport struct {
	mu                sync.Mutex
	callHeaders       http.Header
	nextMutation      string
	errorObservations map[string]errorObservation
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	isToolCall := req.Header.Get("Mcp-Method") == "tools/call"
	t.mu.Lock()
	mutation := ""
	if isToolCall {
		mutation = t.nextMutation
		t.nextMutation = ""
	}
	if isToolCall && mutation == "" {
		t.callHeaders = req.Header.Clone()
	}
	t.mu.Unlock()

	switch mutation {
	case mutationMethodMismatch:
		req.Header.Set("Mcp-Method", "prompts/get")
	case mutationMissingMethod:
		req.Header.Del("Mcp-Method")
	}

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || mutation == "" {
		return resp, err
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	observation := errorObservation{Status: resp.StatusCode, Body: string(body)}
	var wire struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &wire); err == nil && wire.Error != nil {
		observation.Code = wire.Error.Code
		observation.Message = wire.Error.Message
	}
	t.mu.Lock()
	if t.errorObservations == nil {
		t.errorObservations = make(map[string]errorObservation)
	}
	t.errorObservations[mutation] = observation
	t.mu.Unlock()
	return resp, nil
}

func (t *recordingTransport) mutateOneCall(mutation string) {
	t.mu.Lock()
	t.nextMutation = mutation
	t.mu.Unlock()
}

func (t *recordingTransport) latestCallHeaders() http.Header {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.callHeaders.Clone()
}

func (t *recordingTransport) errorObservation(mutation string) (errorObservation, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	observation, ok := t.errorObservations[mutation]
	return observation, ok
}

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
	var toolCalls atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "header-server", Version: "1.0.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "search",
		Description: "Search within a region",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"region": map[string]any{
					"type": "string", "x-mcp-header": "Region",
				},
				"query": map[string]any{"type": "string"},
			},
		},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		toolCalls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "search completed"},
		}}, nil
	})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	recorder := &recordingTransport{}
	httpClient := &http.Client{Transport: recorder}
	connect := func(name string) (*mcp.ClientSession, *mcp.ListToolsResult, error) {
		client := mcp.NewClient(&mcp.Implementation{Name: name, Version: "1.0.0"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: httpServer.URL, HTTPClient: httpClient,
		}, nil)
		if err != nil {
			return nil, nil, err
		}
		listed, err := session.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			session.Close()
			return nil, nil, err
		}
		return session, listed, nil
	}

	session, listed, err := connect("header-client")
	if err != nil {
		return err
	}
	defer session.Close()
	printf("tool schema discovered with ttlMs=%d", listed.TTLMs)

	params := &mcp.CallToolParams{Name: "search", Arguments: map[string]any{
		"region": "ap-east-1", "query": "MCP 2026-07-28",
	}}
	if _, err := session.CallTool(ctx, params); err != nil {
		return fmt.Errorf("valid call: %w", err)
	}
	headers := recorder.latestCallHeaders()
	for _, name := range []string{"Mcp-Method", "Mcp-Name", "Mcp-Protocol-Version", "Mcp-Param-Region"} {
		printf("%s: %s", name, headers.Get(name))
	}
	printf("query copied to header: %t", headers.Get("Mcp-Param-Query") != "")

	nonASCIIParams := &mcp.CallToolParams{Name: "search", Arguments: map[string]any{
		"region": "台北", "query": "MCP header encoding",
	}}
	if _, err := session.CallTool(ctx, nonASCIIParams); err != nil {
		return fmt.Errorf("non-ASCII call: %w", err)
	}
	printf("non-ASCII region header: %s", recorder.latestCallHeaders().Get("Mcp-Param-Region"))

	validToolCalls := toolCalls.Load()
	recorder.mutateOneCall(mutationMethodMismatch)
	_, mismatchErr := session.CallTool(ctx, params)
	if mismatchErr == nil {
		return fmt.Errorf("corrupted header unexpectedly succeeded")
	}
	mismatch, ok := recorder.errorObservation(mutationMethodMismatch)
	if !ok {
		return fmt.Errorf("missing observation for mismatched header")
	}
	printf("mismatched header status: %d", mismatch.Status)
	printf("mismatched header protocol code: %d", mismatch.Code)

	// A protocol-level HTTP error closes this SDK client connection. Use a new
	// independent session to demonstrate a second negative wire case.
	missingSession, _, err := connect("header-client-missing")
	if err != nil {
		return fmt.Errorf("connect for missing-header case: %w", err)
	}
	defer missingSession.Close()
	recorder.mutateOneCall(mutationMissingMethod)
	_, missingErr := missingSession.CallTool(ctx, params)
	if missingErr == nil {
		return fmt.Errorf("missing Mcp-Method header unexpectedly succeeded")
	}
	missing, ok := recorder.errorObservation(mutationMissingMethod)
	if !ok {
		return fmt.Errorf("missing observation for absent Mcp-Method header (client error: %v)", missingErr)
	}
	printf("missing Mcp-Method status: %d", missing.Status)
	printf("missing Mcp-Method protocol code: %d", missing.Code)
	printf("rejected requests reached tool handler: %t", toolCalls.Load() != validToolCalls)
	printf("MCP reserved errors: HeaderMismatch=%d MissingRequiredClientCapability=%d UnsupportedProtocolVersion=%d",
		mcp.CodeHeaderMismatch, mcp.CodeMissingRequiredClientCapabilities, mcp.CodeUnsupportedProtocolVersion)
	return nil
}
