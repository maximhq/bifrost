package governance

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newProviderJobTestPlugin is newAccessTestPlugin over the MCP-stamping VK (id vk-mcp-stamp,
// value mcpTestVKValue, every openai model), backed by a real SQLite config store that holds the
// provider job rows the ownership checks read.
func newProviderJobTestPlugin(t *testing.T) (*GovernancePlugin, configstore.ConfigStore) {
	t.Helper()
	configStore := newProviderObjectConfigStore(t)
	plugin := newAccessTestPlugin(t, buildVKForMCPStamping(nil), nil)
	plugin.configStore = configStore
	return plugin, configStore
}

// newProviderObjectConfigStore is a real SQLite config store, migrated, that holds the provider
// job and provider object rows the ownership checks read.
func newProviderObjectConfigStore(t *testing.T) configstore.ConfigStore {
	t.Helper()
	ctx := context.Background()
	configStore, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/providerobjects.db"},
	}, NewMockLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, configStore.Close(ctx)) })
	return configStore
}

func seedProviderJob(t *testing.T, store configstore.ConfigStore, provider, jobID string, ownerVK *string) {
	t.Helper()
	require.NoError(t, store.UpsertProviderJob(context.Background(), &configstoreTables.TableProviderJob{
		ID:               configstoreTables.ProviderJobID(configstoreTables.ProviderJobKindBatch, provider, jobID),
		Kind:             configstoreTables.ProviderJobKindBatch,
		Provider:         provider,
		JobID:            jobID,
		AccountingStatus: configstoreTables.ProviderJobAccountingStatusPending,
		VirtualKeyID:     ownerVK,
	}))
}

func batchRequest(requestType schemas.RequestType, batchID string) *schemas.BifrostRequest {
	req := &schemas.BifrostRequest{RequestType: requestType}
	switch requestType {
	case schemas.BatchRetrieveRequest:
		req.BatchRetrieveRequest = &schemas.BifrostBatchRetrieveRequest{Provider: schemas.OpenAI, BatchID: batchID}
	case schemas.BatchCancelRequest:
		req.BatchCancelRequest = &schemas.BifrostBatchCancelRequest{Provider: schemas.OpenAI, BatchID: batchID}
	case schemas.BatchResultsRequest:
		req.BatchResultsRequest = &schemas.BifrostBatchResultsRequest{Provider: schemas.OpenAI, BatchID: batchID}
	case schemas.BatchDeleteRequest:
		req.BatchDeleteRequest = &schemas.BifrostBatchDeleteRequest{Provider: schemas.OpenAI, BatchID: batchID}
	}
	return req
}

// A batch addressed by id is reachable through the virtual key that created it. A row naming another
// key is refused as not found; a row naming no key, no row at all, and a request that presented no
// key are all unrestricted, as they were.
func TestPreLLMHookBindsBatchesToTheCreatingVirtualKey(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	own := "vk-mcp-stamp"
	other := "vk-someone-else"
	seedProviderJob(t, store, "openai", "batch-own", &own)
	seedProviderJob(t, store, "openai", "batch-other", &other)
	seedProviderJob(t, store, "openai", "batch-unowned", nil)

	for _, requestType := range []schemas.RequestType{schemas.BatchRetrieveRequest, schemas.BatchCancelRequest, schemas.BatchResultsRequest, schemas.BatchDeleteRequest} {
		t.Run(string(requestType), func(t *testing.T) {
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), batchRequest(requestType, "batch-own"))
			require.NoError(t, err)
			assert.Nil(t, shortCircuit, "the creating key reaches its own batch")

			_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), batchRequest(requestType, "batch-other"))
			require.NoError(t, err)
			require.NotNil(t, shortCircuit, "another key's batch is refused")
			require.NotNil(t, shortCircuit.Error.StatusCode)
			assert.Equal(t, 404, *shortCircuit.Error.StatusCode, "refused as not found, not as forbidden")
			assert.Contains(t, shortCircuit.Error.Error.Message, "batch-other")

			_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), batchRequest(requestType, "batch-unowned"))
			require.NoError(t, err)
			assert.Nil(t, shortCircuit, "a row with no recorded key binds to nobody")

			_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), batchRequest(requestType, "batch-unknown"))
			require.NoError(t, err)
			assert.Nil(t, shortCircuit, "a batch with no row cannot be bound and stays reachable")

			_, shortCircuit, err = plugin.PreLLMHook(emptyCtx(), batchRequest(requestType, "batch-other"))
			require.NoError(t, err)
			assert.Nil(t, shortCircuit, "a request that presented no key is unrestricted")
		})
	}
}

// A batch list is narrowed to the batches the virtual key may see: another key's batches are
// dropped, its own and unbound ones stay, and the provider's pagination cursors are untouched.
func TestPostLLMHookFiltersBatchListToTheVirtualKey(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	own := "vk-mcp-stamp"
	other := "vk-someone-else"
	seedProviderJob(t, store, "openai", "batch-own", &own)
	seedProviderJob(t, store, "openai", "batch-other", &other)
	seedProviderJob(t, store, "openai", "batch-unowned", nil)

	newList := func() *schemas.BifrostResponse {
		return &schemas.BifrostResponse{
			BatchListResponse: &schemas.BifrostBatchListResponse{
				Object:      "list",
				Data:        []schemas.BifrostBatchRetrieveResponse{{ID: "batch-own"}, {ID: "batch-other"}, {ID: "batch-unowned"}, {ID: "batch-unknown"}},
				HasMore:     true,
				LastID:      schemas.Ptr("batch-unknown"),
				ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchListRequest, Provider: schemas.OpenAI},
			},
		}
	}
	ids := func(list *schemas.BifrostBatchListResponse) []string {
		out := make([]string, 0, len(list.Data))
		for _, item := range list.Data {
			out = append(out, item.ID)
		}
		return out
	}

	// The list request is evaluated first, which is what stamps the key on the context.
	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.BatchListRequest, BatchListRequest: &schemas.BifrostBatchListRequest{Provider: schemas.OpenAI}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	result, _, err := plugin.PostLLMHook(ctx, newList(), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"batch-own", "batch-unowned", "batch-unknown"}, ids(result.BatchListResponse))
	assert.True(t, result.BatchListResponse.HasMore, "pagination is the provider's and is left alone")
	assert.Equal(t, "batch-unknown", *result.BatchListResponse.LastID)

	// With no key presented the list is the provider's answer, unchanged.
	anonymous := emptyCtx()
	_, shortCircuit, err = plugin.PreLLMHook(anonymous, &schemas.BifrostRequest{RequestType: schemas.BatchListRequest, BatchListRequest: &schemas.BifrostBatchListRequest{Provider: schemas.OpenAI}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	result, _, err = plugin.PostLLMHook(anonymous, newList(), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"batch-own", "batch-other", "batch-unowned", "batch-unknown"}, ids(result.BatchListResponse))
}

// failingJobLookupStore is a config store whose provider-job batch lookup fails.
type failingJobLookupStore struct {
	configstore.ConfigStore
}

func (failingJobLookupStore) GetProviderJobsByIDs(context.Context, []string) ([]*configstoreTables.TableProviderJob, error) {
	return nil, errors.New("provider job lookup failed")
}

// When the ownership lookup for a batch list fails, the list is not sent as a successful (and
// misleading) page: the request fails instead, so a client never sees an empty page that still
// carries the provider's pagination.
func TestPostLLMHookFailsBatchListWhenOwnershipLookupFails(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	plugin.configStore = failingJobLookupStore{store}

	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.BatchListRequest, BatchListRequest: &schemas.BifrostBatchListRequest{Provider: schemas.OpenAI}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)

	list := &schemas.BifrostResponse{BatchListResponse: &schemas.BifrostBatchListResponse{
		Object:      "list",
		Data:        []schemas.BifrostBatchRetrieveResponse{{ID: "batch-a"}},
		HasMore:     true,
		LastID:      schemas.Ptr("batch-a"),
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchListRequest, Provider: schemas.OpenAI},
	}}
	result, bifrostErr, err := plugin.PostLLMHook(ctx, list, nil)

	require.NoError(t, err)
	assert.Nil(t, result, "a list that could not be checked must not be returned")
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 500, *bifrostErr.StatusCode)
	require.NotNil(t, bifrostErr.Error)
	assert.NotContains(t, bifrostErr.Error.Message, "provider job lookup failed", "store errors stay in the log, not in the response")
}

// Without a config store no batch owner is recorded anywhere, so ownership cannot be verified.
// A virtual-key batch request that would need that check is refused up front, before the provider
// is called; a request that presented no key stays unrestricted, as everywhere else. A list
// addresses no batch and is narrowed only to what is recorded, so with nothing recorded it goes
// through as it always did.
func TestPreLLMHookRefusesVirtualKeyBatchRequestsWithoutConfigStore(t *testing.T) {
	plugin := newAccessTestPlugin(t, buildVKForMCPStamping(nil), nil)
	plugin.configStore = nil

	list := &schemas.BifrostRequest{RequestType: schemas.BatchListRequest, BatchListRequest: &schemas.BifrostBatchListRequest{Provider: schemas.OpenAI}}
	_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), list)
	require.NoError(t, err)
	assert.Nil(t, shortCircuit, "a batch list has nothing to check against and goes through")

	requests := map[string]*schemas.BifrostRequest{
		"retrieve": batchRequest(schemas.BatchRetrieveRequest, "batch-any"),
		"cancel":   batchRequest(schemas.BatchCancelRequest, "batch-any"),
		"results":  batchRequest(schemas.BatchResultsRequest, "batch-any"),
		"delete":   batchRequest(schemas.BatchDeleteRequest, "batch-any"),
	}
	for name, req := range requests {
		t.Run(name, func(t *testing.T) {
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), req)
			require.NoError(t, err)
			require.NotNil(t, shortCircuit, "a virtual-key batch request must not proceed when ownership cannot be verified")
			require.NotNil(t, shortCircuit.Error.StatusCode)
			assert.Equal(t, 403, *shortCircuit.Error.StatusCode)

			_, shortCircuit, err = plugin.PreLLMHook(emptyCtx(), req)
			require.NoError(t, err)
			assert.Nil(t, shortCircuit, "a request that presented no key is unrestricted")
		})
	}
}

// With raw responses enabled, a provider can attach the whole unfiltered page to every item (OpenAI
// does). When the ownership filter removes a batch, no kept item and not the list itself may still
// carry that raw page; a page with nothing removed keeps its raw responses.
func TestPostLLMHookDropsRawResponsesWhenBatchListIsFiltered(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	own := "vk-mcp-stamp"
	other := "vk-someone-else"
	seedProviderJob(t, store, "openai", "batch-own", &own)
	seedProviderJob(t, store, "openai", "batch-other", &other)

	rawPage := map[string]any{"data": []any{map[string]any{"id": "batch-own"}, map[string]any{"id": "batch-other"}}}
	page := func(ids ...string) *schemas.BifrostResponse {
		items := make([]schemas.BifrostBatchRetrieveResponse, 0, len(ids))
		for _, id := range ids {
			item := schemas.BifrostBatchRetrieveResponse{ID: id}
			item.ExtraFields.RawResponse = rawPage
			items = append(items, item)
		}
		return &schemas.BifrostResponse{BatchListResponse: &schemas.BifrostBatchListResponse{
			Object:      "list",
			Data:        items,
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchListRequest, Provider: schemas.OpenAI, RawResponse: rawPage},
		}}
	}
	run := func(resp *schemas.BifrostResponse) *schemas.BifrostBatchListResponse {
		ctx := presentCtx(mcpTestVKValue)
		_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.BatchListRequest, BatchListRequest: &schemas.BifrostBatchListRequest{Provider: schemas.OpenAI}})
		require.NoError(t, err)
		require.Nil(t, shortCircuit)
		result, _, err := plugin.PostLLMHook(ctx, resp, nil)
		require.NoError(t, err)
		return result.BatchListResponse
	}

	filtered := run(page("batch-own", "batch-other"))
	require.Len(t, filtered.Data, 1)
	assert.Equal(t, "batch-own", filtered.Data[0].ID)
	assert.Nil(t, filtered.Data[0].ExtraFields.RawResponse, "a kept item must not carry the unfiltered page")
	assert.Nil(t, filtered.ExtraFields.RawResponse, "the list must not carry the unfiltered page")

	untouched := run(page("batch-own"))
	require.Len(t, untouched.Data, 1)
	assert.NotNil(t, untouched.Data[0].ExtraFields.RawResponse, "nothing was removed, so raw responses stay")
}

func seedProviderObject(t *testing.T, store configstore.ConfigStore, kind, provider, objectID, ownerVK string) {
	t.Helper()
	require.NoError(t, store.UpsertProviderObject(context.Background(), &configstoreTables.TableProviderObject{
		ID:           configstoreTables.ProviderObjectID(kind, provider, objectID),
		Kind:         kind,
		Provider:     provider,
		ObjectID:     objectID,
		VirtualKeyID: ownerVK,
	}))
}

