package governance

import (
	"context"
	"errors"
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
// is called; a request that presented no key stays unrestricted, as everywhere else.
func TestPreLLMHookRefusesVirtualKeyBatchRequestsWithoutConfigStore(t *testing.T) {
	plugin := newAccessTestPlugin(t, buildVKForMCPStamping(nil), nil)
	plugin.configStore = nil

	requests := map[string]*schemas.BifrostRequest{
		"retrieve": batchRequest(schemas.BatchRetrieveRequest, "batch-any"),
		"cancel":   batchRequest(schemas.BatchCancelRequest, "batch-any"),
		"results":  batchRequest(schemas.BatchResultsRequest, "batch-any"),
		"delete":   batchRequest(schemas.BatchDeleteRequest, "batch-any"),
		"list":     {RequestType: schemas.BatchListRequest, BatchListRequest: &schemas.BifrostBatchListRequest{Provider: schemas.OpenAI}},
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
// input, output and error file columns, the files that carry the batch's prompts and completions.
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
		for _, fileID := range []string{"file-other-input", "file-other-output", "file-other-error"} {
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests(fileID)[requestType])
			require.NoError(t, err)
			require.NotNil(t, shortCircuit, "%s on %s: another key's batch file is refused", requestType, fileID)
			assert.Equal(t, 404, *shortCircuit.Error.StatusCode)
		}
		_, shortCircuit, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), providerObjectRequests("file-own-output")[requestType])
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

// Without a config store no creator is recorded anywhere, so a virtual-key request for any bound
// object kind, lists included, is refused up front like a batch request is.
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
	requests[schemas.FileListRequest] = &schemas.BifrostRequest{RequestType: schemas.FileListRequest, FileListRequest: &schemas.BifrostFileListRequest{Provider: schemas.OpenAI}}
	requests[schemas.VideoListRequest] = &schemas.BifrostRequest{RequestType: schemas.VideoListRequest, VideoListRequest: &schemas.BifrostVideoListRequest{Provider: schemas.OpenAI}}
	requests[schemas.ContainerListRequest] = &schemas.BifrostRequest{RequestType: schemas.ContainerListRequest, ContainerListRequest: &schemas.BifrostContainerListRequest{Provider: schemas.OpenAI}}
	requests[schemas.CachedContentListRequest] = &schemas.BifrostRequest{RequestType: schemas.CachedContentListRequest, CachedContentListRequest: &schemas.BifrostCachedContentListRequest{Provider: schemas.OpenAI}}
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
	// A Vertex create records its full name under its short forms as well.
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

// A full resource name cannot be rebuilt from its short form without the key's project and
// region, so a create that returns one is recorded under its short forms too, and a delete in
// any form forgets them all.
func TestPostLLMHookRecordsFullResourceNamesUnderTheirShortForms(t *testing.T) {
	plugin, store := newResourceNameTestPlugin(t)
	const vertexBatch = "projects/p-1/locations/us-central1/batchPredictionJobs/4242"
	batch := configstoreTables.ProviderObjectKindBatch
	rowIDs := []string{
		configstoreTables.ProviderObjectID(batch, "vertex", vertexBatch),
		configstoreTables.ProviderObjectID(batch, "vertex", "4242"),
	}

	ctx := presentCtx(mcpTestVKValue)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{Provider: schemas.Vertex}})
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{BatchCreateResponse: &schemas.BifrostBatchCreateResponse{ID: vertexBatch,
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchCreateRequest, Provider: schemas.Vertex}}}, nil)
	require.NoError(t, err)

	rows, err := store.GetProviderObjectsByIDs(context.Background(), rowIDs)
	require.NoError(t, err)
	require.Len(t, rows, 2, "the full name and the job number are both recorded")
	for _, row := range rows {
		assert.Equal(t, "vk-mcp-stamp", row.VirtualKeyID)
	}

	ctx = presentCtx(mcpTestVKValue)
	_, shortCircuit, err = plugin.PreLLMHook(ctx, resourceRequest(schemas.BatchDeleteRequest, schemas.Vertex, "4242"))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{BatchDeleteResponse: &schemas.BifrostBatchDeleteResponse{ID: "4242",
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.BatchDeleteRequest, Provider: schemas.Vertex}}}, nil)
	require.NoError(t, err)
	rows, err = store.GetProviderObjectsByIDs(context.Background(), rowIDs)
	require.NoError(t, err)
	assert.Empty(t, rows, "a delete by job number forgets the full name too")
}
