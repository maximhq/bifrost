package anthropic

import (
	"encoding/base64"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"strings"
	"testing"
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
