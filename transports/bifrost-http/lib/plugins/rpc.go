package plugins

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"net/rpc"

	"github.com/hashicorp/go-plugin"
	"github.com/maximhq/bifrost/core/schemas"
)

func init() {
	// Register only generic types with gob for RPC serialization
	gob.Register(map[string]interface{}{})
	gob.Register([]interface{}{})
}

// Convert schema types to map[string]interface{} for gob serialization
func schemaToMap(v interface{}) (map[string]interface{}, error) {
	if v == nil {
		return nil, nil
	}

	// Convert to JSON first, then to map
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	err = json.Unmarshal(jsonBytes, &result)
	return result, err
}

// Convert map[string]interface{} back to schema type
func mapToSchema(m map[string]interface{}, target interface{}) error {
	if m == nil {
		return nil
	}

	// Convert to JSON first, then to target type
	jsonBytes, err := json.Marshal(m)
	if err != nil {
		return err
	}

	return json.Unmarshal(jsonBytes, target)
}

// PluginRPCClient is an implementation of schemas.Plugin that talks over RPC.
type PluginRPCClient struct {
	client *rpc.Client
}

// GetName calls the GetName method over RPC
func (c *PluginRPCClient) GetName() string {
	fmt.Printf("[DEBUG] RPC Client GetName called\n")
	var resp string
	err := c.client.Call("Plugin.GetName", new(interface{}), &resp)
	fmt.Printf("[DEBUG] RPC Client GetName returned: resp='%s', err=%v\n", resp, err)
	if err != nil {
		return "unknown-plugin"
	}
	return resp
}

// SetLogger is a no-op for RPC plugins since logger can't be serialized
func (c *PluginRPCClient) SetLogger(logger schemas.Logger) {
	// Logger injection is handled at the host level
}

// PreHook calls the PreHook method over RPC
func (c *PluginRPCClient) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	// Convert request to map
	reqMap, err := schemaToMap(req)
	if err != nil {
		return req, nil, fmt.Errorf("failed to serialize request: %w", err)
	}

	args := map[string]interface{}{
		"Request": reqMap,
	}
	var resp map[string]interface{}

	err = c.client.Call("Plugin.PreHook", args, &resp)
	if err != nil {
		return req, nil, err
	}

	// Extract response values
	modifiedReq := req
	if resp["Request"] != nil {
		if reqMap, ok := resp["Request"].(map[string]interface{}); ok {
			var newReq schemas.BifrostRequest
			if err := mapToSchema(reqMap, &newReq); err == nil {
				modifiedReq = &newReq
			}
		}
	}

	var shortCircuit *schemas.PluginShortCircuit
	if resp["ShortCircuit"] != nil {
		if scMap, ok := resp["ShortCircuit"].(map[string]interface{}); ok {
			var sc schemas.PluginShortCircuit
			if err := mapToSchema(scMap, &sc); err == nil {
				shortCircuit = &sc
			}
		}
	}

	var hookErr error
	if resp["Error"] != nil {
		if errStr, ok := resp["Error"].(string); ok && errStr != "" {
			hookErr = fmt.Errorf("%s", errStr)
		}
	}

	return modifiedReq, shortCircuit, hookErr
}

// PostHook calls the PostHook method over RPC
func (c *PluginRPCClient) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	// Convert to maps
	resultMap, _ := schemaToMap(result)
	errMap, _ := schemaToMap(err)

	args := map[string]interface{}{
		"Result": resultMap,
		"Error":  errMap,
	}
	var resp map[string]interface{}
	rpcErr := c.client.Call("Plugin.PostHook", args, &resp)
	if rpcErr != nil {
		return result, err, rpcErr
	}

	// Extract response values
	modifiedResult := result
	if resp["Result"] != nil {
		if resultMap, ok := resp["Result"].(map[string]interface{}); ok {
			var newResult schemas.BifrostResponse
			if mapErr := mapToSchema(resultMap, &newResult); mapErr == nil {
				modifiedResult = &newResult
			}
		}
	}

	modifiedErr := err
	if resp["Error"] != nil {
		if errMap, ok := resp["Error"].(map[string]interface{}); ok {
			var newErr schemas.BifrostError
			if mapErr := mapToSchema(errMap, &newErr); mapErr == nil {
				modifiedErr = &newErr
			}
		}
	}

	var hookErr error
	if resp["HookError"] != nil {
		if errStr, ok := resp["HookError"].(string); ok && errStr != "" {
			hookErr = fmt.Errorf("%s", errStr)
		}
	}

	return modifiedResult, modifiedErr, hookErr
}