// providerObjectRequests builds, per request type, a request addressing objectID on OpenAI.
func providerObjectRequests(objectID string) map[schemas.RequestType]*schemas.BifrostRequest {
	videoRef := &schemas.BifrostVideoReferenceRequest{Provider: schemas.OpenAI, ID: objectID}
	return map[schemas.RequestType]*schemas.BifrostRequest{
		schemas.FileRetrieveRequest:          {RequestType: schemas.FileRetrieveRequest, FileRetrieveRequest: &schemas.BifrostFileRetrieveRequest{Provider: schemas.OpenAI, FileID: objectID}},
		schemas.FileContentRequest:           {RequestType: schemas.FileContentRequest, FileContentRequest: &schemas.BifrostFileContentRequest{Provider: schemas.OpenAI, FileID: objectID}},
		schemas.FileDeleteRequest:            {RequestType: schemas.FileDeleteRequest, FileDeleteRequest: &schemas.BifrostFileDeleteRequest{Provider: schemas.OpenAI, FileID: objectID}},
		schemas.BatchCreateRequest:           {RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{Provider: schemas.OpenAI, InputFileID: objectID}},
		schemas.VideoRetrieveRequest:         {RequestType: schemas.VideoRetrieveRequest, VideoRetrieveRequest: videoRef},
		schemas.VideoDownloadRequest:         {RequestType: schemas.VideoDownloadRequest, VideoDownloadRequest: &schemas.BifrostVideoDownloadRequest{Provider: schemas.OpenAI, ID: objectID}},
		schemas.VideoDeleteRequest:           {RequestType: schemas.VideoDeleteRequest, VideoDeleteRequest: videoRef},
		schemas.VideoRemixRequest:            {RequestType: schemas.VideoRemixRequest, VideoRemixRequest: &schemas.BifrostVideoRemixRequest{Provider: schemas.OpenAI, ID: objectID}},
		schemas.ContainerRetrieveRequest:     {RequestType: schemas.ContainerRetrieveRequest, ContainerRetrieveRequest: &schemas.BifrostContainerRetrieveRequest{Provider: schemas.OpenAI, ContainerID: objectID}},
		schemas.ContainerDeleteRequest:       {RequestType: schemas.ContainerDeleteRequest, ContainerDeleteRequest: &schemas.BifrostContainerDeleteRequest{Provider: schemas.OpenAI, ContainerID: objectID}},
		schemas.ContainerFileCreateRequest:   {RequestType: schemas.ContainerFileCreateRequest, ContainerFileCreateRequest: &schemas.BifrostContainerFileCreateRequest{Provider: schemas.OpenAI, ContainerID: objectID}},
		schemas.ContainerFileListRequest:     {RequestType: schemas.ContainerFileListRequest, ContainerFileListRequest: &schemas.BifrostContainerFileListRequest{Provider: schemas.OpenAI, ContainerID: objectID}},
		schemas.ContainerFileRetrieveRequest: {RequestType: schemas.ContainerFileRetrieveRequest, ContainerFileRetrieveRequest: &schemas.BifrostContainerFileRetrieveRequest{Provider: schemas.OpenAI, ContainerID: objectID, FileID: "cfile-1"}},
		schemas.ContainerFileContentRequest:  {RequestType: schemas.ContainerFileContentRequest, ContainerFileContentRequest: &schemas.BifrostContainerFileContentRequest{Provider: schemas.OpenAI, ContainerID: objectID, FileID: "cfile-1"}},
		schemas.ContainerFileDeleteRequest:   {RequestType: schemas.ContainerFileDeleteRequest, ContainerFileDeleteRequest: &schemas.BifrostContainerFileDeleteRequest{Provider: schemas.OpenAI, ContainerID: objectID, FileID: "cfile-1"}},
		schemas.CachedContentRetrieveRequest: {RequestType: schemas.CachedContentRetrieveRequest, CachedContentRetrieveRequest: &schemas.BifrostCachedContentRetrieveRequest{Provider: schemas.OpenAI, Name: objectID}},
		schemas.CachedContentUpdateRequest:   {RequestType: schemas.CachedContentUpdateRequest, CachedContentUpdateRequest: &schemas.BifrostCachedContentUpdateRequest{Provider: schemas.OpenAI, Name: objectID}},
		schemas.CachedContentDeleteRequest:   {RequestType: schemas.CachedContentDeleteRequest, CachedContentDeleteRequest: &schemas.BifrostCachedContentDeleteRequest{Provider: schemas.OpenAI, Name: objectID}},
	}
}

// providerObjectKindFor is the object family each request type addresses.
func providerObjectKindFor(requestType schemas.RequestType) string {
	switch requestType {
	case schemas.FileRetrieveRequest, schemas.FileContentRequest, schemas.FileDeleteRequest, schemas.BatchCreateRequest:
		return configstoreTables.ProviderObjectKindFile
	case schemas.VideoRetrieveRequest, schemas.VideoDownloadRequest, schemas.VideoDeleteRequest, schemas.VideoRemixRequest:
		return configstoreTables.ProviderObjectKindVideo
	case schemas.CachedContentRetrieveRequest, schemas.CachedContentUpdateRequest, schemas.CachedContentDeleteRequest:
		return configstoreTables.ProviderObjectKindCachedContent
	default:
		return configstoreTables.ProviderObjectKindContainer
	}
}

// Every provider object addressed by id is reachable through the virtual key that created it. A
// row naming another key is refused as not found; no row, and a request that presented no key,
// stay unrestricted. Container files are bound through their container.
func TestPreLLMHookBindsProviderObjectsToTheCreatingVirtualKey(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	own := "vk-mcp-stamp"
	other := "vk-someone-else"
	for _, kind := range []string{configstoreTables.ProviderObjectKindFile, configstoreTables.ProviderObjectKindVideo, configstoreTables.ProviderObjectKindContainer, configstoreTables.ProviderObjectKindCachedContent} {
		seedProviderObject(t, store, kind, "openai", kind+"-own", own)
		seedProviderObject(t, store, kind, "openai", kind+"-other", other)
	}

	for requestType := range providerObjectRequests("x") {
		t.Run(string(requestType), func(t *testing.T) {
			kind := providerObjectKindFor(requestType)

			_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests(kind + "-own")[requestType])
			require.NoError(t, err)
			assert.Nil(t, shortCircuit, "the creating key reaches its own object")

			_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests(kind + "-other")[requestType])
			require.NoError(t, err)
			require.NotNil(t, shortCircuit, "another key's object is refused")
			require.NotNil(t, shortCircuit.Error.StatusCode)
			assert.Equal(t, 404, *shortCircuit.Error.StatusCode, "refused as not found, not as forbidden")
			assert.Contains(t, shortCircuit.Error.Error.Message, kind+"-other")

			_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests(kind + "-unknown")[requestType])
			require.NoError(t, err)
			assert.Nil(t, shortCircuit, "an object with no row cannot be bound and stays reachable")

			_, shortCircuit, err = plugin.PreLLMHook(emptyCtx(), providerObjectRequests(kind + "-other")[requestType])
			require.NoError(t, err)
			assert.Nil(t, shortCircuit, "a request that presented no key is unrestricted")
		})
	}
}

// With client.enforce_vk_object_ownership turned off, every virtual key reaches every provider
// object and lists come back whole, but creators are still recorded, so turning the setting back
// on also binds what was created while it was off. Unset (nil) means on. The plugin reads the setting
// through a pointer into the live client config, so a flip takes effect on the next request.
func TestProviderObjectOwnershipFollowsTheClientSetting(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	require.True(t, plugin.objectOwnershipEnforced(), "an unset setting enforces ownership")
	enforced := false
	plugin.objectOwnershipEnforcedFn = func() bool { return enforced }
	other := "vk-someone-else"
	for _, kind := range []string{configstoreTables.ProviderObjectKindFile, configstoreTables.ProviderObjectKindVideo, configstoreTables.ProviderObjectKindContainer, configstoreTables.ProviderObjectKindCachedContent} {
		seedProviderObject(t, store, kind, "openai", kind+"-other", other)
	}

	for requestType := range providerObjectRequests("x") {
		kind := providerObjectKindFor(requestType)
		_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests(kind + "-other")[requestType])
		require.NoError(t, err)
		assert.Nil(t, shortCircuit, "%s: another key's object is reachable while the setting is off", requestType)
	}

	listCtx := func() *schemas.BifrostContext {
		ctx := presentCtx(mcpTestVKValue)
		_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.FileListRequest, FileListRequest: &schemas.BifrostFileListRequest{Provider: schemas.OpenAI}})
		require.NoError(t, err)
		require.Nil(t, shortCircuit)
		return ctx
	}
	list := &schemas.BifrostResponse{FileListResponse: &schemas.BifrostFileListResponse{Data: []schemas.FileObject{{ID: "file-other"}, {ID: "file-unbound"}}}}
	require.NoError(t, plugin.filterProviderObjectList(listCtx(), "openai", list))
	assert.Len(t, list.FileListResponse.Data, 2, "the list is not narrowed while the setting is off")

	// Creators are recorded while the setting is off: a plain create, and the automatic
	// container whose marker the pre-hook leaves for the post-hook.
	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.FileUploadRequest, FileUploadRequest: &schemas.BifrostFileUploadRequest{Provider: schemas.OpenAI}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{FileUploadResponse: &schemas.BifrostFileUploadResponse{ID: "file-made-while-off",
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.FileUploadRequest, Provider: schemas.OpenAI}}}, nil)
	require.NoError(t, err)
	ctx = presentCtx(mcpTestVKValue)
	_, shortCircuit, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "m", Params: &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeCodeInterpreter, ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Container: map[string]any{"type": "auto"}}}},
	}}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	callType := schemas.ResponsesMessageTypeCodeInterpreterCall
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{
		Output:      []schemas.ResponsesMessage{{Type: &callType, ResponsesToolMessage: &schemas.ResponsesToolMessage{ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{ContainerID: "cntr_auto_while_off"}}}},
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ResponsesRequest, Provider: schemas.OpenAI}}}, nil)
	require.NoError(t, err)
	rows, err := store.GetProviderObjectsByIDs(context.Background(), []string{
		configstoreTables.ProviderObjectID(configstoreTables.ProviderObjectKindFile, "openai", "file-made-while-off"),
		configstoreTables.ProviderObjectID(configstoreTables.ProviderObjectKindContainer, "openai", "cntr_auto_while_off"),
	})
	require.NoError(t, err)
	require.Len(t, rows, 2, "creators are recorded while the setting is off")
	for _, row := range rows {
		assert.Equal(t, "vk-mcp-stamp", row.VirtualKeyID)
	}

	// Turning the setting on binds the objects, including the ones recorded while it was off.
	enforced = true
	_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests("file-other")[schemas.FileRetrieveRequest])
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "another key's object is refused once the setting is on")
	assert.Equal(t, 404, *shortCircuit.Error.StatusCode)
	require.NoError(t, plugin.filterProviderObjectList(listCtx(), "openai", list))
	require.Len(t, list.FileListResponse.Data, 1, "the list is narrowed once the setting is on")
	assert.Equal(t, "file-unbound", list.FileListResponse.Data[0].ID)
}

// A provider list page can hold more items than one SQL statement binds: the OpenAI files list
// returns up to 10000 per page, and each item is looked up under every provider name that shares
// the upstream. The filter must still narrow the page instead of failing the request with a 500.
func TestPostLLMHookFiltersAListLargerThanOneStatementBinds(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindFile, "openai", "file-other", "vk-someone-else")
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindFile, "openai", "file-own", "vk-mcp-stamp")
	data := make([]schemas.FileObject, 0, 40002)
	for i := 0; i < 40000; i++ {
		data = append(data, schemas.FileObject{ID: fmt.Sprintf("file-shared-%05d", i)})
	}
	data = append(data, schemas.FileObject{ID: "file-other"}, schemas.FileObject{ID: "file-own"})

	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.FileListRequest, FileListRequest: &schemas.BifrostFileListRequest{Provider: schemas.OpenAI}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	list := &schemas.BifrostResponse{FileListResponse: &schemas.BifrostFileListResponse{Data: data}}
	require.NoError(t, plugin.filterProviderObjectList(ctx, "openai", list), "a large page is narrowed, not failed")
	require.Len(t, list.FileListResponse.Data, 40001, "only the other key's file is removed")
	ids := make(map[string]struct{}, len(list.FileListResponse.Data))
	for _, item := range list.FileListResponse.Data {
		ids[item.ID] = struct{}{}
	}
	assert.NotContains(t, ids, "file-other")
	assert.Contains(t, ids, "file-own")
}

// A video is addressed with or without its ":provider" suffix; the row may carry either form.
func TestPreLLMHookBindsVideosUnderEitherIDForm(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	other := "vk-someone-else"
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindVideo, "openai", "video_suffixed:openai", other)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindVideo, "openai", "video_bare", other)

	for _, id := range []string{"video_suffixed", "video_suffixed:openai", "video_bare", "video_bare:openai"} {
		_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests(id)[schemas.VideoDownloadRequest])
		require.NoError(t, err)
		require.NotNil(t, shortCircuit, "video %s belongs to another key", id)
		assert.Equal(t, 404, *shortCircuit.Error.StatusCode)
	}
}

