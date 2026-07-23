package logging

import (
	"errors"
	"math"
	"testing"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingMarshaler always errors from MarshalJSON, simulating a payload type
// with broken custom serialization.
type failingMarshaler struct{}

func (failingMarshaler) MarshalJSON() ([]byte, error) {
	return nil, errors.New("boom")
}

type cyclicNode struct {
	Name string      `json:"name"`
	Next *cyclicNode `json:"next,omitempty"`
}

func TestStripUnserializablePayloadsNilAndClean(t *testing.T) {
	// Nil must not panic.
	stripUnserializablePayloads(nil)

	// Clean values must be left untouched.
	clean := map[string]interface{}{"a": 1, "b": []string{"x"}}
	stripUnserializablePayloads(clean)
	assert.Equal(t, 1, clean["a"])
	assert.Equal(t, []string{"x"}, clean["b"])
}

func TestStripUnserializablePayloadsMapLeaf(t *testing.T) {
	m := map[string]interface{}{
		"good": "keep-me",
		"bad":  make(chan int),
		"nested": map[string]interface{}{
			"fn":   func() {},
			"kept": 42,
		},
	}
	stripUnserializablePayloads(m)

	assert.Equal(t, "keep-me", m["good"])
	assert.Nil(t, m["bad"])
	nested, ok := m["nested"].(map[string]interface{})
	require.True(t, ok)
	assert.Nil(t, nested["fn"])
	assert.Equal(t, 42, nested["kept"])
	assert.True(t, marshals(m))
}

func TestStripUnserializablePayloadsSliceElement(t *testing.T) {
	s := []interface{}{"first", make(chan int), "third"}
	stripUnserializablePayloads(s)

	assert.Equal(t, "first", s[0])
	assert.Nil(t, s[1])
	assert.Equal(t, "third", s[2])
	assert.True(t, marshals(s))
}

func TestStripUnserializablePayloadsNaNAndInf(t *testing.T) {
	m := map[string]interface{}{
		"nan":  math.NaN(),
		"inf":  math.Inf(1),
		"cost": 1.25,
	}
	if marshals(m) {
		t.Skip("sonic accepts NaN/Inf in this configuration; nothing to strip")
	}
	stripUnserializablePayloads(m)

	assert.Equal(t, float64(0), m["nan"])
	assert.Equal(t, float64(0), m["inf"])
	assert.Equal(t, 1.25, m["cost"])
	assert.True(t, marshals(m))
}

func TestStripUnserializablePayloadsFailingMarshaler(t *testing.T) {
	m := map[string]interface{}{
		"broken": failingMarshaler{},
		"kept":   "still-here",
	}
	stripUnserializablePayloads(m)

	assert.Nil(t, m["broken"])
	assert.Equal(t, "still-here", m["kept"])
	assert.True(t, marshals(m))
}

func TestStripUnserializablePayloadsCycle(t *testing.T) {
	a := &cyclicNode{Name: "a"}
	b := &cyclicNode{Name: "b", Next: a}
	a.Next = b
	if marshals(a) {
		t.Skip("sonic tolerates reference cycles in this configuration")
	}
	stripUnserializablePayloads(a)

	assert.True(t, marshals(a))
	assert.Equal(t, "a", a.Name)
}

func TestStripUnserializablePayloadsStructField(t *testing.T) {
	type payload struct {
		Kept   string                 `json:"kept"`
		Params map[string]interface{} `json:"params"`
	}
	p := &payload{
		Kept:   "scalar",
		Params: map[string]interface{}{"ch": make(chan int), "ok": true},
	}
	stripUnserializablePayloads(p)

	assert.Equal(t, "scalar", p.Kept)
	assert.Nil(t, p.Params["ch"])
	assert.Equal(t, true, p.Params["ok"])
	assert.True(t, marshals(p))
}

func TestStripUnserializablePayloadsLogEntry(t *testing.T) {
	entry := &logstore.Log{
		ID:     "log-1",
		Model:  "gpt-4o",
		Status: "success",
		ParamsParsed: map[string]interface{}{
			"temperature": 0.7,
			"broken":      make(chan int),
		},
	}
	stripUnserializablePayloads(entry)

	// Scalar columns untouched.
	assert.Equal(t, "log-1", entry.ID)
	assert.Equal(t, "gpt-4o", entry.Model)
	assert.Equal(t, "success", entry.Status)
	// Only the broken nested value is cleared; the rest of params survives.
	params, ok := entry.ParamsParsed.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 0.7, params["temperature"])
	assert.Nil(t, params["broken"])
	assert.True(t, marshals(entry))
}
