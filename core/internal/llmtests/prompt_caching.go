package llmtests

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Long shared prefix for prompt caching test - similar to Python script
// This prefix is intentionally long (>1024 tokens) to test OpenAI's prompt caching functionality
const longSharedPrefix = `You are an AI assistant for ACME Corp. Your role is to help customers understand our products and services, answer questions about our API, provide technical support, and guide users through our platform. ACME Corp is a leading technology company specializing in cloud infrastructure solutions. We offer a comprehensive suite of services including:

1. Cloud Computing Services: Our cloud platform provides scalable infrastructure for businesses of all sizes. We offer virtual machines, container orchestration, serverless computing, and managed databases. Our infrastructure is built on industry-leading technology with 99.99% uptime SLA guarantees. We support multiple operating systems including Linux distributions (Ubuntu, CentOS, Debian, RHEL), Windows Server editions, and containerized environments using Docker and Kubernetes. Our compute instances range from small development environments (1 vCPU, 2GB RAM) to enterprise-grade configurations (128 vCPUs, 512GB RAM) with dedicated hardware options available. We provide automated scaling capabilities, load balancing, auto-scaling groups, and health monitoring to ensure optimal performance and cost efficiency.

2. API Services: Our RESTful API allows developers to integrate ACME Corp services into their applications. The API supports authentication via API keys and OAuth 2.0, rate limiting for fair usage, webhooks for real-time notifications, and comprehensive error handling with detailed error codes and messages. Our API follows RESTful principles and provides consistent response formats using JSON. We offer comprehensive API documentation with interactive examples, code samples in Python, JavaScript, Go, Java, Ruby, PHP, and C#. Our API versioning strategy ensures backward compatibility while allowing us to introduce new features. We provide SDKs for all major programming languages and frameworks, including official libraries maintained by our engineering team. The API supports pagination, filtering, sorting, and field selection to optimize data transfer. We implement rate limiting with clear headers indicating remaining requests and reset times. Our webhook system supports retry mechanisms, signature verification, and custom event filtering.

3. Data Analytics: We provide powerful analytics tools that help businesses understand their usage patterns, optimize costs, and make data-driven decisions. Our analytics dashboard includes real-time metrics, historical trends, custom reports, and export capabilities. We track resource utilization, cost trends, performance metrics, security events, and user activity patterns. Our analytics platform supports custom dashboards, scheduled reports, alert configurations, and data export in multiple formats (CSV, JSON, Excel, PDF). We provide machine learning-powered insights for cost optimization, anomaly detection, and predictive analytics. Our data retention policies comply with industry standards and regulations. We offer data visualization tools with interactive charts, graphs, and heatmaps. Our analytics API allows programmatic access to all metrics and enables integration with third-party business intelligence tools.

4. Security Features: Security is our top priority. We offer multi-factor authentication, encryption at rest and in transit, DDoS protection, network isolation, compliance certifications (SOC 2, ISO 27001, GDPR), and regular security audits. Our security infrastructure includes advanced threat detection, intrusion prevention systems, web application firewalls, and distributed denial-of-service protection. We implement role-based access control (RBAC) with fine-grained permissions, audit logging for all administrative actions, and security groups for network segmentation. Our encryption standards include AES-256 for data at rest and TLS 1.3 for data in transit. We support customer-managed encryption keys (CMEK) for enhanced control. Our compliance program includes regular third-party audits, penetration testing, vulnerability assessments, and security certifications. We maintain detailed security documentation, incident response procedures, and provide security advisories to customers. Our identity and access management system supports single sign-on (SSO), federated identity providers, and directory service integration.

5. Support Services: Our support team is available 24/7 via email, chat, and phone. We offer different support tiers including Basic (email only), Standard (email + chat), Premium (email + chat + phone with faster response times), and Enterprise (dedicated account manager). Our support team consists of certified engineers, cloud architects, and technical specialists with deep expertise in cloud infrastructure, networking, security, and application development. We provide comprehensive knowledge base articles, video tutorials, step-by-step guides, troubleshooting documentation, and best practices documentation. Our support portal includes ticket management, live chat, community forums, and direct access to our engineering team for enterprise customers. We offer proactive monitoring, health checks, and automated alerting for critical issues. Our support SLA guarantees response times based on severity levels: critical issues (15 minutes), high priority (1 hour), medium priority (4 hours), and low priority (24 hours). We provide regular health reports, performance summaries, and optimization recommendations.

Our platform is designed to be developer-friendly with comprehensive documentation, code examples in multiple programming languages, SDKs for popular frameworks, and an active community forum where developers can share knowledge and best practices. We maintain extensive documentation covering API references, architecture guides, deployment tutorials, security best practices, performance optimization tips, cost management strategies, and troubleshooting guides. Our code examples library includes hundreds of sample applications demonstrating common use cases, integration patterns, and best practices. We provide interactive tutorials, hands-on labs, and certification programs for developers and system administrators. Our community forum is actively moderated and includes contributions from both our team and the developer community. We host regular webinars, office hours, and technical workshops. Our developer relations team engages with the community through conferences, meetups, and open source contributions.

When helping customers, always be professional, clear, and concise. If you don't know the answer to a question, acknowledge it and offer to help them find the right resource or contact our support team. Provide accurate information based on our official documentation and current product capabilities. Use clear language appropriate for the customer's technical level. When discussing technical concepts, provide context and examples. For complex topics, break down information into digestible sections. Always prioritize customer success and satisfaction. If a customer is unclear, ask clarifying questions before providing recommendations. When suggesting solutions, explain the benefits and potential trade-offs. Always verify that recommendations align with the customer's specific use case and requirements.`

