package vertex

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildVertexRankingConfig(t *testing.T) {
	t.Parallel()

	config, err := buildVertexRankingConfig("demo-project", "")
	require.NoError(t, err)
	assert.Equal(t, "projects/demo-project/locations/global/rankingConfigs/default_ranking_config", config)

	config, err = buildVertexRankingConfig("demo-project", "custom_rank")
	require.NoError(t, err)
	assert.Equal(t, "projects/demo-project/locations/global/rankingConfigs/custom_rank", config)

	config, err = buildVertexRankingConfig("demo-project", "projects/other/locations/global/rankingConfigs/custom_rank:rank")
	require.NoError(t, err)
	assert.Equal(t, "projects/other/locations/global/rankingConfigs/custom_rank", config)

	_, err = buildVertexRankingConfig("demo-project", "locations/global/rankingConfigs/custom_rank")
	require.Error(t, err)
}

func TestToVertexRankRequest(t *testing.T) {
	t.Parallel()

	req, err := ToVertexRankRequest(
		&schemas.BifrostRerankRequest{
			Query: "capital of france",
			Documents: []schemas.RerankDocument{
				{Text: "Paris is the capital of France.", Meta: map[string]interface{}{"title": "Doc A"}},
				{Text: "Berlin is the capital of Germany."},
			},
			Params: &schemas.RerankParameters{
				TopN: schemas.Ptr(10),
			},
		},
		&vertexRerankOptions{
			RankingConfig:                 "projects/p/locations/global/rankingConfigs/default_ranking_config",
			IgnoreRecordDetailsInResponse: true,
		},
	)
	require.NoError(t, err)
	require.NotNil(t, req)

	require.NotNil(t, req.Model)
	assert.Equal(t, "semantic-ranker-default@latest", *req.Model)
	require.Len(t, req.Records, 2)
	assert.Equal(t, "idx:0", req.Records[0].ID)
	assert.Equal(t, "idx:1", req.Records[1].ID)
	require.NotNil(t, req.Records[0].Title)
	assert.Equal(t, "Doc A", *req.Records[0].Title)
	require.NotNil(t, req.TopN)
	assert.Equal(t, 2, *req.TopN, "topN should be clamped to document count")
	require.NotNil(t, req.IgnoreRecordDetailsInResponse)
	assert.True(t, *req.IgnoreRecordDetailsInResponse)
}

func TestToVertexRankRequestTooManyRecords(t *testing.T) {
	t.Parallel()

	docs := make([]schemas.RerankDocument, 201)
	for i := range docs {
		docs[i] = schemas.RerankDocument{Text: "doc"}
	}

	_, err := ToVertexRankRequest(
		&schemas.BifrostRerankRequest{
			Query:     "q",
			Documents: docs,
		},
		&vertexRerankOptions{
			RankingConfig:                 "projects/p/locations/global/rankingConfigs/default_ranking_config",
			IgnoreRecordDetailsInResponse: true,
		},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "supports up to")
}

