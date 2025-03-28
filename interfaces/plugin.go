package interfaces

import "context"

type RequestInput struct {
	TextInput *string
	ChatInput *[]Message
}

type BifrostRequest struct {
	Model  string
	Input  RequestInput
	Params *ModelParameters
}

type Plugin interface {
	PreHook(ctx context.Context, req *BifrostRequest) (context.Context, *BifrostRequest, error)
	PostHook(ctx context.Context, result *BifrostResponse) (*BifrostResponse, error)
}
