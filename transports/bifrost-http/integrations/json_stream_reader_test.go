package integrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const defaultBufSize = 64 * 1024

func scan(json string, keys ...string) map[string][]byte {
	jr := newJSONStreamReader(strings.NewReader(json), defaultBufSize)
	return jr.scanTopLevelKeys(keys)
}

// ============================================================================
// Normal cases
// ============================================================================

func TestJSR_Normal_SingleKeyFound(t *testing.T) {
	r := scan(`{"foo": "bar", "baz": 123}`, "foo")
	assert.Equal(t, `"bar"`, string(r["foo"]))
}

func TestJSR_Normal_MultipleKeysFound(t *testing.T) {
	r := scan(`{"a": 1, "b": "two", "c": [3]}`, "a", "c")
	assert.Equal(t, "1", string(r["a"]))
	assert.Equal(t, "[3]", string(r["c"]))
}

func TestJSR_Normal_AllJSONValueTypes(t *testing.T) {
	json := `{
		"str": "hello",
		"num_int": 42,
		"num_float": 3.14,
		"num_neg": -7,
		"num_sci": 1.5e+10,
		"bool_t": true,
		"bool_f": false,
		"null_v": null,
		"obj": {"nested": "value"},
		"arr": [1, "two", null, true]
	}`
	r := scan(json, "str", "num_int", "num_float", "num_neg", "num_sci",
		"bool_t", "bool_f", "null_v", "obj", "arr")

	assert.Equal(t, `"hello"`, string(r["str"]))
	assert.Equal(t, "42", string(r["num_int"]))
	assert.Equal(t, "3.14", string(r["num_float"]))
	assert.Equal(t, "-7", string(r["num_neg"]))
	assert.Equal(t, "1.5e+10", string(r["num_sci"]))
	assert.Equal(t, "true", string(r["bool_t"]))
	assert.Equal(t, "false", string(r["bool_f"]))
	assert.Equal(t, "null", string(r["null_v"]))
	assert.Equal(t, `{"nested": "value"}`, string(r["obj"]))
	assert.Equal(t, `[1, "two", null, true]`, string(r["arr"]))
}

func TestJSR_Normal_KeyNotPresent(t *testing.T) {
	r := scan(`{"foo": "bar"}`, "missing")
	assert.Empty(t, r)
}

func TestJSR_Normal_EmptyObject(t *testing.T) {
	r := scan(`{}`, "anything")
	assert.Empty(t, r)
}

func TestJSR_Normal_EmptyKeysRequest(t *testing.T) {
	// Requesting zero keys — should return immediately with empty map
	r := scan(`{"a": 1, "b": 2}`)
	assert.Empty(t, r)
}

func TestJSR_Normal_EarlyExitOnAllFound(t *testing.T) {
	// "target" is the first key; the second key's value is malformed,
	// but we should never reach it because we exit early after finding "target"
	r := scan(`{"target": 42, "never_reached": BROKEN}`, "target")
	assert.Equal(t, "42", string(r["target"]))
}

func TestJSR_Normal_SomeKeysMissing(t *testing.T) {
	r := scan(`{"a": 1, "b": 2}`, "a", "missing")
	assert.Equal(t, "1", string(r["a"]))
	_, ok := r["missing"]
	assert.False(t, ok)
}

// ============================================================================
// Whitespace torture
// ============================================================================

func TestJSR_Whitespace_ExcessiveEverywhere(t *testing.T) {
	json := "  \t\n\r{  \n\r\t  \"key\"  \t\n  :  \r\n  \"value\"  \n\t  }  "
	r := scan(json, "key")
	assert.Equal(t, `"value"`, string(r["key"]))
}

func TestJSR_Whitespace_TabsOnly(t *testing.T) {
	r := scan("\t{\t\"a\"\t:\t1\t,\t\"b\"\t:\t2\t}\t", "a", "b")
	assert.Equal(t, "1", string(r["a"]))
	assert.Equal(t, "2", string(r["b"]))
}

func TestJSR_Whitespace_CRLFBetweenTokens(t *testing.T) {
	json := "{\r\n\"x\"\r\n:\r\n\"y\"\r\n}"
	r := scan(json, "x")
	assert.Equal(t, `"y"`, string(r["x"]))
}

