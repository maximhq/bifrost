package mocker

import (
	"context"
	"strconv"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// BenchmarkMockerPlugin_PreHook_SimpleRule benchmarks simple rule matching
func BenchmarkMockerPlugin_PreHook_SimpleRule(b *testing.B) {
	plugin, err := Init(MockerConfig{
		Enabled: true,
		Rules: []MockRule{
			{
				Name:        "simple-rule",
				Enabled:     true,
				Priority:    100,
				Probability: 1.0,
				Conditions: Conditions{
					Providers: []string{"openai"},
				},
				Responses: []Response{
					{
						Type: ResponseTypeSuccess,
						Content: &SuccessResponse{
							Message: "Benchmark response",
						},
					},
				},
			},
		},
	})
	if err != nil {
		b.Fatal(err)
	}

	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Hello, benchmark test"),
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	b.ResetTimer()
	b.ReportAllocs()

	// Convert to BifrostRequest for PreLLMHook compatibility
	bifrostReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: req,
	}

	for i := 0; i < b.N; i++ {
		_, _, _ = plugin.PreLLMHook(ctx, bifrostReq)
	}
}

// BenchmarkMockerPlugin_PreHook_RegexRule benchmarks regex rule matching
func BenchmarkMockerPlugin_PreHook_RegexRule(b *testing.B) {
	plugin, err := Init(MockerConfig{
		Enabled: true,
		Rules: []MockRule{
			{
				Name:        "regex-rule",
				Enabled:     true,
				Priority:    100,
				Probability: 1.0,
				Conditions: Conditions{
					MessageRegex: bifrost.Ptr(`(?i).*hello.*`),
				},
				Responses: []Response{
					{
						Type: ResponseTypeSuccess,
						Content: &SuccessResponse{
							Message: "Regex matched response",
						},
					},
				},
			},
		},
	})
	if err != nil {
		b.Fatal(err)
	}

	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Hello, this should match the regex pattern"),
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	b.ResetTimer()
	b.ReportAllocs()

	// Convert to BifrostRequest for PreLLMHook compatibility
	bifrostReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: req,
	}

	for i := 0; i < b.N; i++ {
		_, _, _ = plugin.PreLLMHook(ctx, bifrostReq)
	}
}

// BenchmarkMockerPlugin_PreHook_MultipleRules benchmarks multiple rule evaluation
func BenchmarkMockerPlugin_PreHook_MultipleRules(b *testing.B) {
	rules := make([]MockRule, 10)
	for i := 0; i < 10; i++ {
		rules[i] = MockRule{
			Name:        "rule-" + strconv.Itoa(i),
			Enabled:     true,
			Priority:    100 - i, // Descending priority
			Probability: 1.0,
			Conditions: Conditions{
				Models: []string{"gpt-" + strconv.Itoa(i)},
			},
			Responses: []Response{
				{
					Type: ResponseTypeSuccess,
					Content: &SuccessResponse{
						Message: "Response from rule " + strconv.Itoa(i),
					},
				},
			},
		}
	}

	// Add a matching rule at the end
	rules = append(rules, MockRule{
		Name:        "matching-rule",
		Enabled:     true,
		Priority:    50,
		Probability: 1.0,
		Conditions: Conditions{
			Models: []string{"gpt-4"},
		},
		Responses: []Response{
			{
				Type: ResponseTypeSuccess,
				Content: &SuccessResponse{
					Message: "Matching rule response",
				},
			},
		},
	})

	plugin, err := Init(MockerConfig{
		Enabled: true,
		Rules:   rules,
	})
	if err != nil {
		b.Fatal(err)
	}

	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Test message"),
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	b.ResetTimer()
	b.ReportAllocs()

	// Convert to BifrostRequest for PreLLMHook compatibility
	bifrostReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: req,
	}

	for i := 0; i < b.N; i++ {
		_, _, _ = plugin.PreLLMHook(ctx, bifrostReq)
	}
}

// BenchmarkMockerPlugin_PreHook_NoMatch benchmarks when no rules match
func BenchmarkMockerPlugin_PreHook_NoMatch(b *testing.B) {
	plugin, err := Init(MockerConfig{
		Enabled:         true,
		DefaultBehavior: DefaultBehaviorPassthrough,
		Rules: []MockRule{
			{
				Name:        "non-matching-rule",
				Enabled:     true,
				Priority:    100,
				Probability: 1.0,
				Conditions: Conditions{
					Providers: []string{"anthropic"}, // Won't match OpenAI
				},
				Responses: []Response{
					{
						Type: ResponseTypeSuccess,
						Content: &SuccessResponse{
							Message: "This won't match",
						},
					},
				},
			},
		},
	})
	if err != nil {
		b.Fatal(err)
	}

	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI, // Different from rule condition
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Test message"),
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	b.ResetTimer()
	b.ReportAllocs()

	// Convert to BifrostRequest for PreLLMHook compatibility
	bifrostReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: req,
	}

	for i := 0; i < b.N; i++ {
		_, _, _ = plugin.PreLLMHook(ctx, bifrostReq)
	}
}

// BenchmarkMockerPlugin_PreHook_Template benchmarks template processing
func BenchmarkMockerPlugin_PreHook_Template(b *testing.B) {
	plugin, err := Init(MockerConfig{
		Enabled: true,
		Rules: []MockRule{
			{
				Name:        "template-rule",
				Enabled:     true,
				Priority:    100,
				Probability: 1.0,
				Conditions:  Conditions{}, // Match all
				Responses: []Response{
					{
						Type: ResponseTypeSuccess,
						Content: &SuccessResponse{
							MessageTemplate: bifrost.Ptr("Hello from {{provider}} using model {{model}}!"),
						},
					},
				},
			},
		},
	})
	if err != nil {
		b.Fatal(err)
	}

	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Test message"),
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	b.ResetTimer()
	b.ReportAllocs()

	// Convert to BifrostRequest for PreLLMHook compatibility
	bifrostReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: req,
	}

	for i := 0; i < b.N; i++ {
		_, _, _ = plugin.PreLLMHook(ctx, bifrostReq)
	}
}