// A batch row written before ownership rows existed still binds its batch and, through its
// output and error file columns, the files the batch produced. Its input file is not claimed.
func TestPreLLMHookBindsBatchFilesThroughTheBatchRow(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	other := "vk-someone-else"
	own := "vk-mcp-stamp"
	output := "file-other-output"
	errFile := "file-other-error"
	require.NoError(t, store.UpsertProviderJob(context.Background(), &configstoreTables.TableProviderJob{
		ID:               configstoreTables.ProviderJobID(configstoreTables.ProviderJobKindBatch, "openai", "batch-other"),
		Kind:             configstoreTables.ProviderJobKindBatch,
		Provider:         "openai",
		JobID:            "batch-other",
		InputFileID:      "file-other-input",
		OutputFileID:     &output,
		ErrorFileID:      &errFile,
		AccountingStatus: configstoreTables.ProviderJobAccountingStatusPending,
		VirtualKeyID:     &other,
	}))
	ownOutput := "file-own-output"
	require.NoError(t, store.UpsertProviderJob(context.Background(), &configstoreTables.TableProviderJob{
		ID:               configstoreTables.ProviderJobID(configstoreTables.ProviderJobKindBatch, "openai", "batch-own"),
		Kind:             configstoreTables.ProviderJobKindBatch,
		Provider:         "openai",
		JobID:            "batch-own",
		OutputFileID:     &ownOutput,
		AccountingStatus: configstoreTables.ProviderJobAccountingStatusPending,
		VirtualKeyID:     &own,
	}))
	// The video job row the logging plugin writes binds the video the same way.
	require.NoError(t, store.UpsertProviderJob(context.Background(), &configstoreTables.TableProviderJob{
		ID:               configstoreTables.ProviderJobID(configstoreTables.ProviderJobKindVideo, "openai", "video_other:openai"),
		Kind:             configstoreTables.ProviderJobKindVideo,
		Provider:         "openai",
		JobID:            "video_other:openai",
		AccountingStatus: configstoreTables.ProviderJobAccountingStatusPending,
		VirtualKeyID:     &other,
	}))

	for _, requestType := range []schemas.RequestType{schemas.FileRetrieveRequest, schemas.FileContentRequest, schemas.FileDeleteRequest, schemas.BatchCreateRequest} {
		for _, fileID := range []string{"file-other-output", "file-other-error"} {
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests(fileID)[requestType])
			require.NoError(t, err)
			require.NotNil(t, shortCircuit, "%s on %s: another key's batch file is refused", requestType, fileID)
			assert.Equal(t, 404, *shortCircuit.Error.StatusCode)
		}
		// The input file was there before the batch: a file uploaded through the gateway is bound
		// by its own row, and one uploaded outside it stays shared. Running a batch on it must not
		// make it the batch key's.
		_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests("file-other-input")[requestType])
		require.NoError(t, err)
		assert.Nil(t, shortCircuit, "%s: an unowned input file is not claimed by the batch that ran on it", requestType)
		_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests("file-own-output")[requestType])
		require.NoError(t, err)
		assert.Nil(t, shortCircuit, "%s: the creating key reaches its own batch output", requestType)
	}
	_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests("video_other")[schemas.VideoDownloadRequest])
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "a video with only a job row is still bound")
	assert.Equal(t, 404, *shortCircuit.Error.StatusCode)
}

// A create response seen under a virtual key records that key as the object's creator; the
// object is then refused to another key and reachable to its own. A successful delete forgets it.
func TestPostLLMHookRecordsProviderObjectCreatorsAndForgetsDeleted(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)

	creates := map[string]*schemas.BifrostResponse{
		"file": {FileUploadResponse: &schemas.BifrostFileUploadResponse{ID: "file-new",
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.FileUploadRequest, Provider: schemas.OpenAI}}},
		"video": {VideoGenerationResponse: &schemas.BifrostVideoGenerationResponse{ID: "video_new:openai",
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.VideoGenerationRequest, Provider: schemas.OpenAI}}},
		"container": {ContainerCreateResponse: &schemas.BifrostContainerCreateResponse{ID: "container-new",
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ContainerCreateRequest, Provider: schemas.OpenAI}}},
		"cached_content": {CachedContentCreateResponse: &schemas.BifrostCachedContentCreateResponse{Name: "cachedContents/new",
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.CachedContentCreateRequest, Provider: schemas.OpenAI}}},
		"batch": {BatchCreateResponse: &schemas.BifrostBatchCreateResponse{ID: "batch-new",
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchCreateRequest, Provider: schemas.OpenAI}}},
	}
	objectIDs := map[string]string{"file": "file-new", "video": "video_new:openai", "container": "container-new", "cached_content": "cachedContents/new", "batch": "batch-new"}
	createRequest := func(kind string) *schemas.BifrostRequest {
		switch kind {
		case "file":
			return &schemas.BifrostRequest{RequestType: schemas.FileUploadRequest, FileUploadRequest: &schemas.BifrostFileUploadRequest{Provider: schemas.OpenAI}}
		case "video":
			return &schemas.BifrostRequest{RequestType: schemas.VideoGenerationRequest, VideoGenerationRequest: &schemas.BifrostVideoGenerationRequest{Provider: schemas.OpenAI, Model: "sora-2"}}
		case "container":
			return &schemas.BifrostRequest{RequestType: schemas.ContainerCreateRequest, ContainerCreateRequest: &schemas.BifrostContainerCreateRequest{Provider: schemas.OpenAI}}
		case "cached_content":
			return &schemas.BifrostRequest{RequestType: schemas.CachedContentCreateRequest, CachedContentCreateRequest: &schemas.BifrostCachedContentCreateRequest{Provider: schemas.OpenAI}}
		default:
			return &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{Provider: schemas.OpenAI}}
		}
	}

	for kind, resp := range creates {
		t.Run(kind, func(t *testing.T) {
			ctx := presentCtx(mcpTestVKValue)
			_, shortCircuit, err := plugin.PreLLMHook(ctx, createRequest(kind))
			require.NoError(t, err)
			require.Nil(t, shortCircuit)
			_, _, err = plugin.PostLLMHook(ctx, resp, nil)
			require.NoError(t, err)

			rows, err := store.GetProviderObjectsByIDs(context.Background(), []string{configstoreTables.ProviderObjectID(kind, "openai", objectIDs[kind])})
			require.NoError(t, err)
			require.Len(t, rows, 1, "the create was recorded")
			assert.Equal(t, "vk-mcp-stamp", rows[0].VirtualKeyID)
		})
	}

	// The recorded file is refused to another key and reachable to its creator.
	otherKey := buildVKForMCPStamping(nil)
	otherKey.ID = "vk-other"
	otherKey.Value = *schemas.NewSecretVar("sk-bf-other")
	otherPlugin := newAccessTestPlugin(t, otherKey, nil)
	otherPlugin.configStore = store
	_, shortCircuit, err := otherPlugin.PreLLMHook(presentCtx("sk-bf-other"), providerObjectRequests("file-new")[schemas.FileContentRequest])
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "the recorded file is refused to another key")
	assert.Equal(t, 404, *shortCircuit.Error.StatusCode)
	_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests("file-new")[schemas.FileContentRequest])
	require.NoError(t, err)
	assert.Nil(t, shortCircuit)

	// No key presented: nothing is recorded.
	anonymous := emptyCtx()
	_, shortCircuit, err = plugin.PreLLMHook(anonymous, createRequest("file"))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, _, err = plugin.PostLLMHook(anonymous, &schemas.BifrostResponse{FileUploadResponse: &schemas.BifrostFileUploadResponse{ID: "file-anonymous",
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.FileUploadRequest, Provider: schemas.OpenAI}}}, nil)
	require.NoError(t, err)
	rows, err := store.GetProviderObjectsByIDs(context.Background(), []string{configstoreTables.ProviderObjectID("file", "openai", "file-anonymous")})
	require.NoError(t, err)
	assert.Empty(t, rows, "an anonymous create binds to nobody")

	// A successful delete forgets the object.
	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err = plugin.PreLLMHook(ctx, providerObjectRequests("file-new")[schemas.FileDeleteRequest])
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{FileDeleteResponse: &schemas.BifrostFileDeleteResponse{ID: "file-new", Deleted: true,
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.FileDeleteRequest, Provider: schemas.OpenAI}}}, nil)
	require.NoError(t, err)
	rows, err = store.GetProviderObjectsByIDs(context.Background(), []string{configstoreTables.ProviderObjectID("file", "openai", "file-new")})
	require.NoError(t, err)
	assert.Empty(t, rows, "a deleted object is forgotten")
}

// Every list of provider objects is narrowed to what the virtual key may see, the way batch
// lists are: another key's objects are dropped, its own and unbound ones stay, and the provider's
// pagination is untouched. A file list also hides the files of another key's batches.
func TestPostLLMHookFiltersProviderObjectListsToTheVirtualKey(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	own := "vk-mcp-stamp"
	other := "vk-someone-else"
	for _, kind := range []string{configstoreTables.ProviderObjectKindFile, configstoreTables.ProviderObjectKindVideo, configstoreTables.ProviderObjectKindContainer, configstoreTables.ProviderObjectKindCachedContent} {
		seedProviderObject(t, store, kind, "openai", kind+"-own", own)
		seedProviderObject(t, store, kind, "openai", kind+"-other", other)
	}
	batchOutput := "file-batch-output"
	require.NoError(t, store.UpsertProviderJob(context.Background(), &configstoreTables.TableProviderJob{
		ID:               configstoreTables.ProviderJobID(configstoreTables.ProviderJobKindBatch, "openai", "batch-other"),
		Kind:             configstoreTables.ProviderJobKindBatch,
		Provider:         "openai",
		JobID:            "batch-other",
		OutputFileID:     &batchOutput,
		AccountingStatus: configstoreTables.ProviderJobAccountingStatusPending,
		VirtualKeyID:     &other,
	}))
	rawPage := map[string]any{"page": "unfiltered"}
	extra := func(requestType schemas.RequestType) schemas.BifrostResponseExtraFields {
		return schemas.BifrostResponseExtraFields{RequestType: requestType, Provider: schemas.OpenAI, RawResponse: rawPage}
	}

	type listCase struct {
		request  *schemas.BifrostRequest
		response func() *schemas.BifrostResponse
		ids      func(*schemas.BifrostResponse) []string
		raw      func(*schemas.BifrostResponse) any
	}
	cases := map[string]listCase{
		"files": {
			request: &schemas.BifrostRequest{RequestType: schemas.FileListRequest, FileListRequest: &schemas.BifrostFileListRequest{Provider: schemas.OpenAI}},
			response: func() *schemas.BifrostResponse {
				return &schemas.BifrostResponse{FileListResponse: &schemas.BifrostFileListResponse{Object: "list", HasMore: true, After: schemas.Ptr("cursor"),
					Data:        []schemas.FileObject{{ID: "file-own"}, {ID: "file-other"}, {ID: "file-batch-output"}, {ID: "file-unknown"}},
					ExtraFields: extra(schemas.FileListRequest)}}
			},
			ids: func(r *schemas.BifrostResponse) []string {
				out := []string{}
				for _, item := range r.FileListResponse.Data {
					out = append(out, item.ID)
				}
				assert.True(t, r.FileListResponse.HasMore)
				assert.Equal(t, "cursor", *r.FileListResponse.After)
				return out
			},
			raw: func(r *schemas.BifrostResponse) any { return r.FileListResponse.ExtraFields.RawResponse },
		},
		"videos": {
			request: &schemas.BifrostRequest{RequestType: schemas.VideoListRequest, VideoListRequest: &schemas.BifrostVideoListRequest{Provider: schemas.OpenAI}},
			response: func() *schemas.BifrostResponse {
				return &schemas.BifrostResponse{VideoListResponse: &schemas.BifrostVideoListResponse{Object: "list", LastID: schemas.Ptr("video-unknown:openai"),
					Data:        []schemas.VideoObject{{ID: "video-own:openai"}, {ID: "video-other:openai"}, {ID: "video-unknown:openai"}},
					ExtraFields: extra(schemas.VideoListRequest)}}
			},
			ids: func(r *schemas.BifrostResponse) []string {
				out := []string{}
				for _, item := range r.VideoListResponse.Data {
					out = append(out, item.ID)
				}
				assert.Equal(t, "video-unknown:openai", *r.VideoListResponse.LastID)
				return out
			},
			raw: func(r *schemas.BifrostResponse) any { return r.VideoListResponse.ExtraFields.RawResponse },
		},
		"containers": {
			request: &schemas.BifrostRequest{RequestType: schemas.ContainerListRequest, ContainerListRequest: &schemas.BifrostContainerListRequest{Provider: schemas.OpenAI}},
			response: func() *schemas.BifrostResponse {
				return &schemas.BifrostResponse{ContainerListResponse: &schemas.BifrostContainerListResponse{Object: "list",
					Data:        []schemas.ContainerObject{{ID: "container-own"}, {ID: "container-other"}, {ID: "container-unknown"}},
					ExtraFields: extra(schemas.ContainerListRequest)}}
			},
			ids: func(r *schemas.BifrostResponse) []string {
				out := []string{}
				for _, item := range r.ContainerListResponse.Data {
					out = append(out, item.ID)
				}
				return out
			},
			raw: func(r *schemas.BifrostResponse) any { return r.ContainerListResponse.ExtraFields.RawResponse },
		},
		"cached contents": {
			request: &schemas.BifrostRequest{RequestType: schemas.CachedContentListRequest, CachedContentListRequest: &schemas.BifrostCachedContentListRequest{Provider: schemas.OpenAI}},
			response: func() *schemas.BifrostResponse {
				return &schemas.BifrostResponse{CachedContentListResponse: &schemas.BifrostCachedContentListResponse{NextPageToken: "next",
					CachedContents: []schemas.CachedContentObject{{Name: "cached_content-own"}, {Name: "cached_content-other"}, {Name: "cached_content-unknown"}},
					ExtraFields:    extra(schemas.CachedContentListRequest)}}
			},
			ids: func(r *schemas.BifrostResponse) []string {
				out := []string{}
				for _, item := range r.CachedContentListResponse.CachedContents {
					out = append(out, item.Name)
				}
				assert.Equal(t, "next", r.CachedContentListResponse.NextPageToken)
				return out
			},
			raw: func(r *schemas.BifrostResponse) any { return r.CachedContentListResponse.ExtraFields.RawResponse },
		},
	}
	expected := map[string][]string{
		"files":           {"file-own", "file-unknown"},
		"videos":          {"video-own:openai", "video-unknown:openai"},
		"containers":      {"container-own", "container-unknown"},
		"cached contents": {"cached_content-own", "cached_content-unknown"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := presentCtx(mcpTestVKValue)
			_, shortCircuit, err := plugin.PreLLMHook(ctx, tc.request)
			require.NoError(t, err)
			require.Nil(t, shortCircuit)
			result, bifrostErr, err := plugin.PostLLMHook(ctx, tc.response(), nil)
			require.NoError(t, err)
			require.Nil(t, bifrostErr)
			assert.Equal(t, expected[name], tc.ids(result))
			assert.Nil(t, tc.raw(result), "the unfiltered raw page is dropped once anything is removed")

			anonymous := emptyCtx()
			_, shortCircuit, err = plugin.PreLLMHook(anonymous, tc.request)
			require.NoError(t, err)
			require.Nil(t, shortCircuit)
			result, _, err = plugin.PostLLMHook(anonymous, tc.response(), nil)
			require.NoError(t, err)
			assert.Equal(t, tc.ids(tc.response()), tc.ids(result), "with no key presented the list is the provider's answer")
			assert.NotNil(t, tc.raw(result))
		})
	}
}