func TestJSR_Whitespace_NoWhitespaceAtAll(t *testing.T) {
	r := scan(`{"a":1,"b":"c","d":true}`, "a", "b", "d")
	assert.Equal(t, "1", string(r["a"]))
	assert.Equal(t, `"c"`, string(r["b"]))
	assert.Equal(t, "true", string(r["d"]))
}

// ============================================================================
// Escape sequences in keys and values
// ============================================================================

func TestJSR_Escape_QuotesInStringValue(t *testing.T) {
	json := `{"msg": "he said \"hello\" to her"}`
	r := scan(json, "msg")
	assert.Equal(t, `"he said \"hello\" to her"`, string(r["msg"]))
}

func TestJSR_Escape_BackslashAtEndOfString(t *testing.T) {
	// Value is a string ending in a literal backslash: path\
	// JSON encoding: "path\\" — the scanner must not treat the second \ as escaping the "
	json := `{"path": "C:\\Users\\test\\"}`
	r := scan(json, "path")
	assert.Equal(t, `"C:\\Users\\test\\"`, string(r["path"]))
}

func TestJSR_Escape_DoubleBackslashThenQuote(t *testing.T) {
	// "\\\""  = escaped backslash + escaped quote → literal: \"
	json := `{"x": "\\\""}`
	r := scan(json, "x")
	assert.Equal(t, `"\\\""`, string(r["x"]))
}

func TestJSR_Escape_UnicodeEscapeInValue(t *testing.T) {
	json := `{"emoji": "\u0048\u0065\u006C\u006C\u006F"}`
	r := scan(json, "emoji")
	assert.Equal(t, `"\u0048\u0065\u006C\u006C\u006F"`, string(r["emoji"]))
}

func TestJSR_Escape_EscapedForwardSlash(t *testing.T) {
	json := `{"url": "http:\/\/example.com"}`
	r := scan(json, "url")
	assert.Equal(t, `"http:\/\/example.com"`, string(r["url"]))
}

func TestJSR_Escape_AllEscapeTypes(t *testing.T) {
	// \n \r \t \b \f \/ \\ \"
	json := `{"esc": "\n\r\t\b\f\/\\\""}`
	r := scan(json, "esc")
	assert.Equal(t, `"\n\r\t\b\f\/\\\""`, string(r["esc"]))
}

func TestJSR_Escape_EscapedQuoteInKey(t *testing.T) {
	// JSON key is say\"what — readKeyString preserves the backslash,
	// so the returned key is the raw JSON content: say\"what
	json := `{"say\"what": "found"}`
	r := scan(json, `say\"what`)
	assert.Equal(t, `"found"`, string(r[`say\"what`]))
}

func TestJSR_Escape_BackslashInKey(t *testing.T) {
	// JSON key "path\to" contains escape sequence \t.
	// readKeyString preserves both the backslash and the escaped char,
	// so the key is the raw JSON content: path\to (7 bytes).
	json := `{"path\to": 99}`
	r := scan(json, `path\to`)
	assert.Equal(t, "99", string(r[`path\to`]))
}

// ============================================================================
// Strings containing JSON-like content (must NOT confuse the scanner)
// ============================================================================

func TestJSR_StringsWithBrackets(t *testing.T) {
	json := `{"trap": "{\"not\": [\"parsed\"]}", "target": 42}`
	r := scan(json, "target")
	assert.Equal(t, "42", string(r["target"]))
}

func TestJSR_StringWithNestedBraces(t *testing.T) {
	json := `{"decoy": "[{()}][]}{{{", "real": true}`
	r := scan(json, "real")
	assert.Equal(t, "true", string(r["real"]))
}

func TestJSR_StringWithColonsAndCommas(t *testing.T) {
	json := `{"csv": "a,b,c:d:e", "x": 1}`
	r := scan(json, "x")
	assert.Equal(t, "1", string(r["x"]))
}

// ============================================================================
// Deeply nested structures
// ============================================================================

func TestJSR_DeepNesting_10Levels(t *testing.T) {
	// Skipping a 10-level deep object then finding the target
	deep := `{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":{"j":"deep"}}}}}}}}}}`
	json := `{"nested": ` + deep + `, "target": "found"}`
	r := scan(json, "target")
	assert.Equal(t, `"found"`, string(r["target"]))
}

