package schemas

import (
	"strings"
	"testing"
)

func TestBifrostChatRequestDoesNotSerializeHumeMetadata(t *testing.T) {
	content := "hello"
	request := &BifrostChatRequest{
		Provider: OpenAI,
		Model:    "gpt-4o-mini",
		Input: []ChatMessage{{
			Role:    ChatMessageRoleUser,
			Content: &ChatMessageContent{ContentStr: &content},
		}},
		HumeMetadata: &HumeChatRequestMetadata{
			CustomSessionID: "session-123",
			Messages: map[int]HumeMessageMetadata{0: {
				Time:          &HumeMessageTime{Begin: Ptr(0.0), End: Ptr(100.0)},
				ProsodyScores: map[string]float64{"Joy": 0.9},
			}},
		},
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
