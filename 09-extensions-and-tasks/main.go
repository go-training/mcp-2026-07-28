package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	exampleExtensionID       = "com.example/extension-probe"
	officialTasksExtensionID = "io.modelcontextprotocol/tasks"
	probeMethod              = "example/extension-probe"
)

var registeredExampleMethods = []string{probeMethod}

type extensionProbeParams struct {
	mcp.ParamsBase
}

type extensionProbeResult struct {
	mcp.ResultBase
	Mode             string `json:"mode"`
	ClientAdvertised bool   `json:"clientAdvertised"`
}

type clientCapabilityContextKey struct{}

type probeOutcome struct {
	ServerAdvertised        bool
	OfficialTasksAdvertised bool
	ClientAdvertised        bool
	Mode                    string
}

func hasExtension(extensions map[string]any, identifier string) bool {
	_, ok := extensions[identifier]
	return ok
}

func newExtensionServer() (*mcp.Server, error) {
	serverCapabilities := &mcp.ServerCapabilities{}
	serverCapabilities.AddExtension(exampleExtensionID, nil)
	server := mcp.NewServer(
		&mcp.Implementation{Name: "extension-server", Version: "1.0.0"},
		&mcp.ServerOptions{Capabilities: serverCapabilities},
	)

	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method != probeMethod {
				return next(ctx, method, request)
			}
			probeRequest, ok := request.(*mcp.ServerRequest[*extensionProbeParams])
			if !ok {
				return nil, fmt.Errorf("unexpected probe request type %T", request)
			}
			clientCapabilities := probeRequest.ClientCapabilities()
			clientAdvertised := clientCapabilities != nil &&
				hasExtension(clientCapabilities.Extensions, exampleExtensionID)
			ctx = context.WithValue(ctx, clientCapabilityContextKey{}, clientAdvertised)
			return next(ctx, method, request)
		}
	})

	if err := mcp.AddReceivingCustomMethod(
		server,
		probeMethod,
		func(ctx context.Context, _ *mcp.ServerSession, _ *extensionProbeParams) (*extensionProbeResult, error) {
			clientAdvertised, _ := ctx.Value(clientCapabilityContextKey{}).(bool)
			mode := "core-fallback"
			if clientAdvertised {
				mode = "extension-aware"
			}
			return &extensionProbeResult{
				Mode:             mode,
				ClientAdvertised: clientAdvertised,
			}, nil
		},
	); err != nil {
		return nil, err
	}
	return server, nil
}

func callExtensionProbe(ctx context.Context, server *mcp.Server, advertiseExtension bool) (*probeOutcome, error) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect server: %w", err)
	}
	defer serverSession.Close()

	clientCapabilities := &mcp.ClientCapabilities{}
	if advertiseExtension {
		clientCapabilities.AddExtension(exampleExtensionID, nil)
	}
	client := mcp.NewClient(
		&mcp.Implementation{Name: "extension-client", Version: "1.0.0"},
		&mcp.ClientOptions{Capabilities: clientCapabilities},
	)
	if err := mcp.AddSendingCustomMethod[*extensionProbeParams, *extensionProbeResult](client, probeMethod); err != nil {
		return nil, fmt.Errorf("register probe method: %w", err)
	}
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect client: %w", err)
	}
	defer clientSession.Close()

	initializeResult := clientSession.InitializeResult()
	var serverExtensions map[string]any
	if initializeResult != nil && initializeResult.Capabilities != nil {
		serverExtensions = initializeResult.Capabilities.Extensions
	}
	serverAdvertised := hasExtension(serverExtensions, exampleExtensionID)
	officialTasksAdvertised := hasExtension(serverExtensions, officialTasksExtensionID)
	result, err := mcp.CallCustomMethod[*extensionProbeParams, *extensionProbeResult](
		ctx,
		clientSession,
		probeMethod,
		&extensionProbeParams{},
	)
	if err != nil {
		return nil, fmt.Errorf("call probe method: %w", err)
	}
	return &probeOutcome{
		ServerAdvertised:        serverAdvertised,
		OfficialTasksAdvertised: officialTasksAdvertised,
		ClientAdvertised:        result.ClientAdvertised,
		Mode:                    result.Mode,
	}, nil
}

func registeredMethodInventory() []string {
	return slices.Clone(registeredExampleMethods)
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
	server, err := newExtensionServer()
	if err != nil {
		return err
	}

	supported, err := callExtensionProbe(ctx, server, true)
	if err != nil {
		return err
	}
	printf("server discover advertises %s: %t", exampleExtensionID, supported.ServerAdvertised)
	printf("supported client per-request extension: %t", supported.ClientAdvertised)
	printf("supported client path: %s", supported.Mode)

	unsupported, err := callExtensionProbe(ctx, server, false)
	if err != nil {
		return err
	}
	printf("unsupported client per-request extension: %t", unsupported.ClientAdvertised)
	printf("unsupported client path: %s", unsupported.Mode)
	return nil
}