// GetPromptCachingTools returns 10 tools for testing prompt caching with tools
func GetPromptCachingTools() []schemas.ChatTool {
	return []schemas.ChatTool{
		{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "get_account_info",
				Description: bifrost.Ptr("Retrieve account information for a given account ID"),
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("account_id", map[string]interface{}{
							"type":        "string",
							"description": "The unique account identifier",
						}),
					),
					Required: []string{"account_id"},
				},
			},
		},
		{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "calculate_usage_cost",
				Description: bifrost.Ptr("Calculate the cost for cloud resource usage based on service type and quantity"),
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("service_type", map[string]interface{}{
							"type":        "string",
							"enum":        []string{"compute", "storage", "network", "database"},
							"description": "The type of service being used",
						}),
						schemas.KV("quantity", map[string]interface{}{
							"type":        "number",
							"description": "The quantity of resources used",
						}),
						schemas.KV("unit", map[string]interface{}{
							"type":        "string",
							"enum":        []string{"hours", "GB", "requests", "GB-hours"},
							"description": "The unit of measurement",
						}),
					),
					Required: []string{"service_type", "quantity", "unit"},
				},
			},
		},
		{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "check_api_status",
				Description: bifrost.Ptr("Check the current status and health of the API service"),
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("endpoint", map[string]interface{}{
							"type":        "string",
							"description": "Optional specific API endpoint to check",
						}),
					),
					Required: []string{},
				},
			},
		},
		{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "create_support_ticket",
				Description: bifrost.Ptr("Create a new support ticket for a customer issue"),
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("subject", map[string]interface{}{
							"type":        "string",
							"description": "Brief subject line for the ticket",
						}),
						schemas.KV("description", map[string]interface{}{
							"type":        "string",
							"description": "Detailed description of the issue",
						}),
						schemas.KV("priority", map[string]interface{}{
							"type":        "string",
							"enum":        []string{"low", "medium", "high", "urgent"},
							"description": "Priority level of the ticket",
						}),
						schemas.KV("category", map[string]interface{}{
							"type":        "string",
							"enum":        []string{"technical", "billing", "account", "feature_request"},
							"description": "Category of the support request",
						}),
					),
					Required: []string{"subject", "description", "priority", "category"},
				},
			},
		},
		{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "get_service_documentation",
				Description: bifrost.Ptr("Retrieve documentation for a specific service or feature"),
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("service_name", map[string]interface{}{
							"type":        "string",
							"description": "Name of the service or feature",
						}),
						schemas.KV("doc_type", map[string]interface{}{
							"type":        "string",
							"enum":        []string{"api", "guide", "tutorial", "reference"},
							"description": "Type of documentation requested",
						}),
					),
					Required: []string{"service_name"},
				},
			},
		},
		{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "list_available_regions",
				Description: bifrost.Ptr("Get a list of available cloud regions for deployment"),
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("service_type", map[string]interface{}{
							"type":        "string",
							"enum":        []string{"compute", "storage", "database", "all"},
							"description": "Filter by service type or 'all' for all services",
						}),
					),
					Required: []string{},
				},
			},
		},
		{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "estimate_deployment_cost",
				Description: bifrost.Ptr("Estimate the monthly cost for a cloud deployment configuration"),
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("instance_type", map[string]interface{}{
							"type":        "string",
							"description": "Type of compute instance",
						}),
						schemas.KV("instance_count", map[string]interface{}{
							"type":        "integer",
							"description": "Number of instances",
						}),
						schemas.KV("storage_gb", map[string]interface{}{
							"type":        "number",
							"description": "Storage in GB",
						}),
						schemas.KV("region", map[string]interface{}{
							"type":        "string",
							"description": "Deployment region",
						}),
					),
					Required: []string{"instance_type", "instance_count", "storage_gb", "region"},
				},
			},
		},
		{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "validate_api_key",
				Description: bifrost.Ptr("Validate an API key and return its permissions and status"),
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("api_key", map[string]interface{}{
							"type":        "string",
							"description": "The API key to validate",
						}),
					),
					Required: []string{"api_key"},
				},
			},
		},
		{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "get_usage_analytics",
				Description: bifrost.Ptr("Retrieve usage analytics and metrics for an account"),
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("account_id", map[string]interface{}{
							"type":        "string",
							"description": "Account ID to get analytics for",
						}),
						schemas.KV("start_date", map[string]interface{}{
							"type":        "string",
							"description": "Start date in YYYY-MM-DD format",
						}),
						schemas.KV("end_date", map[string]interface{}{
							"type":        "string",
							"description": "End date in YYYY-MM-DD format",
						}),
						schemas.KV("metric_type", map[string]interface{}{
							"type":        "string",
							"enum":        []string{"requests", "cost", "latency", "errors", "all"},
							"description": "Type of metrics to retrieve",
						}),
					),
					Required: []string{"account_id", "start_date", "end_date"},
				},
			},
		},
		{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "check_compliance_status",
				Description: bifrost.Ptr("Check compliance status and certifications for security and regulatory requirements"),
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("compliance_type", map[string]interface{}{
							"type":        "string",
							"enum":        []string{"SOC2", "ISO27001", "GDPR", "HIPAA", "all"},
							"description": "Type of compliance to check",
						}),
						schemas.KV("region", map[string]interface{}{
							"type":        "string",
							"description": "Optional region to check compliance for",
						}),
					),
					Required: []string{},
				},
			},
			CacheControl: &schemas.CacheControl{
				Type: schemas.CacheControlTypeEphemeral,
			},
		},
	}
}

