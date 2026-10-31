package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestPrintTable verifies common API collection envelopes render as rows.
func TestPrintTable(t *testing.T) {
	var buffer bytes.Buffer
	if err := Print(&buffer, []byte(`{"data":[{"id":"b","name":"Beta"},{"id":"a","name":"Alpha"}]}`), Table); err != nil {
		t.Fatal(err)
	}
	result := buffer.String()
	for _, expected := range []string{"ID", "NAME", "b", "Beta", "a", "Alpha"} {
		if !strings.Contains(result, expected) {
			t.Fatalf("table %q does not contain %q", result, expected)
		}
	}
}

// TestPrintWideTable verifies rich API objects stay readable in the default
// human-oriented format while complete data remains available as JSON/YAML.
func TestPrintWideTable(t *testing.T) {
	body := []byte(`{"data":[{"id":"f1a8276c-333c-4c7b-86a7-3e8d90b27201","name":"Z Test User 03598","email":"ztestuser03598@example.com","status":"active","roles":[{"id":"8837d356-83d2-49fe-9085-a6e33d25cd47","name":"viewer"}],"created_at":"2026-05-15T08:02:44Z","updated_at":"2026-05-15T08:02:44Z","department":"Load Testing","title":"Load Test","businessPhones":[],"extensionAttribute1":null}]}`)

	var buffer bytes.Buffer
	if err := Print(&buffer, body, Table); err != nil {
		t.Fatal(err)
	}
	result := buffer.String()
	for _, expected := range []string{"ID", "NAME", "EMAIL", "STATUS", "ROLES", "viewer", " of 9 fields", "--output json"} {
		if !strings.Contains(result, expected) {
			t.Fatalf("table %q does not contain %q", result, expected)
		}
	}
	for _, unexpected := range []string{"businessPhones", "extensionAttribute1", `[{"id"`} {
		if strings.Contains(result, unexpected) {
			t.Fatalf("table %q unexpectedly contains %q", result, unexpected)
		}
	}

	buffer.Reset()
	if err := Print(&buffer, body, JSON); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"businessPhones": []`, `"extensionAttribute1": null`, `"name": "viewer"`} {
		if !strings.Contains(buffer.String(), expected) {
			t.Fatalf("JSON output does not contain complete field %q: %q", expected, buffer.String())
		}
	}
}

func TestPrintTableSummarizesAndTruncatesNestedValues(t *testing.T) {
	body := []byte(`[{"id":"one","metadata":{"region":"us-east-1","owner":"platform"},"providers":["openai","anthropic"],"description":"This description is deliberately longer than forty-eight Unicode code points so it is truncated."}]`)

	var buffer bytes.Buffer
	if err := Print(&buffer, body, Table); err != nil {
		t.Fatal(err)
	}
	result := buffer.String()
	for _, expected := range []string{"region=us-east-1", "openai, anthropic", "…"} {
		if !strings.Contains(result, expected) {
			t.Fatalf("table %q does not contain %q", result, expected)
		}
	}
	if strings.Contains(result, `{"region"`) {
		t.Fatalf("table contains raw nested JSON: %q", result)
	}
}

// TestPrintRawIsByteIdentical verifies Raw writes the body without interpretation,
// including bodies that do not end in a newline.
func TestPrintRawIsByteIdentical(t *testing.T) {
	body := []byte(`{"ok":true}`)
	var buffer bytes.Buffer
	if err := Print(&buffer, body, Raw); err != nil {
		t.Fatal(err)
	}
	if got := buffer.Bytes(); !bytes.Equal(got, body) {
		t.Fatalf("raw output = %q, want %q", got, body)
	}
}

// TestPrintPreservesLargeIntegers verifies integers beyond float64's exact
// range keep their lexeme through JSON, YAML, and table rendering.
func TestPrintPreservesLargeIntegers(t *testing.T) {
	body := []byte(`{"id":9007199254740993}`)

	var buffer bytes.Buffer
	if err := Print(&buffer, body, JSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), "9007199254740993") {
		t.Fatalf("JSON output = %q, want the exact integer lexeme", buffer.String())
	}

	buffer.Reset()
	if err := Print(&buffer, body, YAML); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); !strings.Contains(got, "id: 9007199254740993") {
		t.Fatalf("YAML output = %q, want an unquoted numeric scalar", got)
	}

	buffer.Reset()
	if err := Print(&buffer, body, Table); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), "9007199254740993") {
		t.Fatalf("table output = %q, want the exact integer lexeme", buffer.String())
	}
}

// TestPrintRejectsTrailingData verifies a body with extra content after the
// first JSON value falls back to Raw instead of silently truncating it.
func TestPrintRejectsTrailingData(t *testing.T) {
	body := []byte(`{"ok":true}garbage`)
	var buffer bytes.Buffer
	if err := Print(&buffer, body, JSON); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != string(body) {
		t.Fatalf("output = %q, want raw fallback %q", got, body)
	}
}

// escapeCode is a literal ESC control byte, built at runtime so the JSON
// fixtures below carry a real control character rather than its escaped form.
var escapeCode = string(rune(0x1b))

// TestPrintTableFallbackSanitizesControlCharacters verifies malformed or
// trailing input that falls back from Table to a raw write still gets
// sanitized, since the user asked for human-oriented output, not Raw.
func TestPrintTableFallbackSanitizesControlCharacters(t *testing.T) {
	body := []byte("not json" + escapeCode + "[31mRed")
	var buffer bytes.Buffer
	if err := Print(&buffer, body, Table); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(buffer.String(), '\x1b') {
		t.Fatalf("table fallback output contains an ESC control character: %q", buffer.String())
	}
	if !strings.Contains(buffer.String(), "Red") {
		t.Fatalf("table fallback output dropped surrounding text: %q", buffer.String())
	}
}

// TestPrintJSONFallbackStaysRaw verifies the JSON/YAML fallback for
// malformed input remains byte-preserving, unlike the Table fallback.
func TestPrintJSONFallbackStaysRaw(t *testing.T) {
	body := []byte("not json" + escapeCode + "[31mRed")
	var buffer bytes.Buffer
	if err := Print(&buffer, body, JSON); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != string(body) {
		t.Fatalf("JSON fallback output = %q, want raw fallback %q", got, body)
	}
}

// TestPrintTableStripsControlCharacters verifies a row cell cannot inject
// terminal escape sequences into human-oriented table output.
func TestPrintTableStripsControlCharacters(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"data": []any{map[string]any{"id": "a", "name": "Evil" + escapeCode + "[31mRed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if err := Print(&buffer, body, Table); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(buffer.String(), '\x1b') {
		t.Fatalf("table output contains an ESC control character: %q", buffer.String())
	}
	if !strings.Contains(buffer.String(), "Red") {
		t.Fatalf("table output dropped surrounding text: %q", buffer.String())
	}
}

// TestPrintObjectStripsControlCharacters verifies the single-object rendering
// path (scalar) shares the same sanitizer as table cells.
func TestPrintObjectStripsControlCharacters(t *testing.T) {
	body, err := json.Marshal(map[string]any{"name": "Evil" + escapeCode + "[31mRed"})
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if err := Print(&buffer, body, Table); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(buffer.String(), '\x1b') {
		t.Fatalf("object output contains an ESC control character: %q", buffer.String())
	}
	if !strings.Contains(buffer.String(), "Red") {
		t.Fatalf("object output dropped surrounding text: %q", buffer.String())
	}
}

// TestPrintObjectKeyStripsControlCharacters verifies an object key
// containing ESC cannot inject terminal escape sequences via printObject.
func TestPrintObjectKeyStripsControlCharacters(t *testing.T) {
	key := "Evil" + escapeCode + "Key"
	body, err := json.Marshal(map[string]any{key: "value"})
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if err := Print(&buffer, body, Table); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(buffer.String(), '\x1b') {
		t.Fatalf("object output contains an ESC control character in a key: %q", buffer.String())
	}
}

// TestPrintRowsHeaderKeyStripsControlCharacters verifies a collection's
// column header cannot inject terminal escape sequences via printRows.
func TestPrintRowsHeaderKeyStripsControlCharacters(t *testing.T) {
	key := "Evil" + escapeCode + "Key"
	body, err := json.Marshal(map[string]any{
		"data": []any{map[string]any{key: "value", "id": "a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if err := Print(&buffer, body, Table); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(buffer.String(), '\x1b') {
		t.Fatalf("table output contains an ESC control character in a header key: %q", buffer.String())
	}
}

// TestPrintNestedObjectKeyStripsControlCharacters verifies summarized nested
// objects cannot inject terminal escape sequences through their keys.
func TestPrintNestedObjectKeyStripsControlCharacters(t *testing.T) {
	key := "Evil" + escapeCode + "Key"
	body, err := json.Marshal(map[string]any{
		"data": []any{map[string]any{"id": "row", "metadata": map[string]any{key: "value"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if err := Print(&buffer, body, Table); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(buffer.String(), '\x1b') {
		t.Fatalf("table output contains an ESC control character in a nested key: %q", buffer.String())
	}
	if !strings.Contains(buffer.String(), "EvilKey=value") {
		t.Fatalf("table output dropped the sanitized nested key: %q", buffer.String())
	}
}

// TestPrintRawDoesNotSanitizeControlCharacters verifies Raw stays unfiltered.
func TestPrintRawDoesNotSanitizeControlCharacters(t *testing.T) {
	body := []byte(escapeCode + "[31mRed" + escapeCode + "[0m")
	var buffer bytes.Buffer
	if err := Print(&buffer, body, Raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer.Bytes(), body) {
		t.Fatalf("raw output modified control characters: %q", buffer.Bytes())
	}
}

// TestPrintJSON verifies compact input is normalized for automation.
func TestPrintJSON(t *testing.T) {
	var buffer bytes.Buffer
	if err := Print(&buffer, []byte(`{"ok":true}`), JSON); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != "{\n  \"ok\": true\n}\n" {
		t.Fatalf("JSON output = %q", got)
	}
}

// TestPrintYAML verifies structured output is available for configuration workflows.
func TestPrintYAML(t *testing.T) {
	var buffer bytes.Buffer
	if err := Print(&buffer, []byte(`{"name":"test","enabled":true}`), YAML); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); !strings.Contains(got, "name: test") || !strings.Contains(got, "enabled: true") {
		t.Fatalf("YAML output = %q", got)
	}
}
