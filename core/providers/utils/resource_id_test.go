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

func TestEscapeResourcePath(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"gpt-4o":              "gpt-4o",
		"openai/gpt-oss-120b": "openai/gpt-oss-120b",
		"qwen/qwen3.8-27b":    "qwen/qwen3.8-27b",
		"org/model name":      "org/model%20name",
		"llama3.2:3b":         "llama3.2:3b",
	}
	for input, expected := range valid {
		actual, bifrostErr := EscapeResourcePath(input, "model")
		if bifrostErr != nil {
			t.Fatalf("EscapeResourcePath(%q) returned error: %v", input, bifrostErr)
		}
		if actual != expected {
			t.Fatalf("EscapeResourcePath(%q) = %q, want %q", input, actual, expected)
		}
	}

	invalid := []string{
		"", "/", "a/", "/a", "a//b",
		".", "..", "a/.", "a/..", "a/./b", "a/../b", "../models",
		"a/%2e%2e", "a/%2f", "a/b?c", "a/b#c", "a\\b", "a/\nb",
	}
	for _, input := range invalid {
		_, bifrostErr := EscapeResourcePath(input, "model")
		if bifrostErr == nil {
			t.Fatalf("EscapeResourcePath(%q) unexpectedly succeeded", input)
		}
		if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
			t.Fatalf("EscapeResourcePath(%q) status = %v, want 400", input, bifrostErr.StatusCode)
		}
	}
}
