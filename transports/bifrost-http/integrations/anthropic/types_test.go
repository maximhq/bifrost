package anthropic

import (
    "testing"

    bifrost "github.com/maximhq/bifrost/core"
    schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestConvertToBifrostRequest(t *testing.T) {
    temp := 0.5
    req := ChatCompletionRequest{
        Model: "claude-test",
        Messages: []schemas.BifrostMessage{
            {Role: schemas.ModelChatMessageRoleUser, Content: bifrost.Ptr("hi")},
        },
        Temperature: &temp,
    }

    bfReq := req.ConvertToBifrostRequest("")

    if bfReq.Provider != schemas.Anthropic {
        t.Errorf("expected provider %s, got %s", schemas.Anthropic, bfReq.Provider)
    }
    if bfReq.Model != "claude-test" {
        t.Errorf("expected model claude-test, got %s", bfReq.Model)
    }
    if bfReq.Params == nil || bfReq.Params.Temperature == nil || *bfReq.Params.Temperature != temp {
        t.Errorf("temperature not copied")
    }
    if bfReq.Input.ChatCompletionInput == nil || len(*bfReq.Input.ChatCompletionInput) != 1 {
        t.Fatalf("expected 1 message, got %v", bfReq.Input.ChatCompletionInput)
    }
}

