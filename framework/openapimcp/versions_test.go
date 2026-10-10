package openapimcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tree    map[string]any
		want    Version
		warn    bool
		wantErr string
	}{
		{name: "swagger 2.0", tree: map[string]any{"swagger": "2.0"}, want: Version{Raw: "2.0", Swagger2: true, Major: 2}},
		{name: "swagger 2.0 as number-ish", tree: map[string]any{"swagger": " 2.0 "}, want: Version{Raw: "2.0", Swagger2: true, Major: 2}},
		{name: "swagger 1.2", tree: map[string]any{"swagger": "1.2"}, wantErr: "unsupported swagger version"},
		{name: "openapi 3.0.3", tree: map[string]any{"openapi": "3.0.3"}, want: Version{Raw: "3.0.3", Major: 3, Minor: 0}},
		{name: "openapi 3.1.0", tree: map[string]any{"openapi": "3.1.0"}, want: Version{Raw: "3.1.0", Major: 3, Minor: 1}},
		{name: "openapi 3.2.0", tree: map[string]any{"openapi": "3.2.0"}, want: Version{Raw: "3.2.0", Major: 3, Minor: 2}},
		{name: "openapi 3.9 best effort", tree: map[string]any{"openapi": "3.9.1"}, want: Version{Raw: "3.9.1", Major: 3, Minor: 2}, warn: true},
		{name: "openapi 4.0", tree: map[string]any{"openapi": "4.0.0"}, wantErr: "unsupported openapi version"},
		{name: "malformed", tree: map[string]any{"openapi": "three"}, wantErr: "malformed openapi version"},
		{name: "missing", tree: map[string]any{"info": map[string]any{}}, wantErr: "not an OpenAPI document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, warnings, err := DetectVersion(tc.tree)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.warn, len(warnings) > 0, "warnings: %v", warnings)
		})
	}
}

func TestVersionFeatureFlags(t *testing.T) {
	v2 := Version{Swagger2: true, Major: 2}
	v30 := Version{Major: 3, Minor: 0}
	v31 := Version{Major: 3, Minor: 1}
	v32 := Version{Major: 3, Minor: 2}

	assert.False(t, v2.HasRequestBody())
	assert.True(t, v30.HasRequestBody())
	assert.True(t, v2.HasNullableKeyword())
	assert.True(t, v30.HasNullableKeyword())
	assert.False(t, v31.HasNullableKeyword())
	assert.False(t, v31.HasQueryMethod())
	assert.True(t, v32.HasQueryMethod())
	assert.True(t, v32.HasAdditionalOperations())
	assert.True(t, v32.HasQuerystringParam())
	assert.True(t, v32.HasSelf())
	assert.False(t, v30.HasSelf())
}