func TestJSR_DeepNesting_CaptureDeepObject(t *testing.T) {
	deep := `{"a":{"b":{"c":1}}}`
	json := `{"deep": ` + deep + `}`
	r := scan(json, "deep")
	assert.Equal(t, deep, string(r["deep"]))
}

func TestJSR_DeepNesting_ArrayOfArrays(t *testing.T) {
	json := `{"skip": [[[[[1,2],[3,4]],[[5,6]]]]],  "want": "yes"}`
	r := scan(json, "want")
	assert.Equal(t, `"yes"`, string(r["want"]))
}

func TestJSR_DeepNesting_MixedObjectsAndArrays(t *testing.T) {
	json := `{"skip": [{"a":[{"b":[{"c":"d"}]}]}], "want": false}`
	r := scan(json, "want")
	assert.Equal(t, "false", string(r["want"]))
}

// ============================================================================
// Number edge cases
// ============================================================================

func TestJSR_Number_Zero(t *testing.T) {
	r := scan(`{"n": 0}`, "n")
	assert.Equal(t, "0", string(r["n"]))
}

func TestJSR_Number_NegativeZero(t *testing.T) {
	r := scan(`{"n": -0}`, "n")
	assert.Equal(t, "-0", string(r["n"]))
}

func TestJSR_Number_LargeInteger(t *testing.T) {
	r := scan(`{"n": 99999999999999999999}`, "n")
	assert.Equal(t, "99999999999999999999", string(r["n"]))
}

func TestJSR_Number_ScientificNotation(t *testing.T) {
	tests := map[string]string{
		`{"n": 1e10}`:    "1e10",
		`{"n": 1E10}`:    "1E10",
		`{"n": 1.5e+3}`:  "1.5e+3",
		`{"n": 2.7e-4}`:  "2.7e-4",
		`{"n": -3.14E2}`: "-3.14E2",
	}
	for json, want := range tests {
		r := scan(json, "n")
		assert.Equal(t, want, string(r["n"]), "input: %s", json)
	}
}

func TestJSR_Number_JustMinus(t *testing.T) {
	// "-" alone is not valid JSON, but we capture what we see before a non-number char
	r := scan(`{"n": -}`, "n")
	// The scanner reads '-', then '}' which is not a number char,
	// so it unreads '}' and returns "-". This is a partial/malformed value.
	assert.Equal(t, "-", string(r["n"]))
}

// ============================================================================
// Empty and minimal values
// ============================================================================

func TestJSR_EmptyStringValue(t *testing.T) {
	r := scan(`{"k": ""}`, "k")
	assert.Equal(t, `""`, string(r["k"]))
}

func TestJSR_EmptyObjectValue(t *testing.T) {
	r := scan(`{"k": {}}`, "k")
	assert.Equal(t, `{}`, string(r["k"]))
}

func TestJSR_EmptyArrayValue(t *testing.T) {
	r := scan(`{"k": []}`, "k")
	assert.Equal(t, `[]`, string(r["k"]))
}

func TestJSR_SingleCharKey(t *testing.T) {
	r := scan(`{"x": 1}`, "x")
	assert.Equal(t, "1", string(r["x"]))
}

func TestJSR_NumericLookingKey(t *testing.T) {
	r := scan(`{"123": "numbers"}`, "123")
	assert.Equal(t, `"numbers"`, string(r["123"]))
}

func TestJSR_EmptyStringKey(t *testing.T) {
	r := scan(`{"": "empty key"}`, "")
	assert.Equal(t, `"empty key"`, string(r[""]))
}

// ============================================================================
// Key ordering and position
// ============================================================================

func TestJSR_KeyAtVeryEnd(t *testing.T) {
	parts := make([]string, 50)
	for i := range parts {
		parts[i] = `"padding_` + strings.Repeat("x", 100) + `": "skip"`
	}
	json := `{` + strings.Join(parts, ",") + `, "target": "end"}`
	r := scan(json, "target")
	assert.Equal(t, `"end"`, string(r["target"]))
}

func TestJSR_KeyAtVeryStart(t *testing.T) {
	parts := make([]string, 50)
	for i := range parts {
		parts[i] = `"padding_` + strings.Repeat("x", 100) + `": "skip"`
	}
	json := `{"target": "start", ` + strings.Join(parts, ",") + `}`
	r := scan(json, "target")
	assert.Equal(t, `"start"`, string(r["target"]))
}

