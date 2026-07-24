package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestConvertMessages_PreservesAssistantImages is a regression test ensuring an assistant
// message's generated images (e.g. from a prior Gemini "Nano Banana" / gemini-*-image turn)
// survive both directions of Bifrost <-> OpenAI-wire-format message conversion, not just
// the initial response decode. Without this, replaying conversation history that includes a
// prior image-generation turn (either accepting it as an inbound OpenAI-format request, or
// forwarding it as history to an OpenAI-compatible upstream) would silently drop the images.
func TestConvertMessages_PreservesAssistantImages(t *testing.T) {
	image := schemas.ChatAssistantMessageImage{
		Type: "image_url",
		ImageURL: &schemas.ChatInputImage{
			URL: "data:image/png;base64,AAAA",
		},
	}

	t.Run("OpenAI wire -> Bifrost", func(t *testing.T) {
		openaiMessages := []OpenAIMessage{
			{
				Role: schemas.ChatMessageRoleAssistant,
				OpenAIChatAssistantMessage: &OpenAIChatAssistantMessage{
					Images: []schemas.ChatAssistantMessageImage{image},
				},
			},
		}

		bifrostMessages := ConvertOpenAIMessagesToBifrostMessages(openaiMessages)

		if len(bifrostMessages) != 1 || bifrostMessages[0].ChatAssistantMessage == nil {
			t.Fatalf("expected 1 assistant message with ChatAssistantMessage attached, got %#v", bifrostMessages)
		}
		if len(bifrostMessages[0].ChatAssistantMessage.Images) != 1 {
			t.Fatalf("expected 1 image preserved, got %#v", bifrostMessages[0].ChatAssistantMessage.Images)
		}
		if bifrostMessages[0].ChatAssistantMessage.Images[0].ImageURL.URL != image.ImageURL.URL {
			t.Fatalf("expected image URL preserved, got %#v", bifrostMessages[0].ChatAssistantMessage.Images[0])
		}
	})

	t.Run("Bifrost -> OpenAI wire", func(t *testing.T) {
		bifrostMessages := []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					Images: []schemas.ChatAssistantMessageImage{image},
				},
			},
		}

		openaiMessages := ConvertBifrostMessagesToOpenAIMessages(bifrostMessages)

		if len(openaiMessages) != 1 || openaiMessages[0].OpenAIChatAssistantMessage == nil {
			t.Fatalf("expected 1 assistant message with OpenAIChatAssistantMessage attached, got %#v", openaiMessages)
		}
		if len(openaiMessages[0].OpenAIChatAssistantMessage.Images) != 1 {
			t.Fatalf("expected 1 image preserved, got %#v", openaiMessages[0].OpenAIChatAssistantMessage.Images)
		}
		if openaiMessages[0].OpenAIChatAssistantMessage.Images[0].ImageURL.URL != image.ImageURL.URL {
			t.Fatalf("expected image URL preserved, got %#v", openaiMessages[0].OpenAIChatAssistantMessage.Images[0])
		}
	})
}