// Cleanup calls the Cleanup method over RPC
func (c *PluginRPCClient) Cleanup() error {
	var resp string
	err := c.client.Call("Plugin.Cleanup", new(interface{}), &resp)
	if err != nil {
		return err
	}
	if resp != "" {
		return fmt.Errorf("%s", resp)
	}
	return nil
}

// PluginRPCServer is the RPC server that PluginRPCClient talks to, conforming to
// the requirements of net/rpc
type PluginRPCServer struct {
	// This is the real implementation
	Impl schemas.Plugin
}

func (s *PluginRPCServer) GetName(args interface{}, resp *string) error {
	*resp = s.Impl.GetName()
	return nil
}

func (s *PluginRPCServer) PreHook(args map[string]interface{}, resp *map[string]interface{}) error {
	// Extract request from args
	reqMap, ok := args["Request"].(map[string]interface{})
	if !ok {
		*resp = map[string]interface{}{"Error": "Invalid request type"}
		return nil
	}

	// Convert map to BifrostRequest
	var req schemas.BifrostRequest
	if err := mapToSchema(reqMap, &req); err != nil {
		*resp = map[string]interface{}{"Error": "Failed to deserialize request: " + err.Error()}
		return nil
	}

	// Create a dummy context since we can't serialize it
	ctx := context.Background()

	modifiedReq, shortCircuit, err := s.Impl.PreHook(&ctx, &req)

	// Build response - convert back to maps
	response := make(map[string]interface{})

	if modifiedReq != nil {
		if reqMap, mapErr := schemaToMap(modifiedReq); mapErr == nil {
			response["Request"] = reqMap
		}
	}

	// Always set ShortCircuit field, even if nil
	if shortCircuit != nil {
		if scMap, mapErr := schemaToMap(shortCircuit); mapErr == nil {
			response["ShortCircuit"] = scMap
		} else {
			response["ShortCircuit"] = nil
		}
	} else {
		response["ShortCircuit"] = nil
	}

	if err != nil {
		response["Error"] = err.Error()
	} else {
		response["Error"] = ""
	}

	*resp = response
	return nil
}

func (s *PluginRPCServer) PostHook(args map[string]interface{}, resp *map[string]interface{}) error {
	// Extract arguments and convert from maps
	var result *schemas.BifrostResponse
	var bifrostErr *schemas.BifrostError

	if args["Result"] != nil {
		if resultMap, ok := args["Result"].(map[string]interface{}); ok {
			var res schemas.BifrostResponse
			if err := mapToSchema(resultMap, &res); err == nil {
				result = &res
			}
		}
	}

	if args["Error"] != nil {
		if errMap, ok := args["Error"].(map[string]interface{}); ok {
			var bErr schemas.BifrostError
			if err := mapToSchema(errMap, &bErr); err == nil {
				bifrostErr = &bErr
			}
		}
	}

	// Create a dummy context since we can't serialize it
	ctx := context.Background()

	modifiedResult, modifiedErr, hookErr := s.Impl.PostHook(&ctx, result, bifrostErr)

	// Build response - convert back to maps
	response := make(map[string]interface{})

	if modifiedResult != nil {
		if resultMap, mapErr := schemaToMap(modifiedResult); mapErr == nil {
			response["Result"] = resultMap
		}
	}

	if modifiedErr != nil {
		if errMap, mapErr := schemaToMap(modifiedErr); mapErr == nil {
			response["Error"] = errMap
		}
	}

	if hookErr != nil {
		response["HookError"] = hookErr.Error()
	} else {
		response["HookError"] = ""
	}

	*resp = response
	return nil
}

func (s *PluginRPCServer) Cleanup(args interface{}, resp *string) error {
	err := s.Impl.Cleanup()
	if err != nil {
		*resp = err.Error()
	} else {
		*resp = ""
	}
	return nil
}

// This is the implementation of plugin.Plugin so we can serve/consume this
type PluginPlugin struct {
	// Impl Injection
	Impl schemas.Plugin
}

func (p *PluginPlugin) Server(*plugin.MuxBroker) (interface{}, error) {
	return &PluginRPCServer{Impl: p.Impl}, nil
}

func (PluginPlugin) Client(b *plugin.MuxBroker, c *rpc.Client) (interface{}, error) {
	return &PluginRPCClient{client: c}, nil
}

// ServePlugin serves a plugin implementation over RPC
func ServePlugin(impl schemas.Plugin) {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: plugin.HandshakeConfig{
			ProtocolVersion:  1,
			MagicCookieKey:   "BIFROST_PLUGIN",
			MagicCookieValue: "bifrost",
		},
		Plugins: map[string]plugin.Plugin{
			"plugin": &PluginPlugin{Impl: impl},
		},
	})
}
