package bedrock

import (
	"testing"
)

// TestPercentEncodeRFC3986 tests the RFC 3986 percent encoding function
func TestPercentEncodeRFC3986(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "unreserved characters",
			input:    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~",
			expected: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~",
		},
		{
			name:     "space becomes %20",
			input:    "hello world",
			expected: "hello%20world",
		},
		{
			name:     "plus becomes %2B",
			input:    "a+b",
			expected: "a%2Bb",
		},
		{
			name:     "equals becomes %3D",
			input:    "a=b",
			expected: "a%3Db",
		},
		{
			name:     "ampersand becomes %26",
			input:    "a&b",
			expected: "a%26b",
		},
		{
			name:     "slash becomes %2F",
			input:    "path/to/file",
			expected: "path%2Fto%2Ffile",
		},
		{
			name:     "colon becomes %3A",
			input:    "http://example.com",
			expected: "http%3A%2F%2Fexample.com",
		},
		{
			name:     "special characters",
			input:    "!@#$%^&*()",
			expected: "%21%40%23%24%25%5E%26%2A%28%29",
		},
		{
			name:     "unicode characters",
			input:    "hello™",
			expected: "hello%E2%84%A2",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := percentEncodeRFC3986(tt.input)
			if result != tt.expected {
				t.Errorf("percentEncodeRFC3986(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestUppercaseHex tests the uppercase hex conversion function
func TestUppercaseHex(t *testing.T) {
	tests := []struct {
		input    byte
		expected byte
	}{
		{0, '0'},
		{5, '5'},
		{9, '9'},
		{10, 'A'},
		{15, 'F'},
	}

	for _, tt := range tests {
		result := uppercaseHex(tt.input)
		if result != tt.expected {
			t.Errorf("uppercaseHex(%d) = %c, want %c", tt.input, result, tt.expected)
		}
	}
}

// TestBuildCanonicalQueryString tests the canonical query string builder
func TestBuildCanonicalQueryString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty query string",
			input:    "",
			expected: "",
		},
		{
			name:     "simple query parameter",
			input:    "key=value",
			expected: "key=value",
		},
		{
			name:     "space in value becomes %20",
			input:    "key=hello world",
			expected: "key=hello%20world",
		},
		{
			name:     "literal plus becomes %2B",
			input:    "key=a+b",
			expected: "key=a%2Bb",
		},
		{
			name:     "mixed spaces and plus",
			input:    "search=hello world+test",
			expected: "search=hello%20world%2Btest",
		},
		{
			name:     "multiple parameters sorted",
			input:    "z=last&a=first&m=middle",
			expected: "a=first&m=middle&z=last",
		},
		{
			name:     "duplicate keys sorted by value",
			input:    "key=z&key=a&key=m",
			expected: "key=a&key=m&key=z",
		},
		{
			name:     "empty value",
			input:    "key=",
			expected: "key=",
		},
		{
			name:     "no equals sign",
			input:    "key",
			expected: "key=",
		},
		{
			name:     "special characters in key",
			input:    "special%key=value",
			expected: "special%25key=value",
		},
		{
			name:     "special characters in value",
			input:    "key=value!@#$%^*()",
			expected: "key=value%21%40%23%24%25%5E%2A%28%29",
		},
		{
			name:     "AWS test vector: spaces",
			input:    "foo=bar baz",
			expected: "foo=bar%20baz",
		},
		{
			name:     "AWS test vector: unreserved characters",
			input:    "foo=bar-._~123",
			expected: "foo=bar-._~123",
		},
		{
			name:     "AWS test vector: reserved characters",
			input:    "foo=!*'()",
			expected: "foo=%21%2A%27%28%29",
		},
		{
			name:     "AWS test vector: slash and colon",
			input:    "foo=/path:to:resource",
			expected: "foo=%2Fpath%3Ato%3Aresource",
		},
		{
			name:     "AWS test vector: complex sorting",
			input:    "Action=ListUsers&Version=2010-05-08",
			expected: "Action=ListUsers&Version=2010-05-08",
		},
		{
			name:     "AWS test vector: sorting with duplicates",
			input:    "a=1&a=2&a=10&b=1",
			expected: "a=1&a=10&a=2&b=1",
		},
		{
			name:     "value with equals sign",
			input:    "key=value=with=equals",
			expected: "key=value%3Dwith%3Dequals",
		},
		{
			name:     "multiple parameters with spaces and special chars",
			input:    "name=John Doe&email=john+doe@example.com&age=30",
			expected: "age=30&email=john%2Bdoe%40example.com&name=John%20Doe",
		},
		{
			name:     "unicode in query string",
			input:    "text=hello™&lang=en",
			expected: "lang=en&text=hello%E2%84%A2",
		},
		{
			name:     "empty pairs filtered",
			input:    "a=1&&b=2&",
			expected: "a=1&b=2",
		},
		{
			name:     "already encoded space",
			input:    "key=%20",
			expected: "key=%20",
		},
		{
			name:     "already encoded percent sign",
			input:    "percent=%25",
			expected: "percent=%25",
		},
		{
			name:     "already encoded slash",
			input:    "path=%2Fto%2Ffile",
			expected: "path=%2Fto%2Ffile",
		},
		{
			name:     "mixed encoded and unencoded",
			input:    "a=hello%20world&b=test value",
			expected: "a=hello%20world&b=test%20value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildCanonicalQueryString(tt.input)
			if result != tt.expected {
				t.Errorf("buildCanonicalQueryString(%q)\n  got:  %q\n  want: %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestBuildCanonicalQueryStringLexicographicSort tests that sorting is truly lexicographic
func TestBuildCanonicalQueryStringLexicographicSort(t *testing.T) {
	// Test that "10" comes before "2" lexicographically
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "lexicographic sort of values",
			input:    "a=2&a=10&a=1",
			expected: "a=1&a=10&a=2",
		},
		{
			name:     "lexicographic sort of keys",
			input:    "z=1&a10=2&a2=3&a=4",
			expected: "a=4&a10=2&a2=3&z=1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildCanonicalQueryString(tt.input)
			if result != tt.expected {
				t.Errorf("buildCanonicalQueryString(%q)\n  got:  %q\n  want: %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestBuildCanonicalQueryStringAWSSigV4Examples tests real AWS SigV4 examples
func TestBuildCanonicalQueryStringAWSSigV4Examples(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		note     string
	}{
		{
			name:     "AWS example: GetObject",
			input:    "response-content-type=application/json",
			expected: "response-content-type=application%2Fjson",
			note:     "Slash must be encoded",
		},
		{
			name:     "AWS example: ListObjects with prefix",
			input:    "prefix=my prefix/",
			expected: "prefix=my%20prefix%2F",
			note:     "Space becomes %20, slash becomes %2F",
		},
		{
			name:     "AWS example: multiple parameters",
			input:    "marker=example.txt&max-keys=100&prefix=photos/",
			expected: "marker=example.txt&max-keys=100&prefix=photos%2F",
			note:     "Sorted and encoded",
		},
		{
			name:     "AWS example: encoding special characters",
			input:    "key=a+b c&other=1/2",
			expected: "key=a%2Bb%20c&other=1%2F2",
			note:     "Plus becomes %2B, space becomes %20, slash becomes %2F",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildCanonicalQueryString(tt.input)
			if result != tt.expected {
				t.Errorf("%s\nbuildCanonicalQueryString(%q)\n  got:  %q\n  want: %q\n  note: %s",
					tt.name, tt.input, result, tt.expected, tt.note)
			}
		})
	}
}

// BenchmarkPercentEncodeRFC3986 benchmarks the encoding function
func BenchmarkPercentEncodeRFC3986(b *testing.B) {
	input := "hello world with spaces and special chars !@#$%"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		percentEncodeRFC3986(input)
	}
}

// BenchmarkBuildCanonicalQueryString benchmarks the query string builder
func BenchmarkBuildCanonicalQueryString(b *testing.B) {
	input := "z=last&a=first&m=middle&search=hello world+test&email=user+name@example.com"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buildCanonicalQueryString(input)
	}
}
