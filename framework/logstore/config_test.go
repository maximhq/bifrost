package logstore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfigObjectStorageRoutesRoundTrip checks that routes survive the
// custom UnmarshalJSON and a marshal/unmarshal cycle, which is how the
// config store persists the logs store config.
func TestConfigObjectStorageRoutesRoundTrip(t *testing.T) {
	raw := `{
		"enabled": true,
		"type": "sqlite",
		"config": {"path": "./logs.db"},
		"object_storage_routes": [{
			"name": "guardrail-flagged",
			"match": {"guardrail_outcomes": ["blocked"], "guardrail_phases": ["input"], "status_codes": [400]},
			"object_storage": {"type": "s3", "bucket": "quarantine", "prefix": "flagged"}
		}]
	}`
	var cfg Config
	require.NoError(t, json.Unmarshal([]byte(raw), &cfg))
	require.Len(t, cfg.ObjectStorageRoutes, 1)
	route := cfg.ObjectStorageRoutes[0]
	assert.Equal(t, "guardrail-flagged", route.Name)
	assert.Equal(t, []string{"blocked"}, route.Match.GuardrailOutcomes)
	assert.Equal(t, []int{400}, route.Match.StatusCodes)
	require.NotNil(t, route.ObjectStorage)
	assert.Equal(t, "flagged", route.ObjectStorage.GetPrefix())

	encoded, err := json.Marshal(&cfg)
	require.NoError(t, err)
	var again Config
	require.NoError(t, json.Unmarshal(encoded, &again))
	assert.Equal(t, cfg.ObjectStorageRoutes[0].Match, again.ObjectStorageRoutes[0].Match)
	assert.Equal(t, "quarantine", again.ObjectStorageRoutes[0].ObjectStorage.Bucket.GetValue())
}

// TestConfigValidateRoutes checks each startup rejection.
func TestConfigValidateRoutes(t *testing.T) {
	bucket := func() *objectstore.Config {
		return &objectstore.Config{Type: objectstore.StoreTypeS3, Bucket: *schemas.NewSecretVar("b")}
	}
	match := ObjectStorageRouteMatch{GuardrailOutcomes: []string{GuardrailOutcomeBlocked}}
	cases := map[string][]ObjectStorageRoute{
		"missing name":    {{Match: match, ObjectStorage: bucket()}},
		"invalid name":    {{Name: "Flagged", Match: match, ObjectStorage: bucket()}},
		"duplicate name":  {{Name: "a", Match: match, ObjectStorage: bucket()}, {Name: "a", Match: match, ObjectStorage: bucket()}},
		"empty match":     {{Name: "a", ObjectStorage: bucket()}},
		"unknown outcome": {{Name: "a", Match: ObjectStorageRouteMatch{GuardrailOutcomes: []string{"flagged"}}, ObjectStorage: bucket()}},
		"missing store":   {{Name: "a", Match: match}},
		"missing bucket":  {{Name: "a", Match: match, ObjectStorage: &objectstore.Config{Type: objectstore.StoreTypeS3}}},
		"missing type":    {{Name: "a", Match: match, ObjectStorage: &objectstore.Config{Bucket: *schemas.NewSecretVar("b")}}},
		"unknown type":    {{Name: "a", Match: match, ObjectStorage: &objectstore.Config{Type: "azure", Bucket: *schemas.NewSecretVar("b")}}},
		"parquet on gcs":  {{Name: "a", Match: match, ObjectStorage: &objectstore.Config{Type: objectstore.StoreTypeGCS, Bucket: *schemas.NewSecretVar("b"), Format: objectstore.FormatParquet}}},
	}
	for name, routes := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, (&Config{ObjectStorageRoutes: routes}).ValidateRoutes())
		})
	}
	assert.NoError(t, (&Config{ObjectStorageRoutes: []ObjectStorageRoute{
		{Name: "a", Match: match, ObjectStorage: bucket()},
		{Name: "b-2", Match: ObjectStorageRouteMatch{StatusCodes: []int{429}}, ObjectStorage: bucket()},
	}}).ValidateRoutes())
}

// TestNewLogStoreRejectsInvalidRoute checks that a bad route fails startup
// before any store is opened.
func TestNewLogStoreRejectsInvalidRoute(t *testing.T) {
	cfg := &Config{
		Enabled:             true,
		Type:                LogStoreTypeSQLite,
		Config:              &SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
		ObjectStorageRoutes: []ObjectStorageRoute{{Name: "a", Match: ObjectStorageRouteMatch{}}},
	}
	_, err := NewLogStore(context.Background(), cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "object_storage_routes")
}
