package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRunShowsSessionlessHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lines []string
	report, err := runScenario(ctx, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"negotiated protocol: 2026-07-28",
		`client session ID: ""`,
		"per-request metadata: protocol=2026-07-28 client=stateless-client capabilities-present=true",
		"result serverInfo present: true",
		"GET status: 405",
		"DELETE status: 405",
		"any session header: false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if report.Metadata.Protocol != "2026-07-28" || report.Metadata.ClientName != "stateless-client" || !report.Metadata.CapabilitiesPresent {
		t.Errorf("unexpected per-request metadata: %+v", report.Metadata)
	}
	if !report.ServerInfoPresent {
		t.Error("CallTool result is missing serverInfo metadata")
	}
}

func TestUnsupportedProtocolVersionReturnsStructuredError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := runScenario(ctx, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	got := report.Unsupported
	if got.Status != 400 {
		t.Errorf("HTTP status = %d, want 400", got.Status)
	}
	if got.Code != mcp.CodeUnsupportedProtocolVersion {
		t.Errorf("JSON-RPC code = %d, want %d", got.Code, mcp.CodeUnsupportedProtocolVersion)
	}
	if got.Requested != "2099-01-01" {
		t.Errorf("requested version = %q, want 2099-01-01", got.Requested)
	}
	if !slices.Contains(got.Supported, "2026-07-28") {
		t.Errorf("supported versions %v do not contain 2026-07-28", got.Supported)
	}
}
