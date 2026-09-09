package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OrderedMap.UnmarshalJSON accepts the null literal by design (it clears the map and
// returns no error), so the json.RawMessage branch handed back an empty *OrderedMap with
// ok == true. Callers read ok as "a schema was present" and got an empty one instead of a
// miss. Only an object-shaped value is a schema.
func TestSafeExtractOrderedMapRejectsNonObjectRawSchemas(t *testing.T) {
	t.Parallel()

	t.Run("object is accepted", func(t *testing.T) {
		t.Parallel()
		var v interface{} = json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
		om, ok := SafeExtractOrderedMap(v)
		require.True(t, ok, "a real object schema must still extract")
		require.NotNil(t, om)
		assert.Equal(t, 2, om.Len())
	})

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"null literal", `null`},
		{"null with whitespace", "  null\n"},
		{"array", `[]`},
		{"string", `"schema"`},
		{"number", `42`},
		{"bool", `true`},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			t.Parallel()
			var v interface{} = json.RawMessage(tc.raw)
			_, ok := SafeExtractOrderedMap(v)
			assert.False(t, ok, "%s is not an object-shaped schema", tc.name)
		})
	}
}
