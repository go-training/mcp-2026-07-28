package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const deployRequestState = "opaque-deploy-state-v1"

type executionReport struct {
	Result             string
	Rounds             int
	RequestStateEchoed bool
	ToolCallRequestIDs []string
	ResultTypes        []string
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := executeDetailed(ctx, "accept", func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	})
	if err != nil {
		panic(err)
	}
	fmt.Printf("final result: %s (tool handler rounds: %d)\n", report.Result, report.Rounds)
}

// execute retains the small API used by the original example and delegates to
// executeDetailed, which also returns the wire observations used by tests.
func execute(ctx context.Context, action string, printf func(string, ...any)) (string, int, error) {
	report, err := executeDetailed(ctx, action, printf)
	return report.Result, report.Rounds, err
}

func executeDetailed(ctx context.Context, action string, printf func(string, ...any)) (executionReport, error) {
	var report executionReport
	server := mcp.NewServer(&mcp.Implementation{Name: "mrtr-server", Version: "1.0.0"}, nil)
	rounds := 0
	mcp.AddTool(server, &mcp.Tool{Name: "deploy", Description: "Deploy after confirmation"},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			rounds++
			if len(req.Params.InputResponses) == 0 {
				printf("round 1: server returns input_required with requestState")
				return &mcp.CallToolResult{
					InputRequests: mcp.InputRequestMap{
						"approval": &mcp.ElicitParams{
							Message: "Approve production deployment?",
							RequestedSchema: &jsonschema.Schema{
								Type: "object",
								Properties: map[string]*jsonschema.Schema{
									"ticket": {Type: "string"},
								},
								Required: []string{"ticket"},
							},
						},
					},
					RequestState: deployRequestState,
				}, nil, nil
			}

			report.RequestStateEchoed = req.Params.RequestState == deployRequestState
			responseAction, ticket, err := validateApproval(req.Params)
			if err != nil {
				printf("round 3: rejected malformed input: %s", err)
				return toolError("invalid approval response: " + err.Error()), nil, nil
			}
			printf("round 3: retried original call with action=%s", responseAction)
			if responseAction != "accept" {
				return toolError("deployment cancelled"), nil, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "deployed with ticket " + ticket},
			}}, nil, nil
		})

	client := mcp.NewClient(&mcp.Implementation{Name: "mrtr-client", Version: "1.0.0"},
		&mcp.ClientOptions{ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			printf("round 2: client handles elicitation: %s", req.Params.Message)
			responseAction := action
			var content map[string]any
			switch action {
			case "accept":
				content = map[string]any{"ticket": "OPS-2575"}
			case "malformed":
				responseAction = "accept"
				// A whitespace-only string passes the wire schema's type check,
				// but the server still rejects it as invalid business input.
				content = map[string]any{"ticket": " "}
			}
			return &mcp.ElicitResult{Action: responseAction, Content: content}, nil
		}})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return report, err
	}
	defer serverSession.Close()

	var wireLog bytes.Buffer
	clientSession, err := client.Connect(ctx, &mcp.LoggingTransport{
		Transport: clientTransport,
		Writer:    &wireLog,
	}, nil)
	if err != nil {
		return report, err
	}
	defer clientSession.Close()

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "deploy"})
	if err != nil {
		return report, err
	}
	report.Result, err = firstText(result)
	if err != nil {
		return report, err
	}
	report.Rounds = rounds
	report.ToolCallRequestIDs, report.ResultTypes, err = analyzeMRTRWire(wireLog.String())
	if err != nil {
		return report, err
	}
	distinctIDs := len(report.ToolCallRequestIDs) == 2 && report.ToolCallRequestIDs[0] != report.ToolCallRequestIDs[1]
	printf("requestState echoed unchanged: %t", report.RequestStateEchoed)
	printf(
		"wire MRTR: tools/call requests=%d distinct IDs=%t resultTypes=%s",
		len(report.ToolCallRequestIDs),
		distinctIDs,
		strings.Join(report.ResultTypes, ","),
	)
	return report, nil
}

func validateApproval(params *mcp.CallToolParamsRaw) (action, ticket string, err error) {
	if params == nil {
		return "", "", fmt.Errorf("missing tool params")
	}
	if params.RequestState != deployRequestState {
		return "", "", fmt.Errorf("requestState mismatch")
	}
	raw, ok := params.InputResponses["approval"]
	if !ok || raw == nil {
		return "", "", fmt.Errorf("missing approval response")
	}
	response, ok := raw.(*mcp.ElicitResult)
	if !ok || response == nil {
		return "", "", fmt.Errorf("approval has unexpected type %T", raw)
	}
	switch response.Action {
	case "decline", "cancel":
		return response.Action, "", nil
	case "accept":
		value, ok := response.Content["ticket"]
		if !ok {
			return "", "", fmt.Errorf("missing ticket")
		}
		ticket, ok := value.(string)
		if !ok || strings.TrimSpace(ticket) == "" {
			return "", "", fmt.Errorf("ticket must be a non-empty string")
		}
		return response.Action, ticket, nil
	default:
		return "", "", fmt.Errorf("unsupported action %q", response.Action)
	}
}

func toolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
	}
}

func firstText(result *mcp.CallToolResult) (string, error) {
	if result == nil || len(result.Content) == 0 {
		return "", fmt.Errorf("tool result has no content")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text == nil {
		return "", fmt.Errorf("tool result content has unexpected type %T", result.Content[0])
	}
	return text.Text, nil
}

func analyzeMRTRWire(logText string) ([]string, []string, error) {
	var toolCallIDs []string
	resultTypeByID := make(map[string]string)
	for line := range strings.SplitSeq(logText, "\n") {
		var payload string
		switch {
		case strings.HasPrefix(line, "write: "):
			payload = strings.TrimPrefix(line, "write: ")
		case strings.HasPrefix(line, "read: "):
			payload = strings.TrimPrefix(line, "read: ")
		default:
			continue
		}
		var envelope struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			return nil, nil, fmt.Errorf("decode logged JSON-RPC message: %w", err)
		}
		id := fmt.Sprint(envelope.ID)
		if envelope.Method == "tools/call" {
			toolCallIDs = append(toolCallIDs, id)
		}
		if len(envelope.Result) > 0 {
			var result struct {
				ResultType string `json:"resultType"`
			}
			if err := json.Unmarshal(envelope.Result, &result); err != nil {
				return nil, nil, fmt.Errorf("decode logged JSON-RPC result: %w", err)
			}
			if result.ResultType != "" {
				resultTypeByID[id] = result.ResultType
			}
		}
	}
	if len(toolCallIDs) != 2 {
		return nil, nil, fmt.Errorf("observed %d tools/call requests, want 2", len(toolCallIDs))
	}
	resultTypes := make([]string, 0, len(toolCallIDs))
	for _, id := range toolCallIDs {
		resultType, ok := resultTypeByID[id]
		if !ok {
			return nil, nil, fmt.Errorf("tools/call %s has no resultType on wire", id)
		}
		resultTypes = append(resultTypes, resultType)
	}
	return toolCallIDs, resultTypes, nil
}
