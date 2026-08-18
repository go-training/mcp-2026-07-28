package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMCPGODEBUGHelper(t *testing.T) {
	if os.Getenv("MCPGODEBUG_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observation, err := inspectScenario(ctx, os.Getenv(scenarioEnv))
	if err != nil {
		panic(err)
	}
	os.Stdout.WriteString(observation)
	os.Exit(0)
}

func runHelper(t *testing.T, scenario, debug string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestMCPGODEBUGHelper")
	cmd.Env = append(scenarioEnvironment(debug, scenario), "MCPGODEBUG_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed for %s with %q: %v\n%s", scenario, debug, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestV170CompatibilityFlags(t *testing.T) {
	tests := []struct {
		flag        string
		scenario    string
		wantDefault string
		wantCompat  string
	}{
		{"customresnotfounderrcode", "resource-not-found", "code=-32602", "code=-32002"},
		{"hintomitempty", "tool-hints", `json={"idempotentHint":false,"readOnlyHint":false}`, "json={}"},
		{"allowsessionsinstateless", "stateless-session", "DELETE status=405", "DELETE status=204"},
		{"nomethodnotfoundcodeinerror", "method-not-found", "code=-32601", "code=0"},
		{"noprotocolerrorbody", "protocol-error-body", "surfaced code=-32022", "surfaced code=0"},
		{"nowrapinvalidparams", "invalid-params", "code=-32602", "code=0"},
		{"disablecompleteparamsvalidation", "complete-validation", "handler-called=false code=-32602", "handler-called=true code=0"},
	}

	for _, test := range tests {
		t.Run(test.flag, func(t *testing.T) {
			if got := runHelper(t, test.scenario, ""); got != test.wantDefault {
				t.Fatalf("default observation = %q, want %q", got, test.wantDefault)
			}
			if got := runHelper(t, test.scenario, test.flag+"=1"); got != test.wantCompat {
				t.Fatalf("compat observation = %q, want %q", got, test.wantCompat)
			}
		})
	}
}

func TestEveryV170ScenarioIsCovered(t *testing.T) {
	if got, want := len(compatibilityScenarios), 7; got != want {
		t.Fatalf("compatibility scenario count = %d, want %d", got, want)
	}
	seen := make(map[string]bool)
	for _, scenario := range compatibilityScenarios {
		if scenario.flag == "" || scenario.name == "" {
			t.Fatalf("incomplete scenario: %+v", scenario)
		}
		if seen[scenario.flag] {
			t.Fatalf("duplicate flag %q", scenario.flag)
		}
		seen[scenario.flag] = true
	}
}
