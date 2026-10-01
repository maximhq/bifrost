package utils

import "testing"

func TestEscapeResourceID(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"file-123_ABC": "file-123_ABC",
		"file name":    "file%20name",
		"file..name":   "file..name",
		"ft:gpt-4o":    "ft:gpt-4o",
	}
	for input, expected := range valid {
		actual, bifrostErr := EscapeResourceID(input, "file_id")
		if bifrostErr != nil {
			t.Fatalf("EscapeResourceID(%q) returned error: %v", input, bifrostErr)
		}
		if actual != expected {
			t.Fatalf("EscapeResourceID(%q) = %q, want %q", input, actual, expected)
		}
	}

	invalid := []string{
		"", "/", "a/b", ".", "..", "../models",
		"?", "a?b", "#", "a#b", "\\", "a\\b",
		"%2f", "%2e%2e", "%252e%252e", "file%20name",
		"line\nbreak", "tab\there", "del\x7f",
	}
	for _, input := range invalid {
		_, bifrostErr := EscapeResourceID(input, "file_id")
		if bifrostErr == nil {
			t.Fatalf("EscapeResourceID(%q) unexpectedly succeeded", input)
		}
		if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
			t.Fatalf("EscapeResourceID(%q) status = %v, want 400", input, bifrostErr.StatusCode)
		}
	}
}
