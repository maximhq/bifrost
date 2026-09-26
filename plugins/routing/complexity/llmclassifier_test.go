package complexity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLLMFallbackAnalyzerConfig() *AnalyzerConfig {
	cfg := DefaultAnalyzerConfig()
	cfg.Semantic = &SemanticConfig{
		Provider:       "openai",
		EmbeddingModel: "text-embedding-3-small",
		Fallback:       configstore.ComplexitySemanticFallbackLLM,
	}
	cfg.LLM = &LLMConfig{Provider: "openai", Model: "gpt-4.1-mini"}
	return &cfg
}

func TestParseLLMTier(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "contract answer", raw: `{"tier": "SIMPLE"}`, want: TierSimple},
		{name: "lowercase tier", raw: `{"tier": "medium"}`, want: TierMedium},
		{name: "fenced json", raw: "```json\n{\"tier\": \"COMPLEX\"}\n```", want: TierComplex},
		{name: "bare fence", raw: "```\n{\"tier\": \"SIMPLE\"}\n```", want: TierSimple},
		{name: "json with prose around it", raw: `Sure! {"tier": "MEDIUM"} Hope that helps.`, want: TierMedium},
		{name: "bare word", raw: "COMPLEX", want: TierComplex},
		{name: "quoted bare word with period", raw: `"simple".`, want: TierSimple},
		{name: "tier buried in prose rejected", raw: "the request is not COMPLEX", wantErr: true},
		{name: "unknown tier rejected", raw: `{"tier": "REASONING"}`, wantErr: true},
		{name: "empty rejected", raw: "", wantErr: true},
		{name: "malformed json falls through to bare word check and fails", raw: `{"tier": SIMPLE`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier, err := parseLLMTier(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrLLMTierUnparseable)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, tier)
		})
	}
}

func TestParseLLMTierErrorTruncatesEcho(t *testing.T) {
	_, err := parseLLMTier(strings.Repeat("z", maxQuotedLLMResponseChars*3))
	require.Error(t, err)
	assert.Less(t, len(err.Error()), maxQuotedLLMResponseChars*2)
}

func TestLLMSystemPrompt(t *testing.T) {
	t.Run("no custom prompt ships guidance plus reinforcement", func(t *testing.T) {
		prompt := llmSystemPrompt(&LLMConfig{})
		require.True(t, strings.HasPrefix(prompt, llmClassifierGuidance))
		require.True(t, strings.HasSuffix(prompt, llmClassifierReinforcement))
	})
	t.Run("custom prompt replaces guidance, never the reinforcement", func(t *testing.T) {
		prompt := llmSystemPrompt(&LLMConfig{Prompt: "route legal work to COMPLEX"})
		require.True(t, strings.HasPrefix(prompt, "route legal work to COMPLEX"))
		assert.NotContains(t, prompt, llmClassifierGuidance)
		require.True(t, strings.HasSuffix(prompt, llmClassifierReinforcement))
	})
	t.Run("reinforcement pins the response contract", func(t *testing.T) {
		// The reinforcement is the half no edit can remove, so it must carry
		// everything parsing depends on: all three tier names and the JSON
		// shape.
		for _, needle := range []string{`"SIMPLE"`, `"MEDIUM"`, `"COMPLEX"`, `{"tier": "SIMPLE"}`} {
			assert.Contains(t, llmClassifierReinforcement, needle)
		}
	})
	t.Run("default guidance is exposed for configuration clients", func(t *testing.T) {
		assert.Equal(t, llmClassifierGuidance, DefaultLLMClassifierGuidance())
	})
}

