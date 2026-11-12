package server

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only an enabled key that is marked for evaluations is an eval key. Its model lists
// still apply, and a key with no concurrency set takes the default.
func TestListEvalKeys(t *testing.T) {
	s := &BifrostHTTPServer{Config: &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
		"openai": {Keys: []schemas.Key{
			{ID: "prod", Models: schemas.WhiteList{"*"}},
			{ID: "prod-explicit", Models: schemas.WhiteList{"*"}, UseForEvals: new(false)},
			{ID: "evals", Models: schemas.WhiteList{"gpt-4o-mini", "gpt-4o"}, BlacklistedModels: schemas.BlackList{"gpt-4o"}, UseForEvals: new(true), EvalConcurrency: new(8)},
			{ID: "evals-default", Models: schemas.WhiteList{"*"}, UseForEvals: new(true)},
			{ID: "evals-zero", Models: schemas.WhiteList{"*"}, UseForEvals: new(true), EvalConcurrency: new(0)},
			{ID: "evals-disabled", Models: schemas.WhiteList{"*"}, UseForEvals: new(true), Enabled: new(false)},
		}},
		"anthropic": {Keys: []schemas.Key{
			{ID: "claude-evals", Models: schemas.WhiteList{"*"}, UseForEvals: new(true), EvalConcurrency: new(2)},
		}},
	}}}

	byID := map[string]evals.EvalKey{}
	for _, key := range s.listEvalKeys() {
		byID[key.ID] = key
	}
	require.Len(t, byID, 4, "production keys and disabled keys are never eval keys")

	evalsKey := byID["evals"]
	assert.Equal(t, "openai", evalsKey.Provider)
	assert.Equal(t, 8, evalsKey.Concurrency)
	assert.True(t, evalsKey.Supports("gpt-4o-mini"))
	assert.False(t, evalsKey.Supports("gpt-4o"), "a model on the key's deny list is not served")
	assert.False(t, evalsKey.Supports("o3"), "nor is one off its allow list")

	assert.Equal(t, evals.DefaultEvalKeyConcurrency, byID["evals-default"].Concurrency)
	assert.Equal(t, evals.DefaultEvalKeyConcurrency, byID["evals-zero"].Concurrency, "zero is not a usable concurrency")
	assert.True(t, byID["evals-default"].Supports("anything"))
	assert.Equal(t, "anthropic", byID["claude-evals"].Provider)
	assert.Equal(t, 2, byID["claude-evals"].Concurrency)
}

// The key source is asked on every job, so it must not rebuild the list each time, and
// must still notice a change soon after.
func TestEvalKeySourceCachesBriefly(t *testing.T) {
	s := &BifrostHTTPServer{Config: &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
		"openai": {Keys: []schemas.Key{{ID: "evals", Models: schemas.WhiteList{"*"}, UseForEvals: new(true)}}},
	}}}
	source := s.evalKeySource()

	first, err := source(context.Background())
	require.NoError(t, err)
	require.Len(t, first, 1)

	s.Config.Mu.Lock()
	s.Config.Providers["openai"] = configstore.ProviderConfig{Keys: []schemas.Key{{ID: "evals", Models: schemas.WhiteList{"*"}}}}
	s.Config.Mu.Unlock()

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