func TestJSR_DuplicateKey_FirstWins(t *testing.T) {
	json := `{"dup": "first", "other": 0, "dup": "second"}`
	r := scan(json, "dup")
	// After finding "dup" the first time, early exit triggers since all keys found
	assert.Equal(t, `"first"`, string(r["dup"]))
}

func TestJSR_SimilarKeyNames(t *testing.T) {
	// Must not confuse "generationConfi" with "generationConfig"
	json := `{"generationConfi": "nope", "generationConfig": "yes", "generationConfigX": "also nope"}`
	r := scan(json, "generationConfig")
	assert.Equal(t, `"yes"`, string(r["generationConfig"]))
}

func TestJSR_SubstringKeyNames(t *testing.T) {
	json := `{"ab": 1, "abc": 2, "abcd": 3, "a": 0}`
	r := scan(json, "abc")
	assert.Equal(t, "2", string(r["abc"]))
}

// ============================================================================
// Inner keys with same name as outer target (must NOT capture)
// ============================================================================

func TestJSR_InnerKeyNotCaptured(t *testing.T) {
	json := `{"wrapper": {"target": "inner"}, "target": "outer"}`
	r := scan(json, "target")
	// The scanner must skip the entire "wrapper" object and find "target" at the top level
	assert.Equal(t, `"outer"`, string(r["target"]))
}

func TestJSR_InnerKeyInArray(t *testing.T) {
	json := `{"list": [{"target": "inner1"}, {"target": "inner2"}], "target": "top"}`
	r := scan(json, "target")
	assert.Equal(t, `"top"`, string(r["target"]))
}

// ============================================================================
// Large payloads and buffer boundary tests
// ============================================================================

func TestJSR_LargeStringValue_Skip(t *testing.T) {
	// 200KB string that must be skipped
	bigStr := strings.Repeat("ABCDEFGH", 25000) // 200KB
	json := `{"big": "` + bigStr + `", "after": 1}`
	r := scan(json, "after")
	assert.Equal(t, "1", string(r["after"]))
}

func TestJSR_LargeStringValue_Capture(t *testing.T) {
	// Small capture after skipping a big value
	json := `{"big": "` + strings.Repeat("x", 200000) + `", "small": {"a":1}}`
	r := scan(json, "small")
	assert.Equal(t, `{"a":1}`, string(r["small"]))
}

func TestJSR_BufferBoundary_KeySpansBuffer(t *testing.T) {
	// Use a tiny 32-byte buffer so the key name straddles the boundary
	json := `{"thisIsAVeryLongKeyName": "value"}`
	jr := newJSONStreamReader(strings.NewReader(json), 32)
	r := jr.scanTopLevelKeys([]string{"thisIsAVeryLongKeyName"})
	assert.Equal(t, `"value"`, string(r["thisIsAVeryLongKeyName"]))
}

func TestJSR_BufferBoundary_ValueSpansBuffer(t *testing.T) {
	// Use a 32-byte buffer; the value straddles the boundary
	json := `{"k":"` + strings.Repeat("a", 100) + `"}`
	jr := newJSONStreamReader(strings.NewReader(json), 32)
	r := jr.scanTopLevelKeys([]string{"k"})
	assert.Equal(t, `"`+strings.Repeat("a", 100)+`"`, string(r["k"]))
}

func TestJSR_BufferBoundary_TinyBuffer_MultipleKeys(t *testing.T) {
	json := `{"first": [1,2,3], "second": {"nested": true}, "third": "end"}`
	jr := newJSONStreamReader(strings.NewReader(json), 16)
	r := jr.scanTopLevelKeys([]string{"first", "third"})
	assert.Equal(t, "[1,2,3]", string(r["first"]))
	assert.Equal(t, `"end"`, string(r["third"]))
}

func TestJSR_LargeNestedSkip(t *testing.T) {
	// A massive array of objects to skip
	parts := make([]string, 1000)
	for i := range parts {
		parts[i] = `{"id":` + strings.Repeat("9", 10) + `,"data":"` + strings.Repeat("x", 200) + `"}`
	}
	bigArray := "[" + strings.Join(parts, ",") + "]"
	json := `{"items": ` + bigArray + `, "meta": "ok"}`
	r := scan(json, "meta")
	assert.Equal(t, `"ok"`, string(r["meta"]))
}

