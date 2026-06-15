package mcp

import (
	"fmt"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGetToolDefinitionReturnsConcurrentSafeSnapshot(t *testing.T) {
	name := "weather"
	description := "Get weather"
	manager := &MCPManager{
		logger: defaultLogger,
		clientMap: map[string]*schemas.MCPClientState{
			"client-1": {
				Name:    "client-1",
				ToolMap: make(map[string]schemas.ChatTool),
			},
		},
	}
	manager.SetClientTools("client-1", map[string]schemas.ChatTool{
		name: {
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        name,
				Description: &description,
			},
		},
	}, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 100 {
			desc := fmt.Sprintf("Get weather %d", i)
			manager.SetClientTools("client-1", map[string]schemas.ChatTool{
				name: {
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name:        name,
						Description: &desc,
					},
				},
			}, nil)
		}
	}()
	go func() {
		defer wg.Done()
		for range 100 {
			tool := manager.GetToolDefinition(name)
			if tool == nil || tool.Function == nil || tool.Function.Description == nil {
				t.Error("resolved tool definition was not populated")
				return
			}
		}
	}()
	wg.Wait()

	tool := manager.GetToolDefinition(name)
	if tool == nil || tool.Function == nil || tool.Function.Description == nil {
		t.Fatal("resolved tool definition was not populated")
	}
	*tool.Function.Description = "mutated"
	stored := manager.GetToolDefinition(name)
	if stored == nil || stored.Function == nil || stored.Function.Description == nil || *stored.Function.Description == "mutated" {
		t.Fatal("resolved tool definition shares state with the manager")
	}
}

func TestResetMCPRequestClearsToolDefinition(t *testing.T) {
	request := &schemas.BifrostMCPRequest{
		ToolDefinition: &schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name: "weather",
			},
		},
	}

	resetMCPRequest(request)
	if request.ToolDefinition != nil {
		t.Fatal("resetMCPRequest did not clear tool definition")
	}
}
