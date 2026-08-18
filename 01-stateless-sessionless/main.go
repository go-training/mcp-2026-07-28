package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoArgs struct {
	Message string `json:"message" jsonschema:"message to echo"`
}

type requestObservation struct {
	Method       string
	MCPMethod    string
	Protocol     string
	HasSessionID bool
}

type requestMetadataObservation struct {
	Protocol            string
	ClientName          string
	CapabilitiesPresent bool
}

type unsupportedProtocolObservation struct {
	Status    int
	Code      int
	Requested string
	Supported []string
}

type statelessReport struct {
	Metadata          requestMetadataObservation
	ServerInfoPresent bool
	Unsupported       unsupportedProtocolObservation
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
	_, err := runScenario(ctx, printf)
	return err
}

func runScenario(ctx context.Context, printf func(string, ...any)) (statelessReport, error) {
	var report statelessReport
	metadataSeen := make(chan requestMetadataObservation, 1)

	server := mcp.NewServer(&mcp.Implementation{
		Name: "stateless-server", Version: "1.0.0",
	}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo a message"},
		func(_ context.Context, req *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
			clientName := ""
			if info := req.ClientInfo(); info != nil {
				clientName = info.Name
			}
			metadataSeen <- requestMetadataObservation{
				Protocol:            req.ProtocolVersion(),
				ClientName:          clientName,
				CapabilitiesPresent: req.ClientCapabilities() != nil,
			}
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: args.Message},
			}}, nil, nil
		})

	var (
		observationsMu sync.Mutex
		observations   []requestObservation
	)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observationsMu.Lock()
		observations = append(observations, requestObservation{
			Method:       r.Method,
			MCPMethod:    r.Header.Get("Mcp-Method"),
			Protocol:     r.Header.Get("Mcp-Protocol-Version"),
			HasSessionID: r.Header.Get("Mcp-Session-Id") != "",
		})
		observationsMu.Unlock()
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{
		Name: "stateless-client", Version: "1.0.0",
	}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		return report, fmt.Errorf("connect: %w", err)
	}
	defer session.Close()

	printf("negotiated protocol: %s", session.InitializeResult().ProtocolVersion)
	printf("client session ID: %q", session.ID())

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "echo", Arguments: map[string]any{"message": "hello, stateless MCP"},
	})
	if err != nil {
		return report, fmt.Errorf("call echo: %w", err)
	}
	printf("tool result: %s", result.Content[0].(*mcp.TextContent).Text)

	select {
	case report.Metadata = <-metadataSeen:
	case <-ctx.Done():
		return report, fmt.Errorf("wait for per-request metadata: %w", ctx.Err())
	}
	printf(
		"per-request metadata: protocol=%s client=%s capabilities-present=%t",
		report.Metadata.Protocol,
		report.Metadata.ClientName,
		report.Metadata.CapabilitiesPresent,
	)
	_, report.ServerInfoPresent = result.GetMeta()[mcp.MetaKeyServerInfo]
	printf("result serverInfo present: %t", report.ServerInfoPresent)

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req, err := http.NewRequestWithContext(ctx, method, httpServer.URL, nil)
		if err != nil {
			return report, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return report, err
		}
		resp.Body.Close()
		printf("%s status: %d", method, resp.StatusCode)
	}

	report.Unsupported, err = requestUnsupportedProtocol(ctx, httpServer.URL)
	if err != nil {
		return report, err
	}
	printf(
		"unsupported protocol: status=%d code=%d requested=%s supported=%s",
		report.Unsupported.Status,
		report.Unsupported.Code,
		report.Unsupported.Requested,
		strings.Join(report.Unsupported.Supported, ","),
	)

	observationsMu.Lock()
	observed := append([]requestObservation(nil), observations...)
	observationsMu.Unlock()
	requestCount := 0
	hasSessionID := false
	for _, obs := range observed {
		if obs.Method == http.MethodPost {
			requestCount++
		}
		hasSessionID = hasSessionID || obs.HasSessionID
		if obs.MCPMethod != "" {
			printf("wire request: method=%s protocol=%s session-header=%t", obs.MCPMethod, obs.Protocol, obs.HasSessionID)
		}
	}
	printf("independent POST requests: %d; any session header: %t", requestCount, hasSessionID)
	return report, nil
}

func requestUnsupportedProtocol(ctx context.Context, endpoint string) (unsupportedProtocolObservation, error) {
	const requestedVersion = "2099-01-01"
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      99,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"message": "not executed"},
			"_meta": map[string]any{
				mcp.MetaKeyProtocolVersion: requestedVersion,
				mcp.MetaKeyClientInfo: map[string]any{
					"name": "raw-client", "version": "1.0.0",
				},
				mcp.MetaKeyClientCapabilities: map[string]any{},
			},
		},
	})
	if err != nil {
		return unsupportedProtocolObservation{}, fmt.Errorf("marshal unsupported-version request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return unsupportedProtocolObservation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", requestedVersion)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "echo")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return unsupportedProtocolObservation{}, fmt.Errorf("send unsupported-version request: %w", err)
	}
	defer resp.Body.Close()
	wireBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return unsupportedProtocolObservation{}, fmt.Errorf("read unsupported-version response: %w", err)
	}
	var wire struct {
		Error *struct {
			Code int `json:"code"`
			Data struct {
				Requested string   `json:"requested"`
				Supported []string `json:"supported"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(wireBody, &wire); err != nil {
		return unsupportedProtocolObservation{}, fmt.Errorf("decode unsupported-version response %q: %w", wireBody, err)
	}
	if wire.Error == nil {
		return unsupportedProtocolObservation{}, fmt.Errorf("unsupported-version response has no JSON-RPC error: %s", wireBody)
	}
	return unsupportedProtocolObservation{
		Status:    resp.StatusCode,
		Code:      wire.Error.Code,
		Requested: wire.Error.Data.Requested,
		Supported: wire.Error.Data.Supported,
	}, nil
}