// ============================================================================
// Weird one-offs that SHOULD parse
// ============================================================================

func TestJSR_Weird_ValueIsJustTrue(t *testing.T) {
	r := scan(`{"ok": true}`, "ok")
	assert.Equal(t, "true", string(r["ok"]))
}

func TestJSR_Weird_ValueIsJustFalse(t *testing.T) {
	r := scan(`{"ok": false}`, "ok")
	assert.Equal(t, "false", string(r["ok"]))
}

func TestJSR_Weird_ValueIsJustNull(t *testing.T) {
	r := scan(`{"ok": null}`, "ok")
	assert.Equal(t, "null", string(r["ok"]))
}

func TestJSR_Weird_ReallyLongKey(t *testing.T) {
	longKey := strings.Repeat("k", 10000)
	json := `{"` + longKey + `": "found"}`
	r := scan(json, longKey)
	assert.Equal(t, `"found"`, string(r[longKey]))
}

func TestJSR_Weird_UnicodeKey(t *testing.T) {
	json := `{"\u0066\u006f\u006f": "bar"}`
	// readKeyString preserves backslashes, so the key is the raw JSON
	// content: \u0066\u006f\u006f (with literal backslashes)
	r := scan(json, `\u0066\u006f\u006f`)
	assert.Equal(t, `"bar"`, string(r[`\u0066\u006f\u006f`]))
}

func TestJSR_Weird_OnlyWhitespaceAround(t *testing.T) {
	json := "\n\n\n   { \n  \"a\" \n : \n  1 \n  } \n\n"
	r := scan(json, "a")
	assert.Equal(t, "1", string(r["a"]))
}

func TestJSR_Weird_ManyKeys_100(t *testing.T) {
	parts := make([]string, 100)
	for i := range parts {
		parts[i] = `"key_` + strings.Repeat("0", 5) + `_` + string(rune('A'+i%26)) + `": ` + strings.Repeat("1", i+1)
	}
	json := `{` + strings.Join(parts, ", ") + `}`
	r := scan(json, "key_00000_Z")
	require.Contains(t, r, "key_00000_Z")
}

func TestJSR_Weird_StringContainingNull(t *testing.T) {
	// Literal characters n,u,l,l inside a string value
	json := `{"val": "null", "after": 1}`
	r := scan(json, "val", "after")
	assert.Equal(t, `"null"`, string(r["val"]))
	assert.Equal(t, "1", string(r["after"]))
}

func TestJSR_Weird_StringContainingTrue(t *testing.T) {
	json := `{"val": "true", "x": 2}`
	r := scan(json, "val")
	assert.Equal(t, `"true"`, string(r["val"]))
}

func TestJSR_Weird_StringContainingNumber(t *testing.T) {
	json := `{"val": "12345", "x": 2}`
	r := scan(json, "val")
	assert.Equal(t, `"12345"`, string(r["val"]))
}

func TestJSR_Weird_NestedEmptyContainers(t *testing.T) {
	json := `{"a": [[],[],[]], "b": {"x":{},"y":{}}, "target": 9}`
	r := scan(json, "target")
	assert.Equal(t, "9", string(r["target"]))
}

func TestJSR_Weird_ConsecutiveEscapes(t *testing.T) {
	// Eight backslashes in JSON = four literal backslashes
	json := `{"k": "\\\\\\\\"}`
	r := scan(json, "k")
	assert.Equal(t, `"\\\\\\\\"`, string(r["k"]))
}

func TestJSR_Weird_ValueIsSingleDigit(t *testing.T) {
	r := scan(`{"n": 0, "m": 9}`, "n", "m")
	assert.Equal(t, "0", string(r["n"]))
	assert.Equal(t, "9", string(r["m"]))
}

func TestJSR_Weird_NegativeFloat(t *testing.T) {
	r := scan(`{"n": -0.001}`, "n")
	assert.Equal(t, "-0.001", string(r["n"]))
}

func TestJSR_Weird_ArrayWithMixedTypes(t *testing.T) {
	json := `{"mix": [1, "two", true, null, {"five": 5}, [6]]}`
	r := scan(json, "mix")
	assert.Equal(t, `[1, "two", true, null, {"five": 5}, [6]]`, string(r["mix"]))
}

