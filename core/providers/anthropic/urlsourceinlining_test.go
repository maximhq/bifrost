package anthropic

import (
	"encoding/base64"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// Bedrock Mantle rejects {"source":{"type":"url"}} with 400 "URL content sources are
// not yet supported for this model". These tests pin the inlining that rewrites those
// sources into base64/text before the body leaves Bifrost.
//
// Note on coverage limits: the fetch itself cannot be driven from a unit test.
// FetchAndEncodeURL dials through network.SSRFSafeDialContext, which rejects every
// non-public address unconditionally and has no test seam, so an httptest server on
// 127.0.0.1 is unreachable by design and weakening that guard would be a bad trade
// (same constraint documented in core/providers/openai/chatfileurl_test.go). Both
// halves either side of the fetch — which sources get collected, and what the body
// looks like once bytes come back — are covered here.

func TestCollectURLSourceRefs(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://x.invalid/a.pdf"}}]},` +
		`{"role":"assistant","content":"acknowledged"},` +
		`{"role":"user","content":[{"type":"text","text":"and this"},{"type":"image","source":{"type":"url","url":"https://x.invalid/b.png"}}]}]}`)

	refs := collectURLSourceRefs(body)
	if len(refs) != 2 {
		t.Fatalf("expected 2 url sources, got %d: %+v", len(refs), refs)
	}
	if refs[0].path != "messages.0.content.0.source" || refs[0].blockType != "document" ||
		refs[0].url != "https://x.invalid/a.pdf" {
		t.Errorf("unexpected first ref: %+v", refs[0])
	}
	// String-valued content (messages.1) carries no sources and must be skipped, so the
	// image keeps its true index rather than shifting.
	if refs[1].path != "messages.2.content.1.source" || refs[1].blockType != "image" ||
		refs[1].url != "https://x.invalid/b.png" {
		t.Errorf("unexpected second ref: %+v", refs[1])
	}
}

func TestCollectURLSourceRefs_IgnoresAlreadyInlinedAndMalformed(t *testing.T) {
	cases := map[string]string{
		"base64 source":     `{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"AAAA"}}]}]}`,
		"file_id source":    `{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file_abc"}}]}]}`,
		"url with no value": `{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"url","url":""}}]}]}`,
		"no source at all":  `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		"string content":    `{"messages":[{"role":"user","content":"hi"}]}`,
		"no messages":       `{"model":"anthropic.claude-opus-4-8"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if refs := collectURLSourceRefs([]byte(body)); len(refs) != 0 {
				t.Errorf("expected no refs, got %+v", refs)
			}
		})
	}
}

func TestApplyInlinedSource_BinaryBecomesBase64(t *testing.T) {
	body := []byte(`{"model":"anthropic.claude-opus-4-8","messages":[{"role":"user","content":[` +
		`{"type":"document","source":{"type":"url","url":"https://x.invalid/2024ltr.pdf"}},` +
		`{"type":"text","text":"Summarize this document in one sentence."}]}]}`)
	ref := collectURLSourceRefs(body)[0]
	encoded := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 fake document bytes"))

	out, err := applyInlinedSource(body, ref, "application/pdf", encoded)
	if err != nil {
		t.Fatalf("applyInlinedSource: %v", err)
	}

	source := gjson.GetBytes(out, "messages.0.content.0.source")
	if got := source.Get("type").String(); got != "base64" {
		t.Errorf("source.type = %q, want base64", got)
	}
	if got := source.Get("media_type").String(); got != "application/pdf" {
		t.Errorf("source.media_type = %q, want application/pdf", got)
	}
	if got := source.Get("data").String(); got != encoded {
		t.Errorf("source.data = %q, want the fetched bytes", got)
	}
	if source.Get("url").Exists() {
		t.Error("source.url must be gone once inlined")
	}
	if strings.Contains(string(out), `"type":"url"`) {
		t.Errorf("a url source survived: %s", out)
	}

	// Neighbouring content and top-level fields must survive untouched.
	if got := gjson.GetBytes(out, "messages.0.content.1.text").String(); got != "Summarize this document in one sentence." {
		t.Errorf("sibling text block altered: %q", got)
	}
	if got := gjson.GetBytes(out, "model").String(); got != "anthropic.claude-opus-4-8" {
		t.Errorf("model altered: %q", got)
	}
}

