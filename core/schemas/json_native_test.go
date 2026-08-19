package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test helper types for ConvertViaJSON tests
type convertTestTarget struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type convertTestSource struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

type convertTestDest struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

func TestConvertViaJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		validate func(t *testing.T)
	}{
		{
			name:  "map_to_struct",
			input: map[string]interface{}{"type": "json_object", "name": "test_format"},
			validate: func(t *testing.T) {
				result, err := ConvertViaJSON[convertTestTarget](map[string]interface{}{"type": "json_object", "name": "test_format"})
				require.NoError(t, err)
				assert.Equal(t, "json_object", result.Type)
				assert.Equal(t, "test_format", result.Name)
			},
		},
		{
			name: "struct_to_struct",
			validate: func(t *testing.T) {
				src := convertTestSource{Name: "hello", Value: 42}
				result, err := ConvertViaJSON[convertTestDest](src)
				require.NoError(t, err)
				assert.Equal(t, "hello", result.Name)
				assert.Equal(t, 42, result.Value)
			},
		},
		{
			name: "slice_conversion",
			validate: func(t *testing.T) {
				src := []interface{}{
					map[string]interface{}{"type": "image", "name": "a"},
					map[string]interface{}{"type": "video", "name": "b"},
				}
				result, err := ConvertViaJSON[[]convertTestTarget](src)
				require.NoError(t, err)
				require.Len(t, result, 2)
				assert.Equal(t, "image", result[0].Type)
				assert.Equal(t, "a", result[0].Name)
				assert.Equal(t, "video", result[1].Type)
				assert.Equal(t, "b", result[1].Name)
			},
		},
		{
			name: "invalid_input_returns_error",
			validate: func(t *testing.T) {
				result, err := ConvertViaJSON[convertTestTarget](42)
				require.Error(t, err)
				assert.Equal(t, convertTestTarget{}, result)
			},
		},
		{
			name: "nil_input",
			validate: func(t *testing.T) {
				result, err := ConvertViaJSON[convertTestTarget](nil)
				// nil marshals to "null" which unmarshals to zero struct — no error
				require.NoError(t, err)
				assert.Equal(t, convertTestTarget{}, result)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.validate(t)
		})
	}
}
