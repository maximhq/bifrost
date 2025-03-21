package interfaces

import "context"

type Plugin interface {
	PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, error)
	PostHook(ctx *context.Context, result *BifrostResponse) (*BifrostResponse, error)
}
