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
		ExtraParams: map[string]interface{}{
			"ranking_config":                    "custom_rank",
			"ignore_record_details_in_response": false,
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
	assert.Equal(t, schemas.Vertex, result.Provider)
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

	// ExtraParams
	require.NotNil(t, result.Params.ExtraParams)
	assert.Equal(t, true, result.Params.ExtraParams["ignore_record_details_in_response"])
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

func TestToVertexRankResponseRequiresDocuments(t *testing.T) {
	_, err := ToVertexRankResponse(&schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{{Index: 0, RelevanceScore: 0.88}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no document")

	_, err = ToVertexRankResponse(nil)
	require.Error(t, err)
}

func TestVertexRankRequestToBifrostRerankRequestDefaultsIgnoreRecordDetailsToFalse(t *testing.T) {
	t.Parallel()

	// Discovery Engine defaults the flag to false, so an omitted field must not
	// fall through to Bifrost's own default of true.
	result := (&VertexRankRequest{
		Query:   "capital of france",
		Records: []VertexRankRecord{{ID: "rec-1", Content: new("Paris is the capital of France.")}},
	}).ToBifrostRerankRequest(nil)

	require.NotNil(t, result)
	require.NotNil(t, result.Params)
	require.NotNil(t, result.Params.ExtraParams)
	assert.Equal(t, false, result.Params.ExtraParams["ignore_record_details_in_response"])
}