// RunPromptCachingToolBlocksTest validates that cache_control on tool_use and tool_result
// content blocks survives the Bifrost round-trip (Anthropic format -> Bifrost ResponsesMessage -> Provider format).
// It sends a Responses API request with cache_control on function_call and function_call_output messages,
// enables raw request capture, and inspects the outgoing provider request to verify cache markers are present.
//
// For Anthropic/Vertex: verifies "cache_control" appears on tool_use and tool_result content blocks.
// For Bedrock: verifies "cachePoint" blocks appear after toolUse and toolResult blocks.
func RunPromptCachingToolBlocksTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.PromptCaching {
		t.Logf("Prompt caching tool blocks test not supported for provider %s", testConfig.Provider)
		return
	}
	if testConfig.PromptCachingModel == "" {
		t.Logf("No PromptCachingModel configured for provider %s, skipping", testConfig.Provider)
		return
	}

	t.Run("PromptCachingToolBlocks", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		weatherTool := GetSampleResponsesTool(SampleToolTypeWeather)
		if weatherTool == nil {
			t.Fatal("Failed to get sample weather tool")
		}

		cacheControl := &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}

		// System message with long prefix (ensures we exceed minimum cache token threshold)
		systemMsg := schemas.ResponsesMessage{
			Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleSystem),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{
						Type:         schemas.ResponsesInputMessageContentBlockTypeText,
						Text:         bifrost.Ptr(longSharedPrefix),
						CacheControl: cacheControl,
					},
				},
			},
		}

		// User asks about weather
		userMsg1 := schemas.ResponsesMessage{
			Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: bifrost.Ptr("What's the weather in San Francisco?"),
			},
		}

		// Assistant responds with a tool call (function_call with cache_control)
		toolCallMsg := schemas.ResponsesMessage{
			Type:         bifrost.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			Status:       bifrost.Ptr("completed"),
			CacheControl: cacheControl,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    bifrost.Ptr("call_weather_001"),
				Name:      bifrost.Ptr("weather"),
				Arguments: bifrost.Ptr(`{"location":"San Francisco"}`),
			},
		}

		// Tool result (function_call_output with cache_control)
		toolResultMsg := schemas.ResponsesMessage{
			Type:         bifrost.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			CacheControl: cacheControl,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: bifrost.Ptr("call_weather_001"),
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: bifrost.Ptr(`{"temperature": 18, "unit": "celsius", "condition": "partly cloudy", "humidity": 72}`),
				},
			},
		}

		// Follow-up user message to prompt a response
		userMsg2 := schemas.ResponsesMessage{
			Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: bifrost.Ptr("Summarize the weather information you received."),
			},
		}

		responsesReq := &schemas.BifrostResponsesRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.PromptCachingModel,
			Input: []schemas.ResponsesMessage{
				systemMsg,
				userMsg1,
				toolCallMsg,
				toolResultMsg,
				userMsg2,
			},
			Params: &schemas.ResponsesParameters{
				Tools:           []schemas.ResponsesTool{*weatherTool},
				MaxOutputTokens: bifrost.Ptr(200),
			},
		}

		// Enable raw request capture so we can inspect the outgoing provider request
		rawCtx := context.WithValue(ctx, schemas.BifrostContextKeySendBackRawRequest, true)

		retryConfig := ResponsesRetryConfig{
			MaxAttempts: 5,
			BaseDelay:   2 * time.Second,
			MaxDelay:    10 * time.Second,
			Conditions:  []ResponsesRetryCondition{},
			OnRetry: func(attempt int, reason string, t *testing.T) {
				t.Logf("Retrying (attempt %d): %s", attempt, reason)
			},
			OnFinalFail: func(attempts int, finalErr error, t *testing.T) {
				t.Logf("Failed after %d attempts: %v", attempts, finalErr)
			},
		}

		expectations := ResponseExpectations{
			ShouldHaveContent:    true,
			ShouldHaveUsageStats: true,
		}

		retryContext := TestRetryContext{
			ScenarioName: "PromptCachingToolBlocks",
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.PromptCachingModel,
			},
		}

		operation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(rawCtx, schemas.NoDeadline)
			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		response, err := WithResponsesTestRetry(t, retryConfig, retryContext, expectations, "PromptCachingToolBlocks", operation)

		require.Nil(t, err, "Responses request should succeed: %v", err)
		require.NotNil(t, response, "Response should not be nil")

		// Verify response content
		content := GetResponsesContent(response)
		assert.NotEmpty(t, content, "Response should have content")

		// Inspect the raw request to verify cache_control markers survived the conversion
		rawReq := response.ExtraFields.RawRequest
		require.NotNil(t, rawReq, "Raw request should be present (BifrostContextKeySendBackRawRequest was set)")

		rawJSON, marshalErr := sonic.Marshal(rawReq)
		require.NoError(t, marshalErr, "Raw request should be marshalable to JSON")
		rawStr := string(rawJSON)

		t.Logf("  Raw request length: %d bytes", len(rawStr))

		// Validate based on provider type:
		// - Anthropic/Vertex: tool_use and tool_result blocks should have "cache_control"
		// - Bedrock: "cachePoint" blocks should appear after toolUse and toolResult blocks
		switch testConfig.Provider {
		case schemas.Bedrock:
			// Bedrock translates cache_control into separate cachePoint blocks.
			// Count occurrences: we expect at least 2 cachePoint blocks from tool blocks
			// (1 after toolUse, 1 after toolResult), plus possibly more from system/text blocks.
			cachePointCount := strings.Count(rawStr, `"cachePoint"`)
			t.Logf("  Bedrock: found %d cachePoint blocks in raw request", cachePointCount)

			// We put cache_control on: system text (1), tool_use (1), tool_result (1) = at least 3
			require.GreaterOrEqual(t, cachePointCount, 3,
				"Expected at least 3 cachePoint blocks (system + toolUse + toolResult), got %d", cachePointCount)

		case schemas.Anthropic, schemas.Vertex:
			// Anthropic/Vertex: cache_control should appear on content blocks directly.
			// The raw request should contain cache_control on the tool_use and tool_result blocks.
			cacheControlCount := strings.Count(rawStr, `"cache_control"`)
			t.Logf("  %s: found %d cache_control markers in raw request", testConfig.Provider, cacheControlCount)

			// We put cache_control on: system text (1), tool_use (1), tool_result (1) = at least 3
			require.GreaterOrEqual(t, cacheControlCount, 3,
				"Expected at least 3 cache_control markers (system + tool_use + tool_result), got %d", cacheControlCount)

		default:
			t.Logf("  Provider %s: skipping raw request cache marker validation (not Anthropic/Bedrock/Vertex)", testConfig.Provider)
		}

		t.Logf("  Prompt caching tool blocks test completed!")
	})
}

