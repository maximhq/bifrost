package mistral

import (
    "testing"

    bifrost "github.com/maximhq/bifrost/core"
    schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestConvertToBifrostRequest(t *testing.T) {
    temp := 0.5
    req := ChatCompletionRequest{
        Model: "mistral-test",
        Messages: []schemas.BifrostMessage{
            {Role: schemas.ModelChatMessageRoleUser, Content: bifrost.Ptr("hi")},
        },
        Temperature: &temp,
    }

    bfReq := req.ConvertToBifrostRequest("override")

    if bfReq.Provider != schemas.Mistral {
        t.Errorf("expected provider %s, got %s", schemas.Mistral, bfReq.Provider)
    }
    if bfReq.Model != "override" {
        t.Errorf("expected model override, got %s", bfReq.Model)
    }
    if bfReq.Params == nil || bfReq.Params.Temperature == nil || *bfReq.Params.Temperature != temp {
        t.Errorf("temperature not copied")
    }
    if bfReq.Input.ChatCompletionInput == nil || len(*bfReq.Input.ChatCompletionInput) != 1 {
        t.Fatalf("expected 1 message, got %v", bfReq.Input.ChatCompletionInput)
    }
}

