package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func runSubscriptionTest(t *testing.T) (subscriptionReport, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lines []string
	report, err := runSubscriptionScenario(ctx, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	if err != nil {
		t.Fatal(err)
	}
	return report, strings.Join(lines, "\n")
}

func TestRunReceivesListChangeOnListenStream(t *testing.T) {
	report, output := runSubscriptionTest(t)
	for _, want := range []string{
		"acknowledged subset: tools=true prompts=false",
		"received tools/list_changed; subscription ID present: true; matches acknowledgement: true",
		"fresh tool list: weather",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
	if report.ToolEventSubscriptionID == "" || report.ToolEventSubscriptionID != report.ToolAcknowledgement.SubscriptionID {
		t.Errorf("tool event ID %q does not match acknowledgement %q", report.ToolEventSubscriptionID, report.ToolAcknowledgement.SubscriptionID)
	}
}

func TestRunReceivesResourceUpdateOnListenStream(t *testing.T) {
	report, output := runSubscriptionTest(t)
	if report.ResourceURI != weatherResourceURI {
		t.Errorf("resource URI = %q, want %q", report.ResourceURI, weatherResourceURI)
	}
	if report.ResourceEventID == "" || report.ResourceEventID != report.ResourceAcknowledgement.SubscriptionID {
		t.Errorf("resource event ID %q does not match acknowledgement %q", report.ResourceEventID, report.ResourceAcknowledgement.SubscriptionID)
	}
	if report.SubscribeHandlerURI != weatherResourceURI || report.UnsubscribeHandlerURI != weatherResourceURI {
		t.Errorf("subscribe/unsubscribe handlers = %q/%q, want %q", report.SubscribeHandlerURI, report.UnsubscribeHandlerURI, weatherResourceURI)
	}
	if !strings.Contains(output, "received resources/updated: uri="+weatherResourceURI+" subscription ID present=true; matches acknowledgement: true") {
		t.Errorf("output missing resource update:\n%s", output)
	}
}

func TestAcknowledgementContainsOnlySupportedSubscriptions(t *testing.T) {
	report, _ := runSubscriptionTest(t)
	ack := report.ToolAcknowledgement
	if !ack.Tools || ack.Prompts || ack.Resources || len(ack.ResourceURIs) != 0 {
		t.Errorf("tool/prompt acknowledgement is not the supported subset: %+v", ack)
	}
	resourceAck := report.ResourceAcknowledgement
	if resourceAck.Tools || resourceAck.Prompts || resourceAck.Resources {
		t.Errorf("resource acknowledgement unexpectedly enables list notifications: %+v", resourceAck)
	}
	if len(resourceAck.ResourceURIs) != 1 || resourceAck.ResourceURIs[0] != weatherResourceURI {
		t.Errorf("resource acknowledgement URIs = %v, want [%s]", resourceAck.ResourceURIs, weatherResourceURI)
	}
}