// Without a config store no creator is recorded anywhere, so a virtual-key request that addresses
// a bound object is refused up front like a batch request is. Lists address nothing and are only
// ever narrowed to what is recorded, so they go through unfiltered, as they did before.
func TestPreLLMHookRefusesVirtualKeyProviderObjectRequestsWithoutConfigStore(t *testing.T) {
	plugin := newAccessTestPlugin(t, buildVKForMCPStamping(nil), nil)
	plugin.configStore = nil

	requests := providerObjectRequests("object-any")
	// A batch create only names its input file and stays open: nothing is recorded to check it
	// against, and the batch's results are refused on their own.
	_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), requests[schemas.BatchCreateRequest])
	require.NoError(t, err)
	assert.Nil(t, shortCircuit, "a batch create is not refused for naming an input file")
	delete(requests, schemas.BatchCreateRequest)
	for requestType, list := range map[schemas.RequestType]*schemas.BifrostRequest{
		schemas.FileListRequest:          {RequestType: schemas.FileListRequest, FileListRequest: &schemas.BifrostFileListRequest{Provider: schemas.OpenAI}},
		schemas.VideoListRequest:         {RequestType: schemas.VideoListRequest, VideoListRequest: &schemas.BifrostVideoListRequest{Provider: schemas.OpenAI}},
		schemas.ContainerListRequest:     {RequestType: schemas.ContainerListRequest, ContainerListRequest: &schemas.BifrostContainerListRequest{Provider: schemas.OpenAI}},
		schemas.CachedContentListRequest: {RequestType: schemas.CachedContentListRequest, CachedContentListRequest: &schemas.BifrostCachedContentListRequest{Provider: schemas.OpenAI}},
	} {
		_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), list)
		require.NoError(t, err)
		assert.Nil(t, shortCircuit, "a %s has nothing to check against and goes through", requestType)
	}
	for requestType, req := range requests {
		t.Run(string(requestType), func(t *testing.T) {
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), req)
			require.NoError(t, err)
			require.NotNil(t, shortCircuit, "a virtual-key request must not proceed when ownership cannot be verified")
			require.NotNil(t, shortCircuit.Error.StatusCode)
			assert.Equal(t, 403, *shortCircuit.Error.StatusCode)

			_, shortCircuit, err = plugin.PreLLMHook(emptyCtx(), req)
			require.NoError(t, err)
			assert.Nil(t, shortCircuit, "a request that presented no key is unrestricted")
		})
	}
}

// recordingObjectStore is the real store with the batch-row fallback observed and object writes
// failing on demand.
type recordingObjectStore struct {
	configstore.ConfigStore
	fileLookups [][]string
	failUpserts int
	upserts     int
}

func (s *recordingObjectStore) GetProviderJobsByFileIDs(ctx context.Context, provider string, fileIDs []string) ([]*configstoreTables.TableProviderJob, error) {
	s.fileLookups = append(s.fileLookups, append([]string(nil), fileIDs...))
	return s.ConfigStore.GetProviderJobsByFileIDs(ctx, provider, fileIDs)
}

func (s *recordingObjectStore) UpsertProviderObject(ctx context.Context, object *configstoreTables.TableProviderObject) error {
	s.upserts++
	if s.failUpserts > 0 {
		s.failUpserts--
		return errors.New("store hiccup")
	}
	return s.ConfigStore.UpsertProviderObject(ctx, object)
}

