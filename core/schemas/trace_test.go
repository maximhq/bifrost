package schemas

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestTraceGetSpanNilSafe(t *testing.T) {
	var nilTrace *Trace
	if span := nilTrace.GetSpan("span"); span != nil {
		t.Fatalf("nil trace GetSpan returned %v, want nil", span)
	}

	trace := &Trace{Spans: []*Span{nil, &Span{SpanID: "target"}}}
	if span := trace.GetSpan(""); span != nil {
		t.Fatalf("empty span ID = %v, want nil", span)
	}
	if span := trace.GetSpan("missing"); span != nil {
		t.Fatalf("missing span = %v, want nil", span)
	}
	if span := trace.GetSpan("target"); span == nil || span.SpanID != "target" {
		t.Fatalf("target span = %v, want target", span)
	}
}

func TestTraceAndSpanNilMutatorsNoop(t *testing.T) {
	trace := &Trace{}
	trace.AddSpan(nil)
	if len(trace.Spans) != 0 {
		t.Fatalf("nil span was appended: %v", trace.Spans)
	}

	var nilTrace *Trace
	nilTrace.AddSpan(&Span{SpanID: "ignored"})

	var nilSpan *Span
	nilSpan.SetAttribute("key", "value")
	nilSpan.AddEvent(SpanEvent{Name: "event"})
	nilSpan.End(SpanStatusOk, "")
}

func sptr(s string) *string { return &s }

// TestAttachmentSummaryBoundsInlinePayloads verifies that a data: URL becomes a
// sized reference by default and is only copied when the caller opts in.
func TestAttachmentSummaryBoundsInlinePayloads(t *testing.T) {
	msg := ChatMessage{
		Role: ChatMessageRoleUser,
		Content: &ChatMessageContent{ContentBlocks: []ChatContentBlock{
			{Type: ChatContentBlockTypeText, Text: sptr("what is this")},
			{Type: ChatContentBlockTypeImage, ImageURLStruct: &ChatInputImage{
				URL: "data:image/png;base64,aGVsbG93b3JsZA==", Detail: sptr("high"),
			}},
			{Type: ChatContentBlockTypeImage, ImageURLStruct: &ChatInputImage{
				URL: "https://example.com/cat.png",
			}},
		}},
	}

	got := ExtractMessageSummary(&msg, AttachmentOptions{})
	if got.Content != "what is this" {
		t.Errorf("Content = %q, want text blocks only", got.Content)
	}
	if len(got.Attachments) != 2 {
		t.Fatalf("Attachments = %d, want 2", len(got.Attachments))
	}

	inline := got.Attachments[0]
	if !inline.Inline || inline.Data != "" {
		t.Errorf("inline attachment Data = %q, want empty by default", inline.Data)
	}
	if inline.MediaType != "image/png" || inline.ByteSize == 0 || inline.Detail != "high" {
		t.Errorf("inline attachment = %#v, want media type, size and detail", inline)
	}

	ref := got.Attachments[1]
	if ref.Inline || ref.URL != "https://example.com/cat.png" {
		t.Errorf("reference attachment = %#v, want the URL carried verbatim", ref)
	}

	opted := ExtractMessageSummary(&msg, AttachmentOptions{Inline: true})
	if opted.Attachments[0].Data == "" {
		t.Error("opted-in attachment Data is empty, want the payload copied")
	}

	capped := ExtractMessageSummary(&msg, AttachmentOptions{Inline: true, Cap: 4})
	if capped.Attachments[0].Data != "" {
		t.Error("attachment over cap was copied, want size only")
	}
}

// RFC 2397 makes ";base64" optional; without it the payload is percent-encoded.
// The code assumed every data URL was base64, so non-base64 URLs landed
// percent-encoded text in Data (which is base64 by contract) with a wrong size.
func TestDataURLPayloadsAreNormalized(t *testing.T) {
	for _, tc := range []struct {
		name, url, wantData string
		wantSize            int
	}{
		{"base64, one pad", "data:image/png;base64,aGVsbG8=", "aGVsbG8=", 5},
		{"base64, two pads", "data:image/png;base64,aGk=", "aGk=", 2},
		{"percent-encoded", "data:text/plain;charset=utf-8,Hello%20World", "SGVsbG8gV29ybGQ=", 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &AttachmentSummary{}
			applyPayload(a, tc.url, AttachmentOptions{Inline: true})
			if a.ByteSize != tc.wantSize {
				t.Errorf("ByteSize = %d, want %d", a.ByteSize, tc.wantSize)
			}
			if a.Data != tc.wantData {
				t.Errorf("Data = %q, want %q (must be base64)", a.Data, tc.wantData)
			}
			if !a.Inline {
				t.Error("Inline = false for a data: URL")
			}
			if a.URL != "" {
				t.Errorf("URL = %q; a data: URL must never land in the reference-only field", a.URL)
			}
		})
	}

	// ParseDataURL requires a media type, so "data:,..." is unparseable. It must
	// still never land in the reference-only URL field.
	a0 := &AttachmentSummary{}
	applyPayload(a0, "data:,A%20brief%20note", AttachmentOptions{Inline: true})
	if a0.URL != "" {
		t.Errorf("unparseable data: URL leaked into the reference field: %q", a0.URL)
	}

	// A real reference still goes to URL, untouched.
	a := &AttachmentSummary{}
	applyPayload(a, "https://example.com/x.png", AttachmentOptions{Inline: true})
	if a.URL != "https://example.com/x.png" || a.Inline || a.Data != "" {
		t.Errorf("reference URL mishandled: %#v", a)
	}
}

// Exact decoded size without allocating: base64.DecodedLen over-reports padded
// payloads, and decoding to measure would copy the whole payload on the span path.
func TestBase64DecodedSizeIsExact(t *testing.T) {
	for _, raw := range []string{"", "h", "he", "hel", "hello", "helloworld", strings.Repeat("x", 4096)} {
		payload := base64.StdEncoding.EncodeToString([]byte(raw))
		if got := base64DecodedSize(payload); got != len(raw) {
			t.Errorf("base64DecodedSize(%d-char payload) = %d, want %d", len(payload), got, len(raw))
		}
	}
	if got := base64DecodedSize("===="); got < 0 {
		t.Errorf("padding-only = %d, want >= 0", got)
	}
}