func TestJSR_Weird_KeyIsReservedWord(t *testing.T) {
	json := `{"true": 1, "false": 2, "null": 3}`
	r := scan(json, "true", "false", "null")
	assert.Equal(t, "1", string(r["true"]))
	assert.Equal(t, "2", string(r["false"]))
	assert.Equal(t, "3", string(r["null"]))
}

func TestJSR_Weird_SkipBoolThenCapture(t *testing.T) {
	json := `{"a": true, "b": false, "c": null, "target": "yes"}`
	r := scan(json, "target")
	assert.Equal(t, `"yes"`, string(r["target"]))
}

func TestJSR_Weird_NumberAsLastValue_NoClosingBrace(t *testing.T) {
	// Truncated — no closing brace, but key found before truncation
	// Scanner finds "a", captures 1, then tries to continue and hits EOF
	r := scan(`{"a": 1, "b": 2`, "a")
	assert.Equal(t, "1", string(r["a"]))
}

// ============================================================================
// Failing / invalid inputs that should return nil or empty
// ============================================================================

func TestJSR_Fail_EmptyInput(t *testing.T) {
	r := scan("", "key")
	assert.Nil(t, r)
}

func TestJSR_Fail_JustWhitespace(t *testing.T) {
	r := scan("   \t\n\r  ", "key")
	assert.Nil(t, r)
}

func TestJSR_Fail_RootIsString(t *testing.T) {
	r := scan(`"hello"`, "key")
	assert.Nil(t, r)
}

func TestJSR_Fail_RootIsNumber(t *testing.T) {
	r := scan(`42`, "key")
	assert.Nil(t, r)
}

func TestJSR_Fail_RootIsTrue(t *testing.T) {
	r := scan(`true`, "key")
	assert.Nil(t, r)
}

func TestJSR_Fail_RootIsNull(t *testing.T) {
	r := scan(`null`, "key")
	assert.Nil(t, r)
}

func TestJSR_Fail_RootIsArray(t *testing.T) {
	r := scan(`[1, 2, 3]`, "key")
	assert.Nil(t, r)
}

func TestJSR_Fail_TruncatedOpenBrace(t *testing.T) {
	r := scan(`{`, "key")
	assert.Empty(t, r)
}

func TestJSR_Fail_TruncatedAfterKey(t *testing.T) {
	r := scan(`{"key"`, "key")
	assert.Empty(t, r)
}

func TestJSR_Fail_TruncatedAfterColon(t *testing.T) {
	r := scan(`{"key":`, "key")
	assert.Empty(t, r)
}

func TestJSR_Fail_TruncatedStringValue(t *testing.T) {
	r := scan(`{"key": "val`, "key")
	assert.Empty(t, r)
}

func TestJSR_Fail_TruncatedObjectValue(t *testing.T) {
	r := scan(`{"key": {"inner": `, "key")
	assert.Empty(t, r)
}

func TestJSR_Fail_TruncatedArrayValue(t *testing.T) {
	r := scan(`{"key": [1, 2`, "key")
	assert.Empty(t, r)
}

func TestJSR_Fail_TruncatedTrue(t *testing.T) {
	r := scan(`{"key": tr`, "key")
	assert.Empty(t, r)
}

func TestJSR_Fail_TruncatedFalse(t *testing.T) {
	r := scan(`{"key": fal`, "key")
	assert.Empty(t, r)
}

func TestJSR_Fail_TruncatedNull(t *testing.T) {
	r := scan(`{"key": nu`, "key")
	assert.Empty(t, r)
}

func TestJSR_Fail_MissingColon(t *testing.T) {
	r := scan(`{"key" "value"}`, "key")
	// After reading key, expect(':') sees '"' — returns error
	assert.Empty(t, r)
}

func TestJSR_Fail_MissingComma(t *testing.T) {
	r := scan(`{"a": 1 "b": 2}`, "b")
	// After parsing "a":1, peekNonWhitespace sees '"' which is not ',' or '}'
	// The scanner returns the partial result (empty since "b" not found yet)
	_, ok := r["b"]
	assert.False(t, ok)
}

func TestJSR_Fail_TrailingComma(t *testing.T) {
	r := scan(`{"a": 1,}`, "a")
	// "a" is found and captured. Then scanner sees '}' after comma — but
	// since "a" was the only requested key and early exit triggered, it works
	assert.Equal(t, "1", string(r["a"]))
}

