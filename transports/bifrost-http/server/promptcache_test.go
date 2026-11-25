package server

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestRefreshPromptsFollowsEachWrite checks that a prompt write refreshes only the prompt it names:
// a refresh brings the prompt and its versions into memory, a version write re-read through its
// prompt replaces the versions, a removal drops the prompt with its versions, and no IDs is a no-op.
func TestRefreshPromptsFollowsEachWrite(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())
	ctx := context.Background()
	config := libtest.Config(t, nil, nil)
	server := &BifrostHTTPServer{Config: config, Ctx: schemas.NewBifrostContext(ctx, schemas.NoDeadline)}
	prompts, versions := config.LiveStore.Collections.Prompts, config.LiveStore.Collections.PromptVersions

	require.NoError(t, config.ConfigStore.CreatePrompt(ctx, &configstoreTables.TablePrompt{ID: "p1", Name: "Support"}))
	require.NoError(t, config.ConfigStore.CreatePrompt(ctx, &configstoreTables.TablePrompt{ID: "p2", Name: "Other"}))
	require.NoError(t, server.RefreshPrompts(ctx, nil, false))
	_, held := prompts.Get("p1")
	require.False(t, held, "no IDs refreshes nothing")

	require.NoError(t, server.RefreshPrompts(ctx, []string{"p1"}, false))
	_, held = prompts.Get("p1")
	require.True(t, held)
	_, held = prompts.Get("p2")
	require.False(t, held, "only the named prompt is refreshed")

	version := &configstoreTables.TablePromptVersion{PromptID: "p1", Messages: []configstoreTables.TablePromptVersionMessage{{Message: []byte(`{"role":"system","content":"hi"}`)}}}
	require.NoError(t, config.ConfigStore.CreatePromptVersion(ctx, version))
	require.NoError(t, server.RefreshPrompts(ctx, []string{"p1"}, false))
	latest, held := versions.GetBy(registry.PromptVersionLatest, "p1")
	require.True(t, held, "a version write is read through its prompt")
	require.Len(t, latest.Messages, 1)

	require.NoError(t, config.ConfigStore.DeletePrompt(ctx, "p1"))
	require.NoError(t, server.RefreshPrompts(ctx, []string{"p1"}, true))
	_, held = prompts.Get("p1")
	require.False(t, held, "a removed prompt is dropped")
	_, held = versions.GetBy(registry.PromptVersionLatest, "p1")
	require.False(t, held, "its versions go with it")
}

// TestRefreshPromptsServesWhatIsStoredWhateverTheCallerSees checks that a refresh run on an
// editor's request serves the saved prompt, not only what the editor's data access lets them see:
// the refresh is the gateway's, so a scoped editor's save must not hide the prompt from it.
func TestRefreshPromptsServesWhatIsStoredWhateverTheCallerSees(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())
	ctx := context.Background()
	config := libtest.Config(t, nil, nil)
	server := &BifrostHTTPServer{Config: config, Ctx: schemas.NewBifrostContext(ctx, schemas.NoDeadline)}

	require.NoError(t, config.ConfigStore.CreatePrompt(ctx, &configstoreTables.TablePrompt{ID: "p1", Name: "Support"}))
	version := &configstoreTables.TablePromptVersion{PromptID: "p1", Messages: []configstoreTables.TablePromptVersionMessage{{Message: []byte(`{"role":"system","content":"hi"}`)}}}
	require.NoError(t, config.ConfigStore.CreatePromptVersion(ctx, version))
	callerCtx := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("1 = 0") })
	visible, err := config.ConfigStore.GetPrompts(callerCtx, nil)
	require.NoError(t, err)
	require.Empty(t, visible, "the caller sees no prompt")

	require.NoError(t, server.RefreshPrompts(callerCtx, []string{"p1"}, false))
	_, ok := config.LiveStore.Collections.Prompts.Get("p1")
	require.True(t, ok, "the refresh serves the prompt the caller cannot see")
	latest, ok := config.LiveStore.Collections.PromptVersions.GetBy(registry.PromptVersionLatest, "p1")
	require.True(t, ok)
	require.Len(t, latest.Messages, 1)
}
