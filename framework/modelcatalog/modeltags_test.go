package modelcatalog

import (
	"context"
	"errors"
	"testing"

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
