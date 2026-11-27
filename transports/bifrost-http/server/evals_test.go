package server

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only an enabled key that is marked for evaluations is an eval key. Its model lists
// still apply, and a key with no concurrency set takes the default.
func TestListEvalKeys(t *testing.T) {
	s := &BifrostHTTPServer{Config: libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		"openai":    {},
		"anthropic": {},
	}, map[schemas.ModelProvider][]schemas.Key{
		"openai": {
			{ID: "prod", Models: schemas.WhiteList{"*"}},
			{ID: "prod-explicit", Models: schemas.WhiteList{"*"}, UseForEvals: new(false)},
			{ID: "evals", Models: schemas.WhiteList{"gpt-4o-mini", "gpt-4o"}, BlacklistedModels: schemas.BlackList{"gpt-4o"}, UseForEvals: new(true), EvalConcurrency: new(8)},
			{ID: "evals-default", Models: schemas.WhiteList{"*"}, UseForEvals: new(true)},
			{ID: "evals-zero", Models: schemas.WhiteList{"*"}, UseForEvals: new(true), EvalConcurrency: new(0)},
			{ID: "evals-disabled", Models: schemas.WhiteList{"*"}, UseForEvals: new(true), Enabled: new(false)},
			{ID: "evals-huge", Models: schemas.WhiteList{"*"}, UseForEvals: new(true), EvalConcurrency: new(100000)},
		},
		"anthropic": {
			{ID: "claude-evals", Models: schemas.WhiteList{"*"}, UseForEvals: new(true), EvalConcurrency: new(2)},
		},
	})}

	byID := map[string]evals.EvalKey{}
	for _, key := range s.listEvalKeys() {
		byID[key.ID] = key
	}
	require.Len(t, byID, 5, "production keys and disabled keys are never eval keys")

	evalsKey := byID["evals"]
	assert.Equal(t, "openai", evalsKey.Provider)
	assert.Equal(t, 8, evalsKey.Concurrency)
	assert.True(t, evalsKey.Supports("gpt-4o-mini"))
	assert.False(t, evalsKey.Supports("gpt-4o"), "a model on the key's deny list is not served")
	assert.False(t, evalsKey.Supports("o3"), "nor is one off its allow list")

	assert.Equal(t, evals.DefaultEvalKeyConcurrency, byID["evals-default"].Concurrency)
	assert.Equal(t, evals.DefaultEvalKeyConcurrency, byID["evals-zero"].Concurrency, "zero is not a usable concurrency")
	assert.Equal(t, evals.MaxEvalKeyConcurrency, byID["evals-huge"].Concurrency, "a value stored before the maximum existed is clamped to it")
	assert.True(t, byID["evals-default"].Supports("anything"))
	assert.Equal(t, "anthropic", byID["claude-evals"].Provider)
	assert.Equal(t, 2, byID["claude-evals"].Concurrency)
}

// A vLLM key serves only the model its instance runs, as core's key selection requires.
// Supports must say so, or a job for one model is placed on the key of another and core
// then refuses the pin. A custom provider based on vLLM follows the same rule.
func TestListEvalKeysFollowsTheVLLMServedModel(t *testing.T) {
	vllmKey := func(id, served string) schemas.Key {
		return schemas.Key{ID: id, Models: schemas.WhiteList{"*"}, UseForEvals: new(true), VLLMKeyConfig: &schemas.VLLMKeyConfig{ModelName: served}}
	}
	s := &BifrostHTTPServer{Config: libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.VLLM: {},
		"my-vllm":    {CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.VLLM}},
	}, map[schemas.ModelProvider][]schemas.Key{
		schemas.VLLM: {vllmKey("alpha", "alpha"), vllmKey("beta", "beta")},
		"my-vllm":    {vllmKey("custom-alpha", "alpha")},
	})}
	byID := map[string]evals.EvalKey{}
	for _, key := range s.listEvalKeys() {
		byID[key.ID] = key
	}
	require.Len(t, byID, 3)
	assert.True(t, byID["alpha"].Supports("alpha"))
	assert.False(t, byID["alpha"].Supports("beta"), "the alpha instance does not serve beta")
	assert.True(t, byID["beta"].Supports("beta"))
	assert.True(t, byID["custom-alpha"].Supports("alpha"))
	assert.False(t, byID["custom-alpha"].Supports("beta"), "a custom provider based on vLLM follows the served model too")
}

// The key source is asked on every job, so it must not rebuild the list each time, and
// must still notice a change soon after.
func TestEvalKeySourceCachesBriefly(t *testing.T) {
	s := &BifrostHTTPServer{Config: libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		"openai": {},
	}, map[schemas.ModelProvider][]schemas.Key{
		"openai": {{ID: "evals", Models: schemas.WhiteList{"*"}, UseForEvals: new(true)}},
	})}
	source := s.evalKeySource()

	first, err := source(context.Background())
	require.NoError(t, err)
	require.Len(t, first, 1)

	libtest.SetProvider(t, s.Config, "openai", configstore.ProviderConfig{}, []schemas.Key{{ID: "evals", Models: schemas.WhiteList{"*"}}})

	cached, err := source(context.Background())
	require.NoError(t, err)
	assert.Len(t, cached, 1, "within the cache window the earlier answer is reused")

	require.Eventually(t, func() bool {
		keys, _ := source(context.Background())
		return len(keys) == 0
	}, 2*evalKeysCacheTTL+500_000_000, 50_000_000, "an unmarked key stops being an eval key shortly after")
}

// Shutdown gives all of the server's cleanup 30 seconds. Waiting for evaluations in
// flight must leave time for the stores and the client to close after it.
func TestEvalsShutdownLeavesTimeForTheRest(t *testing.T) {
	assert.LessOrEqual(t, evalsShutdownTimeout, 10*time.Second)
}

// Without a log store the evaluation queue is in memory, and the runner has to be
// told that it forgets its messages on a restart.
func TestEvalsQueueWithoutALogStoreForgets(t *testing.T) {
	s := &BifrostHTTPServer{Config: &lib.Config{}}
	q, forgets := s.newEvalsQueue(context.Background())
	require.NotNil(t, q)
	t.Cleanup(func() { _ = q.Close(context.Background()) })
	assert.True(t, forgets)
}

// unqueueableLogStore is a log store that cannot back a queue: it exposes no database.
type unqueueableLogStore struct{ logstore.LogStore }

// With a log store configured, other instances may share the execution store. A queue
// that falls back to memory there would resume their running jobs as its own, so when
// the log store cannot back the queue, evaluations are unavailable instead.
func TestEvalsQueueDoesNotFallBackToMemoryWithALogStore(t *testing.T) {
	previous := logger
	logger = noopTestLogger{}
	t.Cleanup(func() { logger = previous })
	s := &BifrostHTTPServer{Config: &lib.Config{LogsStore: unqueueableLogStore{}}}
	q, forgets := s.newEvalsQueue(context.Background())
	if q != nil {
		t.Cleanup(func() { _ = q.Close(context.Background()) })
	}
	assert.Nil(t, q, "no in-memory queue next to a shared execution store")
	assert.False(t, forgets)
}