// RunPromptCachingMultipleToolCallsTest verifies prompt caching across a 10-turn
// conversation with tool calls scattered throughout. This directly reproduces the
// Vertex caching bug where Bifrost's key reordering in tool_use input fields caused
// the cache prefix to diverge at the first tool_use block.
//
// The conversation grows from ~9 messages (turn 1) to ~19 messages (turn 5),
// matching the real-world Claude Code pattern (11, 13, 15 messages across turns).
// Tool calls use DIFFERENT key orderings per block to test key order preservation.
//
// Each turn verifies:
//  1. The response succeeds and has content
//  2. cache_read_input_tokens grows across turns (proving prefix stability)
//  3. For Anthropic/Vertex: cache_control markers survive in raw request
//  4. For Anthropic/Vertex: tool_use input key ordering is preserved in raw request
func RunPromptCachingMultipleToolCallsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.PromptCaching {
		t.Logf("Prompt caching multiple tool calls test not supported for provider %s", testConfig.Provider)
		return
	}
	if testConfig.PromptCachingModel == "" {
		t.Logf("No PromptCachingModel configured for provider %s, skipping", testConfig.Provider)
		return
	}

	t.Run("PromptCachingMultipleToolCalls", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		weatherTool := GetSampleResponsesTool(SampleToolTypeWeather)
		if weatherTool == nil {
			t.Fatal("Failed to get sample weather tool")
		}

		cacheControl := &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}

		// Helper to create a user message
		makeUserMsg := func(text string) schemas.ResponsesMessage {
			return schemas.ResponsesMessage{
				Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: bifrost.Ptr(text),
				},
			}
		}

		// Helper to create an assistant message
		makeAssistantMsg := func(text string) schemas.ResponsesMessage {
			return schemas.ResponsesMessage{
				Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: bifrost.Ptr(text),
				},
			}
		}

		// Helper to create a tool call with specific key ordering in arguments
		makeToolCall := func(callID, name, args string, withCacheControl bool) schemas.ResponsesMessage {
			msg := schemas.ResponsesMessage{
				Type:   bifrost.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: bifrost.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    bifrost.Ptr(callID),
					Name:      bifrost.Ptr(name),
					Arguments: bifrost.Ptr(args),
				},
			}
			if withCacheControl {
				msg.CacheControl = cacheControl
			}
			return msg
		}

		// Helper to create a tool result
		makeToolResult := func(callID, output string, withCacheControl bool) schemas.ResponsesMessage {
			msg := schemas.ResponsesMessage{
				Type: bifrost.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: bifrost.Ptr(callID),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: bifrost.Ptr(output),
					},
				},
			}
			if withCacheControl {
				msg.CacheControl = cacheControl
			}
			return msg
		}

		// System message with long prefix (exceeds minimum cache token threshold)
		systemMsg := schemas.ResponsesMessage{
			Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleSystem),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{
						Type:         schemas.ResponsesInputMessageContentBlockTypeText,
						Text:         bifrost.Ptr(longSharedPrefix),
						CacheControl: cacheControl,
					},
				},
			},
		}

		// Build the initial conversation with 3 parallel tool calls using DIFFERENT key orderings.
		// This is the exact pattern from Claude Code: each tool_use has input keys
		// in the order the model generated them (not alphabetical).
		// If Bifrost re-orders these keys, the cache prefix diverges at this point.
		// Only the system message gets cache_control. Tool calls/results do NOT get cache_control
		// markers — Vertex/Anthropic limits to 4 blocks with cache_control per request, and as
		// the conversation grows across turns, markers would accumulate past the limit.
		// The key ordering test is about the input JSON bytes, not cache_control placement.
		initialConversation := []schemas.ResponsesMessage{
			systemMsg,
			makeUserMsg("What's the weather in San Francisco, New York, and London?"),
			// 3 parallel tool calls — note the DIFFERENT key orderings
			makeToolCall("call_weather_sf", "weather", `{"location":"San Francisco","unit":"celsius"}`, false),
			// Keys: unit BEFORE location (REVERSED from alphabetical — key order preservation test)
			makeToolCall("call_weather_ny", "weather", `{"unit":"fahrenheit","location":"New York"}`, false),
			makeToolCall("call_weather_london", "weather", `{"location":"London","unit":"celsius"}`, false),
			// Tool results
			makeToolResult("call_weather_sf", `{"temperature":18,"condition":"partly cloudy"}`, false),
			makeToolResult("call_weather_ny", `{"temperature":72,"condition":"sunny"}`, false),
			makeToolResult("call_weather_london", `{"temperature":12,"condition":"rainy"}`, false),
			makeUserMsg("Compare the weather across all three cities."),
		}

		// Define the turns: each turn adds new messages to the growing conversation.
		// Some turns include tool calls (with varying key orderings), others are plain chat.
		// This simulates a real Claude Code session.
		type turn struct {
			name     string
			query    string
			// If non-nil, these are tool call + result messages to inject before the query
			// (simulating the assistant calling tools in the previous turn)
			toolExchange []schemas.ResponsesMessage
		}

		turns := []turn{
			{
				name:  "Turn1_InitialToolCalls",
				query: "Compare the weather across all three cities.",
			},
			{
				name:  "Turn2_FollowUp",
				query: "Which city would you recommend for outdoor activities today?",
			},
			{
				name:  "Turn3_MoreToolCalls",
				query: "What about Tokyo and Sydney?",
				toolExchange: []schemas.ResponsesMessage{
					// 2 more tool calls with different key orderings
					// Keys: unit, location (non-alphabetical)
				makeToolCall("call_weather_tokyo", "weather", `{"unit":"celsius","location":"Tokyo"}`, false),
				makeToolCall("call_weather_sydney", "weather", `{"location":"Sydney","unit":"celsius"}`, false),
				makeToolResult("call_weather_tokyo", `{"temperature":25,"condition":"sunny"}`, false),
				makeToolResult("call_weather_sydney", `{"temperature":22,"condition":"clear"}`, false),
				},
			},
			{
				name:  "Turn4_PlainChat",
				query: "Rank all five cities by temperature from warmest to coldest.",
			},
			{
				name:  "Turn5_FinalToolCall",
				query: "What's the weather in Berlin?",
				toolExchange: []schemas.ResponsesMessage{
					// Single tool call with keys in non-alphabetical order
				makeToolCall("call_weather_berlin", "weather", `{"unit":"celsius","location":"Berlin"}`, false),
				makeToolResult("call_weather_berlin", `{"temperature":8,"condition":"overcast"}`, false),
				},
			},
			{
				name:  "Turn6_PlainChat",
				query: "Now rank all six cities including Berlin.",
			},
			{
				name:  "Turn7_PlainChat",
				query: "Which cities have the best and worst conditions for cycling?",
			},
			{
				name:  "Turn8_MoreToolCalls",
				query: "Check Paris and Rome too.",
				toolExchange: []schemas.ResponsesMessage{
					// Keys: location, unit vs unit, location (mixed ordering)
				makeToolCall("call_weather_paris", "weather", `{"location":"Paris","unit":"celsius"}`, false),
				makeToolCall("call_weather_rome", "weather", `{"unit":"celsius","location":"Rome"}`, false),
				makeToolResult("call_weather_paris", `{"temperature":15,"condition":"light rain"}`, false),
				makeToolResult("call_weather_rome", `{"temperature":20,"condition":"partly sunny"}`, false),
				},
			},
			{
				name:  "Turn9_PlainChat",
				query: "Give me the final ranking of all eight cities by temperature.",
			},
			{
				name:  "Turn10_Summary",
				query: "Summarize everything we discussed about the weather across all cities.",
			},
		}

		rawCtx := context.WithValue(ctx, schemas.BifrostContextKeySendBackRawRequest, true)

		retryConfig := ResponsesRetryConfig{
			MaxAttempts: 5,
			BaseDelay:   2 * time.Second,
			MaxDelay:    10 * time.Second,
			Conditions:  []ResponsesRetryCondition{},
			OnRetry: func(attempt int, reason string, t *testing.T) {
				t.Logf("Retrying (attempt %d): %s", attempt, reason)
			},
			OnFinalFail: func(attempts int, finalErr error, t *testing.T) {
				t.Logf("Failed after %d attempts: %v", attempts, finalErr)
			},
		}

		// The conversation history grows with each turn
		conversationHistory := make([]schemas.ResponsesMessage, len(initialConversation)-1)
		copy(conversationHistory, initialConversation[:len(initialConversation)-1]) // Everything except last user msg

		var prevCacheRead int
		var turnsWithCacheHit int

		for i, turn := range turns {
			turnName := fmt.Sprintf("Turn%d", i+1)
			t.Run(turnName, func(t *testing.T) {
				// If this turn has tool exchange from "previous turn's response",
				// add an assistant message + tool calls + results
				if i > 0 {
					// Add assistant response from previous turn
					conversationHistory = append(conversationHistory,
						makeAssistantMsg(fmt.Sprintf("Here's my analysis for turn %d based on the weather data.", i)))
				}

				// Add tool exchange if present (simulating tools called in previous response)
				if turn.toolExchange != nil {
					conversationHistory = append(conversationHistory, turn.toolExchange...)
				}

			// Build full input: conversation history + current user query
			input := make([]schemas.ResponsesMessage, len(conversationHistory)+1)
			copy(input, conversationHistory)
			input[len(input)-1] = makeUserMsg(turn.query)

			// Advance the cache checkpoint so that cache_read grows with each turn.
			// For providers with explicit caching (Anthropic, Vertex, Bedrock), place
			// cache_control on the penultimate message to expand the cached prefix.
			// For providers with automatic caching (OpenAI), skip this entirely —
			// adding cache_control expands ContentStr to ContentBlocks which changes
			// the serialization format and breaks prefix matching across turns.
			if testConfig.Provider != schemas.OpenAI && len(input) >= 2 {
				cacheTargetIdx := len(input) - 2 // default: penultimate
				for j := len(input) - 2; j >= 0; j-- {
					jType := schemas.ResponsesMessageTypeMessage
					if input[j].Type != nil {
						jType = *input[j].Type
					}
					if jType == schemas.ResponsesMessageTypeFunctionCall {
						cacheTargetIdx = j
						break
					} else if jType == schemas.ResponsesMessageTypeFunctionCallOutput {
						continue // skip tool results; keep searching for the last tool call
					} else {
						cacheTargetIdx = j // regular message — use it if no tool call found before it
						break
					}
				}

				target := input[cacheTargetIdx] // struct copy; does not alias conversationHistory
				tType := schemas.ResponsesMessageTypeMessage
				if target.Type != nil {
					tType = *target.Type
				}
				switch tType {
				case schemas.ResponsesMessageTypeFunctionCall,
					schemas.ResponsesMessageTypeFunctionCallOutput:
					// Message-level CacheControl is forwarded to the tool_use / tool_result block
					target.CacheControl = cacheControl
				default:
					// Regular user/assistant message: set cc on the last content block.
					// Create new Content objects to avoid mutating the shared pointer from conversationHistory.
					if target.Content != nil {
						if target.Content.ContentStr != nil {
							// Use the correct content block type based on message role:
							// assistant messages require "output_text", others use "input_text"
							blockType := schemas.ResponsesInputMessageContentBlockTypeText
							if target.Role != nil && *target.Role == schemas.ResponsesInputMessageRoleAssistant {
								blockType = schemas.ResponsesOutputMessageContentTypeText
							}
							target.Content = &schemas.ResponsesMessageContent{
								ContentBlocks: []schemas.ResponsesMessageContentBlock{
									{
										Type:         blockType,
										Text:         target.Content.ContentStr,
										CacheControl: cacheControl,
									},
								},
							}
						} else if len(target.Content.ContentBlocks) > 0 {
							blocks := make([]schemas.ResponsesMessageContentBlock, len(target.Content.ContentBlocks))
							copy(blocks, target.Content.ContentBlocks)
							blocks[len(blocks)-1].CacheControl = cacheControl
							target.Content = &schemas.ResponsesMessageContent{ContentBlocks: blocks}
						}
					}
				}
				input[cacheTargetIdx] = target
			}

			req := &schemas.BifrostResponsesRequest{
					Provider: testConfig.Provider,
					Model:    testConfig.PromptCachingModel,
					Input:    input,
					Params: &schemas.ResponsesParameters{
						Tools:           []schemas.ResponsesTool{*weatherTool},
						MaxOutputTokens: bifrost.Ptr(200),
					},
				}

				expectations := ResponseExpectations{
					ShouldHaveContent:    true,
					ShouldHaveUsageStats: true,
				}

				retryContext := TestRetryContext{
					ScenarioName: "PromptCachingMultipleToolCalls_" + turnName,
					TestMetadata: map[string]interface{}{
						"provider": testConfig.Provider,
						"model":    testConfig.PromptCachingModel,
					},
				}

				operation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
					bfCtx := schemas.NewBifrostContext(rawCtx, schemas.NoDeadline)
					return client.ResponsesRequest(bfCtx, req)
				}

				response, err := WithResponsesTestRetry(t, retryConfig, retryContext, expectations, turnName, operation)
				require.Nil(t, err, "%s should succeed: %v", turnName, err)
				require.NotNil(t, response, "%s response should not be nil", turnName)

				content := GetResponsesContent(response)
				assert.NotEmpty(t, content, "%s should have content", turnName)

				// Extract usage metrics
				var cacheRead, cacheWrite, inputTokens int
				if response.Usage != nil {
					inputTokens = response.Usage.InputTokens
					if response.Usage.InputTokensDetails != nil {
						cacheRead = response.Usage.InputTokensDetails.CachedReadTokens
						cacheWrite = response.Usage.InputTokensDetails.CachedWriteTokens
					}
				}

				t.Logf("  %s: messages=%d, input_tokens=%d, cache_read=%d, cache_write=%d",
					turnName, len(input), inputTokens, cacheRead, cacheWrite)

				// Turn 1: just establish the cache (cache_read may be 0 or non-zero from prior runs)
				// Turn 2+: verify caching is working
				if i >= 1 && inputTokens > 0 {
					readPercentage := float64(cacheRead) / float64(inputTokens)
					totalCachedPercentage := float64(cacheRead+cacheWrite) / float64(inputTokens)
					t.Logf("  %s: cache_read=%.2f%%, total_cached=%.2f%%",
						turnName, readPercentage*100, totalCachedPercentage*100)

					switch testConfig.Provider {
					case schemas.OpenAI:
						// OpenAI uses automatic best-effort caching — individual turns may
						// miss due to server-side load or cache eviction. Track hits for
						// aggregate validation after all turns complete.
						if totalCachedPercentage >= 0.50 {
							turnsWithCacheHit++
						}
					default:
						// Explicit caching providers (Anthropic, Vertex, Bedrock):
						// cache_read > 0 proves prefix reuse is working.
						require.Greater(t, cacheRead, 0,
							"%s should reuse an existing prefix; got cache_read=0 and cache_write=%d",
							turnName, cacheWrite)
						require.GreaterOrEqual(t, readPercentage, 0.50,
							"%s should have >= 50%% cache reads (got %.2f%%). "+
								"If this fails, tool_use input key ordering may be broken — "+
								"the cache prefix diverges at the first tool_use block. "+
								"cache_read=%d, cache_write=%d, input_tokens=%d",
							turnName, readPercentage*100, cacheRead, cacheWrite, inputTokens)
					}

					// cache_read should grow (or stay comparable) as conversation grows
					// (skip for OpenAI where individual turns may miss)
					if testConfig.Provider != schemas.OpenAI && i >= 2 && prevCacheRead > 0 {
						// Allow some variance but cache_read should not dramatically drop
						assert.GreaterOrEqual(t, cacheRead, prevCacheRead/2,
							"%s: cache_read dropped significantly from previous turn (%d -> %d), "+
								"suggesting cache prefix mismatch", turnName, prevCacheRead, cacheRead)
					}
				}

				// Verify raw request on first turn (key ordering + cache markers)
				if i == 0 {
					rawReq := response.ExtraFields.RawRequest
					switch testConfig.Provider {
					case schemas.Anthropic, schemas.Vertex, schemas.Bedrock:
						require.NotNil(t, rawReq,
							"Raw request should be present for %s prompt-caching validation",
							testConfig.Provider)
					}
					if rawReq != nil {
						rawJSON, marshalErr := sonic.Marshal(rawReq)
						require.NoError(t, marshalErr, "Raw request should be marshalable to JSON")
						rawStr := string(rawJSON)

						switch testConfig.Provider {
						case schemas.Anthropic, schemas.Vertex:
					// Verify cache_control markers survived: system block (1) + penultimate message (1) = 2
					cacheControlCount := strings.Count(rawStr, `"cache_control"`)
					t.Logf("  %s: found %d cache_control markers in raw request", testConfig.Provider, cacheControlCount)
					require.GreaterOrEqual(t, cacheControlCount, 2,
						"Expected at least 2 cache_control markers (system + penultimate), got %d", cacheControlCount)

							// Verify key ordering: call_weather_ny has {"unit":...,"location":...}
							nyCallIdx := strings.Index(rawStr, `call_weather_ny`)
							require.NotEqual(t, -1, nyCallIdx,
								"Raw request should contain call_weather_ny for key-order validation")

							afterNY := rawStr[nyCallIdx:]
							unitIdx := strings.Index(afterNY, `"unit"`)
							locIdx := strings.Index(afterNY, `"location"`)
							require.NotEqual(t, -1, unitIdx, `Expected "unit" in call_weather_ny payload`)
							require.NotEqual(t, -1, locIdx, `Expected "location" in call_weather_ny payload`)

							assert.True(t, unitIdx < locIdx,
								"Key order not preserved for call_weather_ny: expected unit before location")
							t.Logf("  Key order preserved: 'unit' at %d, 'location' at %d", unitIdx, locIdx)

					case schemas.Bedrock:
						cachePointCount := strings.Count(rawStr, `"cachePoint"`)
						t.Logf("  Bedrock: found %d cachePoint blocks", cachePointCount)
						require.GreaterOrEqual(t, cachePointCount, 2,
							"Expected at least 2 cachePoint blocks (system + last tool_use), got %d", cachePointCount)
						}
					}
				}

				prevCacheRead = cacheRead

				// Add the current user message to history for next turn
				conversationHistory = append(conversationHistory, makeUserMsg(turn.query))
			})
		}

		// For OpenAI (best-effort automatic caching), verify that at least 3 out of 9
		// turns (Turn 2-10) had cache hits. This proves caching works without requiring
		// deterministic per-turn behavior.
		if testConfig.Provider == schemas.OpenAI {
			totalEligibleTurns := len(turns) - 1 // exclude Turn 1 (cache warmup)
			t.Logf("  OpenAI aggregate: %d/%d turns had cache hits (>= 50%%)", turnsWithCacheHit, totalEligibleTurns)
			require.GreaterOrEqual(t, turnsWithCacheHit, 3,
				"OpenAI: expected at least 3 out of %d turns to have cache hits, got %d. "+
					"This suggests the request prefix is not stable across turns.",
				totalEligibleTurns, turnsWithCacheHit)
		}

		t.Logf("  Prompt caching multiple tool calls test completed (10 turns)!")
	})
}