// A request can name another key's object without addressing it by id: a container create
// copies the files in file_ids into the new container, and a chat, responses, image or
// transcription request can run on a cached content. Each is checked like a direct reference.
func TestPreLLMHookBindsObjectsReferencedInsideRequests(t *testing.T) {
	store := newProviderObjectConfigStore(t)
	vk := buildVKForMCPStamping(nil)
	vk.ProviderConfigs = append(vk.ProviderConfigs, buildProviderConfig("gemini", []string{"*"}))
	plugin := newAccessTestPlugin(t, vk, nil)
	plugin.configStore = store
	own := "vk-mcp-stamp"
	other := "vk-someone-else"
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindFile, "openai", "file-own", own)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindFile, "openai", "file-other", other)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindCachedContent, "openai", "cachedContents/own", own)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindCachedContent, "openai", "cachedContents/other", other)

	container := func(fileIDs ...string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{RequestType: schemas.ContainerCreateRequest, ContainerCreateRequest: &schemas.BifrostContainerCreateRequest{Provider: schemas.OpenAI, Name: "c", FileIDs: fileIDs}}
	}
	cached := func(requestType schemas.RequestType, key, name string) *schemas.BifrostRequest {
		extra := map[string]interface{}{key: name}
		switch requestType {
		case schemas.ChatCompletionRequest:
			return &schemas.BifrostRequest{RequestType: requestType, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "m", Params: &schemas.ChatParameters{ExtraParams: extra}}}
		case schemas.ResponsesRequest:
			return &schemas.BifrostRequest{RequestType: requestType, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "m", Params: &schemas.ResponsesParameters{ExtraParams: extra}}}
		case schemas.ImageGenerationRequest:
			return &schemas.BifrostRequest{RequestType: requestType, ImageGenerationRequest: &schemas.BifrostImageGenerationRequest{Provider: schemas.OpenAI, Model: "m", Params: &schemas.ImageGenerationParameters{ExtraParams: extra}}}
		case schemas.ImageEditRequest:
			return &schemas.BifrostRequest{RequestType: requestType, ImageEditRequest: &schemas.BifrostImageEditRequest{Provider: schemas.OpenAI, Model: "m", Params: &schemas.ImageEditParameters{ExtraParams: extra}}}
		default:
			return &schemas.BifrostRequest{RequestType: requestType, TranscriptionRequest: &schemas.BifrostTranscriptionRequest{Provider: schemas.OpenAI, Model: "m", Params: &schemas.TranscriptionParameters{ExtraParams: extra}}}
		}
	}
	refused := func(t *testing.T, req *schemas.BifrostRequest, named string) {
		t.Helper()
		_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), req)
		require.NoError(t, err)
		require.NotNil(t, shortCircuit, "another key's object is refused")
		assert.Equal(t, 404, *shortCircuit.Error.StatusCode)
		assert.Contains(t, shortCircuit.Error.Error.Message, named)
	}
	allowed := func(t *testing.T, ctx *schemas.BifrostContext, req *schemas.BifrostRequest, why string) {
		t.Helper()
		_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
		require.NoError(t, err)
		assert.Nil(t, shortCircuit, why)
	}

	t.Run("container create file_ids", func(t *testing.T) {
		refused(t, container("file-own", "file-other"), "file-other")
		allowed(t, presentCtx(mcpTestVKValue), container("file-own", "file-unknown"), "own and unrecorded files may be copied in")
		allowed(t, presentCtx(mcpTestVKValue), container(), "a container with no files names nothing")
		allowed(t, emptyCtx(), container("file-other"), "a request that presented no key is unrestricted")
	})
	for _, requestType := range []schemas.RequestType{schemas.ChatCompletionRequest, schemas.ResponsesRequest, schemas.ImageGenerationRequest, schemas.ImageEditRequest, schemas.TranscriptionRequest} {
		for _, key := range []string{"cached_content", "cachedContent"} {
			t.Run(string(requestType)+" "+key, func(t *testing.T) {
				refused(t, cached(requestType, key, "cachedContents/other"), "cachedContents/other")
				allowed(t, presentCtx(mcpTestVKValue), cached(requestType, key, "cachedContents/own"), "the creating key runs on its own cache")
				allowed(t, presentCtx(mcpTestVKValue), cached(requestType, key, "cachedContents/unknown"), "an unrecorded cache stays usable")
				allowed(t, emptyCtx(), cached(requestType, key, "cachedContents/other"), "a request that presented no key is unrestricted")
			})
		}
	}

	// Gemini's image converters read cachedContent before cached_content, its chat converter the
	// other way round, so a request carrying both spellings is checked on both: naming one's own
	// cache under one spelling cannot carry another key's cache through under the other.
	t.Run("both cache spellings", func(t *testing.T) {
		both := func(requestType schemas.RequestType, snake, camel string) *schemas.BifrostRequest {
			req := cached(requestType, "cached_content", snake)
			extra := map[string]interface{}{"cached_content": snake, "cachedContent": camel}
			switch requestType {
			case schemas.ImageGenerationRequest:
				req.ImageGenerationRequest.Params.ExtraParams = extra
			case schemas.ImageEditRequest:
				req.ImageEditRequest.Params.ExtraParams = extra
			default:
				req.ChatRequest.Params.ExtraParams = extra
			}
			return req
		}
		for _, requestType := range []schemas.RequestType{schemas.ImageGenerationRequest, schemas.ImageEditRequest, schemas.ChatCompletionRequest} {
			refused(t, both(requestType, "cachedContents/own", "cachedContents/other"), "cachedContents/other")
			refused(t, both(requestType, "cachedContents/other", "cachedContents/own"), "cachedContents/other")
			allowed(t, presentCtx(mcpTestVKValue), both(requestType, "cachedContents/own", "cachedContents/own"), "the same own cache under both spellings is fine")
		}
	})

	str := func(v string) *string { return &v }
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindVideo, "openai", "video_other", other)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindContainerFile, "openai", "cfile-other", other)
	t.Run("container file create file_id", func(t *testing.T) {
		copyInto := func(fileID string) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.ContainerFileCreateRequest, ContainerFileCreateRequest: &schemas.BifrostContainerFileCreateRequest{Provider: schemas.OpenAI, ContainerID: "container-unbound", FileID: str(fileID)}}
		}
		refused(t, copyInto("file-other"), "file-other")
		allowed(t, presentCtx(mcpTestVKValue), copyInto("file-own"), "the creating key copies its own file")
		allowed(t, presentCtx(mcpTestVKValue), copyInto("file-unknown"), "an unrecorded file may be copied in")
	})
	t.Run("video edit source", func(t *testing.T) {
		edit := func(id string) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.VideoEditRequest, VideoEditRequest: &schemas.BifrostVideoEditRequest{Provider: schemas.OpenAI, Model: "sora-2", Input: &schemas.VideoEditInput{Video: schemas.VideoInput{ID: id}, Prompt: "p"}}}
		}
		refused(t, edit("video_other"), "video_other")
		allowed(t, presentCtx(mcpTestVKValue), edit("video_unknown"), "an unrecorded video may be edited")
		allowed(t, emptyCtx(), edit("video_other"), "a request that presented no key is unrestricted")
	})
	t.Run("chat message file_id", func(t *testing.T) {
		chat := func(block schemas.ChatContentBlock) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "m", Input: []schemas.ChatMessage{{
				Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeText, Text: str("hi")}, block}}}}}}
		}
		refused(t, chat(schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileID: str("file-other")}}), "file-other")
		refused(t, chat(schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{FileID: str("file-other")}}), "file-other")
		allowed(t, presentCtx(mcpTestVKValue), chat(schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileID: str("file-own")}}), "the creating key reads its own file")
		allowed(t, presentCtx(mcpTestVKValue), chat(schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileData: str("ZGF0YQ==")}}), "inline file data names no object")
	})
	t.Run("responses input file_id", func(t *testing.T) {
		responses := func(fileID string) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "m", Input: []schemas.ResponsesMessage{{
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesInputMessageContentBlockTypeFile, FileID: str(fileID)}}}}}}}
		}
		refused(t, responses("file-other"), "file-other")
		allowed(t, presentCtx(mcpTestVKValue), responses("file-own"), "the creating key reads its own file")
		allowed(t, emptyCtx(), responses("file-other"), "a request that presented no key is unrestricted")
	})
	// A Responses code_interpreter tool runs in a container: either an existing one named by id, or
	// an automatic one seeded with file_ids. Both reach OpenAI unchanged through the shared key.
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindContainer, "openai", "container-other", other)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindContainer, "openai", "container-own", own)
	t.Run("code interpreter tool container", func(t *testing.T) {
		withTool := func(container interface{}) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "m", Params: &schemas.ResponsesParameters{
				Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeCodeInterpreter, ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Container: container}}},
			}}}
		}
		refused(t, withTool("container-other"), "container-other")
		refused(t, withTool(map[string]interface{}{"type": "auto", "file_ids": []interface{}{"file-own", "file-other"}}), "file-other")
		allowed(t, presentCtx(mcpTestVKValue), withTool("container-own"), "the creating key runs code in its own container")
		allowed(t, presentCtx(mcpTestVKValue), withTool(map[string]interface{}{"type": "auto", "file_ids": []interface{}{"file-own"}}), "an automatic container seeded with own files")
		allowed(t, presentCtx(mcpTestVKValue), withTool(map[string]interface{}{"type": "auto"}), "an empty automatic container names nothing")
		allowed(t, emptyCtx(), withTool("container-other"), "a request that presented no key is unrestricted")
	})
	// Gemini names an uploaded file by its URI (https://generativelanguage.googleapis.com/v1beta/files/<id>),
	// which the GenAI route turns into a file or image URL. That URI is the file recorded as
	// files/<id>, so it is checked like a file_id, and so are the URIs a cached content is created on.
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindFile, "gemini", "files/g-other", other)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindFile, "gemini", "files/g-own", own)
	const geminiFiles = "https://generativelanguage.googleapis.com/v1beta/files/"
	t.Run("gemini file uris", func(t *testing.T) {
		chatFile := func(uri string) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.Gemini, Model: "m", Input: []schemas.ChatMessage{{
				Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileURL: str(uri)}}}}}}}}
		}
		chatImage := func(uri string) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.Gemini, Model: "m", Input: []schemas.ChatMessage{{
				Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: uri}}}}}}}}
		}
		responsesFile := func(uri string) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.Gemini, Model: "m", Input: []schemas.ResponsesMessage{{
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesInputMessageContentBlockTypeFile, ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{FileURL: str(uri)}}}}}}}}
		}
		responsesImage := func(uri string) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.Gemini, Model: "m", Input: []schemas.ResponsesMessage{{
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesInputMessageContentBlockTypeImage, ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: str(uri)}}}}}}}}
		}
		cacheOn := func(uri string) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.CachedContentCreateRequest, CachedContentCreateRequest: &schemas.BifrostCachedContentCreateRequest{Provider: schemas.Gemini, Model: "m",
				Contents: []any{map[string]any{"role": "user", "parts": []any{map[string]any{"fileData": map[string]any{"fileUri": uri, "mimeType": "application/pdf"}}}}}}}
		}
		for name, build := range map[string]func(string) *schemas.BifrostRequest{"chat file": chatFile, "chat image": chatImage, "responses file": responsesFile, "responses image": responsesImage, "cached content create": cacheOn} {
			t.Run(name, func(t *testing.T) {
				refused(t, build(geminiFiles+"g-other"), "files/g-other")
				allowed(t, presentCtx(mcpTestVKValue), build(geminiFiles+"g-own"), "the creating key uses its own file")
				allowed(t, presentCtx(mcpTestVKValue), build("https://example.com/files/g-other"), "a URL on another host is not a Gemini file")
				allowed(t, emptyCtx(), build(geminiFiles+"g-other"), "a request that presented no key is unrestricted")
			})
		}
	})
	// A passthrough request carries the provider's raw body and path, forwarded under the shared
	// key. The objects its body names and the object its path addresses are checked like the same
	// references on the native routes.
	t.Run("passthrough", func(t *testing.T) {
		pass := func(provider schemas.ModelProvider, method, path, body string) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.PassthroughRequest, PassthroughRequest: &schemas.BifrostPassthroughRequest{Provider: provider, Method: method, Path: path, Body: []byte(body)}}
		}
		inputFile := func(id string) string {
			return `{"model":"gpt-4o-mini","input":[{"role":"user","content":[{"type":"input_file","file_id":"` + id + `"}]}]}`
		}
		codeIn := func(container string) string {
			return `{"model":"gpt-4o-mini","input":"x","tools":[{"type":"code_interpreter","container":` + container + `}]}`
		}
		geminiBody := func(uri string) string {
			return `{"contents":[{"role":"user","parts":[{"fileData":{"fileUri":"` + uri + `","mimeType":"application/pdf"}}]}]}`
		}
		refused(t, pass(schemas.OpenAI, "POST", "/v1/responses", inputFile("file-other")), "file-other")
		refused(t, pass(schemas.OpenAI, "POST", "/v1/responses", codeIn(`"container-other"`)), "container-other")
		refused(t, pass(schemas.OpenAI, "POST", "/v1/responses", codeIn(`{"type":"auto","file_ids":["file-other"]}`)), "file-other")
		refused(t, pass(schemas.OpenAI, "GET", "/v1/files/file-other/content", ""), "file-other")
		refused(t, pass(schemas.OpenAI, "GET", "/v1/containers/container-other/files", ""), "container-other")
		refused(t, pass(schemas.Gemini, "POST", "/v1beta/models/gemini-2.5-flash:generateContent", geminiBody(geminiFiles+"g-other")), "files/g-other")
		refused(t, pass(schemas.Gemini, "GET", "/v1beta/files/g-other", ""), "files/g-other")
		// A batch or fine-tune created through passthrough runs on the input file its body names, as the
		// native batch create runs on InputFileID.
		refused(t, pass(schemas.OpenAI, "POST", "/v1/batches", `{"input_file_id":"file-other","endpoint":"/v1/chat/completions","completion_window":"24h"}`), "file-other")
		refused(t, pass(schemas.OpenAI, "POST", "/v1/fine_tuning/jobs", `{"model":"gpt-4o-mini","training_file":"file-other"}`), "file-other")
		refused(t, pass(schemas.OpenAI, "POST", "/v1/fine_tuning/jobs", `{"model":"gpt-4o-mini","training_file":"file-own","validation_file":"file-other"}`), "file-other")
		refused(t, pass(schemas.Gemini, "POST", "/v1beta/models/gemini-2.5-flash:batchGenerateContent", `{"batch":{"displayName":"b","inputConfig":{"fileName":"files/g-other"}}}`), "files/g-other")
		allowed(t, presentCtx(mcpTestVKValue), pass(schemas.OpenAI, "POST", "/v1/batches", `{"input_file_id":"file-own","endpoint":"/v1/chat/completions","completion_window":"24h"}`), "the creating key batches its own file")
		// The Go SDK hands a path over as written, and the HTTP client resolves dot segments and
		// repeated slashes before sending, so the path is checked as it will be sent.
		refused(t, pass(schemas.OpenAI, "GET", "/v1/files/x/../file-other/content", ""), "file-other")
		refused(t, pass(schemas.OpenAI, "GET", "/v1/files/./file-other/content", ""), "file-other")
		refused(t, pass(schemas.OpenAI, "GET", "/v1/files//file-other/content", ""), "file-other")
		// JSON lets a key or value spell a character as a \u escape, which the provider decodes to the
		// same name, so an escaped body is checked like the plain one.
		refused(t, pass(schemas.OpenAI, "POST", "/v1/responses", `{"input":[{"content":[{"type":"input_file","file\u005fid":"file-other"}]}]}`), "file-other")
		refused(t, pass(schemas.OpenAI, "POST", "/v1/responses", `{"input":"x","tools":[{"type":"code\u005finterpreter","container":"container-other"}]}`), "container-other")
		refused(t, pass(schemas.Gemini, "POST", "/v1beta/models/gemini-2.5-flash:generateContent", `{"contents":[{"parts":[{"fileData":{"file\u0055ri":"`+geminiFiles+`g-other"}}]}]}`), "files/g-other")
		allowed(t, presentCtx(mcpTestVKValue), pass(schemas.OpenAI, "POST", "/v1/responses", inputFile("file-own")), "the creating key reads its own file")
		allowed(t, presentCtx(mcpTestVKValue), pass(schemas.OpenAI, "GET", "/v1/files/file-own/content", ""), "the creating key downloads its own file")
		allowed(t, presentCtx(mcpTestVKValue), pass(schemas.OpenAI, "POST", "/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`), "a body naming no object")
		allowed(t, presentCtx(mcpTestVKValue), pass(schemas.OpenAI, "POST", "/v1/responses", `not json`), "a body that is not JSON names nothing")
		allowed(t, emptyCtx(), pass(schemas.OpenAI, "GET", "/v1/files/file-other/content", ""), "a request that presented no key is unrestricted")

		// A video is recorded with its :provider suffix; a passthrough path names it bare.
		seedProviderObject(t, store, configstoreTables.ProviderObjectKindVideo, "openai", "video_sfx:openai", other)
		refused(t, pass(schemas.OpenAI, "GET", "/v1/videos/video_sfx/content", ""), "video_sfx")

		// A passthrough list returns the provider's raw page, which cannot be narrowed to the key's
		// objects, so a virtual key is sent to the gateway's own list route instead.
		for _, path := range []string{"/v1/files", "/v1/batches", "/v1/containers", "/v1/containers/container-own/files", "/v1/videos"} {
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), pass(schemas.OpenAI, "GET", path, ""))
			require.NoError(t, err)
			require.NotNil(t, shortCircuit, "a passthrough list of %s is refused for a virtual key", path)
			assert.Equal(t, 403, *shortCircuit.Error.StatusCode)
		}
		_, shortCircuit, err := plugin.PreLLMHook(emptyCtx(), pass(schemas.OpenAI, "GET", "/v1/files", ""))
		require.NoError(t, err)
		assert.Nil(t, shortCircuit, "a request that presented no key may list")
	})

	// An inline batch carries its work in each item's body, where the provider reads the files and
	// caches named there under the shared key.
	t.Run("inline batch items", func(t *testing.T) {
		inline := func(provider schemas.ModelProvider, body map[string]any) *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{Provider: provider,
				Requests: []schemas.BatchRequestItem{{CustomID: "a", Body: map[string]any{"model": "m"}}, {CustomID: "b", Body: body}}}}
		}
		refused(t, inline(schemas.Gemini, map[string]any{"model": "m", "contents": []any{map[string]any{"parts": []any{map[string]any{"fileData": map[string]any{"fileUri": geminiFiles + "g-other"}}}}}}), "files/g-other")
		refused(t, inline(schemas.OpenAI, map[string]any{"model": "m", "cached_content": "cachedContents/other"}), "cachedContents/other")
		refused(t, inline(schemas.OpenAI, map[string]any{"model": "m", "input": []any{map[string]any{"content": []any{map[string]any{"type": "input_file", "file_id": "file-other"}}}}}), "file-other")
		allowed(t, presentCtx(mcpTestVKValue), inline(schemas.OpenAI, map[string]any{"model": "m", "input": []any{map[string]any{"content": []any{map[string]any{"type": "input_file", "file_id": "file-own"}}}}}), "own files in inline items")
	})
	t.Run("container file addressed by its own id", func(t *testing.T) {
		for name, req := range map[string]*schemas.BifrostRequest{
			"retrieve": {RequestType: schemas.ContainerFileRetrieveRequest, ContainerFileRetrieveRequest: &schemas.BifrostContainerFileRetrieveRequest{Provider: schemas.OpenAI, ContainerID: "container-unbound", FileID: "cfile-other"}},
			"content":  {RequestType: schemas.ContainerFileContentRequest, ContainerFileContentRequest: &schemas.BifrostContainerFileContentRequest{Provider: schemas.OpenAI, ContainerID: "container-unbound", FileID: "cfile-other"}},
			"delete":   {RequestType: schemas.ContainerFileDeleteRequest, ContainerFileDeleteRequest: &schemas.BifrostContainerFileDeleteRequest{Provider: schemas.OpenAI, ContainerID: "container-unbound", FileID: "cfile-other"}},
		} {
			t.Run(name, func(t *testing.T) { refused(t, req, "cfile-other") })
		}
		allowed(t, presentCtx(mcpTestVKValue), &schemas.BifrostRequest{RequestType: schemas.ContainerFileRetrieveRequest, ContainerFileRetrieveRequest: &schemas.BifrostContainerFileRetrieveRequest{Provider: schemas.OpenAI, ContainerID: "container-unbound", FileID: "cfile-unknown"}}, "an unrecorded container file stays reachable")
	})
}

// A file created inside a container belongs to the key that created it, even when the container
// itself is unbound: the create response records it, and the container's file list is narrowed
// to what the key may see.
func TestPostLLMHookRecordsAndFiltersContainerFiles(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindContainerFile, "openai", "cfile-other", "vk-someone-else")

	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.ContainerFileCreateRequest, ContainerFileCreateRequest: &schemas.BifrostContainerFileCreateRequest{Provider: schemas.OpenAI, ContainerID: "container-unbound", File: []byte("x")}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{ContainerFileCreateResponse: &schemas.BifrostContainerFileCreateResponse{ID: "cfile-new", ContainerID: "container-unbound",
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ContainerFileCreateRequest, Provider: schemas.OpenAI}}}, nil)
	require.NoError(t, err)
	rows, err := store.GetProviderObjectsByIDs(context.Background(), []string{configstoreTables.ProviderObjectID(configstoreTables.ProviderObjectKindContainerFile, "openai", "cfile-new")})
	require.NoError(t, err)
	require.Len(t, rows, 1, "the container file create was recorded")
	assert.Equal(t, "vk-mcp-stamp", rows[0].VirtualKeyID)

	ctx = presentCtx(mcpTestVKValue)
	_, shortCircuit, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.ContainerFileListRequest, ContainerFileListRequest: &schemas.BifrostContainerFileListRequest{Provider: schemas.OpenAI, ContainerID: "container-unbound"}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	list := &schemas.BifrostResponse{ContainerFileListResponse: &schemas.BifrostContainerFileListResponse{Data: []schemas.ContainerFileObject{{ID: "cfile-new"}, {ID: "cfile-other"}, {ID: "cfile-unknown"}}, HasMore: true,
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ContainerFileListRequest, Provider: schemas.OpenAI}}}
	_, _, err = plugin.PostLLMHook(ctx, list, nil)
	require.NoError(t, err)
	ids := make([]string, 0, 3)
	for _, item := range list.ContainerFileListResponse.Data {
		ids = append(ids, item.ID)
	}
	assert.Equal(t, []string{"cfile-new", "cfile-unknown"}, ids, "another key's container file is hidden; own and unbound ones stay")
	assert.True(t, list.ContainerFileListResponse.HasMore, "pagination is the provider's")
}

