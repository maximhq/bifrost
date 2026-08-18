package schemas

import (
	"testing"
)

// Regression tests for #6184: input_video chat content parts (llama.cpp-style
// OpenAI-compatible backends — input_video.data raw base64 or input_video.url)
// lost their payload passing through Bifrost. The type survived unmarshal but
// the body was silently dropped, so the backend received a video part with no
// data and rejected the request.

func TestChatContentBlock_InputVideoWireRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"data", `{"role":"user","content":[{"type":"input_video","input_video":{"data":"AAAAGGZ0eXBpc29t"}},{"type":"text","text":"describe this video"}]}`},
		{"url", `{"role":"user","content":[{"type":"input_video","input_video":{"url":"https://example.com/clip.mp4"}},{"type":"text","text":"describe this video"}]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var msg ChatMessage
			if err := Unmarshal([]byte(tc.in), &msg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			blocks := msg.Content.ContentBlocks
			if len(blocks) != 2 || blocks[0].Type != ChatContentBlockTypeInputVideo {
				t.Fatalf("expected input_video block first, got %+v", blocks)
			}
			if blocks[0].InputVideo == nil {
				t.Fatalf("input_video payload dropped on unmarshal")
			}

			out, err := Marshal(&msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var back ChatMessage
			if err := Unmarshal(out, &back); err != nil {
				t.Fatalf("re-unmarshal: %v", err)
			}
			got := back.Content.ContentBlocks[0].InputVideo
			if got == nil {
				t.Fatalf("input_video payload dropped on marshal round-trip: %s", out)
			}
			want := blocks[0].InputVideo
			if (want.Data == nil) != (got.Data == nil) || (want.Data != nil && *want.Data != *got.Data) {
				t.Errorf("data not preserved: want %v got %v", want.Data, got.Data)
			}
			if (want.URL == nil) != (got.URL == nil) || (want.URL != nil && *want.URL != *got.URL) {
				t.Errorf("url not preserved: want %v got %v", want.URL, got.URL)
			}
		})
	}
}

func TestChatContentBlock_InputVideoBridgeRoundTrip(t *testing.T) {
	data := "AAAAGGZ0eXBpc29t"
	text := "describe this video"
	msg := ChatMessage{
		Role: ChatMessageRoleUser,
		Content: &ChatMessageContent{
			ContentBlocks: []ChatContentBlock{
				{Type: ChatContentBlockTypeInputVideo, InputVideo: &ChatInputVideo{Data: &data}},
				{Type: ChatContentBlockTypeText, Text: &text},
			},
		},
	}

	rms := msg.ToResponsesMessages()
	if len(rms) != 1 || rms[0].Content == nil {
		t.Fatalf("unexpected responses messages: %+v", rms)
	}
	rBlocks := rms[0].Content.ContentBlocks
	if len(rBlocks) != 2 || rBlocks[0].Type != ResponsesInputMessageContentBlockTypeVideo {
		t.Fatalf("expected input_video responses block first, got %+v", rBlocks)
	}
	if rBlocks[0].Video == nil || rBlocks[0].Video.Data == nil || *rBlocks[0].Video.Data != data {
		t.Fatalf("video payload lost converting chat -> responses: %+v", rBlocks[0])
	}

	back := ToChatMessages(rms)
	if len(back) != 1 || back[0].Content == nil {
		t.Fatalf("unexpected chat messages: %+v", back)
	}
	cBlocks := back[0].Content.ContentBlocks
	if len(cBlocks) != 2 || cBlocks[0].Type != ChatContentBlockTypeInputVideo {
		t.Fatalf("expected input_video chat block first, got %+v", cBlocks)
	}
	if cBlocks[0].InputVideo == nil || cBlocks[0].InputVideo.Data == nil || *cBlocks[0].InputVideo.Data != data {
		t.Fatalf("video payload lost converting responses -> chat: %+v", cBlocks[0])
	}
}

// Tool-output messages convert through two separate paths
// (ToResponsesToolMessage and the function_call_output branch of
// ToResponsesMessages) — both must carry input_video like regular messages do.
func TestChatContentBlock_InputVideoToolOutputPaths(t *testing.T) {
	data := "AAAAGGZ0eXBpc29t"
	url := "https://example.com/clip.mp4"
	callID := "call_1"
	msg := ChatMessage{
		Role:            ChatMessageRoleTool,
		ChatToolMessage: &ChatToolMessage{ToolCallID: &callID},
		Content: &ChatMessageContent{
			ContentBlocks: []ChatContentBlock{
				{Type: ChatContentBlockTypeInputVideo, InputVideo: &ChatInputVideo{Data: &data}},
				{Type: ChatContentBlockTypeInputVideo, InputVideo: &ChatInputVideo{URL: &url}},
			},
		},
	}

	checkBlocks := func(t *testing.T, path string, out *ResponsesToolMessageOutputStruct) {
		t.Helper()
		if out == nil || len(out.ResponsesFunctionToolCallOutputBlocks) != 2 {
			t.Fatalf("%s: unexpected tool output: %+v", path, out)
		}
		blocks := out.ResponsesFunctionToolCallOutputBlocks
		if blocks[0].Video == nil || blocks[0].Video.Data == nil || *blocks[0].Video.Data != data {
			t.Fatalf("%s: video data payload lost: %+v", path, blocks[0])
		}
		if blocks[1].Video == nil || blocks[1].Video.URL == nil || *blocks[1].Video.URL != url {
			t.Fatalf("%s: video url payload lost: %+v", path, blocks[1])
		}
	}

	rm := msg.ToResponsesToolMessage()
	if rm == nil || rm.ResponsesToolMessage == nil {
		t.Fatalf("ToResponsesToolMessage returned no tool message: %+v", rm)
	}
	checkBlocks(t, "ToResponsesToolMessage", rm.ResponsesToolMessage.Output)

	rms := msg.ToResponsesMessages()
	if len(rms) != 1 || rms[0].ResponsesToolMessage == nil {
		t.Fatalf("ToResponsesMessages returned no tool message: %+v", rms)
	}
	checkBlocks(t, "ToResponsesMessages", rms[0].ResponsesToolMessage.Output)
}