// RunPromptCachingTest executes the prompt caching test scenario
// This test verifies that OpenAI's prompt caching works correctly with tools
// by making multiple requests with the same long prefix and tools, and verifying
// that cached tokens increase in subsequent requests.
func RunPromptCachingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SimpleChat {
		t.Logf("Prompt caching test requires SimpleChat support")
		return
	}
	if !testConfig.Scenarios.PromptCaching {
		t.Logf("Prompt caching test not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("PromptCaching", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		tools := GetPromptCachingTools()
		systemMessage := schemas.ChatMessage{
			Role: schemas.ChatMessageRoleSystem,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{
					{
						Type: schemas.ChatContentBlockTypeText,
						Text: bifrost.Ptr(longSharedPrefix),
						CacheControl: &schemas.CacheControl{
							Type: schemas.CacheControlTypeEphemeral,
						},
					},
				},
			},
		}

		// Test queries - same pattern as Python script
		testQueries := []struct {
			name    string
			message string
		}{
			{"FirstQuery", "Explain our API to a beginner."},
			{"SecondQuery", "Now give me a 5-step onboarding checklist."},
		}

		for i, query := range testQueries {
			t.Run(query.name, func(t *testing.T) {
				userMessage := schemas.ChatMessage{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr(query.message),
					},
				}

				chatReq := &schemas.BifrostChatRequest{
					Provider: testConfig.Provider,
					Model:    testConfig.PromptCachingModel,
					Input: []schemas.ChatMessage{
						systemMessage,
						userMessage,
					},
					Params: &schemas.ChatParameters{
						Tools: tools,
						ToolChoice: &schemas.ChatToolChoice{
							ChatToolChoiceStr: bifrost.Ptr("auto"),
						},
					},
					Fallbacks: testConfig.Fallbacks,
				}

				// Create retry config with 5 attempts
				retryConfig := ChatRetryConfig{
					MaxAttempts: 5,
					BaseDelay:   2 * time.Second,
					MaxDelay:    10 * time.Second,
					Conditions:  []ChatRetryCondition{},
					OnRetry: func(attempt int, reason string, t *testing.T) {
						t.Logf("🔄 Retrying query %d (attempt %d): %s", i+1, attempt, reason)
					},
					OnFinalFail: func(attempts int, finalErr error, t *testing.T) {
						t.Logf("❌ Query %d failed after %d attempts: %v", i+1, attempts, finalErr)
					},
				}

				// Create expectations - only add cached tokens validation for query 3
				expectations := ResponseExpectations{
					ShouldHaveContent:    true,
					ShouldHaveUsageStats: true,
				}

				// For the second query (index 1), add cached tokens validation
				if i == 1 {
					expectations.ProviderSpecific = map[string]interface{}{
						"min_cached_tokens_percentage": 0.80, // 80% minimum
						"query_index":                  i,
					}
				}

				retryContext := TestRetryContext{
					ScenarioName: "PromptCaching",
					ExpectedBehavior: map[string]interface{}{
						"query_index": i,
						"query_name":  query.name,
					},
					TestMetadata: map[string]interface{}{
						"provider": testConfig.Provider,
						"model":    testConfig.ChatModel,
					},
				}

				// Execute with retry framework
				operation := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
					bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
					return client.ChatCompletionRequest(bfCtx, chatReq)
				}

				response, err := WithChatTestRetry(t, retryConfig, retryContext, expectations, query.name, operation)

				require.Nil(t, err, "Chat completion request should succeed: %v", err)
				require.NotNil(t, response, "Response should not be nil")
				require.NotNil(t, response.Usage, "Usage information should be present")

				// Extract cached tokens
				var cachedTokens int
				if response.Usage.PromptTokensDetails != nil {
					cachedTokens = response.Usage.PromptTokensDetails.CachedReadTokens + response.Usage.PromptTokensDetails.CachedWriteTokens
				}

				promptTokens := response.Usage.PromptTokens
				totalTokens := response.Usage.TotalTokens

				t.Logf("Query %d (%s):", i+1, query.name)
				t.Logf("  Total tokens: %d", totalTokens)
				t.Logf("  Prompt tokens: %d", promptTokens)
				t.Logf("  Cached tokens: %d", cachedTokens)

				// Verify response has content
				content := GetChatContent(response)
				assert.NotEmpty(t, content, "Response should have content")
				if len(content) > 100 {
					t.Logf("  Response preview: %s...", content[:100])
				} else {
					t.Logf("  Response preview: %s", content)
				}

				// For the first request, log cached tokens (may be non-zero if cache exists from previous runs)
				if i == 0 {
					if cachedTokens == 0 {
						t.Logf("  ✅ First request has 0 cached tokens (fresh cache)")
					} else {
						t.Logf("  ℹ️  First request has %d cached tokens (cache from previous test run)", cachedTokens)
					}
				} else if i == 1 {
					// Query 2: Verify cached tokens are >80% of prompt tokens
					// This validation is also done in the retry framework, but we verify here as well
					if promptTokens > 0 {
						cachedPercentage := float64(cachedTokens) / float64(promptTokens)
						t.Logf("  Cached tokens percentage: %.2f%%", cachedPercentage*100)

						require.GreaterOrEqual(t, cachedPercentage, 0.80,
							"Query 2 should have at least 80%% cached tokens (got %.2f%%, cached: %d, prompt: %d)",
							cachedPercentage*100, cachedTokens, promptTokens)

						t.Logf("  ✅ Cached tokens percentage: %.2f%% (>= 80%%)", cachedPercentage*100)
					} else {
						t.Fatalf("Prompt tokens is 0, cannot calculate cached percentage")
					}
				}
			})
		}

		t.Logf("🎉 Prompt caching test completed!")
	})
}

