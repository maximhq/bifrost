package modelcatalog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeModelTagsStore serves GetModelTags; every other ConfigStore method is unused here.
type fakeModelTagsStore struct {
	configstore.ConfigStore
	tags map[string]map[string][]string
	err  error
}

func (f *fakeModelTagsStore) GetModelTags(context.Context) (map[string]map[string][]string, error) {
	return f.tags, f.err
}

// TestModelTagsOverlay covers the overlay's nil and store-less no-ops, copy-on-read, whole
// replacement on reload, and keeping the last good overlay when a reload fails.
func TestModelTagsOverlay(t *testing.T) {
	ctx := context.Background()

	var nilCatalog *ModelCatalog
	assert.Nil(t, nilCatalog.GetModelTags(schemas.OpenAI, "gpt-5.1"))
	require.NoError(t, nilCatalog.ReloadModelTags(ctx))

	noStore := &ModelCatalog{}
	require.NoError(t, noStore.ReloadModelTags(ctx), "without a config store there is nothing to load")
	assert.Nil(t, noStore.GetModelTags(schemas.OpenAI, "gpt-5.1"))

	store := &fakeModelTagsStore{tags: map[string]map[string][]string{"openai": {"gpt-5.1": {"approved-for-pii", "prod"}}}}
	mc := &ModelCatalog{configStore: store}
	require.NoError(t, mc.ReloadModelTags(ctx))
	assert.Equal(t, []string{"approved-for-pii", "prod"}, mc.GetModelTags(schemas.OpenAI, "gpt-5.1"))
	assert.Nil(t, mc.GetModelTags(schemas.OpenAI, "gpt-4o"))
	assert.Nil(t, mc.GetModelTags(schemas.Anthropic, "gpt-5.1"))

	got := mc.GetModelTags(schemas.OpenAI, "gpt-5.1")
	got[0] = "mutated"
	assert.Equal(t, "approved-for-pii", mc.GetModelTags(schemas.OpenAI, "gpt-5.1")[0], "callers must get a copy")

	store.tags = map[string]map[string][]string{"anthropic": {"claude-sonnet-4-5": {"prod"}}}
	require.NoError(t, mc.ReloadModelTags(ctx))
	assert.Nil(t, mc.GetModelTags(schemas.OpenAI, "gpt-5.1"), "a reload replaces the overlay as a whole")
	assert.Equal(t, []string{"prod"}, mc.GetModelTags(schemas.Anthropic, "claude-sonnet-4-5"))

	store.err = errors.New("db down")
	require.Error(t, mc.ReloadModelTags(ctx))
	assert.Equal(t, []string{"prod"}, mc.GetModelTags(schemas.Anthropic, "claude-sonnet-4-5"), "a failed reload keeps the last good overlay")
}

// TestApplyModelTags pins the lookup by ID then alias, replacement of upstream tags, and the
// nil-catalog behavior.
func TestApplyModelTags(t *testing.T) {
	mc := &ModelCatalog{configStore: &fakeModelTagsStore{tags: map[string]map[string][]string{
		"openai": {"gpt-5.1": {"prod"}, "gpt-5.1-2026-01-01": {"pinned"}},
	}}}
	require.NoError(t, mc.ReloadModelTags(context.Background()))

	byID := &schemas.Model{ID: "openai/gpt-5.1", Tags: []string{"from-upstream"}}
	mc.ApplyModelTags(byID)
	assert.Equal(t, []string{"prod"}, byID.Tags, "gateway tags replace whatever the field held")

	alias := "gpt-5.1-2026-01-01"
	byAlias := &schemas.Model{ID: "openai/my-deployment", Alias: &alias}
	mc.ApplyModelTags(byAlias)
	assert.Equal(t, []string{"pinned"}, byAlias.Tags, "the alias is the fallback lookup")

	untagged := &schemas.Model{ID: "openai/o3", Tags: []string{"from-upstream"}}
	mc.ApplyModelTags(untagged)
	assert.Nil(t, untagged.Tags)

	var nilCatalog *ModelCatalog
	cleared := &schemas.Model{ID: "openai/gpt-5.1", Tags: []string{"from-upstream"}}
	nilCatalog.ApplyModelTags(cleared)
	assert.Nil(t, cleared.Tags, "without a catalog a model carries no tags")
	nilCatalog.ApplyModelTags(nil)
}

// sequencedModelTagsStore returns snapshots[i] on the i-th GetModelTags call. The first call
// signals entered and then waits for release, so a test can hold one reload inside its read.
type sequencedModelTagsStore struct {
	configstore.ConfigStore
	mu        sync.Mutex
	calls     int
	snapshots []map[string]map[string][]string
	entered   chan struct{}
	release   chan struct{}
}