// A Responses code_interpreter tool asked for an automatic container makes the provider create
// one, named only in the response. That container is recorded for the key that asked for it,
// first writer wins, so another key cannot reach it; a container the request named by id is
// never claimed by using it.
func TestPostLLMHookRecordsAutomaticContainers(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	run := func(t *testing.T, container interface{}, outputContainer string) {
		t.Helper()
		ctx := presentCtx(mcpTestVKValue)
		req := &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "m", Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeCodeInterpreter, ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Container: container}}},
		}}}
		_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
		require.NoError(t, err)
		require.Nil(t, shortCircuit)
		callType := schemas.ResponsesMessageTypeCodeInterpreterCall
		resp := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output:      []schemas.ResponsesMessage{{Type: &callType, ResponsesToolMessage: &schemas.ResponsesToolMessage{ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{ContainerID: outputContainer}}}},
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ResponsesRequest, Provider: schemas.OpenAI}}}
		_, _, err = plugin.PostLLMHook(ctx, resp, nil)
		require.NoError(t, err)
	}
	owner := func(id string) string {
		rows, err := store.GetProviderObjectsByIDs(context.Background(), []string{configstoreTables.ProviderObjectID(configstoreTables.ProviderObjectKindContainer, "openai", id)})
		require.NoError(t, err)
		if len(rows) == 0 {
			return ""
		}
		return rows[0].VirtualKeyID
	}

	run(t, map[string]interface{}{"type": "auto", "file_ids": []interface{}{}}, "cntr_auto")
	assert.Equal(t, "vk-mcp-stamp", owner("cntr_auto"), "the automatic container belongs to the key that asked for it")

	run(t, "cntr_unbound_existing", "cntr_unbound_existing")
	assert.Empty(t, owner("cntr_unbound_existing"), "a container named by id is not claimed by using it")

	// One request can ask for an automatic container and name an existing one in another tool; the
	// response then lists both, and only the automatic one is the key's.
	ctx := presentCtx(mcpTestVKValue)
	req := &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "m", Params: &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeCodeInterpreter, ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Container: map[string]interface{}{"type": "auto"}}},
			{Type: schemas.ResponsesToolTypeCodeInterpreter, ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Container: "cntr_named_shared"}},
		},
	}}}
	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	callType := schemas.ResponsesMessageTypeCodeInterpreterCall
	call := func(container string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{Type: &callType, ResponsesToolMessage: &schemas.ResponsesToolMessage{ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{ContainerID: container}}}
	}
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{
		Output:      []schemas.ResponsesMessage{call("cntr_auto_second"), call("cntr_named_shared")},
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ResponsesRequest, Provider: schemas.OpenAI}}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "vk-mcp-stamp", owner("cntr_auto_second"), "the automatic container is recorded")
	assert.Empty(t, owner("cntr_named_shared"), "the container the request named by id is not claimed")
}

// A custom provider built on the same upstream as a standard one addresses the same objects: an
// OpenAI file id is the same file through `openai` and through a custom provider whose base is
// OpenAI. Ownership recorded under one name holds under every provider name that shares its base,
// so a second key cannot reach the object by switching the provider it asks through.
func TestPreLLMHookBindsObjectsAcrossProviderAliases(t *testing.T) {
	store := newProviderObjectConfigStore(t)
	vk := buildVKForMCPStamping(nil)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
		buildProviderConfig("my-openai", []string{"*"}),
		buildProviderConfig("other-service", []string{"*"}),
		buildProviderConfig("anthropic", []string{"*"}),
	}
	plugin := newAccessTestPlugin(t, vk, nil)
	plugin.configStore = store
	plugin.inMemoryStore = &mockInMemoryStore{configuredProviders: map[schemas.ModelProvider]configstore.ProviderConfig{
		// The standard provider names its default endpoint explicitly and the custom one leaves it
		// unset: both reach api.openai.com, so they are still one upstream.
		schemas.OpenAI: {NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.openai.com/"}},
		"my-openai":    {CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI}},
		"other-service": {
			CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI},
			NetworkConfig:        &schemas.NetworkConfig{BaseURL: "https://inference.other-service.example/v1"},
		},
		schemas.Anthropic: {},
	}}
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindFile, "openai", "file-other", "vk-someone-else")

	_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), &schemas.BifrostRequest{RequestType: schemas.FileRetrieveRequest, FileRetrieveRequest: &schemas.BifrostFileRetrieveRequest{Provider: "my-openai", FileID: "file-other"}})
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "another key's OpenAI file is refused through an OpenAI-based custom provider")
	assert.Equal(t, 404, *shortCircuit.Error.StatusCode)

	_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), &schemas.BifrostRequest{RequestType: schemas.FileRetrieveRequest, FileRetrieveRequest: &schemas.BifrostFileRetrieveRequest{Provider: schemas.Anthropic, FileID: "file-other"}})
	require.NoError(t, err)
	assert.Nil(t, shortCircuit, "a provider on a different upstream shares no objects with OpenAI")

	// Speaking OpenAI's API is not being OpenAI: a custom provider on another service's base URL
	// is a different upstream, and an id that happens to match there is not the OpenAI file.
	_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), &schemas.BifrostRequest{RequestType: schemas.FileRetrieveRequest, FileRetrieveRequest: &schemas.BifrostFileRetrieveRequest{Provider: "other-service", FileID: "file-other"}})
	require.NoError(t, err)
	assert.Nil(t, shortCircuit, "an OpenAI-compatible provider on another base URL shares no objects with OpenAI")

	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.FileListRequest, FileListRequest: &schemas.BifrostFileListRequest{Provider: "my-openai"}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	list := &schemas.BifrostResponse{FileListResponse: &schemas.BifrostFileListResponse{Data: []schemas.FileObject{{ID: "file-other"}, {ID: "file-unknown"}},
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.FileListRequest, Provider: "my-openai"}}}
	_, _, err = plugin.PostLLMHook(ctx, list, nil)
	require.NoError(t, err)
	require.Len(t, list.FileListResponse.Data, 1, "the alias list hides the file recorded under openai")
	assert.Equal(t, "file-unknown", list.FileListResponse.Data[0].ID)
}

// The virtual key a request is attributed to is read from its Grant. A context whose identity
// names the key, with none of the deprecated context keys set, is still bound and still records.
func TestProviderObjectOwnershipReadsTheGrant(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindFile, "openai", "file-other", "vk-someone-else")
	grantOnly := func() *schemas.BifrostContext {
		ctx := emptyCtx()
		settleTestIdentity(ctx)
		recordVirtualKeyIdentity(ctx, &configstoreTables.TableVirtualKey{ID: "vk-grant-only", Name: "grant-only"})
		require.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID), "the deprecated key is deliberately absent")
		return ctx
	}

	shortCircuit := plugin.enforceProviderObjectOwnership(grantOnly(), &schemas.BifrostRequest{RequestType: schemas.FileRetrieveRequest, FileRetrieveRequest: &schemas.BifrostFileRetrieveRequest{Provider: schemas.OpenAI, FileID: "file-other"}})
	require.NotNil(t, shortCircuit, "the key on the Grant is the one checked")
	assert.Equal(t, 404, *shortCircuit.Error.StatusCode)

	plugin.recordProviderObjectLifecycle(grantOnly(), schemas.FileUploadRequest, "openai", &schemas.BifrostResponse{FileUploadResponse: &schemas.BifrostFileUploadResponse{ID: "file-grant"}})
	rows, err := store.GetProviderObjectsByIDs(context.Background(), []string{configstoreTables.ProviderObjectID(configstoreTables.ProviderObjectKindFile, "openai", "file-grant")})
	require.NoError(t, err)
	require.Len(t, rows, 1, "the create is recorded under the key on the Grant")
	assert.Equal(t, "vk-grant-only", rows[0].VirtualKeyID)

	ctx := grantOnly()
	list := &schemas.BifrostResponse{FileListResponse: &schemas.BifrostFileListResponse{Data: []schemas.FileObject{{ID: "file-other"}, {ID: "file-grant"}}}}
	require.NoError(t, plugin.filterProviderObjectList(ctx, "openai", list))
	require.Len(t, list.FileListResponse.Data, 1, "the list is narrowed for the key on the Grant")
	assert.Equal(t, "file-grant", list.FileListResponse.Data[0].ID)
}

// A batch retrieve names the files the batch produced. Governance records them under the
// batch's own recorded creator, so they are bound even when no accounting row is ever written
// (the sweeper that writes those rows runs only with the logging plugin). Files of a batch
// nobody recorded are not claimed for whoever retrieved it.
func TestPostLLMHookRecordsBatchFilesFromRetrieve(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindBatch, "openai", "batch-own", "vk-mcp-stamp")
	retrieve := func(t *testing.T, batchID, output, errFile string) {
		t.Helper()
		ctx := presentCtx(mcpTestVKValue)
		_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.BatchRetrieveRequest, BatchRetrieveRequest: &schemas.BifrostBatchRetrieveRequest{Provider: schemas.OpenAI, BatchID: batchID}})
		require.NoError(t, err)
		require.Nil(t, shortCircuit)
		resp := &schemas.BifrostBatchRetrieveResponse{ID: batchID, Status: schemas.BatchStatusCompleted,
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchRetrieveRequest, Provider: schemas.OpenAI}}
		if output != "" {
			resp.OutputFileID = &output
		}
		if errFile != "" {
			resp.ErrorFileID = &errFile
		}
		_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{BatchRetrieveResponse: resp}, nil)
		require.NoError(t, err)
	}
	owner := func(t *testing.T, fileID string) string {
		t.Helper()
		rows, err := store.GetProviderObjectsByIDs(context.Background(), []string{configstoreTables.ProviderObjectID(configstoreTables.ProviderObjectKindFile, "openai", fileID)})
		require.NoError(t, err)
		if len(rows) == 0 {
			return ""
		}
		return rows[0].VirtualKeyID
	}

	retrieve(t, "batch-own", "file-own-output", "file-own-error")
	assert.Equal(t, "vk-mcp-stamp", owner(t, "file-own-output"), "the output file follows the batch's creator")
	assert.Equal(t, "vk-mcp-stamp", owner(t, "file-own-error"), "the error file follows the batch's creator")

	otherKey := buildVKForMCPStamping(nil)
	otherKey.ID = "vk-other"
	otherKey.Value = *schemas.NewSecretVar("sk-bf-other")
	otherPlugin := newAccessTestPlugin(t, otherKey, nil)
	otherPlugin.configStore = store
	_, shortCircuit, err := otherPlugin.PreLLMHook(presentCtx("sk-bf-other"), providerObjectRequests("file-own-output")[schemas.FileContentRequest])
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "another key cannot download the batch output")
	assert.Equal(t, 404, *shortCircuit.Error.StatusCode)

	retrieve(t, "batch-unbound", "file-unbound-output", "")
	assert.Empty(t, owner(t, "file-unbound-output"), "a batch nobody recorded does not hand its files to whoever retrieved it")
}

// The batch-row fallback is a scan of the batch history; it runs only for the ids the ownership
// rows did not answer.
func TestProviderObjectOwnersReadBatchRowsOnlyForUnrecordedFiles(t *testing.T) {
	plugin, real := newProviderJobTestPlugin(t)
	store := &recordingObjectStore{ConfigStore: real}
	plugin.configStore = store
	seedProviderObject(t, real, configstoreTables.ProviderObjectKindFile, "openai", "file-own", "vk-mcp-stamp")

	_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests("file-own")[schemas.FileRetrieveRequest])
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assert.Empty(t, store.fileLookups, "a file with an ownership row needs no batch-row lookup")

	_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests("file-unknown")[schemas.FileRetrieveRequest])
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	require.Len(t, store.fileLookups, 1, "an unrecorded file is looked up in the batch rows once")
	assert.Contains(t, store.fileLookups[0], "file-unknown")
	assert.NotContains(t, store.fileLookups[0], "file-own", "only the unrecorded file is looked up, under every form the provider accepts")
}

// A list page whose ids, across every form, outnumber the bind-parameter limit (a 10,000-item
// file page under an openai provider that eight custom providers share reaches it) still has its
// ownership read: another key's file is hidden and the lookup does not fail the page.
func TestHiddenProviderObjectsReadsAPageOverTheBindParameterLimit(t *testing.T) {
	plugin, store := newProviderJobTestPlugin(t)
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindFile, "openai", "file-other", "vk-someone-else")
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindFile, "openai", "file-own", "vk-mcp-stamp")
	ids := make([]string, 0, 40002)
	for i := range 40000 {
		ids = append(ids, fmt.Sprintf("file-unrecorded-%05d", i))
	}
	ids = append(ids, "file-other", "file-own")

	hidden, err := plugin.hiddenProviderObjects(presentCtx(mcpTestVKValue), configstoreTables.ProviderObjectKindFile, "openai", "vk-mcp-stamp", ids)
	require.NoError(t, err)
	require.NotNil(t, hidden)
	assert.True(t, hidden("file-other"), "another key's file is hidden")
	assert.False(t, hidden("file-own"))
	assert.False(t, hidden("file-unrecorded-00001"), "an unrecorded file stays shared")
}

