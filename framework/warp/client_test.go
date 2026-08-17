package warp

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Warp speaks OpenAI to Bifrost's compatibility mount whatever provider is
// configured. Declaring the configured provider instead makes that provider's
// implementation build its own path - Anthropic asks for /v1/messages, which
// under /openai is not a route and comes back as "Method Not Allowed".
func TestWarpAccountSpeaksOpenAIRegardlessOfConfiguredProvider(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{schemas.Anthropic, schemas.Bedrock, schemas.Vertex, schemas.OpenAI} {
		account := &warpAccount{config: &schemas.WarpConfig{Provider: provider, Model: "some-model"}}
		declared, err := account.GetConfiguredProviders()
		require.NoError(t, err)
		require.Equal(t, []schemas.ModelProvider{schemas.OpenAI}, declared,
			"%s must still be reached over the OpenAI wire format", provider)
	}
}

// The configured provider is not lost, it moves into the model string - which is
// what actually routes the request once it reaches Bifrost.
func TestWarpModelCarriesConfiguredProvider(t *testing.T) {
	require.Equal(t, "anthropic/claude-sonnet-5",
		modelForRequest(&schemas.WarpConfig{Provider: schemas.Anthropic, Model: "claude-sonnet-5"}))
	// An operator who typed the qualified form gets exactly what they typed.
	require.Equal(t, "vertex/gemini-2.5-pro",
		modelForRequest(&schemas.WarpConfig{Provider: schemas.Anthropic, Model: "vertex/gemini-2.5-pro"}))
}