func TestJSR_Fail_TrailingComma_KeyNotFound(t *testing.T) {
	r := scan(`{"a": 1,}`, "b")
	// Scanner finds "a", skips it, sees comma, then expects '"' but gets '}'
	assert.Empty(t, r)
}

func TestJSR_Fail_DoubleComma(t *testing.T) {
	r := scan(`{"a": 1,, "b": 2}`, "b")
	// After "a":1, first comma consumed, then peekNonWhitespace sees ','
	// which is not '"' for expect('"'), so it returns error
	_, ok := r["b"]
	assert.False(t, ok)
}

func TestJSR_Fail_UnquotedKey(t *testing.T) {
	r := scan(`{key: "value"}`, "key")
	// expect('"') gets 'k' — error
	assert.Empty(t, r)
}

func TestJSR_Fail_SingleQuotedKey(t *testing.T) {
	r := scan(`{'key': "value"}`, "key")
	// expect('"') gets '\'' — error
	assert.Empty(t, r)
}

func TestJSR_Fail_SingleQuotedValue(t *testing.T) {
	r := scan(`{"key": 'value'}`, "key")
	// captureValue sees '\'' — unexpected byte error
	assert.Empty(t, r)
}

func TestJSR_Fail_InvalidValueByte(t *testing.T) {
	r := scan(`{"key": @invalid}`, "key")
	assert.Empty(t, r)
}

func TestJSR_Fail_BinaryGarbage(t *testing.T) {
	r := scan("\x00\x01\x02\x03", "key")
	assert.Nil(t, r)
}

func TestJSR_Fail_JustCloseBrace(t *testing.T) {
	r := scan(`}`, "key")
	assert.Nil(t, r)
}

func TestJSR_Fail_MismatchedBrackets_ArrayCloseInObject(t *testing.T) {
	// The skip logic doesn't validate bracket matching — it just counts depth.
	// `{"a": {]}` — skipCompound sees ']' as depth--, which is fine structurally
	// for our depth-only counter. The result is "a" is considered fully skipped.
	// This is intentional: we optimize for speed, not full JSON validation.
	r := scan(`{"a": {], "b": 2}`, "b")
	// The scanner may or may not find "b" depending on bracket depth tracking.
	// Just verify it doesn't panic.
	_ = r
}

// ============================================================================
// Multiple key capture ordering
// ============================================================================

func TestJSR_MultiKey_CapturedInStreamOrder(t *testing.T) {
	json := `{"z": 26, "a": 1, "m": 13}`
	r := scan(json, "z", "a", "m")
	assert.Equal(t, "26", string(r["z"]))
	assert.Equal(t, "1", string(r["a"]))
	assert.Equal(t, "13", string(r["m"]))
}

func TestJSR_MultiKey_RequestedInReverseOrder(t *testing.T) {
	json := `{"first": 1, "second": 2, "third": 3}`
	r := scan(json, "third", "second", "first")
	assert.Equal(t, "1", string(r["first"]))
	assert.Equal(t, "2", string(r["second"]))
	assert.Equal(t, "3", string(r["third"]))
}

func TestJSR_MultiKey_OnlyMiddleExists(t *testing.T) {
	json := `{"exists": "yes"}`
	r := scan(json, "before", "exists", "after")
	assert.Len(t, r, 1)
	assert.Equal(t, `"yes"`, string(r["exists"]))
}

// ============================================================================
// Realistic Gemini API payloads
// ============================================================================

func TestJSR_Realistic_GeminiWithAudio(t *testing.T) {
	json := `{
		"contents": [
			{
				"role": "user",
				"parts": [
					{"text": "Tell me a story about a brave knight."}
				]
			}
		],
		"generationConfig": {
			"responseModalities": ["AUDIO"],
			"speechConfig": {
				"voiceConfig": {
					"prebuiltVoiceConfig": {
						"voiceName": "Kore"
					}
				}
			},
			"temperature": 0.7,
			"topP": 0.9,
			"maxOutputTokens": 2048
		},
		"safetySettings": [
			{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_NONE"}
		]
	}`
	r := scan(json, "generationConfig")
	require.Contains(t, r, "generationConfig")
	// The captured value should be a valid JSON object containing all the config
	val := string(r["generationConfig"])
	assert.Contains(t, val, `"responseModalities"`)
	assert.Contains(t, val, `"speechConfig"`)
	assert.Contains(t, val, `"Kore"`)
}

