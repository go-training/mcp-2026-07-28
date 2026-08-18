package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func probeForTest(t *testing.T, advertiseExtension bool) *probeOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server, err := newExtensionServer()
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := callExtensionProbe(ctx, server, advertiseExtension)
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

func TestServerExtensionAdvertisedByDiscover(t *testing.T) {
	outcome := probeForTest(t, true)
	if !outcome.ServerAdvertised {
		t.Fatalf("server discovery did not advertise %q", exampleExtensionID)
	}
	if outcome.OfficialTasksAdvertised {
		t.Fatalf("generic example unexpectedly advertised %q", officialTasksExtensionID)
	}
}

func TestClientExtensionIsPresentPerRequest(t *testing.T) {
	outcome := probeForTest(t, true)
	if !outcome.ClientAdvertised {
		t.Fatalf("server did not observe %q in per-request capabilities", exampleExtensionID)
	}
	if outcome.Mode != "extension-aware" {
		t.Fatalf("mode = %q, want extension-aware", outcome.Mode)
	}
}

func TestUnsupportedClientUsesCoreFallback(t *testing.T) {
	outcome := probeForTest(t, false)
	if outcome.ClientAdvertised {
		t.Fatalf("unsupported client unexpectedly advertised %q", exampleExtensionID)
	}
	if outcome.Mode != "core-fallback" {
		t.Fatalf("mode = %q, want core-fallback", outcome.Mode)
	}
}

func TestNilExtensionSettingsEncodeAsObject(t *testing.T) {
	capabilities := &mcp.ClientCapabilities{}
	capabilities.AddExtension(exampleExtensionID, nil)
	wire, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Extensions map[string]json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	settings, ok := decoded.Extensions[exampleExtensionID]
	if !ok {
		t.Fatalf("encoded capabilities missing %q: %s", exampleExtensionID, wire)
	}
	if string(settings) != "{}" {
		t.Fatalf("extension settings = %s, want {}", settings)
	}
}

func TestExampleDoesNotRegisterOfficialTaskMethods(t *testing.T) {
	for _, method := range registeredMethodInventory() {
		if strings.HasPrefix(method, "tasks"+"/") || method == "notifications"+"/tasks" {
			t.Fatalf("example registered reserved Tasks method %q", method)
		}
	}
	if got := registeredMethodInventory(); len(got) != 1 || got[0] != probeMethod {
		t.Fatalf("registered methods = %v, want only %q", got, probeMethod)
	}
}

func TestServerReturnsMethodNotFoundForOfficialTaskMethods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server, err := newExtensionServer()
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "boundary-client", Version: "1.0.0"}, nil)
	for _, method := range []string{"tasks/get", "tasks/update", "tasks/cancel"} {
		if err := mcp.AddSendingCustomMethod[*extensionProbeParams, *extensionProbeResult](client, method); err != nil {
			t.Fatalf("register client method %q: %v", method, err)
		}
	}
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	for _, method := range []string{"tasks/get", "tasks/update", "tasks/cancel"} {
		_, err := mcp.CallCustomMethod[*extensionProbeParams, *extensionProbeResult](
			ctx, clientSession, method, &extensionProbeParams{},
		)
		var wireError *jsonrpc.Error
		if !errors.As(err, &wireError) || wireError.Code != jsonrpc.CodeMethodNotFound {
			t.Errorf("%s error = %v, want MethodNotFound (%d)", method, err, jsonrpc.CodeMethodNotFound)
		}
	}
}

func TestRunShowsNegotiationAndFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lines []string
	if err := run(ctx, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"server discover advertises com.example/extension-probe: true",
		"supported client per-request extension: true",
		"supported client path: extension-aware",
		"unsupported client per-request extension: false",
		"unsupported client path: core-fallback",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}