func TestApplyInlinedSource_TextBecomesTextSourceWithDecodedData(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"document","source":{"type":"url","url":"https://x.invalid/notes.txt"}}]}]}`)
	ref := collectURLSourceRefs(body)[0]
	encoded := base64.StdEncoding.EncodeToString([]byte("hello from a text file"))

	// The charset parameter must be stripped rather than carried into media_type.
	out, err := applyInlinedSource(body, ref, "text/plain; charset=utf-8", encoded)
	if err != nil {
		t.Fatalf("applyInlinedSource: %v", err)
	}

	source := gjson.GetBytes(out, "messages.0.content.0.source")
	if got := source.Get("type").String(); got != "text" {
		t.Errorf("source.type = %q, want text", got)
	}
	if got := source.Get("media_type").String(); got != "text/plain" {
		t.Errorf("source.media_type = %q, want text/plain", got)
	}
	if got := source.Get("data").String(); got != "hello from a text file" {
		t.Errorf("source.data = %q, want the decoded characters", got)
	}
}

func TestApplyInlinedSource_MediaTypeFallbacks(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		blockType string
		served    string
		want      string
	}{
		{"served type wins", "https://x.invalid/report.bin", "document", "application/pdf", "application/pdf"},
		{"generic type falls back to extension", "https://x.invalid/report.pdf", "document", "application/octet-stream", "application/pdf"},
		{"missing type falls back to extension", "https://x.invalid/photo.png", "image", "", "image/png"},
		{"extensionless document defaults to pdf", "https://x.invalid/download", "document", "", "application/pdf"},
		{"query string does not confuse the extension", "https://x.invalid/report.pdf?v=2", "document", "", "application/pdf"},
		// An image with no usable signal keeps octet-stream so the provider reports a
		// clear media-type error rather than us guessing wrong about the bytes.
		{"extensionless image stays generic", "https://x.invalid/download", "image", "", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"messages":[{"role":"user","content":[` +
				`{"type":"` + tt.blockType + `","source":{"type":"url","url":"` + tt.url + `"}}]}]}`)
			ref := collectURLSourceRefs(body)[0]

			out, err := applyInlinedSource(body, ref, tt.served, base64.StdEncoding.EncodeToString([]byte("x")))
			if err != nil {
				t.Fatalf("applyInlinedSource: %v", err)
			}
			if got := gjson.GetBytes(out, "messages.0.content.0.source.media_type").String(); got != tt.want {
				t.Errorf("media_type = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyInlinedSource_RejectsUndecodableTextPayload(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"document","source":{"type":"url","url":"https://x.invalid/notes.txt"}}]}]}`)
	ref := collectURLSourceRefs(body)[0]

	if _, err := applyInlinedSource(body, ref, "text/plain", "not!valid!base64"); err == nil {
		t.Fatal("expected an error for an undecodable text payload, got nil")
	}
}

// TestApplyInlinedSource_ErrorDoesNotLeakURLCredentials pins the redaction at the layer
// the client actually sees: requestbuilder.go wraps this error as
// ErrProviderRequestMarshal, so it reaches the response body and the logs. A pre-signed
// URL's query is a bearer credential valid for up to 7 days
// (docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html), so echoing it
// there hands out working access to whoever reads either one.
func TestApplyInlinedSource_ErrorDoesNotLeakURLCredentials(t *testing.T) {
	signed := "https://amzn-s3-demo-bucket.s3.amazonaws.com/notes.txt" +
		"?X-Amz-Credential=AKIAIOSFODNN7EXAMPLE&X-Amz-Signature=deadbeefcafe"
	body := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"document","source":{"type":"url","url":"` + signed + `"}}]}]}`)
	ref := collectURLSourceRefs(body)[0]

	_, err := applyInlinedSource(body, ref, "text/plain", "not!valid!base64")
	if err == nil {
		t.Fatal("expected an error for an undecodable text payload, got nil")
	}
	for _, secret := range []string{"X-Amz-Signature", "deadbeefcafe", "AKIAIOSFODNN7EXAMPLE"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks %q: %s", secret, err.Error())
		}
	}
	// The host and path still identify which attachment failed, which is the whole
	// diagnostic value of naming the URL at all.
	if !strings.Contains(err.Error(), "amzn-s3-demo-bucket.s3.amazonaws.com/notes.txt") {
		t.Errorf("expected the redacted URL to still identify the resource, got %s", err.Error())
	}
}

func TestInlineURLContentSources_NoURLSourcesIsAPassthrough(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"AAAA"}},` +
		`{"type":"text","text":"hi"}]}]}`)

	out, err := InlineURLContentSources(nil, body)
	if err != nil {
		t.Fatalf("InlineURLContentSources: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("body was rewritten with nothing to inline:\n got %s\nwant %s", out, body)
	}
}

// BedrockMantle is the provider that needs this; native Anthropic fetches URLs itself
// and must not pay for a redundant download.
func TestInlineURLSourcesEnabledOnlyForBedrockMantle(t *testing.T) {
	if !AnthropicProviderRequestDefaultsMap[schemas.BedrockMantle].InlineURLSources {
		t.Error("BedrockMantle must inline URL sources")
	}
	for _, provider := range []schemas.ModelProvider{schemas.Anthropic, schemas.Azure, schemas.Vertex, schemas.Bedrock} {
		if AnthropicProviderRequestDefaultsMap[provider].InlineURLSources {
			t.Errorf("%s must not inline URL sources", provider)
		}
	}
}

// TestInlineURLContentSourcesForwardsUnfetchableSchemes: bifrost inlines only what it can
// download. A source it cannot fetch is left in the body as {"type":"url"} and the platform
// answers for itself - which keeps working if AWS-hosted Claude ever gains URL support.
func TestInlineURLContentSourcesForwardsUnfetchableSchemes(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{name: "s3", url: "s3://my-bucket/doc.pdf"},
		{name: "gcs", url: "gs://my-bucket/doc.pdf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"url","url":"` + tc.url + `"}}]}]}`)

			out, err := InlineURLContentSources(nil, body)
			if err != nil {
				t.Fatalf("expected pass-through for %q, got error: %v", tc.url, err)
			}
			if got := gjson.GetBytes(out, "messages.0.content.0.source.url").String(); got != tc.url {
				t.Errorf("url = %q, want %q left untouched", got, tc.url)
			}
			if got := gjson.GetBytes(out, "messages.0.content.0.source.type").String(); got != "url" {
				t.Errorf("source.type = %q, want it to stay url", got)
			}
		})
	}
}
