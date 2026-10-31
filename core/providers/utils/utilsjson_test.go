package utils

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSetJSONField(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		path     string
		value    interface{}
		expected string
	}{
		{
			name:     "set_string_on_empty_object",
			data:     []byte(`{}`),
			path:     "name",
			value:    "test",
			expected: `{"name":"test"}`,
		},
		{
			name:     "set_nested_path",
			data:     []byte(`{}`),
			path:     "file.displayName",
			value:    "photo.jpg",
			expected: `{"file":{"displayName":"photo.jpg"}}`,
		},
		{
			name:     "set_boolean",
			data:     []byte(`{"model":"x"}`),
			path:     "stream",
			value:    true,
			expected: `{"model":"x","stream":true}`,
		},
		{
			name:     "set_string_array",
			data:     []byte(`{}`),
			path:     "betas",
			value:    []string{"a", "b"},
			expected: `{"betas":["a","b"]}`,
		},
		{
			name:     "preserves_existing_fields",
			data:     []byte(`{"a":1,"b":2}`),
			path:     "c",
			value:    3,
			expected: `{"a":1,"b":2,"c":3}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := SetJSONField(tt.data, tt.path, tt.value)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, string(result), "exact byte-level ordering must match")
		})
	}
}

// TestSetJSONStringFieldInPlace verifies owned-buffer reuse without sacrificing JSON string encoding or allocation fallback.
func TestSetJSONStringFieldInPlace(t *testing.T) {
	t.Run("shorter replacement reuses owned bytes", func(t *testing.T) {
		data := []byte(`{"message":"alice@example.com","metadata":"keep"}`)
		result, err := SetJSONStringFieldInPlace(data, "message", "[EMAIL]")

		require.NoError(t, err)
		require.NotEmpty(t, result)
		assert.Equal(t, "[EMAIL]", gjson.GetBytes(result, "message").String())
		assert.Equal(t, "keep", gjson.GetBytes(result, "metadata").String())
		assert.True(t, &data[0] == &result[0], "a shorter replacement should reuse the owned backing array")
	})

	t.Run("escaped and unicode replacement remains valid JSON", func(t *testing.T) {
		data := []byte(`{"message":"a sufficiently long original string","metadata":"keep"}`)
		replacement := `["EMAIL"] \\ 日本`
		result, err := SetJSONStringFieldInPlace(data, "message", replacement)

		require.NoError(t, err)
		assert.True(t, gjson.ValidBytes(result))
		assert.Equal(t, replacement, gjson.GetBytes(result, "message").String())
		assert.Equal(t, "keep", gjson.GetBytes(result, "metadata").String())
	})

	t.Run("longer replacement allocates without mutating input", func(t *testing.T) {
		data := []byte(`{"message":"x","metadata":"keep"}`)
		original := bytes.Clone(data)
		result, err := SetJSONStringFieldInPlace(data, "message", "a replacement longer than the original")

		require.NoError(t, err)
		require.NotEmpty(t, result)
		assert.Equal(t, "a replacement longer than the original", gjson.GetBytes(result, "message").String())
		assert.Equal(t, original, data, "allocation fallback must not partially mutate the input")
		assert.False(t, &data[0] == &result[0], "a growing replacement cannot reuse the original backing array")
	})
}

func TestDeleteJSONField(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		path     string
		expected string
		wantErr  bool
	}{
		{
			name:     "delete_existing_field",
			data:     []byte(`{"a":1,"b":2}`),
			path:     "a",
			expected: `{"b":2}`,
		},
		{
			name:     "delete_nonexistent_field",
			data:     []byte(`{"a":1}`),
			path:     "b",
			expected: `{"a":1}`,
		},
		{
			name:     "sequential_deletes",
			data:     []byte(`{"a":1,"b":2,"c":3}`),
			path:     "", // handled in validate
			expected: `{}`,
		},
		{
			name:     "preserves_remaining_order",
			data:     []byte(`{"x":1,"y":2,"z":3}`),
			path:     "y",
			expected: `{"x":1,"z":3}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "sequential_deletes" {
				data := []byte(`{"a":1,"b":2,"c":3}`)
				var err error
				data, err = DeleteJSONField(data, "a")
				require.NoError(t, err)
				data, err = DeleteJSONField(data, "b")
				require.NoError(t, err)
				data, err = DeleteJSONField(data, "c")
				require.NoError(t, err)
				assert.Equal(t, tt.expected, string(data), "exact byte-level ordering must match")
				return
			}

			result, err := DeleteJSONField(tt.data, tt.path)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, string(result), "exact byte-level ordering must match")
		})
	}
}