// A creator write that fails is tried once more before it is given up on and logged: the object
// exists at the provider either way, and a bound object beats an unbound one.
func TestPostLLMHookRetriesAFailedCreatorWriteOnce(t *testing.T) {
	plugin, real := newProviderJobTestPlugin(t)
	store := &recordingObjectStore{ConfigStore: real}
	plugin.configStore = store
	upload := func(t *testing.T, fileID string) {
		t.Helper()
		ctx := presentCtx(mcpTestVKValue)
		_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.FileUploadRequest, FileUploadRequest: &schemas.BifrostFileUploadRequest{Provider: schemas.OpenAI}})
		require.NoError(t, err)
		require.Nil(t, shortCircuit)
		_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{FileUploadResponse: &schemas.BifrostFileUploadResponse{ID: fileID,
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.FileUploadRequest, Provider: schemas.OpenAI}}}, nil)
		require.NoError(t, err)
	}
	recorded := func(t *testing.T, fileID string) bool {
		t.Helper()
		rows, err := real.GetProviderObjectsByIDs(context.Background(), []string{configstoreTables.ProviderObjectID(configstoreTables.ProviderObjectKindFile, "openai", fileID)})
		require.NoError(t, err)
		return len(rows) == 1
	}

	store.failUpserts = 1
	upload(t, "file-hiccup")
	assert.True(t, recorded(t, "file-hiccup"), "one failed write is retried")
	assert.Equal(t, 2, store.upserts)

	store.upserts, store.failUpserts = 0, 2
	upload(t, "file-down")
	assert.False(t, recorded(t, "file-down"))
	assert.Equal(t, 2, store.upserts, "the retry is bounded")
}

// BenchmarkPreLLMHookProviderObjectChecks measures the governance pre-hook for a virtual key
// against a real SQLite config store: a chat request that names no provider object (the common
// case), a file retrieve by id, and a passthrough Responses body that names a file.
func BenchmarkPreLLMHookProviderObjectChecks(b *testing.B) {
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: b.TempDir() + "/bench.db"},
	}, NewMockLogger())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close(context.Background())
	plugin := newAccessTestPlugin(b, buildVKForMCPStamping(nil), nil)
	plugin.configStore = store
	enforced := false
	plugin.objectOwnershipEnforcedFn = func() bool { return enforced }
	text := "hello"
	cases := map[string]func() *schemas.BifrostRequest{
		"chat_no_references": func() *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o",
				Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &text}}}}}
		},
		"file_retrieve": func() *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.FileRetrieveRequest, FileRetrieveRequest: &schemas.BifrostFileRetrieveRequest{Provider: schemas.OpenAI, FileID: "file-bench"}}
		},
		"passthrough_no_references": func() *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.PassthroughRequest, PassthroughRequest: &schemas.BifrostPassthroughRequest{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions",
				Body: []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)}}
		},
		"passthrough_responses_body": func() *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.PassthroughRequest, PassthroughRequest: &schemas.BifrostPassthroughRequest{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/responses",
				Body: []byte(`{"model":"gpt-4o","input":[{"role":"user","content":[{"type":"input_text","text":"hi"},{"type":"input_file","file_id":"file-bench"}]}]}`)}}
		},
	}
	// "on" is the default and runs every check; "off" is client.enforce_vk_object_ownership: false.
	for _, mode := range []string{"off", "on"} {
		for _, name := range []string{"chat_no_references", "file_retrieve", "passthrough_no_references", "passthrough_responses_body"} {
			build := cases[name]
			b.Run(mode+"/"+name, func(b *testing.B) {
				enforced = mode == "on"
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, _, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), build()); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// newResourceNameTestPlugin is newProviderJobTestPlugin with the key also allowed on Gemini and
// Vertex, the providers that address objects by resource name.
func newResourceNameTestPlugin(t *testing.T) (*GovernancePlugin, configstore.ConfigStore) {
	t.Helper()
	configStore := newProviderObjectConfigStore(t)
	vk := buildVKForMCPStamping(nil)
	vk.ProviderConfigs = append(vk.ProviderConfigs, buildProviderConfig("gemini", []string{"*"}), buildProviderConfig("vertex", []string{"*"}))
	plugin := newAccessTestPlugin(t, vk, nil)
	plugin.configStore = configStore
	return plugin, configStore
}

// resourceRequest builds a request of requestType addressing id on provider, for the request
// types Gemini and Vertex answer by resource name.
func resourceRequest(requestType schemas.RequestType, provider schemas.ModelProvider, id string) *schemas.BifrostRequest {
	req := &schemas.BifrostRequest{RequestType: requestType}
	switch requestType {
	case schemas.BatchRetrieveRequest:
		req.BatchRetrieveRequest = &schemas.BifrostBatchRetrieveRequest{Provider: provider, BatchID: id}
	case schemas.BatchCancelRequest:
		req.BatchCancelRequest = &schemas.BifrostBatchCancelRequest{Provider: provider, BatchID: id}
	case schemas.BatchResultsRequest:
		req.BatchResultsRequest = &schemas.BifrostBatchResultsRequest{Provider: provider, BatchID: id}
	case schemas.BatchDeleteRequest:
		req.BatchDeleteRequest = &schemas.BifrostBatchDeleteRequest{Provider: provider, BatchID: id}
	case schemas.FileContentRequest:
		req.FileContentRequest = &schemas.BifrostFileContentRequest{Provider: provider, FileID: id}
	case schemas.CachedContentRetrieveRequest:
		req.CachedContentRetrieveRequest = &schemas.BifrostCachedContentRetrieveRequest{Provider: provider, Name: id}
	case schemas.CachedContentDeleteRequest:
		req.CachedContentDeleteRequest = &schemas.BifrostCachedContentDeleteRequest{Provider: provider, Name: id}
	}
	return req
}

// Gemini and Vertex address an object by a resource name and accept its short forms: a Gemini
// batch is "batches/<id>" or "<id>", a Vertex batch is its full resource name or its job number,
// and files and cached contents follow suit. The row carries the name the create response
// returned; every form the provider accepts resolves to that row, including the job rows the
// logging plugin wrote before ownership rows existed.
func TestPreLLMHookBindsResourceNamesUnderEveryForm(t *testing.T) {
	plugin, store := newResourceNameTestPlugin(t)
	own := "vk-mcp-stamp"
	other := "vk-someone-else"
	batch := configstoreTables.ProviderObjectKindBatch
	file := configstoreTables.ProviderObjectKindFile
	cached := configstoreTables.ProviderObjectKindCachedContent
	const vertexBatch = "projects/p-1/locations/us-central1/batchPredictionJobs/4242"
	const vertexCached = "projects/p-1/locations/us-central1/cachedContents/9797"
	seedProviderObject(t, store, batch, "gemini", "batches/g-other", other)
	seedProviderObject(t, store, batch, "gemini", "batches/g-own", own)
	seedProviderObject(t, store, file, "gemini", "files/f-other", other)
	seedProviderObject(t, store, cached, "gemini", "cachedContents/c-other", other)
	// Rows keyed by a short form directly (written before full names were resolved by suffix) still count.
	for _, id := range []string{vertexBatch, "4242"} {
		seedProviderObject(t, store, batch, "vertex", id, other)
	}
	for _, id := range []string{vertexCached, "cachedContents/9797", "9797"} {
		seedProviderObject(t, store, cached, "vertex", id, other)
	}
	seedProviderJob(t, store, "gemini", "batches/g-job", &other)
	seedProviderJob(t, store, "vertex", "projects/p-1/locations/us-central1/batchPredictionJobs/5151", &other)

	refused := []struct {
		name string
		req  *schemas.BifrostRequest
		id   string
	}{
		{"gemini batch retrieve by bare id", resourceRequest(schemas.BatchRetrieveRequest, schemas.Gemini, "g-other"), "g-other"},
		{"gemini batch cancel by bare id", resourceRequest(schemas.BatchCancelRequest, schemas.Gemini, "g-other"), "g-other"},
		{"gemini batch results by bare id", resourceRequest(schemas.BatchResultsRequest, schemas.Gemini, "g-other"), "g-other"},
		{"gemini batch retrieve by full name", resourceRequest(schemas.BatchRetrieveRequest, schemas.Gemini, "batches/g-other"), "batches/g-other"},
		{"gemini file content by bare id", resourceRequest(schemas.FileContentRequest, schemas.Gemini, "f-other"), "f-other"},
		{"gemini cached content retrieve by bare id", resourceRequest(schemas.CachedContentRetrieveRequest, schemas.Gemini, "c-other"), "c-other"},
		{"vertex batch retrieve by job number", resourceRequest(schemas.BatchRetrieveRequest, schemas.Vertex, "4242"), "4242"},
		{"vertex batch results by full name", resourceRequest(schemas.BatchResultsRequest, schemas.Vertex, vertexBatch), vertexBatch},
		{"vertex cached content retrieve by short name", resourceRequest(schemas.CachedContentRetrieveRequest, schemas.Vertex, "cachedContents/9797"), "cachedContents/9797"},
		{"vertex cached content delete by bare id", resourceRequest(schemas.CachedContentDeleteRequest, schemas.Vertex, "9797"), "9797"},
		{"gemini batch retrieve by bare id through the job row", resourceRequest(schemas.BatchRetrieveRequest, schemas.Gemini, "g-job"), "g-job"},
		{"vertex batch retrieve by job number through the job row", resourceRequest(schemas.BatchRetrieveRequest, schemas.Vertex, "5151"), "5151"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), tc.req)
			require.NoError(t, err)
			require.NotNil(t, shortCircuit, "another key's object is refused in every form")
			require.NotNil(t, shortCircuit.Error.StatusCode)
			assert.Equal(t, 404, *shortCircuit.Error.StatusCode)
			assert.Contains(t, shortCircuit.Error.Error.Message, "'"+tc.id+"'", "the message names the id as the caller wrote it")
		})
	}

	for _, id := range []string{"g-own", "batches/g-own"} {
		_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), resourceRequest(schemas.BatchRetrieveRequest, schemas.Gemini, id))
		require.NoError(t, err)
		assert.Nil(t, shortCircuit, "the creating key reaches its batch as %q", id)
	}

	// A job number is matched whole: 5151 is not reached as 151.
	_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), resourceRequest(schemas.BatchRetrieveRequest, schemas.Vertex, "151"))
	require.NoError(t, err)
	assert.Nil(t, shortCircuit, "a job number that is only a suffix of another is not that job")
}

// A full resource name is recorded as the provider returned it and nothing else: a Vertex job
// number is scoped to a project and region, so a row keyed by the bare number would be shared by
// every project's job of that number. A short-form request is resolved against the full names
// instead (see TestPreLLMHookResolvesShortResourceNamesThroughFullNameRows), and a delete by the
// short form forgets only the deleter's own full-name rows.
func TestPostLLMHookRecordsFullResourceNamesOnceAndForgetsOnlyTheDeletersRows(t *testing.T) {
	plugin, store := newResourceNameTestPlugin(t)
	const vertexBatch = "projects/p-1/locations/us-central1/batchPredictionJobs/4242"
	const otherProjectBatch = "projects/p-2/locations/us-central1/batchPredictionJobs/4242"
	batch := configstoreTables.ProviderObjectKindBatch
	fullRow := configstoreTables.ProviderObjectID(batch, "vertex", vertexBatch)
	bareRow := configstoreTables.ProviderObjectID(batch, "vertex", "4242")
	otherRow := configstoreTables.ProviderObjectID(batch, "vertex", otherProjectBatch)

	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{Provider: schemas.Vertex}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{BatchCreateResponse: &schemas.BifrostBatchCreateResponse{ID: vertexBatch,
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchCreateRequest, Provider: schemas.Vertex}}}, nil)
	require.NoError(t, err)

	rows, err := store.GetProviderObjectsByIDs(context.Background(), []string{fullRow, bareRow})
	require.NoError(t, err)
	require.Len(t, rows, 1, "only the full name is recorded; a bare job number is not one project's to claim")
	assert.Equal(t, vertexBatch, rows[0].ObjectID)
	assert.Equal(t, "vk-mcp-stamp", rows[0].VirtualKeyID)

	// Another project's job with the same number, created by another key, survives this key's
	// delete. The delete is admitted by the full name, which is one project's; the provider then
	// echoes the job number.
	seedProviderObject(t, store, batch, "vertex", otherProjectBatch, "vk-someone-else")
	ctx = presentCtx(mcpTestVKValue)
	_, shortCircuit, err = plugin.PreLLMHook(ctx, resourceRequest(schemas.BatchDeleteRequest, schemas.Vertex, vertexBatch))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{BatchDeleteResponse: &schemas.BifrostBatchDeleteResponse{ID: "4242",
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchDeleteRequest, Provider: schemas.Vertex}}}, nil)
	require.NoError(t, err)
	rows, err = store.GetProviderObjectsByIDs(context.Background(), []string{fullRow, otherRow})
	require.NoError(t, err)
	require.Len(t, rows, 1, "a delete by job number forgets the deleter's full name and nobody else's")
	assert.Equal(t, otherProjectBatch, rows[0].ObjectID)
}

