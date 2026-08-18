package main

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func executeForTest(t *testing.T, action string) executionReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := executeDetailed(ctx, action, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestExecuteAcceptsAndRetriesOriginalCall(t *testing.T) {
	report := executeForTest(t, "accept")
	if report.Result != "deployed with ticket OPS-2575" || report.Rounds != 2 {
		t.Fatalf("got result %q, rounds %d", report.Result, report.Rounds)
	}
}

func TestExecuteRejectsWithoutDeploying(t *testing.T) {
	for _, action := range []string{"decline", "cancel"} {
		t.Run(action, func(t *testing.T) {
			report := executeForTest(t, action)
			if report.Result != "deployment cancelled" || report.Rounds != 2 {
				t.Fatalf("got result %q, rounds %d", report.Result, report.Rounds)
			}
		})
	}
}

func TestExecuteEchoesRequestState(t *testing.T) {
	report := executeForTest(t, "accept")
	if !report.RequestStateEchoed {
		t.Fatal("MRTR retry did not echo requestState unchanged")
	}
}

func TestExecuteRejectsMalformedInputWithoutPanic(t *testing.T) {
	report := executeForTest(t, "malformed")
	if report.Result != "invalid approval response: ticket must be a non-empty string" {
		t.Fatalf("malformed response result = %q", report.Result)
	}
	if report.Rounds != 2 || !report.RequestStateEchoed {
		t.Fatalf("malformed response rounds/state = %d/%t", report.Rounds, report.RequestStateEchoed)
	}
}

func TestValidateApprovalRejectsUnexpectedResponseType(t *testing.T) {
	params := &mcp.CallToolParamsRaw{
		RequestState: deployRequestState,
		InputResponses: mcp.InputResponseMap{
			"approval": &mcp.ListRootsResult{},
		},
	}
	if _, _, err := validateApproval(params); err == nil {
		t.Fatal("validateApproval accepted a non-elicitation response")
	}
}

func TestMRTRWireUsesResultTypesAndNewRequestID(t *testing.T) {
	report := executeForTest(t, "accept")
	if len(report.ToolCallRequestIDs) != 2 || report.ToolCallRequestIDs[0] == report.ToolCallRequestIDs[1] {
		t.Fatalf("tools/call request IDs = %v, want two distinct IDs", report.ToolCallRequestIDs)
	}
	if !slices.Equal(report.ResultTypes, []string{"input_required", "complete"}) {
		t.Fatalf("result types = %v, want [input_required complete]", report.ResultTypes)
	}
}
