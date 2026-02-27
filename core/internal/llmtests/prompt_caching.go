package llmtests

import (
	"context"
	"os"
	"testing"
	"time"

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
						"min_cached_tokens_percentage": 0.90, // 90% minimum
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
					cachedTokens = response.Usage.PromptTokensDetails.CachedTokens
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
					// Query 2: Verify cached tokens are >90% of prompt tokens
					// This validation is also done in the retry framework, but we verify here as well
					if promptTokens > 0 {
						cachedPercentage := float64(cachedTokens) / float64(promptTokens)
						t.Logf("  Cached tokens percentage: %.2f%%", cachedPercentage*100)

						require.GreaterOrEqual(t, cachedPercentage, 0.90,
							"Query 2 should have at least 90%% cached tokens (got %.2f%%, cached: %d, prompt: %d)",
							cachedPercentage*100, cachedTokens, promptTokens)

						t.Logf("  ✅ Cached tokens percentage: %.2f%% (>= 90%%)", cachedPercentage*100)
					} else {
						t.Fatalf("Prompt tokens is 0, cannot calculate cached percentage")
					}
				}
			})
		}

		t.Logf("🎉 Prompt caching test completed!")
	})
}
