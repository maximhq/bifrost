package schemas

import (
	"encoding/json"
	"testing"
)

func TestOptionalJSONDistinguishesOmittedNullAndValue(t *testing.T) {
	var omitted struct {
		Name OptionalJSON[string] `json:"name,omitempty"`
	}
	if err := json.Unmarshal([]byte(`{}`), &omitted); err != nil {
		t.Fatalf("unmarshal omitted: %v", err)
	}
	if omitted.Name.Set || omitted.Name.Null || omitted.Name.Value != "" {
		t.Fatalf("omitted field = %#v, want unset zero value", omitted.Name)
	}

	var nullValue struct {
		Name OptionalJSON[string] `json:"name,omitempty"`
	}
	if err := json.Unmarshal([]byte(`{"name": null}`), &nullValue); err != nil {
		t.Fatalf("unmarshal null: %v", err)
	}
	if !nullValue.Name.Set || !nullValue.Name.Null || nullValue.Name.Value != "" {
		t.Fatalf("null field = %#v, want set null", nullValue.Name)
	}

	var concrete struct {
		Name OptionalJSON[string] `json:"name,omitempty"`
	}
	if err := json.Unmarshal([]byte(`{"name": "alice"}`), &concrete); err != nil {
		t.Fatalf("unmarshal value: %v", err)
	}
	if !concrete.Name.Set || concrete.Name.Null || concrete.Name.Value != "alice" {
		t.Fatalf("value field = %#v, want set value alice", concrete.Name)
	}
}

func TestOptionalJSONMarshalDoesNotLeakInternalState(t *testing.T) {
	unset, err := json.Marshal(OptionalJSON[string]{})
	if err != nil {
		t.Fatalf("marshal unset: %v", err)
	}
	if string(unset) != "null" {
		t.Fatalf("unset marshaled as %s, want null", unset)
	}

	nullValue, err := json.Marshal(OptionalJSON[string]{Set: true, Null: true})
	if err != nil {
		t.Fatalf("marshal null: %v", err)
	}
	if string(nullValue) != "null" {
		t.Fatalf("null marshaled as %s, want null", nullValue)
	}

	value, err := json.Marshal(OptionalJSON[string]{Set: true, Value: "alice"})
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	if string(value) != `"alice"` {
		t.Fatalf("value marshaled as %s, want alice string", value)
	}
}
