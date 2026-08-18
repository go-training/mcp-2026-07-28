package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRunUsesExplicitReplacementPatterns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lines []string
	var logs bytes.Buffer
	if err := run(ctx, &logs, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"first call: handle=job-42 step=1 workspace=file:///workspace",
		"second call: handle=job-42 step=2 workspace=file:///workspace",
		"roots replacement:",
		"sampling replacement:",
		"logging replacement:",
		"feature lifecycle: Active -> Deprecated -> Removed (minimum 12-month deprecation window)",
		"transport migration: legacy HTTP+SSE -> stateless Streamable HTTP",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(logs.String(), `level=INFO msg="workflow step"`) {
		t.Fatalf("application log was not written to the dedicated log writer:\n%s", logs.String())
	}
	if strings.Contains(got, "level=INFO") {
		t.Fatalf("application log leaked into protocol/application stdout output:\n%s", got)
	}
}