func TestGetVertexRerankOptions(t *testing.T) {
	t.Parallel()

	options, err := getVertexRerankOptions("project-x", &schemas.RerankParameters{
		// Returning documents is Bifrost's neutral spelling of "do not ignore record details".
		ReturnDocuments: new(true),
		ExtraParams: map[string]interface{}{
			"ranking_config": "custom_rank",
			"user_labels": map[string]interface{}{
				"env": "test",
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "projects/project-x/locations/global/rankingConfigs/custom_rank", options.RankingConfig)
	assert.False(t, options.IgnoreRecordDetailsInResponse)
	assert.Equal(t, map[string]string{"env": "test"}, options.UserLabels)
}

func TestGetVertexRerankOptionsReturnDocumentsUnset(t *testing.T) {
	t.Parallel()

	// A nil ReturnDocuments only reaches here from the canonical rerank route, where "unset"
	// means the caller did not ask for documents - the same default Cohere and Bedrock apply.
	// The /genai/v1/rank route never passes nil: ToBifrostRerankRequest resolves an omitted
	// ignoreRecordDetailsInResponse into an explicit ReturnDocuments first. Fetching record
	// details here would be discarded by Rerank, which attaches documents only on an explicit
	// ReturnDocuments.
	for _, params := range []*schemas.RerankParameters{nil, {}, {ReturnDocuments: new(false)}} {
		options, err := getVertexRerankOptions("project-x", params)
		require.NoError(t, err)
		assert.True(t, options.IgnoreRecordDetailsInResponse,
			"documents must not be fetched unless the caller asked for them: %+v", params)
	}
}

func TestVertexRankResponseToBifrostRerankResponse(t *testing.T) {
	t.Parallel()

	docs := []schemas.RerankDocument{
		{Text: "doc-0"},
		{Text: "doc-1"},
		{Text: "doc-2"},
	}

	response, err := (&VertexRankResponse{
		Records: []VertexRankedRecord{
			{ID: "idx:2", Score: 0.12},
			{ID: "idx:1", Score: 0.91},
			{ID: "idx:0", Score: 0.91},
		},
	}).ToBifrostRerankResponse(docs, true)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Results, 3)

	assert.Equal(t, 0, response.Results[0].Index)
	assert.Equal(t, 1, response.Results[1].Index)
	assert.Equal(t, 2, response.Results[2].Index)
	require.NotNil(t, response.Results[0].Document)
	assert.Equal(t, "doc-0", response.Results[0].Document.Text)
}

func TestVertexRankRequestToBifrostRerankRequest(t *testing.T) {
	t.Parallel()

	topN := 5
	model := "semantic-ranker-default@latest"
	ignoreDetails := true
	title := "Doc A"
	content1 := "Paris is the capital of France."
	content2 := "Berlin is the capital of Germany."

	req := &VertexRankRequest{
		Model: &model,
		Query: "capital of france",
		Records: []VertexRankRecord{
			{ID: "rec-1", Content: &content1, Title: &title},
			{ID: "rec-2", Content: &content2},
		},
		TopN:                          &topN,
		IgnoreRecordDetailsInResponse: &ignoreDetails,
		UserLabels:                    map[string]string{"env": "test"},
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result := req.ToBifrostRerankRequest(bifrostCtx)

	require.NotNil(t, result)
	// Provider resolution is deferred to the route header and the modelcatalogresolver plugin,
	// matching the Cohere and Bedrock rerank converters.
	assert.Equal(t, schemas.ModelProvider(""), result.Provider)
	assert.Equal(t, "semantic-ranker-default@latest", result.Model)
	assert.Equal(t, "capital of france", result.Query)
	require.Len(t, result.Documents, 2)

	// First document has ID, content, and title in meta
	require.NotNil(t, result.Documents[0].ID)
	assert.Equal(t, "rec-1", *result.Documents[0].ID)
	assert.Equal(t, "Paris is the capital of France.", result.Documents[0].Text)
	require.NotNil(t, result.Documents[0].Meta)
	assert.Equal(t, "Doc A", result.Documents[0].Meta["title"])

	// Second document has no title
	require.NotNil(t, result.Documents[1].ID)
	assert.Equal(t, "rec-2", *result.Documents[1].ID)
	assert.Equal(t, "Berlin is the capital of Germany.", result.Documents[1].Text)
	assert.Nil(t, result.Documents[1].Meta)

	// TopN
	require.NotNil(t, result.Params)
	require.NotNil(t, result.Params.TopN)
	assert.Equal(t, 5, *result.Params.TopN)

	// ignoreRecordDetailsInResponse maps onto the neutral ReturnDocuments flag.
	require.NotNil(t, result.Params.ReturnDocuments)
	assert.False(t, *result.Params.ReturnDocuments)
	require.NotNil(t, result.Params.ExtraParams)
	assert.Equal(t, map[string]string{"env": "test"}, result.Params.ExtraParams["user_labels"])
}

func TestVertexRankRequestToBifrostRerankRequestNil(t *testing.T) {
	t.Parallel()

	var req *VertexRankRequest
	assert.Nil(t, req.ToBifrostRerankRequest(nil))
}

func TestVertexRankResponseToBifrostRerankResponseInvalidID(t *testing.T) {
	t.Parallel()

	_, err := (&VertexRankResponse{
		Records: []VertexRankedRecord{
			{ID: "bad-id", Score: 0.9},
		},
	}).ToBifrostRerankResponse([]schemas.RerankDocument{{Text: "doc"}}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid record id")
}

func TestToVertexRankResponse(t *testing.T) {
	response, err := ToVertexRankResponse(&schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{
			{Index: 1, RelevanceScore: 0.88, Document: &schemas.RerankDocument{
				ID: new("doc-paris"), Text: "Paris is capital of France", Meta: map[string]interface{}{"title": "Paris"},
			}},
			{Index: 0, RelevanceScore: 0.12, Document: &schemas.RerankDocument{
				ID: new("doc-berlin"), Text: "Berlin is capital of Germany",
			}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Records, 2)

	// Caller record IDs are restored; upstream only ever saw synthetic idx:N ids.
	assert.Equal(t, "doc-paris", response.Records[0].ID)
	assert.InDelta(t, 0.88, response.Records[0].Score, 1e-9)
	require.NotNil(t, response.Records[0].Title)
	assert.Equal(t, "Paris", *response.Records[0].Title)
	require.NotNil(t, response.Records[0].Content)
	assert.Equal(t, "Paris is capital of France", *response.Records[0].Content)

	assert.Equal(t, "doc-berlin", response.Records[1].ID)
	assert.Nil(t, response.Records[1].Title)
}

func TestToVertexRankResponseIdentityWithoutDocuments(t *testing.T) {
	// ignoreRecordDetailsInResponse means no document comes back, but the record id is identity
	// rather than detail, so it must still address the caller's record.
	response, err := ToVertexRankResponse(&schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{{Index: 0, RelevanceScore: 0.88, ID: new("doc-paris")}},
	})
	require.NoError(t, err)
	require.Len(t, response.Records, 1)
	assert.Equal(t, "doc-paris", response.Records[0].ID)
	assert.InDelta(t, 0.88, response.Records[0].Score, 1e-9)
	assert.Nil(t, response.Records[0].Title)
	assert.Nil(t, response.Records[0].Content)

	_, err = ToVertexRankResponse(nil)
	require.Error(t, err)
}

func TestVertexRankRequestToBifrostRerankRequestDefaultsToReturningDocuments(t *testing.T) {
	t.Parallel()

	// Discovery Engine returns record details by default, so an omitted flag must mean
	// documents come back rather than falling through to Bifrost's own default.
	result := (&VertexRankRequest{
		Query:   "capital of france",
		Records: []VertexRankRecord{{ID: "rec-1", Content: new("Paris is the capital of France.")}},
	}).ToBifrostRerankRequest(nil)

	require.NotNil(t, result)
	require.NotNil(t, result.Params)
	require.NotNil(t, result.Params.ReturnDocuments)
	assert.True(t, *result.Params.ReturnDocuments)
}