func TestJSR_Realistic_GeminiWithLargeVideo(t *testing.T) {
	// Simulates a payload where contents has a huge base64 video
	videoData := strings.Repeat("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef", 5000) // 160KB
	json := `{
		"contents": [
			{
				"parts": [
					{"inlineData": {"mimeType": "video/mp4", "data": "` + videoData + `"}},
					{"text": "Describe this video"}
				]
			}
		],
		"generationConfig": {
			"responseModalities": ["TEXT"],
			"temperature": 0.5
		}
	}`
	r := scan(json, "generationConfig")
	require.Contains(t, r, "generationConfig")
	assert.Contains(t, string(r["generationConfig"]), `"TEXT"`)
}

func TestJSR_Realistic_MultipleConfigKeys(t *testing.T) {
	json := `{
		"contents": [{"parts": [{"text": "hi"}]}],
		"generationConfig": {"temperature": 0.9},
		"safetySettings": [{"category": "HARM_CATEGORY_HATE_SPEECH"}],
		"systemInstruction": {"parts": [{"text": "Be helpful"}]}
	}`
	r := scan(json, "generationConfig", "safetySettings", "systemInstruction")
	assert.Len(t, r, 3)
	assert.Contains(t, string(r["generationConfig"]), "temperature")
	assert.Contains(t, string(r["safetySettings"]), "HARM_CATEGORY_HATE_SPEECH")
	assert.Contains(t, string(r["systemInstruction"]), "Be helpful")
}

// ============================================================================
// Stress: many keys, large skip distances
// ============================================================================

func TestJSR_Stress_100Keys_FindLast(t *testing.T) {
	parts := make([]string, 100)
	for i := range parts {
		parts[i] = `"k` + strings.Repeat("0", 5) + string(rune('0'+i/10)) + string(rune('0'+i%10)) + `": ` + string(rune('0'+i%10))
	}
	parts = append(parts, `"target": "found"`)
	json := `{` + strings.Join(parts, ", ") + `}`
	r := scan(json, "target")
	assert.Equal(t, `"found"`, string(r["target"]))
}

func TestJSR_Stress_SkipManyLargeStrings(t *testing.T) {
	// 10 strings of 50KB each, then our target
	parts := make([]string, 10)
	for i := range parts {
		parts[i] = `"skip` + string(rune('0'+i)) + `": "` + strings.Repeat("x", 50000) + `"`
	}
	parts = append(parts, `"target": 42`)
	json := `{` + strings.Join(parts, ", ") + `}`
	r := scan(json, "target")
	assert.Equal(t, "42", string(r["target"]))
}

func TestJSR_Stress_SkipDeeplyNestedThenCapture(t *testing.T) {
	// Build a 50-level deep object
	var sb strings.Builder
	for range 50 {
		sb.WriteString(`{"d":`)
	}
	sb.WriteString(`"bottom"`)
	for range 50 {
		sb.WriteString(`}`)
	}
	deep := sb.String()
	json := `{"deep": ` + deep + `, "shallow": "here"}`
	r := scan(json, "shallow")
	assert.Equal(t, `"here"`, string(r["shallow"]))
}

// ============================================================================
// NoPanic: ensure nothing panics even on garbage
// ============================================================================

func TestJSR_NoPanic_RandomBytes(t *testing.T) {
	inputs := []string{
		"\xff\xfe\xfd",
		"{\"key\": \xff}",
		"{\x00: \x00}",
		string([]byte{'{', '"', 'a', '"', ':', 0x80, '}'}),
		"{\"k\":\x01}",
	}
	for _, input := range inputs {
		assert.NotPanics(t, func() {
			scan(input, "key", "k", "a")
		}, "input: %q", input)
	}
}

func TestJSR_NoPanic_PartialTokens(t *testing.T) {
	inputs := []string{
		`{"`,
		`{"k`,
		`{"k"`,
		`{"k":`,
		`{"k":"`,
		`{"k":"v`,
		`{"k":"v"`,
		`{"k":"v",`,
		`{"k":"v","`,
	}
	for _, input := range inputs {
		assert.NotPanics(t, func() {
			scan(input, "k")
		}, "input: %q", input)
	}
}