// A short form of a Google resource name is resolved against the full names on record, not only
// against rows keyed by the short form itself: a cached content recorded as its full name before
// short forms were understood, or a batch recorded by its create, is still another key's when
// asked for as "9797" or "cachedContents/9797".
func TestPreLLMHookResolvesShortResourceNamesThroughFullNameRows(t *testing.T) {
	plugin, store := newResourceNameTestPlugin(t)
	other := "vk-someone-else"
	batch := configstoreTables.ProviderObjectKindBatch
	cached := configstoreTables.ProviderObjectKindCachedContent
	seedProviderObject(t, store, cached, "vertex", "projects/p-1/locations/us-central1/cachedContents/9797", other)
	seedProviderObject(t, store, batch, "vertex", "projects/p-1/locations/us-central1/batchPredictionJobs/4242", other)
	seedProviderObject(t, store, cached, "vertex", "projects/p-1/locations/us-central1/cachedContents/7777", "vk-mcp-stamp")

	for _, tc := range []struct {
		name string
		req  *schemas.BifrostRequest
		id   string
	}{
		{"cached content by bare id", resourceRequest(schemas.CachedContentRetrieveRequest, schemas.Vertex, "9797"), "9797"},
		{"cached content by short name", resourceRequest(schemas.CachedContentRetrieveRequest, schemas.Vertex, "cachedContents/9797"), "cachedContents/9797"},
		{"cached content delete by bare id", resourceRequest(schemas.CachedContentDeleteRequest, schemas.Vertex, "9797"), "9797"},
		{"batch by job number", resourceRequest(schemas.BatchRetrieveRequest, schemas.Vertex, "4242"), "4242"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), tc.req)
			require.NoError(t, err)
			require.NotNil(t, shortCircuit, "another key's object is refused through its full-name row")
			require.NotNil(t, shortCircuit.Error.StatusCode)
			assert.Equal(t, 404, *shortCircuit.Error.StatusCode)
			assert.Contains(t, shortCircuit.Error.Error.Message, "'"+tc.id+"'")
		})
	}
	_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), resourceRequest(schemas.CachedContentRetrieveRequest, schemas.Vertex, "7777"))
	require.NoError(t, err)
	assert.Nil(t, shortCircuit, "the creating key reaches its own cached content by the bare id")
	_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), resourceRequest(schemas.CachedContentRetrieveRequest, schemas.Vertex, "797"))
	require.NoError(t, err)
	assert.Nil(t, shortCircuit, "a bare id that is only a suffix of another is not that object")
}

// A bare Vertex id names one job per project and region, which governance cannot tell apart before
// a key is chosen. When the full names on record for a job number belong to more than one virtual
// key, the request is refused as ambiguous instead of guessing, and the caller is told to use the
// full resource name; a list hides such an item. Full names are never ambiguous.
func TestPreLLMHookRefusesAmbiguousShortResourceNames(t *testing.T) {
	plugin, store := newResourceNameTestPlugin(t)
	batch := configstoreTables.ProviderObjectKindBatch
	const own = "projects/p-1/locations/us-central1/batchPredictionJobs/4242"
	const theirs = "projects/p-2/locations/us-central1/batchPredictionJobs/4242"
	seedProviderObject(t, store, batch, "vertex", own, "vk-mcp-stamp")
	seedProviderObject(t, store, batch, "vertex", theirs, "vk-someone-else")

	_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), resourceRequest(schemas.BatchRetrieveRequest, schemas.Vertex, "4242"))
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "a job number recorded under two keys is refused")
	require.NotNil(t, shortCircuit.Error.StatusCode)
	assert.Equal(t, 400, *shortCircuit.Error.StatusCode)
	assert.Contains(t, shortCircuit.Error.Error.Message, "'4242'")
	assert.Contains(t, shortCircuit.Error.Error.Message, "full resource name")

	_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), resourceRequest(schemas.BatchRetrieveRequest, schemas.Vertex, own))
	require.NoError(t, err)
	assert.Nil(t, shortCircuit, "the full name is one project's and reaches the key's own job")
	_, shortCircuit, err = plugin.PreLLMHook(presentCtx(mcpTestVKValue), resourceRequest(schemas.BatchRetrieveRequest, schemas.Vertex, theirs))
	require.NoError(t, err)
	require.NotNil(t, shortCircuit)
	assert.Equal(t, 404, *shortCircuit.Error.StatusCode, "the other project's full name is another key's")

	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.BatchListRequest, BatchListRequest: &schemas.BifrostBatchListRequest{Provider: schemas.Vertex}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	list := &schemas.BifrostResponse{BatchListResponse: &schemas.BifrostBatchListResponse{Data: []schemas.BifrostBatchRetrieveResponse{{ID: "4242"}, {ID: own}, {ID: "unrecorded"}},
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchListRequest, Provider: schemas.Vertex}}}
	_, _, err = plugin.PostLLMHook(ctx, list, nil)
	require.NoError(t, err)
	ids := make([]string, 0, 3)
	for _, item := range list.BatchListResponse.Data {
		ids = append(ids, item.ID)
	}
	assert.Equal(t, []string{own, "unrecorded"}, ids, "an ambiguous short id is hidden from the page; the full name and unbound items stay")
}

// A bare Vertex id can be answered by two sources: the full-name rows that end in it and an exact
// job row under the bare id itself. When they name different keys the id is ambiguous, refused
// with 400 and hidden from lists, whichever source answered first.
func TestPreLLMHookRefusesABareIDWhoseSourcesDisagree(t *testing.T) {
	plugin, store := newResourceNameTestPlugin(t)
	batch := configstoreTables.ProviderObjectKindBatch
	seedProviderObject(t, store, batch, "vertex", "projects/p-1/locations/us-central1/batchPredictionJobs/4242", "vk-mcp-stamp")
	other := "vk-someone-else"
	seedProviderJob(t, store, "vertex", "4242", &other)

	_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), resourceRequest(schemas.BatchRetrieveRequest, schemas.Vertex, "4242"))
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "a bare id whose full-name row and exact job row name different keys is refused")
	require.NotNil(t, shortCircuit.Error.StatusCode)
	assert.Equal(t, 400, *shortCircuit.Error.StatusCode)
}

// A bare Vertex number can match the deleting key's own jobs in two projects. Vertex deleted one of
// them and echoes only the number, so which one is unknown: both bindings are kept rather than the
// surviving job left unbound. With a single match, that one is forgotten.
func TestPostLLMHookKeepsBindingsWhenABareDeleteIsAmbiguous(t *testing.T) {
	plugin, store := newResourceNameTestPlugin(t)
	batch := configstoreTables.ProviderObjectKindBatch
	const p1 = "projects/p-1/locations/us-central1/batchPredictionJobs/4242"
	const p2 = "projects/p-2/locations/us-central1/batchPredictionJobs/4242"
	const single = "projects/p-1/locations/us-central1/batchPredictionJobs/5151"
	for _, id := range []string{p1, p2, single} {
		seedProviderObject(t, store, batch, "vertex", id, "vk-mcp-stamp")
	}
	deleteBare := func(id string) {
		ctx := presentCtx(mcpTestVKValue)
		_, shortCircuit, err := plugin.PreLLMHook(ctx, resourceRequest(schemas.BatchDeleteRequest, schemas.Vertex, id))
		require.NoError(t, err)
		require.Nil(t, shortCircuit, "the key's own jobs may be deleted")
		_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{BatchDeleteResponse: &schemas.BifrostBatchDeleteResponse{ID: id,
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchDeleteRequest, Provider: schemas.Vertex}}}, nil)
		require.NoError(t, err)
	}
	rowIDs := func(ids ...string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, configstoreTables.ProviderObjectID(batch, "vertex", id))
		}
		return out
	}

	deleteBare("4242")
	rows, err := store.GetProviderObjectsByIDs(context.Background(), rowIDs(p1, p2))
	require.NoError(t, err)
	assert.Len(t, rows, 2, "neither binding is forgotten when the bare number matched two of the key's jobs")

	deleteBare("5151")
	rows, err = store.GetProviderObjectsByIDs(context.Background(), rowIDs(single))
	require.NoError(t, err)
	assert.Empty(t, rows, "a bare number that matched one job forgets it")
}

// A delete admitted through one provider name forgets the object's rows under every name that
// reaches the same upstream, so a binding recorded through another alias does not linger.
func TestPostLLMHookForgetsDeletedObjectsUnderEveryAlias(t *testing.T) {
	store := newProviderObjectConfigStore(t)
	vk := buildVKForMCPStamping(nil)
	vk.ProviderConfigs = append(vk.ProviderConfigs, buildProviderConfig("my-openai", []string{"*"}))
	plugin := newAccessTestPlugin(t, vk, nil)
	plugin.configStore = store
	plugin.inMemoryStore = &mockInMemoryStore{configuredProviders: map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {},
		"my-openai":    {CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI}},
	}}
	seedProviderObject(t, store, configstoreTables.ProviderObjectKindFile, "openai", "file-aliased", "vk-mcp-stamp")

	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.FileDeleteRequest, FileDeleteRequest: &schemas.BifrostFileDeleteRequest{Provider: "my-openai", FileID: "file-aliased"}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{FileDeleteResponse: &schemas.BifrostFileDeleteResponse{ID: "file-aliased", Deleted: true,
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.FileDeleteRequest, Provider: "my-openai"}}}, nil)
	require.NoError(t, err)
	rows, err := store.GetProviderObjectsByIDs(context.Background(), []string{configstoreTables.ProviderObjectID(configstoreTables.ProviderObjectKindFile, "openai", "file-aliased")})
	require.NoError(t, err)
	assert.Empty(t, rows, "the binding recorded under openai is forgotten by a delete through my-openai")
}

// suffixCountingStore is the real store with the suffix lookups observed.
type suffixCountingStore struct {
	configstore.ConfigStore
	objectSuffixes [][]string
	jobSuffixes    [][]string
}

func (s *suffixCountingStore) GetProviderObjectsByObjectIDSuffixes(ctx context.Context, kind, provider string, suffixes []string) ([]*configstoreTables.TableProviderObject, error) {
	s.objectSuffixes = append(s.objectSuffixes, append([]string(nil), suffixes...))
	return s.ConfigStore.GetProviderObjectsByObjectIDSuffixes(ctx, kind, provider, suffixes)
}

func (s *suffixCountingStore) GetProviderJobsByJobIDSuffixes(ctx context.Context, kind, provider string, suffixes []string) ([]*configstoreTables.TableProviderJob, error) {
	s.jobSuffixes = append(s.jobSuffixes, append([]string(nil), suffixes...))
	return s.ConfigStore.GetProviderJobsByJobIDSuffixes(ctx, kind, provider, suffixes)
}

// A list page of Vertex batches resolves its short ids against the full names in one query, for
// the ids the exact rows did not answer only: an id whose row was found is not scanned for again,
// and the remaining ids are deduplicated into a single lookup.
func TestProviderObjectOwnersScanSuffixesOnceForUnresolvedShortIDs(t *testing.T) {
	plugin, real := newResourceNameTestPlugin(t)
	store := &suffixCountingStore{ConfigStore: real}
	plugin.configStore = store
	batch := configstoreTables.ProviderObjectKindBatch
	seedProviderObject(t, real, batch, "vertex", "4242", "vk-mcp-stamp")
	seedProviderObject(t, real, batch, "vertex", "5151", "vk-mcp-stamp")

	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.BatchListRequest, BatchListRequest: &schemas.BifrostBatchListRequest{Provider: schemas.Vertex}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	list := &schemas.BifrostResponse{BatchListResponse: &schemas.BifrostBatchListResponse{Data: []schemas.BifrostBatchRetrieveResponse{{ID: "4242"}, {ID: "5151"}, {ID: "6161"}, {ID: "6161"}},
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchListRequest, Provider: schemas.Vertex}}}
	_, _, err = plugin.PostLLMHook(ctx, list, nil)
	require.NoError(t, err)
	assert.Len(t, list.BatchListResponse.Data, 4, "nothing on the page is another key's")
	assert.Equal(t, [][]string{{"6161"}}, store.objectSuffixes, "one ownership suffix lookup, for the unresolved id only")
	assert.Equal(t, [][]string{{"6161"}}, store.jobSuffixes, "one job suffix lookup, for the unresolved id only")
}

// BenchmarkPreLLMHookResourceNameLookup measures the governance pre-hook for batch requests that
// address an object by a short form of its resource name, against a real SQLite config store: a
// Gemini batch by its bare id, and a Vertex batch by its job number, which is resolved against
// the full names on record.
func BenchmarkPreLLMHookResourceNameLookup(b *testing.B) {
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: b.TempDir() + "/bench.db"},
	}, NewMockLogger())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close(context.Background())
	vk := buildVKForMCPStamping(nil)
	vk.ProviderConfigs = append(vk.ProviderConfigs, buildProviderConfig("gemini", []string{"*"}), buildProviderConfig("vertex", []string{"*"}))
	plugin := newAccessTestPlugin(b, vk, nil)
	plugin.configStore = store
	cases := []struct {
		name     string
		provider schemas.ModelProvider
		id       string
	}{
		{"gemini_batch_bare_id", schemas.Gemini, "g-bench"},
		{"vertex_batch_job_number", schemas.Vertex, "4242"},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				req := &schemas.BifrostRequest{RequestType: schemas.BatchRetrieveRequest, BatchRetrieveRequest: &schemas.BifrostBatchRetrieveRequest{Provider: tc.provider, BatchID: tc.id}}
				if _, _, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
