package vertex

import (
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

	topN := 10
	req, err := ToVertexRankRequest(
		&schemas.BifrostRerankRequest{
			Query: "capital of france",
			Documents: []schemas.RerankDocument{
				{Text: "Paris is the capital of France.", Meta: map[string]interface{}{"title": "Doc A"}},
				{Text: "Berlin is the capital of Germany."},
			},
			Params: &schemas.RerankParameters{
				TopN: &topN,
			},
		},
		"semantic-ranker-default@latest",
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
		"",
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