func TestLLMClassifierLifecycle(t *testing.T) {
	classifier := NewLLMClassifier(nil)

	t.Run("unconfigured classifies to nil,nil", func(t *testing.T) {
		result, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "hello"})
		require.NoError(t, err)
		assert.Nil(t, result)
		assert.False(t, classifier.IsConfigured())
		assert.False(t, classifier.FallbackEnabled())
		assert.Equal(t, LLMStatusDisabled, classifier.Status().State)
	})

	t.Run("configured but unwired classifies to nil,nil", func(t *testing.T) {
		classifier.Configure(testLLMFallbackAnalyzerConfig())
		result, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "hello"})
		require.NoError(t, err)
		assert.Nil(t, result)
		assert.True(t, classifier.IsConfigured())
		assert.True(t, classifier.FallbackEnabled())
		assert.Equal(t, LLMStatusReady, classifier.Status().State)
	})

	t.Run("dormant llm block is configured but not enabled", func(t *testing.T) {
		cfg := testLLMFallbackAnalyzerConfig()
		cfg.Semantic.Fallback = configstore.ComplexitySemanticFallbackNone
		classifier.Configure(cfg)
		assert.True(t, classifier.IsConfigured())
		assert.False(t, classifier.FallbackEnabled())
	})
}

func TestLLMClassifierClassify(t *testing.T) {
	newClassifier := func(chat ChatFunc) *LLMClassifier {
		classifier := NewLLMClassifier(nil)
		classifier.Configure(testLLMFallbackAnalyzerConfig())
		classifier.SetChatFunc(chat)
		return classifier
	}

	t.Run("returns the named tier", func(t *testing.T) {
		var gotSystem, gotUser string
		classifier := newClassifier(func(_ context.Context, _ *LLMConfig, systemPrompt, userText string) (string, error) {
			gotSystem, gotUser = systemPrompt, userText
			return `{"tier": "MEDIUM"}`, nil
		})
		result, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "refactor the parser"})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, TierMedium, result.Tier)
		assert.Equal(t, llmSystemPrompt(&LLMConfig{}), gotSystem)
		assert.Equal(t, "refactor the parser", gotUser)
	})

	t.Run("custom prompt reaches the provider call", func(t *testing.T) {
		cfg := testLLMFallbackAnalyzerConfig()
		cfg.LLM.Prompt = "route payroll to COMPLEX"
		var gotSystem string
		classifier := NewLLMClassifier(nil)
		classifier.Configure(cfg)
		classifier.SetChatFunc(func(_ context.Context, _ *LLMConfig, systemPrompt, _ string) (string, error) {
			gotSystem = systemPrompt
			return `{"tier": "COMPLEX"}`, nil
		})
		_, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "run payroll"})
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(gotSystem, "route payroll to COMPLEX"))
		require.True(t, strings.HasSuffix(gotSystem, llmClassifierReinforcement))
	})

	t.Run("propagates transport errors", func(t *testing.T) {
		transportErr := errors.New("boom")
		classifier := newClassifier(func(context.Context, *LLMConfig, string, string) (string, error) {
			return "", transportErr
		})
		_, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "hello"})
		assert.ErrorIs(t, err, transportErr)
	})

	t.Run("unparseable answer is a tagged error", func(t *testing.T) {
		classifier := newClassifier(func(context.Context, *LLMConfig, string, string) (string, error) {
			return "I would say this one is fairly hard.", nil
		})
		_, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "hello"})
		assert.ErrorIs(t, err, ErrLLMTierUnparseable)
	})

	t.Run("blank input skips the provider call", func(t *testing.T) {
		called := false
		classifier := newClassifier(func(context.Context, *LLMConfig, string, string) (string, error) {
			called = true
			return `{"tier": "SIMPLE"}`, nil
		})
		result, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "   "})
		require.NoError(t, err)
		assert.Nil(t, result)
		assert.False(t, called)
	})

	t.Run("history window follows message_history_count", func(t *testing.T) {
		cfg := testLLMFallbackAnalyzerConfig()
		cfg.LLM.MessageHistoryCount = 2
		var gotUser string
		classifier := NewLLMClassifier(nil)
		classifier.Configure(cfg)
		classifier.SetChatFunc(func(_ context.Context, _ *LLMConfig, _, userText string) (string, error) {
			gotUser = userText
			return `{"tier": "SIMPLE"}`, nil
		})
		_, err := classifier.Classify(context.Background(), ComplexityInput{
			LastUserText:   "and make it faster",
			PriorUserTexts: []string{"first turn", "second turn"},
		})
		require.NoError(t, err)
		assert.Equal(t, "second turn\nand make it faster", gotUser)
	})
}