func (s *sequencedModelTagsStore) GetModelTags(context.Context) (map[string]map[string][]string, error) {
	s.mu.Lock()
	call := s.calls
	s.calls++
	s.mu.Unlock()
	if call == 0 {
		close(s.entered)
		<-s.release
	}
	return s.snapshots[call], nil
}

// TestReloadModelTags_OlderSnapshotCannotPublishLast holds a first reload inside its store read
// (it has read the older snapshot) while a second reload runs. Reloads are serialized, so the
// second one reads only after the first has published, and the newer snapshot ends up live.
func TestReloadModelTags_OlderSnapshotCannotPublishLast(t *testing.T) {
	ctx := context.Background()
	store := &sequencedModelTagsStore{
		snapshots: []map[string]map[string][]string{
			{"openai": {"gpt-5.1": {"old"}}},
			{"openai": {"gpt-5.1": {"new"}}},
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	mc := &ModelCatalog{configStore: store}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); assert.NoError(t, mc.ReloadModelTags(ctx)) }()
	<-store.entered
	go func() { defer wg.Done(); assert.NoError(t, mc.ReloadModelTags(ctx)) }()
	// Give the second reload time to run ahead if it were not serialized.
	time.Sleep(50 * time.Millisecond)
	close(store.release)
	wg.Wait()

	assert.Equal(t, []string{"new"}, mc.GetModelTags(schemas.OpenAI, "gpt-5.1"), "the newer snapshot must be the one left live")
}

// flakyModelTagsStore fails GetModelTags for the first failures calls, then serves tags.
type flakyModelTagsStore struct {
	configstore.ConfigStore
	mu       sync.Mutex
	calls    int
	failures int
	tags     map[string]map[string][]string
}

func (f *flakyModelTagsStore) GetModelTags(context.Context) (map[string]map[string][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failures {
		return nil, errors.New("config store unavailable")
	}
	return f.tags, nil
}

// TestLoadModelTagsAtStartup_RetriesAfterFailure pins that a failed startup load of the tag
// overlay is retried in the background instead of leaving every listing without tags until the
// next tag write, and that the retry stops when the catalog shuts down.
func TestLoadModelTagsAtStartup_RetriesAfterFailure(t *testing.T) {
	store := &flakyModelTagsStore{failures: 2, tags: map[string]map[string][]string{"openai": {"gpt-5.1": {"prod"}}}}
	mc := &ModelCatalog{configStore: store, done: make(chan struct{})}
	mc.loadModelTagsAtStartup(context.Background(), []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond})
	require.Eventually(t, func() bool { return len(mc.GetModelTags(schemas.OpenAI, "gpt-5.1")) == 1 }, time.Second, 5*time.Millisecond,
		"the overlay must load once the config store recovers")
	mc.wg.Wait()

	down := &flakyModelTagsStore{failures: 1000}
	stopped := &ModelCatalog{configStore: down, done: make(chan struct{})}
	stopped.loadModelTagsAtStartup(context.Background(), []time.Duration{time.Hour})
	close(stopped.done)
	stopped.wg.Wait()
	assert.Nil(t, stopped.modelTags.Load(), "a shut-down catalog stops retrying")
}

// TestRetryModelTags_StaleOverlayIsReloaded pins that a failed reload after an overlay was already
// published (a tag write whose reload and retries all failed) marks the overlay stale, and that the
// periodic retry keeps reloading until the store answers, then stops.
func TestRetryModelTags_StaleOverlayIsReloaded(t *testing.T) {
	store := &fakeModelTagsStore{tags: map[string]map[string][]string{"openai": {"gpt-5.1": {"old"}}}}
	mc := NewTestCatalogWithConfigStore(store)
	require.NoError(t, mc.ReloadModelTags(context.Background()))
	assert.False(t, mc.ModelTagsStale())

	// A tag write commits "new", but the reload after it fails: the overlay keeps "old" and is stale.
	store.tags = map[string]map[string][]string{"openai": {"gpt-5.1": {"new"}}}
	store.err = errors.New("config store unavailable")
	require.Error(t, mc.ReloadModelTags(context.Background()))
	assert.True(t, mc.ModelTagsStale(), "a failed reload must mark the published overlay stale")
	mc.retryModelTagsIfNeeded(context.Background())
	assert.Equal(t, []string{"old"}, mc.GetModelTags(schemas.OpenAI, "gpt-5.1"))
	assert.True(t, mc.ModelTagsStale(), "the overlay stays stale while the store is down")

	store.err = nil
	mc.retryModelTagsIfNeeded(context.Background())
	assert.Equal(t, []string{"new"}, mc.GetModelTags(schemas.OpenAI, "gpt-5.1"), "the periodic retry must publish the committed tags")
	assert.False(t, mc.ModelTagsStale())
}
