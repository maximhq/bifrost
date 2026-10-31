package anthropic

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/memtest"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// TestInlineTextDocumentDataURL verifies base64 transport becomes readable text in both inference APIs.
func TestInlineTextDocumentDataURL(t *testing.T) {
	for _, media := range []string{"text/plain", "text/markdown", "text/csv", "application/json", "application/har+json"} {
		t.Run(media, func(t *testing.T) {
			text := "hello, café\noriginal file bytes"
			data := "data:" + media + ";base64," + base64.StdEncoding.EncodeToString([]byte(text))
			chat := ConvertToAnthropicDocumentBlock(schemas.ChatContentBlock{File: &schemas.ChatInputFile{FileData: &data, FileType: &media}})
			responses := ConvertResponsesFileBlockToAnthropic(&schemas.ResponsesInputMessageContentBlockFile{FileData: &data, FileType: &media}, nil, nil, nil)
			for _, block := range []AnthropicContentBlock{chat, responses} {
				s := block.Source.SourceObj
				if s.Type != "text" || s.MediaType == nil || *s.MediaType != "text/plain" || s.Data == nil || *s.Data != text {
					t.Fatalf("base64 document was not decoded: %+v", s)
				}
			}
		})
	}
}

// TestInlineDocumentPreservesLegacyData keeps unprefixed plaintext and binary base64 compatible.
func TestInlineDocumentPreservesLegacyData(t *testing.T) {
	for _, tc := range []struct{ media, data, kind string }{{"text/plain", "literal text", "text"}, {"application/pdf", "JVBERg==", "base64"}} {
		block := ConvertToAnthropicDocumentBlock(schemas.ChatContentBlock{File: &schemas.ChatInputFile{FileData: &tc.data, FileType: &tc.media}})
		if s := block.Source.SourceObj; s.Type != tc.kind || s.Data == nil || *s.Data != tc.data {
			t.Fatalf("legacy source changed: %+v", s)
		}
	}
}

// TestNativeBase64TextDocument verifies native Messages transport text decoding and rejects corrupt uploads.
func TestNativeBase64TextDocument(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"text/markdown","data":"aGVsbG8="}}]}]}`)
	body, err := normalizeBase64TextSources(raw)
	if err != nil || gjson.GetBytes(body, "messages.0.content.0.source.data").String() != "hello" || gjson.GetBytes(body, "messages.0.content.0.source.type").String() != "text" {
		t.Fatalf("native text normalization failed: %s %v", body, err)
	}
	if _, err := normalizeBase64TextSources([]byte(strings.Replace(string(raw), "aGVsbG8=", "!!!", 1))); err == nil {
		t.Fatal("corrupt base64 accepted")
	}
}

// TestNativeJSONDocument preserves HAR bytes while adapting their source to Anthropic text.
func TestNativeJSONDocument(t *testing.T) {
	for _, media := range []string{"application/json", "application/har+json"} {
		raw := []byte(`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"` + media + `","data":"eyJsb2ciOnt9fQ=="}}]}]}`)
		body, err := normalizeBase64TextSources(raw)
		source := gjson.GetBytes(body, "messages.0.content.0.source")
		if err != nil || source.Get("type").String() != "text" || source.Get("media_type").String() != "text/plain" || source.Get("data").String() != `{"log":{}}` {
			t.Fatalf("JSON normalization failed: %s %v", body, err)
		}
	}
}

// TestNormalizeBase64TextSources_AllocationScaling pins the allocation shape of the
// base64 text-document rewrite.
//
// The loop performs three whole-body sjson.SetBytes calls per document source, and each
// reserialises the entire request, so N text documents cost 3N copies of the body.
func TestNormalizeBase64TextSources_AllocationScaling(t *testing.T) {
	memtest.AssertAllocScaling(t, func(docs int) []byte {
		// A base64 text/plain document source is what the rewrite targets; the payload
		// grows with N because each document carries its own encoded body.
		payload := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("t", 300)))
		var b bytes.Buffer
		b.WriteString(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":[`)
		for i := range docs {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"type":"document","source":{"type":"base64","media_type":"text/plain","data":"`)
			b.WriteString(payload)
			b.WriteString(`"}}`)
		}
		b.WriteString(`]}]}`)
		return b.Bytes()
	}, func(body []byte) {
		if _, err := normalizeBase64TextSources(body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
