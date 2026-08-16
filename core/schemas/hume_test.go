package schemas

import (
	"strings"
	"testing"
)

func TestBifrostChatRequestDoesNotSerializeIntegrationMetadata(t *testing.T) {
	content := "hello"
	humeMetadata := &HumeChatRequestMetadata{
		CustomSessionID: "session-123",
		Messages: map[int]HumeMessageMetadata{0: {
			Time:          &HumeMessageTime{Begin: Ptr(0.0), End: Ptr(100.0)},
			ProsodyScores: map[string]float64{"Joy": 0.9},
		}},
	}
	request := &BifrostChatRequest{
		Provider: OpenAI,
		Model:    "gpt-4o-mini",
		Input: []ChatMessage{{
			Role:    ChatMessageRoleUser,
			Content: &ChatMessageContent{ContentStr: &content},
		}},
		IntegrationMetadata: humeMetadata,
	}
	got, ok := GetChatIntegrationMetadata[*HumeChatRequestMetadata](request)
	if !ok || got != humeMetadata {
		t.Fatalf("GetChatIntegrationMetadata() = (%p, %t), want (%p, true)", got, ok, humeMetadata)
	}

	encoded, err := MarshalSorted(request)
	if err != nil {
		t.Fatalf("MarshalSorted() error = %v", err)
	}
	for _, forbidden := range []string{"Hume", "hume", "session-123", "prosody", "Joy"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("serialized request leaked Hume metadata %q: %s", forbidden, encoded)
		}
	}
}
