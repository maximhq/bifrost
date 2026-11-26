package vertex

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var (
	oldBodyProjectsRe   = regexp.MustCompile(`(["/])projects/[^/"]+`)
	oldBodyShortModelRe = regexp.MustCompile(`"(models/[^/"]+)"`)
)

// oldRewrite is the whole-body regex rewrite this package used to run, kept as the benchmark baseline.
func oldRewrite(body []byte, projectID, region string) []byte {
	bodyStr := oldBodyProjectsRe.ReplaceAllString(string(body), "${1}projects/"+projectID)
	bodyStr = vertexLocationsPathRe.ReplaceAllString(bodyStr, "/locations/"+region)
	bodyStr = oldBodyShortModelRe.ReplaceAllString(bodyStr,
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

// TestRewritePassthroughBodyLeavesContentUntouched pins that prompt text and any field other than the
// resource-name fields pass through byte-identical, even when they spell resource paths.
func TestRewritePassthroughBodyLeavesContentUntouched(t *testing.T) {
	cases := map[string]string{
		"prompt text with projects and locations":  `{"contents":[{"role":"user","parts":[{"text":"see github.com/acme/projects/roadmap and /locations/paris"}]}],"generationConfig":{"temperature":0}}`,
		"prompt text that is a full resource name": `{"contents":[{"parts":[{"text":"projects/acme/locations/us-east1/cachedContents/1"}]}]}`,
		"prompt text that is a short model name":   `{"contents":[{"parts":[{"text":"models/gemini-2.5-flash"}]}]}`,
		"system instruction":                       `{"systemInstruction":{"parts":[{"text":"our projects/alpha repo lives at /locations/hq"}]},"contents":[{"parts":[{"text":"hi"}]}]}`,
		"function call args named model":           `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":{"model":"models/x","cachedContent":"projects/p/locations/l/cachedContents/1"}}}]}]}`,
		"tool schema enum":                         `{"tools":[{"functionDeclarations":[{"name":"f","parameters":{"type":"object","properties":{"p":{"type":"string","enum":["projects/a","/locations/b"]}}}}]}]}`,
		"anthropic messages":                       `{"anthropic_version":"vertex-2023-10-16","messages":[{"role":"user","content":"check projects/foo and /locations/bar"}],"max_tokens":10}`,
		"openai chat":                              `{"model":"google/gemini-2.5-flash","messages":[{"role":"user","content":"projects/foo/locations/bar"}]}`,
		"unlisted top-level field":                 `{"name":"/projects/other/locations/us-central1/operations/123","labels":{"k":"projects/x"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, changed := rewritePassthroughBody([]byte(body), benchProject, benchRegion)
			if changed || string(got) != body {
				t.Errorf("body rewritten\n got: %s\nwant: %s", got, body)
			}
		})
	}
}

// TestRewritePassthroughBodyResourceFields pins each body field the google-genai SDK (Vertex mode) fills with
// the client's project and location.
func TestRewritePassthroughBodyResourceFields(t *testing.T) {
	const full = "projects/" + benchProject + "/locations/" + benchRegion
	cases := []struct{ name, body, want string }{
		{"cache create model", `{"model":"projects/dummy/locations/us-central1/publishers/google/models/gemini-2.5-flash","contents":[{"parts":[{"text":"projects/keep"}]}]}`,
			`{"model":"` + full + `/publishers/google/models/gemini-2.5-flash","contents":[{"parts":[{"text":"projects/keep"}]}]}`},
		{"short model", `{"model":"models/gemini-2.5-flash"}`, `{"model":"` + full + `/publishers/google/models/gemini-2.5-flash"}`},
		{"publisher model untouched", `{"model":"publishers/google/models/gemini-2.5-flash"}`, `{"model":"publishers/google/models/gemini-2.5-flash"}`},
		{"cachedContent", `{"contents":[{"parts":[{"text":"hi"}]}],"cachedContent":"projects/dummy/locations/us-central1/cachedContents/123"}`,
			`{"contents":[{"parts":[{"text":"hi"}]}],"cachedContent":"` + full + `/cachedContents/123"}`},
		{"leading slash", `{"cachedContent":"/projects/dummy/locations/us-central1/cachedContents/1"}`, `{"cachedContent":"/` + full + `/cachedContents/1"}`},
		{"veo operationName", `{"operationName":"projects/dummy/locations/us-central1/publishers/google/models/veo-3.0-generate-001/operations/abc"}`,
			`{"operationName":"` + full + `/publishers/google/models/veo-3.0-generate-001/operations/abc"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := rewritePassthroughBody([]byte(tc.body), benchProject, benchRegion)
			if string(got) != tc.want {
				t.Errorf("rewrite mismatch\n got: %s\nwant: %s", got, tc.want)
			}
			if wantChanged := tc.want != tc.body; changed != wantChanged {
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

