package sdk

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// BifrostRPCServer is the RPC server that BifrostRPC talks to, conforming to
// the requirements of net/rpc
type BifrostRPCServer struct {
	// This is the real implementation
	Impl schemas.Plugin
}

// Initialize handles the Initialize RPC call to configure the plugin
func (s *BifrostRPCServer) Initialize(args *InitializeArgs, resp *InitializeReply) error {
	// If the plugin has a NewPlugin method (implements PluginFactory pattern),
	// we need to create a new instance with the provided config

	// For now, we'll assume the plugin is already created and this is a no-op
	// In the future, we could extend this to support dynamic reconfiguration
	resp.Success = true
	return nil
}

// GetName handles the GetName RPC call
func (s *BifrostRPCServer) GetName(args *GetNameArgs, resp *GetNameReply) error {
	resp.Name = s.Impl.GetName()
	return nil
}

// SetLogger handles the SetLogger RPC call (placeholder)
func (s *BifrostRPCServer) SetLogger(args *SetLoggerArgs, resp *SetLoggerReply) error {
	// Logger injection is handled at the host level
	// This is a placeholder for future logger RPC support
	resp.Success = true
	return nil
}

// PreHook handles the PreHook RPC call
func (s *BifrostRPCServer) PreHook(args *PreHookArgs, resp *PreHookReply) error {
	// Create a new context for the plugin call since we don't serialize context
	ctx := context.Background()
	req, shortCircuit, err := s.Impl.PreHook(&ctx, args.Req)
	resp.Req = req
	resp.ShortCircuit = shortCircuit
	resp.Err = err
	return nil
}

// PostHook handles the PostHook RPC call
func (s *BifrostRPCServer) PostHook(args *PostHookArgs, resp *PostHookReply) error {
	// Create a new context for the plugin call since we don't serialize context
	ctx := context.Background()
	result, err, error := s.Impl.PostHook(&ctx, args.Result, args.Err)
	resp.Result = result
	resp.Err = err
	resp.Error = error
	return nil
}

// Cleanup handles the Cleanup RPC call
func (s *BifrostRPCServer) Cleanup(args *CleanupArgs, resp *CleanupReply) error {
	resp.Err = s.Impl.Cleanup()
	return nil
}

// PluginFactoryRPCServer wraps a plugin factory and handles configuration via RPC
type PluginFactoryRPCServer struct {
	Factory       schemas.PluginConstructor
	CreatedPlugin schemas.Plugin
}

// Initialize handles plugin creation with configuration
func (s *PluginFactoryRPCServer) Initialize(args *InitializeArgs, resp *InitializeReply) error {
	plugin, err := s.Factory(args.Config)
	if err != nil {
		resp.Success = false
		resp.Error = err.Error()
		return nil
	}

	s.CreatedPlugin = plugin
	resp.Success = true
	return nil
}

// GetName handles the GetName RPC call for factory-created plugins
func (s *PluginFactoryRPCServer) GetName(args *GetNameArgs, resp *GetNameReply) error {
	if s.CreatedPlugin == nil {
		resp.Name = "uninitialized-plugin"
		return nil
	}
	resp.Name = s.CreatedPlugin.GetName()
	return nil
}

// SetLogger handles the SetLogger RPC call for factory-created plugins
func (s *PluginFactoryRPCServer) SetLogger(args *SetLoggerArgs, resp *SetLoggerReply) error {
	if s.CreatedPlugin != nil {
		// Logger injection is handled at the host level
		s.CreatedPlugin.SetLogger(nil) // Placeholder
	}
	resp.Success = true
	return nil
}

// PreHook handles the PreHook RPC call for factory-created plugins
func (s *PluginFactoryRPCServer) PreHook(args *PreHookArgs, resp *PreHookReply) error {
	if s.CreatedPlugin == nil {
		return fmt.Errorf("plugin not initialized")
	}

	ctx := context.Background()
	req, shortCircuit, err := s.CreatedPlugin.PreHook(&ctx, args.Req)
	resp.Req = req
	resp.ShortCircuit = shortCircuit
	resp.Err = err
	return nil
}

// PostHook handles the PostHook RPC call for factory-created plugins
func (s *PluginFactoryRPCServer) PostHook(args *PostHookArgs, resp *PostHookReply) error {
	if s.CreatedPlugin == nil {
		return fmt.Errorf("plugin not initialized")
	}

	ctx := context.Background()
	result, err, error := s.CreatedPlugin.PostHook(&ctx, args.Result, args.Err)
	resp.Result = result
	resp.Err = err
	resp.Error = error
	return nil
}

// Cleanup handles the Cleanup RPC call for factory-created plugins
func (s *PluginFactoryRPCServer) Cleanup(args *CleanupArgs, resp *CleanupReply) error {
	if s.CreatedPlugin == nil {
		resp.Err = nil
		return nil
	}
	resp.Err = s.CreatedPlugin.Cleanup()
	return nil
}
