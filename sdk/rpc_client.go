package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/rpc"

	"github.com/maximhq/bifrost/core/schemas"
)

// BifrostRPC is an implementation of schemas.Plugin that talks over RPC.
type BifrostRPC struct {
	client *rpc.Client
}

// GetNameArgs represents the arguments for GetName RPC call
type GetNameArgs struct{}

// GetNameReply represents the reply for GetName RPC call
type GetNameReply struct {
	Name string
}

// InitializeArgs represents the arguments for Initialize RPC call
type InitializeArgs struct {
	Config json.RawMessage
}

// InitializeReply represents the reply for Initialize RPC call
type InitializeReply struct {
	Success bool
	Error   string
}

// SetLoggerArgs represents the arguments for SetLogger RPC call
type SetLoggerArgs struct {
	// We can't serialize Logger interface over RPC, so we'll handle this differently
	// For now, this is a placeholder - logger injection will be handled at the host level
}

// SetLoggerReply represents the reply for SetLogger RPC call
type SetLoggerReply struct {
	Success bool
}

// PreHookArgs represents the arguments for PreHook RPC call
// Note: We don't serialize context as it doesn't work well with gob encoding
type PreHookArgs struct {
	Req *schemas.BifrostRequest
}

// PreHookReply represents the reply for PreHook RPC call
type PreHookReply struct {
	Req          *schemas.BifrostRequest
	ShortCircuit *schemas.PluginShortCircuit
	Err          error
}

// PostHookArgs represents the arguments for PostHook RPC call
// Note: We don't serialize context as it doesn't work well with gob encoding
type PostHookArgs struct {
	Result *schemas.BifrostResponse
	Err    *schemas.BifrostError
}

// PostHookReply represents the reply for PostHook RPC call
type PostHookReply struct {
	Result *schemas.BifrostResponse
	Err    *schemas.BifrostError
	Error  error
}

// CleanupArgs represents the arguments for Cleanup RPC call
type CleanupArgs struct{}

// CleanupReply represents the reply for Cleanup RPC call
type CleanupReply struct {
	Err error
}

// Initialize calls the Initialize method over RPC to configure the plugin
func (g *BifrostRPC) Initialize(config json.RawMessage) error {
	var resp InitializeReply
	err := g.client.Call("Plugin.Initialize", &InitializeArgs{
		Config: config,
	}, &resp)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("plugin initialization failed: %s", resp.Error)
	}
	return nil
}

// GetName calls the GetName method over RPC
func (g *BifrostRPC) GetName() string {
	var resp GetNameReply
	err := g.client.Call("Plugin.GetName", new(GetNameArgs), &resp)
	if err != nil {
		// If RPC fails, return a default name
		return "unknown-plugin"
	}
	return resp.Name
}

// SetLogger is a no-op for RPC plugins since logger can't be serialized
// Logger injection is handled at the host level after plugin creation
func (g *BifrostRPC) SetLogger(logger schemas.Logger) {
	// Logger injection is handled at the host level
	// RPC plugins will receive a logger through other means if needed
}

// PreHook calls the PreHook method over RPC
func (g *BifrostRPC) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	var resp PreHookReply
	err := g.client.Call("Plugin.PreHook", &PreHookArgs{
		Req: req,
	}, &resp)
	if err != nil {
		return req, nil, err
	}
	return resp.Req, resp.ShortCircuit, resp.Err
}

// PostHook calls the PostHook method over RPC
func (g *BifrostRPC) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	var resp PostHookReply
	rpcErr := g.client.Call("Plugin.PostHook", &PostHookArgs{
		Result: result,
		Err:    err,
	}, &resp)
	if rpcErr != nil {
		return result, err, rpcErr
	}
	return resp.Result, resp.Err, resp.Error
}

// Cleanup calls the Cleanup method over RPC
func (g *BifrostRPC) Cleanup() error {
	var resp CleanupReply
	err := g.client.Call("Plugin.Cleanup", new(CleanupArgs), &resp)
	if err != nil {
		return err
	}
	return resp.Err
}
