package schemas

import (
	"encoding/json"
	"testing"
)

// TestChatMessage_UnmarshalJSON_PreservesAssistantImages is a regression test for
// message.images (e.g. from OpenRouter/Gemini image-generation models such as
// "Nano Banana") being silently dropped during response normalization. It verifies
// the OpenAI-compatible "images" array on an assistant message round-trips onto
// ChatAssistantMessage.Images.
func TestChatMessage_UnmarshalJSON_PreservesAssistantImages(t *testing.T) {
	raw := []byte(`{
		"role": "assistant",
		"content": "Here's your cat!",
		"images": [
			{
				"type": "image_url",
				"image_url": {
					"url": "data:image/png;base64,AAAA"
				}
			}
		]
	}`)

	var msg ChatMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unexpected error unmarshalling message: %v", err)
	}

	if msg.ChatAssistantMessage == nil {
		t.Fatal("expected ChatAssistantMessage attached to carry images")
	}
	if len(msg.ChatAssistantMessage.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(msg.ChatAssistantMessage.Images))
	}
	img := msg.ChatAssistantMessage.Images[0]
	if img.Type != "image_url" {
		t.Fatalf("expected type %q, got %q", "image_url", img.Type)
	}
	if img.ImageURL == nil || img.ImageURL.URL != "data:image/png;base64,AAAA" {
		t.Fatalf("expected image_url.url to be preserved, got %#v", img.ImageURL)
	}
}

// TestChatMessage_UnmarshalJSON_ImagesOnlyAssistantMessage verifies an assistant
// message consisting only of "images" (no text/refusal/tool_calls/etc.) still
// attaches ChatAssistantMessage instead of being dropped entirely.
func TestChatMessage_UnmarshalJSON_ImagesOnlyAssistantMessage(t *testing.T) {
	raw := []byte(`{
		"role": "assistant",
		"images": [
			{"type": "image_url", "image_url": {"url": "data:image/png;base64,BBBB"}}
		]
	}`)

	var msg ChatMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unexpected error unmarshalling message: %v", err)
	}

	if msg.ChatAssistantMessage == nil {
		t.Fatal("expected ChatAssistantMessage attached even with only images populated")
	}
	if len(msg.ChatAssistantMessage.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(msg.ChatAssistantMessage.Images))
	}
}
