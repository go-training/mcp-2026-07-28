package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRunMirrorsAndValidatesHeaders(t *testing.T) {
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
		"tool schema discovered with ttlMs=0",
		"Mcp-Method: tools/call",
		"Mcp-Name: search",
		"Mcp-Protocol-Version: 2026-07-28",
		"Mcp-Param-Region: ap-east-1",
		"query copied to header: false",
		"non-ASCII region header: =?base64?5Y+w5YyX?=",
		"mismatched header status: 400",
		"mismatched header protocol code: -32020",
		"missing Mcp-Method status: 400",
		"missing Mcp-Method protocol code: -32020",
		"rejected requests reached tool handler: false",
		"MCP reserved errors: HeaderMismatch=-32020 MissingRequiredClientCapability=-32021 UnsupportedProtocolVersion=-32022",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}
