package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRunCachesUntilTTLExpires(t *testing.T) {
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
		"first list: server calls=1 ttlMs=120 scope=private order=alpha-tool,stable-tool,zeta-tool",
		"inside TTL: server calls=1 (cache hit; order=alpha-tool,stable-tool,zeta-tool)",
		"after TTL: server calls=2 (re-fetched; order=alpha-tool,stable-tool,zeta-tool)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}