// RunPromptCachingMultiTurnTest verifies prompt caching across a 10-turn
// multi-turn conversation. Each turn appends the assistant's previous response
// and a new user message, while keeping the system message and tools constant.
// The system prefix + tools form the cached prefix; turns 2+ should show
// cached_read_tokens > 0, proving caching is intact.
func RunPromptCachingMultiTurnTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SimpleChat {
		t.Logf("Prompt caching multi-turn test requires SimpleChat support")
		return
	}
	if !testConfig.Scenarios.PromptCaching {
		t.Logf("Prompt caching multi-turn test not supported for provider %s", testConfig.Provider)
		return
	}
	if testConfig.PromptCachingModel == "" {
		t.Logf("No PromptCachingModel configured for provider %s, skipping", testConfig.Provider)
		return
	}

	t.Run("PromptCachingMultiTurn", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		tools := GetPromptCachingTools()
		systemMessage := schemas.ChatMessage{
			Role: schemas.ChatMessageRoleSystem,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{
					{
						Type: schemas.ChatContentBlockTypeText,
						Text: bifrost.Ptr(longSharedPrefix),
						CacheControl: &schemas.CacheControl{
							Type: schemas.CacheControlTypeEphemeral,
						},
					},
				},
			},
		}

		queries := []string{
			"Explain our API to a beginner.",
			"What authentication methods does the API support?",
			"How do I set up rate limiting?",
			"Describe the analytics dashboard features.",
			"What security certifications do we have?",
			"How does the support tier system work?",
			"Explain the pricing model for compute instances.",
			"What SDKs are available for developers?",
			"How do I configure webhooks?",
			"Give me a summary of everything we discussed.",
		}

		// Conversation history grows with each turn
		var conversationMessages []schemas.ChatMessage
		turnsWithCacheHit := 0

		for i, query := range queries {
			turnName := fmt.Sprintf("Turn%d", i+1)
			t.Run(turnName, func(t *testing.T) {
				userMessage := schemas.ChatMessage{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr(query),
					},
				}

				// Build input: system + conversation history + new user message
				input := make([]schemas.ChatMessage, 0, 2+len(conversationMessages))
				input = append(input, systemMessage)
				input = append(input, conversationMessages...)
				input = append(input, userMessage)

				chatReq := &schemas.BifrostChatRequest{
					Provider: testConfig.Provider,
					Model:    testConfig.PromptCachingModel,
					Input:    input,
					Params: &schemas.ChatParameters{
						Tools: tools,
						ToolChoice: &schemas.ChatToolChoice{
							ChatToolChoiceStr: bifrost.Ptr("none"),
						},
					},
					Fallbacks: testConfig.Fallbacks,
				}

				retryConfig := ChatRetryConfig{
					MaxAttempts: 5,
					BaseDelay:   2 * time.Second,
					MaxDelay:    10 * time.Second,
					Conditions:  []ChatRetryCondition{},
					OnRetry: func(attempt int, reason string, t *testing.T) {
						t.Logf("Retrying turn %d (attempt %d): %s", i+1, attempt, reason)
					},
					OnFinalFail: func(attempts int, finalErr error, t *testing.T) {
						t.Logf("Turn %d failed after %d attempts: %v", i+1, attempts, finalErr)
					},
				}

				expectations := ResponseExpectations{
					ShouldHaveContent:    true,
					ShouldHaveUsageStats: true,
				}

				// No percentage-based validation here — the conversation grows faster
				// than the cached prefix, so percentage drops naturally. The meaningful
				// assertion is cached_read_tokens > 0 on turns 2+, checked below.

				retryContext := TestRetryContext{
					ScenarioName: "PromptCachingMultiTurn",
					ExpectedBehavior: map[string]interface{}{
						"turn":  i + 1,
						"query": query,
					},
					TestMetadata: map[string]interface{}{
						"provider": testConfig.Provider,
						"model":    testConfig.PromptCachingModel,
					},
				}

				operation := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
					bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
					return client.ChatCompletionRequest(bfCtx, chatReq)
				}

				response, err := WithChatTestRetry(t, retryConfig, retryContext, expectations, turnName, operation)

				require.Nil(t, err, "Turn %d should succeed: %v", i+1, err)
				require.NotNil(t, response, "Response should not be nil")
				require.NotNil(t, response.Usage, "Usage information should be present")

				var cachedTokens int
				if response.Usage.PromptTokensDetails != nil {
					cachedTokens = response.Usage.PromptTokensDetails.CachedReadTokens + response.Usage.PromptTokensDetails.CachedWriteTokens
				}

				promptTokens := response.Usage.PromptTokens

				t.Logf("Turn %d: prompt_tokens=%d, cached_tokens=%d (read=%d, write=%d)",
					i+1, promptTokens, cachedTokens,
					func() int {
						if response.Usage.PromptTokensDetails != nil {
							return response.Usage.PromptTokensDetails.CachedReadTokens
						}
						return 0
					}(),
					func() int {
						if response.Usage.PromptTokensDetails != nil {
							return response.Usage.PromptTokensDetails.CachedWriteTokens
						}
						return 0
					}(),
				)

			// For turns 2+, verify cache is being used
			if i >= 1 {
				var cachedRead int
				if response.Usage.PromptTokensDetails != nil {
					cachedRead = response.Usage.PromptTokensDetails.CachedReadTokens
				}
				if testConfig.Provider == schemas.OpenAI {
					// OpenAI uses best-effort automatic caching; individual turns may miss.
					// Track hits for the aggregate assertion after the loop.
					if cachedRead > 0 {
						turnsWithCacheHit++
						t.Logf("Turn %d: cache HIT confirmed (cached_read_tokens=%d)", i+1, cachedRead)
					} else {
						t.Logf("Turn %d: cache MISS (OpenAI best-effort, expected occasionally)", i+1)
					}
				} else {
					require.Greater(t, cachedRead, 0,
						"Turn %d: cached_read_tokens should be > 0 (caching broken)", i+1)
					t.Logf("Turn %d: cache HIT confirmed (cached_read_tokens=%d)", i+1, cachedRead)
				}
			}

				// Log final turn percentage for observability (no assertion — conversation
				// growth naturally dilutes the cached prefix percentage)
				if i == len(queries)-1 && promptTokens > 0 {
					cachedPercentage := float64(cachedTokens) / float64(promptTokens)
					t.Logf("Turn %d: final cached percentage: %.2f%% (cached=%d, prompt=%d)",
						i+1, cachedPercentage*100, cachedTokens, promptTokens)
				}

				// Add user message and assistant response to conversation history
				content := GetChatContent(response)
				conversationMessages = append(conversationMessages, userMessage)
				conversationMessages = append(conversationMessages, schemas.ChatMessage{
					Role: schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{
						ContentStr: &content,
					},
				})
			})
		}

		// For OpenAI (best-effort automatic caching), verify that at least 5 out of 9
		// turns (Turn 2-10) had cache hits. This is stricter than the Multiple Tool Calls
		// test (>= 3) since this simpler conversation should cache more reliably.
		if testConfig.Provider == schemas.OpenAI {
			totalEligibleTurns := len(queries) - 1 // exclude Turn 1 (cache warmup)
			t.Logf("OpenAI aggregate: %d/%d turns had cache hits", turnsWithCacheHit, totalEligibleTurns)
			require.GreaterOrEqual(t, turnsWithCacheHit, 5,
				"OpenAI: expected at least 5 out of %d turns to have cache hits, got %d. "+
					"This suggests the request prefix is not stable across turns.",
				totalEligibleTurns, turnsWithCacheHit)
		}

		t.Logf("Multi-turn prompt caching test completed (%d turns)", len(queries))
	})
}
