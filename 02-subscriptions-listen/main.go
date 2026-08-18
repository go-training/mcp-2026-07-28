package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const weatherResourceURI = "file:///weather/taipei"

type acknowledgementObservation struct {
	Tools          bool
	Prompts        bool
	Resources      bool
	ResourceURIs   []string
	SubscriptionID string
}

type subscriptionReport struct {
	ToolAcknowledgement     acknowledgementObservation
	ResourceAcknowledgement acknowledgementObservation
	ToolEventSubscriptionID string
	ResourceEventID         string
	ResourceURI             string
	FreshTool               string
	SubscribeHandlerURI     string
	UnsubscribeHandlerURI   string
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
	_, err := runSubscriptionScenario(ctx, printf)
	return err
}

func runSubscriptionScenario(ctx context.Context, printf func(string, ...any)) (subscriptionReport, error) {
	var report subscriptionReport
	subscribed := make(chan string, 2)
	unsubscribed := make(chan string, 2)

	server := mcp.NewServer(
		&mcp.Implementation{Name: "subscription-server", Version: "1.0.0"},
		&mcp.ServerOptions{
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{ListChanged: true},
			},
			SubscribeHandler: func(_ context.Context, req *mcp.SubscribeRequest) error {
				subscribed <- req.Params.URI
				return nil
			},
			UnsubscribeHandler: func(_ context.Context, req *mcp.UnsubscribeRequest) error {
				unsubscribed <- req.Params.URI
				return nil
			},
		},
	)
	server.AddResource(
		&mcp.Resource{Name: "taipei-weather", URI: weatherResourceURI, Description: "Current Taipei weather"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
				URI: weatherResourceURI, Text: "sunny",
			}}}, nil
		},
	)

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	acknowledgements := make(chan acknowledgementObservation, 4)
	toolEvents := make(chan *mcp.ToolListChangedRequest, 1)
	resourceEvents := make(chan *mcp.ResourceUpdatedNotificationRequest, 1)
	client := mcp.NewClient(
		&mcp.Implementation{Name: "subscription-client", Version: "1.0.0"},
		&mcp.ClientOptions{
			ToolListChangedHandler: func(_ context.Context, req *mcp.ToolListChangedRequest) {
				toolEvents <- req
			},
			// This handler deliberately requests a notification the server does not
			// support, so the acknowledgement can demonstrate the agreed subset.
			PromptListChangedHandler: func(context.Context, *mcp.PromptListChangedRequest) {},
			ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
				resourceEvents <- req
			},
		},
	)
	client.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if ack, ok := req.(*mcp.ClientRequest[*mcp.SubscriptionsAcknowledgedParams]); ok && ack.Params != nil {
				n := ack.Params.Notifications
				acknowledgements <- acknowledgementObservation{
					Tools:          n.ToolsListChanged,
					Prompts:        n.PromptsListChanged,
					Resources:      n.ResourcesListChanged,
					ResourceURIs:   append([]string(nil), n.ResourceSubscriptions...),
					SubscriptionID: subscriptionID(ack.Params.GetMeta()),
				}
			}
			return next(ctx, method, req)
		}
	})

	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		return report, fmt.Errorf("connect and open subscriptions/listen: %w", err)
	}
	defer session.Close()
	printf("connected with protocol: %s", session.InitializeResult().ProtocolVersion)

	report.ToolAcknowledgement, err = waitFor(ctx, acknowledgements, "tool/prompt subscription acknowledgement")
	if err != nil {
		return report, err
	}
	printf(
		"acknowledged subset: tools=%t prompts=%t resources=%t resource-subscriptions=%d subscription ID present=%t",
		report.ToolAcknowledgement.Tools,
		report.ToolAcknowledgement.Prompts,
		report.ToolAcknowledgement.Resources,
		len(report.ToolAcknowledgement.ResourceURIs),
		report.ToolAcknowledgement.SubscriptionID != "",
	)

	mcp.AddTool(server, &mcp.Tool{Name: "weather", Description: "New dynamic tool"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})

	toolEvent, err := waitFor(ctx, toolEvents, "tools/list_changed")
	if err != nil {
		return report, err
	}
	report.ToolEventSubscriptionID = subscriptionID(toolEvent.Params.GetMeta())
	printf(
		"received tools/list_changed; subscription ID present: %t; matches acknowledgement: %t",
		report.ToolEventSubscriptionID != "",
		report.ToolEventSubscriptionID == report.ToolAcknowledgement.SubscriptionID,
	)

	listed, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return report, fmt.Errorf("list tools after notification: %w", err)
	}
	report.FreshTool = listed.Tools[0].Name
	printf("fresh tool list: %s", report.FreshTool)

	if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: weatherResourceURI}); err != nil {
		return report, fmt.Errorf("subscribe to %s: %w", weatherResourceURI, err)
	}
	report.ResourceAcknowledgement, err = waitFor(ctx, acknowledgements, "resource subscription acknowledgement")
	if err != nil {
		return report, err
	}
	report.SubscribeHandlerURI, err = waitFor(ctx, subscribed, "server SubscribeHandler")
	if err != nil {
		return report, err
	}
	acknowledgedURI := ""
	if len(report.ResourceAcknowledgement.ResourceURIs) == 1 {
		acknowledgedURI = report.ResourceAcknowledgement.ResourceURIs[0]
	}
	printf(
		"resource subscription acknowledged: uri=%s subscription ID present=%t",
		acknowledgedURI,
		report.ResourceAcknowledgement.SubscriptionID != "",
	)

	if err := server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: weatherResourceURI}); err != nil {
		return report, fmt.Errorf("publish resource update: %w", err)
	}
	resourceEvent, err := waitFor(ctx, resourceEvents, "resources/updated")
	if err != nil {
		return report, err
	}
	report.ResourceURI = resourceEvent.Params.URI
	report.ResourceEventID = subscriptionID(resourceEvent.Params.GetMeta())
	printf(
		"received resources/updated: uri=%s subscription ID present=%t; matches acknowledgement: %t",
		report.ResourceURI,
		report.ResourceEventID != "",
		report.ResourceEventID == report.ResourceAcknowledgement.SubscriptionID,
	)

	if err := session.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: weatherResourceURI}); err != nil {
		return report, fmt.Errorf("unsubscribe from %s: %w", weatherResourceURI, err)
	}
	report.UnsubscribeHandlerURI, err = waitFor(ctx, unsubscribed, "server UnsubscribeHandler")
	if err != nil {
		return report, err
	}
	printf("resource unsubscribed: %t", report.UnsubscribeHandlerURI == weatherResourceURI)
	return report, nil
}

func subscriptionID(meta map[string]any) string {
	if raw, ok := meta[mcp.MetaKeySubscriptionID]; ok && raw != nil {
		return fmt.Sprint(raw)
	}
	return ""
}

func waitFor[T any](ctx context.Context, ch <-chan T, description string) (T, error) {
	select {
	case value := <-ch:
		return value, nil
	case <-ctx.Done():
		var zero T
		return zero, fmt.Errorf("wait for %s: %w", description, ctx.Err())
	}
}
