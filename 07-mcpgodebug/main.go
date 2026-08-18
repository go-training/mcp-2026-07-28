package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const scenarioEnv = "MCPGODEBUG_SCENARIO"

type compatibilityScenario struct {
	flag string
	name string
}

var compatibilityScenarios = []compatibilityScenario{
	{flag: "customresnotfounderrcode", name: "resource-not-found"},
	{flag: "hintomitempty", name: "tool-hints"},
	{flag: "allowsessionsinstateless", name: "stateless-session"},
	{flag: "nomethodnotfoundcodeinerror", name: "method-not-found"},
	{flag: "noprotocolerrorbody", name: "protocol-error-body"},
	{flag: "nowrapinvalidparams", name: "invalid-params"},
	{flag: "disablecompleteparamsvalidation", name: "complete-validation"},
}

type customParams struct {
	mcp.ParamsBase
	Count int `json:"count"`
}

type customResult struct {
	mcp.ResultBase
	OK bool `json:"ok"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if scenario := os.Getenv(scenarioEnv); scenario != "" {
		observation, err := inspectScenario(ctx, scenario)
		if err != nil {
			panic(err)
		}
		fmt.Println(observation)
		return
	}

	if err := compareAll(ctx, os.Stdout); err != nil {
		panic(err)
	}
}

func compareAll(ctx context.Context, out io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Go SDK v1.7.0 MCPGODEBUG comparisons:")
	for _, scenario := range compatibilityScenarios {
		defaultObservation, err := runScenarioProcess(ctx, executable, scenario.name, "")
		if err != nil {
			return fmt.Errorf("%s default: %w", scenario.flag, err)
		}
		compatObservation, err := runScenarioProcess(ctx, executable, scenario.name, scenario.flag+"=1")
		if err != nil {
			return fmt.Errorf("%s compatibility: %w", scenario.flag, err)
		}
		fmt.Fprintf(out, "%s\n  default: %s\n  =1:      %s\n", scenario.flag, defaultObservation, compatObservation)
	}
	return nil
}

func runScenarioProcess(ctx context.Context, executable, scenario, debug string) (string, error) {
	cmd := exec.CommandContext(ctx, executable)
	cmd.Env = scenarioEnvironment(debug, scenario)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func scenarioEnvironment(debug, scenario string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "MCPGODEBUG=") || strings.HasPrefix(entry, scenarioEnv+"=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "MCPGODEBUG="+debug, scenarioEnv+"="+scenario)
}

func inspectScenario(ctx context.Context, scenario string) (string, error) {
	switch scenario {
	case "resource-not-found":
		return fmt.Sprintf("code=%d", mcp.CodeResourceNotFound), nil
	case "tool-hints":
		data, err := json.Marshal(mcp.ToolAnnotations{})
		return "json=" + string(data), err
	case "stateless-session":
		status, err := statelessDeleteStatus(ctx)
		return fmt.Sprintf("DELETE status=%d", status), err
	case "method-not-found":
		code, err := methodNotFoundCode(ctx)
		return fmt.Sprintf("code=%d", code), err
	case "protocol-error-body":
		code, err := protocolErrorBodyCode(ctx)
		return fmt.Sprintf("surfaced code=%d", code), err
	case "invalid-params":
		code, err := invalidParamsCode(ctx)
		return fmt.Sprintf("code=%d", code), err
	case "complete-validation":
		called, code, err := completeValidation(ctx)
		return fmt.Sprintf("handler-called=%t code=%d", called, code), err
	default:
		return "", fmt.Errorf("unknown scenario %q", scenario)
	}
}

func statelessDeleteStatus(ctx context.Context) (int, error) {
	server := mcp.NewServer(&mcp.Implementation{Name: "debug-server", Version: "1.0.0"}, nil)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, httpServer.URL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Mcp-Session-Id", "compat-session")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func methodNotFoundCode(ctx context.Context) (int64, error) {
	server := mcp.NewServer(&mcp.Implementation{Name: "debug-server", Version: "1.0.0"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return 0, err
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "debug-client", Version: "1.0.0"}, nil)
	if err := mcp.AddSendingCustomMethod[*customParams, *customResult](client, "example/missing"); err != nil {
		return 0, err
	}
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return 0, err
	}
	defer clientSession.Close()

	_, callErr := mcp.CallCustomMethod[*customParams, *customResult](ctx, clientSession, "example/missing", &customParams{})
	if callErr == nil {
		return 0, errors.New("unhandled custom method unexpectedly succeeded")
	}
	return jsonRPCErrorCode(callErr), nil
}

func protocolErrorBodyCode(ctx context.Context) (int64, error) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"unsupported protocol version"}}`)
	}))
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "debug-client", Version: "1.0.0"}, nil)
	_, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err == nil {
		return 0, errors.New("non-2xx protocol response unexpectedly succeeded")
	}
	return jsonRPCErrorCode(err), nil
}

func invalidParamsCode(ctx context.Context) (int64, error) {
	server := mcp.NewServer(&mcp.Implementation{Name: "debug-server", Version: "1.0.0"}, nil)
	if err := mcp.AddReceivingCustomMethod(server, "example/validate",
		func(context.Context, *mcp.ServerSession, *customParams) (*customResult, error) {
			return &customResult{OK: true}, nil
		}); err != nil {
		return 0, err
	}
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"example/validate","params":{"count":"not-an-integer","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"raw-client","version":"1.0.0"}}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "example/validate")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	var envelope struct {
		Error *jsonrpc.Error `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return 0, fmt.Errorf("decode response %q: %w", data, err)
	}
	if envelope.Error == nil {
		return 0, fmt.Errorf("response has no JSON-RPC error: %s", data)
	}
	return envelope.Error.Code, nil
}

func completeValidation(ctx context.Context) (bool, int64, error) {
	handlerCalled := false
	server := mcp.NewServer(&mcp.Implementation{Name: "debug-server", Version: "1.0.0"},
		&mcp.ServerOptions{CompletionHandler: func(context.Context, *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
			handlerCalled = true
			return &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{Values: []string{"ok"}}}, nil
		}})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return false, 0, err
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "debug-client", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return false, 0, err
	}
	defer clientSession.Close()
	_, callErr := clientSession.Complete(ctx, &mcp.CompleteParams{})
	return handlerCalled, jsonRPCErrorCode(callErr), nil
}

func jsonRPCErrorCode(err error) int64 {
	if err == nil {
		return 0
	}
	var wireError *jsonrpc.Error
	if errors.As(err, &wireError) {
		return wireError.Code
	}
	return 0
}
