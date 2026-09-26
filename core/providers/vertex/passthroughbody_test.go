package vertex

import (
	"fmt"
	"strings"
	"testing"
)

// oldRewrite is the pre-refactor implementation, kept as a differential oracle
// and as the benchmark baseline.
func oldRewrite(body []byte, projectID, region string) []byte {
	bodyStr := vertexBodyProjectsRe.ReplaceAllString(string(body), "${1}projects/"+projectID)
	bodyStr = vertexLocationsPathRe.ReplaceAllString(bodyStr, "/locations/"+region)
	bodyStr = vertexShortModelRe.ReplaceAllString(bodyStr,
		fmt.Sprintf(`"projects/%s/locations/%s/publishers/google/$1"`, projectID, region))
	return []byte(bodyStr)
}

const (
	benchProject = "my-real-project"
	benchRegion  = "europe-west4"
)

// jsonBodyWithImage builds a Gemini-shaped request whose inline image data is
// approximately sizeBytes of base64 payload.
func jsonBodyWithImage(sizeBytes int, withResourceNames bool) []byte {
	var b strings.Builder
	b.WriteString(`{`)
	if withResourceNames {
		b.WriteString(`"model":"models/gemini-2.5-flash",`)
		b.WriteString(`"endpoint":"projects/None/locations/None/publishers/google/models/gemini-2.5-flash",`)
	} else {
		b.WriteString(`"model":"gemini-2.5-flash",`)
	}
	b.WriteString(`"contents":[{"role":"user","parts":[{"inline_data":{"mime_type":"image/png","data":"`)
	b.WriteString(strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVph", sizeBytes/36+1))
	b.WriteString(`"}},{"text":"describe this image"}]}]}`)
	return []byte(b.String())
}

func TestRewritePassthroughBodyMatchesOldImplementation(t *testing.T) {
	cases := map[string][]byte{
		"placeholder resource names": []byte(`{"model":"projects/None/locations/None/publishers/google/models/gemini-2.5-flash"}`),
		"short form model":           []byte(`{"model":"models/gemini-2.5-flash"}`),
		"mid path projects":          []byte(`{"name":"/projects/other/locations/us-central1/operations/123"}`),
		"multiple occurrences":       []byte(`{"a":"projects/x/locations/us/y","b":"/projects/z/locations/asia/w"}`),
		"no patterns":                []byte(`{"contents":[{"parts":[{"text":"hello world"}]}]}`),
		"empty object":               []byte(`{}`),
		"large image with names":     jsonBodyWithImage(64*1024, true),
		"large image without names":  jsonBodyWithImage(64*1024, false),
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			want := oldRewrite(body, benchProject, benchRegion)
			got, changed := rewritePassthroughBody(body, benchProject, benchRegion)
			if string(got) != string(want) {
				t.Errorf("rewrite mismatch\n got: %s\nwant: %s", truncate(got), truncate(want))
			}
			// changed must be a faithful report of whether anything differs.
			if wantChanged := string(want) != string(body); changed != wantChanged {
				t.Errorf("changed = %v, want %v", changed, wantChanged)
			}
		})
	}
}

func TestRewritePassthroughBodyDoesNotMutateInput(t *testing.T) {
	body := []byte(`{"model":"projects/None/locations/None/publishers/google/models/gemini-2.5-flash"}`)
	original := string(body)
	if _, changed := rewritePassthroughBody(body, benchProject, benchRegion); !changed {
		t.Fatal("expected rewrite")
	}
	if string(body) != original {
		t.Errorf("input mutated: %s", body)
	}
}

func TestRewritePassthroughBodyNoMatchReturnsSameSlice(t *testing.T) {
	body := jsonBodyWithImage(4*1024, false)
	got, changed := rewritePassthroughBody(body, benchProject, benchRegion)
	if changed {
		t.Fatal("expected changed=false")
	}
	if &got[0] != &body[0] {
		t.Error("expected the original slice back, not a copy")
	}
}

func truncate(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}

var sizes = []struct {
	name string
	size int
}{
	{"1KB", 1 << 10},
	{"64KB", 64 << 10},
	{"1MB", 1 << 20},
	{"8MB", 8 << 20},
}

// BenchmarkRewriteNoMatch is the common case: a large payload with no Vertex
// resource names anywhere in it.
func BenchmarkRewriteNoMatch(b *testing.B) {
	for _, s := range sizes {
		body := jsonBodyWithImage(s.size, false)
		b.Run("old/"+s.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = oldRewrite(body, benchProject, benchRegion)
			}
		})
		b.Run("new/"+s.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = rewritePassthroughBody(body, benchProject, benchRegion)
			}
		})
	}
}

// BenchmarkRewriteWithMatch is the worst case: the rewrite actually fires on a
// large payload.
func BenchmarkRewriteWithMatch(b *testing.B) {
	for _, s := range sizes {
		body := jsonBodyWithImage(s.size, true)
		b.Run("old/"+s.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = oldRewrite(body, benchProject, benchRegion)
			}
		})
		b.Run("new/"+s.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = rewritePassthroughBody(body, benchProject, benchRegion)
			}
		})
	}
}