// bifrost.Init stores a context derived from the one it is handed, so passing
// the request context ties the cached instance's lifetime to whichever request
// happened to build it. That request ending - a user closing the tab mid-answer
// - then poisons the shared instance for everyone after them.
func TestWarpClientInstanceOutlivesTheRequestThatBuiltIt(t *testing.T) {
	client := NewClient(bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(client.Shutdown)
	config := &schemas.WarpConfig{Enabled: true, Provider: schemas.OpenAI, Model: "gpt-4o"}

	first, cancelFirst := context.WithCancel(context.Background())
	instance, err := client.instanceFor(first, config)
	require.NoError(t, err)
	require.NotNil(t, instance)

	// The request that built the instance goes away. Cancellation propagates
	// through a watcher goroutine, so give it time to land rather than racing it.
	cancelFirst()
	require.Eventually(t, func() bool { return first.Err() != nil }, time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	second, err := client.instanceFor(context.Background(), config)
	require.NoError(t, err)
	require.Same(t, instance, second, "the cached instance should be reused")

	// UpdateProvider refuses once the instance context is done, which is the
	// observable form of "this cached client is dead".
	require.NoError(t, second.UpdateProvider(schemas.OpenAI),
		"the cached instance must not be torn down with the request that built it")
}

// A replaced instance has to outlive any turn already running against it.
// Shutdown cancels the instance context, and a turn makes up to maxIterations
// sequential model calls - so a grace of one call's timeout aborted a turn
// mid-flight whenever settings were saved while somebody was waiting.
func TestWarpClientRetirementGraceCoversAWholeTurn(t *testing.T) {
	config := &schemas.WarpConfig{
		Enabled: true, Provider: schemas.OpenAI, Model: "gpt-4o",
		MaxIterations: 6, RequestTimeoutSeconds: 30,
	}
	require.Equal(t, 180*time.Second, retirementGrace(config),
		"the grace must cover every iteration a turn may take, not one call")

	// Defaults resolve the same way Turn.Budget does.
	defaults := &schemas.WarpConfig{Enabled: true, Provider: schemas.OpenAI, Model: "gpt-4o"}
	expected := time.Duration(schemas.WarpDefaultMaxIterations*schemas.WarpDefaultRequestTimeoutSeconds) * time.Second
	require.Equal(t, expected, retirementGrace(defaults))
	require.Greater(t, retirementGrace(defaults),
		time.Duration(defaults.EffectiveRequestTimeoutSeconds())*time.Second)
}

// A replaced instance has to be retired on its own grace, not its successor's.
//
// The grace is a whole turn's worth of budget, computed from the config the
// instance was built with. Scheduling the old instance's shutdown from the new
// config meant that saving a shorter timeout cancelled a turn already running
// under the longer one - the setting change reached back and killed work that
// started before it.
func TestWarpReplacedInstanceRetiresOnItsOwnGrace(t *testing.T) {
	var scheduled []time.Duration
	original := scheduleRetirement
	scheduleRetirement = func(d time.Duration, fn func()) *time.Timer {
		scheduled = append(scheduled, d)
		return time.AfterFunc(time.Hour, func() {}) // never fires during the test
	}
	t.Cleanup(func() { scheduleRetirement = original })

	client := NewClient(nil)
	slow := &schemas.WarpConfig{
		Provider: "openai", Model: "gpt-4o",
		MaxIterations: 8, RequestTimeoutSeconds: 600,
	}
	fast := &schemas.WarpConfig{
		Provider: "openai", Model: "gpt-4o-mini",
		MaxIterations: 2, RequestTimeoutSeconds: 5,
	}

	_, err := client.instanceFor(context.Background(), slow)
	require.NoError(t, err)
	require.Empty(t, scheduled, "the first instance replaces nothing")

	_, err = client.instanceFor(context.Background(), fast)
	require.NoError(t, err)
	require.Len(t, scheduled, 1)
	require.Equal(t, retirementGrace(slow), scheduled[0],
		"the replaced instance must be retired on the grace it was built with, not the replacement's")
	require.NotEqual(t, retirementGrace(fast), scheduled[0])
}

// An operator may type a provider-qualified model, and modelForRequest keeps it
// that way on the wire. The catalog keys on the bare name, so handing it the
// qualified form misses every entry and prices the turn at zero - the doc on
// costFuncFor already says as much about the prefix it adds.
func TestWarpCatalogModelStripsProviderPrefix(t *testing.T) {
	require.Equal(t, "claude-sonnet-5", catalogModel("anthropic/claude-sonnet-5"))
	require.Equal(t, "gpt-5.5", catalogModel("openai/gpt-5.5"))
	require.Equal(t, "gpt-5.5", catalogModel("gpt-5.5"))
	require.Equal(t, "", catalogModel(""))

	// Only the first segment is a provider; a model whose own name has slashes
	// keeps the rest.
	require.Equal(t, "meta/llama-3/70b", catalogModel("bedrock/meta/llama-3/70b"))
}

// A slash in a model name does not make it provider-qualified.
//
// Native slugs carry slashes of their own - "meta/llama-3-8b" on Replicate,
// "meta-llama/Llama-3.1-8B" elsewhere - and "meta" is not a Bifrost provider.
// Treating any slash as a prefix meant a configured provider was dropped from
// the wire name, so the request could take the default route instead of the one
// the operator chose, and the catalog lookup was handed a truncated slug.
func TestWarpModelNameHandlingRespectsKnownProviders(t *testing.T) {
	t.Run("a slash-bearing slug keeps its provider prefix", func(t *testing.T) {
		config := &schemas.WarpConfig{Provider: "bedrock", Model: "meta/llama-3-8b"}
		require.Equal(t, "bedrock/meta/llama-3-8b", modelForRequest(config),
			"meta is not a provider, so the configured one must still be sent")
	})

	t.Run("an already-qualified model is left alone", func(t *testing.T) {
		config := &schemas.WarpConfig{Provider: "bedrock", Model: "anthropic/claude-sonnet-5"}
		require.Equal(t, "anthropic/claude-sonnet-5", modelForRequest(config),
			"the operator typed a real provider prefix, so it stands")
	})

	t.Run("a bare model takes the configured provider", func(t *testing.T) {
		require.Equal(t, "openai/gpt-5.5", modelForRequest(&schemas.WarpConfig{Provider: "openai", Model: "gpt-5.5"}))
	})

	t.Run("the catalog strips only a real provider segment", func(t *testing.T) {
		require.Equal(t, "claude-sonnet-5", catalogModel("anthropic/claude-sonnet-5"))
		require.Equal(t, "meta/llama-3-8b", catalogModel("meta/llama-3-8b"),
			"meta is part of the slug, so stripping it would miss the catalog entry")
		require.Equal(t, "meta/llama-3-8b", catalogModel("bedrock/meta/llama-3-8b"),
			"only the provider segment comes off, not every segment")
		require.Equal(t, "gpt-5.5", catalogModel("gpt-5.5"))
	})
}
