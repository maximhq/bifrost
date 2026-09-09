// Package schemaorder provides shared fixtures and assertions for the
// response_format / JSON Schema key-order tests.
//
// OpenAI Structured Outputs generates fields in the order the schema declares
// them ("outputs will be produced in the same order as the ordering of keys in
// the schema" - https://developers.openai.com/api/docs/guides/structured-outputs),
// so a gateway that re-sorts a user's schema silently changes model behavior.
// Every provider that forwards or rewrites a JSON Schema must preserve the
// declared key order; these helpers are how each provider package asserts it.
package schemaorder

import (
	"strings"
	"testing"
)

// ChatBody is a chat/completions body whose JSON Schema is deliberately written
// in non-alphabetical order: `reasoning` before `assigned_group_id` (so the
// model explains before it decides), and schema keys as type/title/description/
// properties/required/additionalProperties.
const ChatBody = `{
  "model": "gpt-5.4-nano",
  "messages": [{"role": "user", "content": "assign it"}],
  "response_format": {
    "type": "json_schema",
    "json_schema": {
      "name": "AssignmentResult",
      "description": "Assignment result for a single activity.",
      "schema": {
        "type": "object",
        "title": "AssignmentResult",
        "description": "Assignment result for a single activity.",
        "properties": {
          "reasoning": {
            "type": "string",
            "title": "Reasoning",
            "description": "Brief explanation of the assignment decision"
          },
          "assigned_group_id": {
            "anyOf": [{"type": "integer"}, {"type": "null"}],
            "title": "Assigned Group ID",
            "description": "The group ID to assign the activity to, or null if no good match"
          }
        },
        "required": ["assigned_group_id", "reasoning"],
        "additionalProperties": false
      },
      "strict": true
    }
  }
}`

// ResponsesBody is the Responses API equivalent of ChatBody, carrying the same
// schema under text.format.
const ResponsesBody = `{
  "model": "gpt-5.4-nano",
  "input": [{"role": "user", "content": "assign it"}],
  "text": {
    "format": {
      "type": "json_schema",
      "name": "AssignmentResult",
      "schema": {
        "type": "object",
        "title": "AssignmentResult",
        "description": "Assignment result for a single activity.",
        "properties": {
          "reasoning": {
            "type": "string",
            "title": "Reasoning",
            "description": "Brief explanation of the assignment decision"
          },
          "assigned_group_id": {
            "anyOf": [{"type": "integer"}, {"type": "null"}],
            "title": "Assigned Group ID",
            "description": "The group ID to assign the activity to, or null if no good match"
          }
        },
        "required": ["assigned_group_id", "reasoning"],
        "additionalProperties": false
      },
      "strict": true
    }
  }
}`

// ByteExactSchema is a JSON Schema that no provider needs to rewrite: it has no
// union type arrays, so every normalizer is an identity transform on it. It
// therefore must reach the wire byte for byte, which pins two things a
// parse-and-re-encode gateway cannot deliver:
//
//   - 9007199254740993 exceeds float64's exact integer range and comes back as
//     ...992 once it round-trips through a Go number.
//   - 1.0 re-encodes as 1.
//
// Written compact and with deliberately non-alphabetical keys so a byte
// comparison against the outgoing payload is meaningful.
const ByteExactSchema = `{"type":"object","title":"AssignmentResult","description":"Assignment result for a single activity.","properties":{"reasoning":{"type":"string","title":"Reasoning"},"assigned_group_id":{"type":"integer","title":"Assigned Group ID","enum":[9007199254740993,7],"multipleOf":1.0}},"required":["assigned_group_id","reasoning"],"additionalProperties":false}`

// ByteExactChatBody is a chat/completions body carrying ByteExactSchema.
const ByteExactChatBody = `{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"assign it"}],"response_format":{"type":"json_schema","json_schema":{"name":"AssignmentResult","schema":` + ByteExactSchema + `,"strict":true}}}`

// AssertSchemaBytes fails the test unless the schema under key in wire is
// byte-identical to ByteExactSchema. key names the field holding the schema on
// this provider's wire.
func AssertSchemaBytes(t *testing.T, wire string, key string) {
	t.Helper()
	got := ObjectAfterKey(t, wire, key)
	if got != ByteExactSchema {
		t.Errorf("schema was rewritten on the way out\n want: %s\n  got: %s", ByteExactSchema, got)
	}
}

// PropertyOrder is the order in which ChatBody declares its schema properties.
var PropertyOrder = []string{"reasoning", "assigned_group_id"}

// SchemaKeyOrder is the order in which ChatBody declares the schema object's own keys.
var SchemaKeyOrder = []string{"type", "title", "description", "properties", "required", "additionalProperties"}

// AssertKeyOrder fails the test unless the given JSON keys appear in wire in the
// given relative order. It matches on the first occurrence of `"key"`, which is
// sufficient for these fixtures because every asserted key is unique within the
// schema blob.
func AssertKeyOrder(t *testing.T, wire string, keys ...string) {
	t.Helper()
	prev, prevKey := -1, ""
	for _, k := range keys {
		idx := strings.Index(wire, `"`+k+`"`)
		if idx == -1 {
			t.Fatalf("key %q missing from payload: %s", k, wire)
		}
		if idx <= prev {
			t.Errorf("key %q must be serialized after %q, but came before it\npayload: %s", k, prevKey, wire)
		}
		prev, prevKey = idx, k
	}
}

// AssertPropertyOrder asserts the schema properties reached the wire in declared order.
func AssertPropertyOrder(t *testing.T, wire string) {
	t.Helper()
	AssertKeyOrder(t, wire, PropertyOrder...)
}

// AssertSchemaKeyOrder asserts the schema object's own keys reached the wire in
// declared order. key names the field holding the schema on this provider's wire
// ("schema" for OpenAI-shaped payloads, "responseJsonSchema" for Gemini,
// "inputSchema"/"json" for Bedrock tools, and so on), because the same key names
// also appear at outer levels of the payload.
func AssertSchemaKeyOrder(t *testing.T, wire string, key string) {
	t.Helper()
	AssertKeyOrder(t, ObjectAfterKey(t, wire, key), SchemaKeyOrder...)
}

// ObjectAfterKey returns the balanced JSON object that follows the first
// occurrence of "key" in wire. It fails the test if no object follows.
func ObjectAfterKey(t *testing.T, wire string, key string) string {
	t.Helper()
	at := strings.Index(wire, `"`+key+`"`)
	if at == -1 {
		t.Fatalf("key %q missing from payload: %s", key, wire)
	}
	start := strings.Index(wire[at:], "{")
	if start == -1 {
		t.Fatalf("no object follows key %q in payload: %s", key, wire)
	}
	start += at

	depth, inString, escaped := 0, false, false
	for i := start; i < len(wire); i++ {
		c := wire[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// literal content, nothing to balance
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return wire[start : i+1]
			}
		}
	}
	t.Fatalf("unbalanced object after key %q in payload: %s", key, wire)
	return ""
}
