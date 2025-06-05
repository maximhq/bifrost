package langgraph

import (
	"encoding/json"

	"github.com/maximhq/bifrost/core/schemas"
)

// LangGraph core types

// LangGraphNode represents a node in a LangGraph workflow
type LangGraphNode struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"` // "chat", "completion", "tool", "conditional"
	Model      string                 `json:"model"`
	Provider   *string                `json:"provider,omitempty"`
	Parameters map[string]interface{} `json:"parameters,omitempty"`
	Prompt     *string                `json:"prompt,omitempty"`
	Tools      *[]LangGraphTool       `json:"tools,omitempty"`
	Condition  *string                `json:"condition,omitempty"` // For conditional nodes
}

// LangGraphEdge represents an edge connecting nodes in the graph
type LangGraphEdge struct {
	From      string  `json:"from"`
	To        string  `json:"to"`
	Condition *string `json:"condition,omitempty"` // Optional condition for conditional edges
	Transform *string `json:"transform,omitempty"` // Optional data transformation
}

// LangGraphTool represents a tool available to graph nodes
type LangGraphTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	ArgsSchema  map[string]interface{} `json:"args_schema"`
	Function    *string                `json:"function,omitempty"` // Function reference
}

// LangGraphDefinition represents a complete graph workflow
type LangGraphDefinition struct {
	Name        string                 `json:"name"`
	Description *string                `json:"description,omitempty"`
	Nodes       []LangGraphNode        `json:"nodes"`
	Edges       []LangGraphEdge        `json:"edges"`
	StartNode   string                 `json:"start_node"`
	EndNodes    []string               `json:"end_nodes"`
	Variables   map[string]interface{} `json:"variables,omitempty"`
}

// Request types

// LangGraphInvokeRequest represents a request to execute a graph
type LangGraphInvokeRequest struct {
	Graph    *LangGraphDefinition   `json:"graph"`
	Input    interface{}            `json:"input"`
	Config   map[string]interface{} `json:"config,omitempty"`
	ThreadID *string                `json:"thread_id,omitempty"` // For conversation threading
}

// LangGraphStreamRequest represents a streaming graph execution request
type LangGraphStreamRequest struct {
	Graph      *LangGraphDefinition   `json:"graph"`
	Input      interface{}            `json:"input"`
	Config     map[string]interface{} `json:"config,omitempty"`
	StreamMode *string                `json:"stream_mode,omitempty"` // "values", "updates", "debug"
}

// LangGraphBatchRequest represents a batch graph execution request
type LangGraphBatchRequest struct {
	Inputs []LangGraphInvokeRequest `json:"inputs"`
	Config map[string]interface{}   `json:"config,omitempty"`
}

// LangGraphCreateRequest represents a request to create/store a graph
type LangGraphCreateRequest struct {
	Graph    LangGraphDefinition    `json:"graph"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// LangGraphExecuteRequest represents a request to execute a stored graph
type LangGraphExecuteRequest struct {
	GraphID string                 `json:"graph_id"`
	Input   interface{}            `json:"input"`
	Config  map[string]interface{} `json:"config,omitempty"`
}

// Response types

// LangGraphInvokeResponse represents the response from graph execution
type LangGraphInvokeResponse struct {
	Output   interface{}            `json:"output"`
	State    map[string]interface{} `json:"state,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Usage    *LangGraphUsage        `json:"usage,omitempty"`
	Error    string                 `json:"error,omitempty"`
	ThreadID *string                `json:"thread_id,omitempty"`
}

// LangGraphBatchResponse represents a batch execution response
type LangGraphBatchResponse struct {
	Results []LangGraphInvokeResponse `json:"results"`
}

// LangGraphCreateResponse represents the response from creating a graph
type LangGraphCreateResponse struct {
	GraphID string              `json:"graph_id"`
	Status  string              `json:"status"`
	Message string              `json:"message"`
	Graph   LangGraphDefinition `json:"graph"`
}

// LangGraphStreamEvent represents a single event in a stream
type LangGraphStreamEvent struct {
	Event    string                 `json:"event"` // "on_node_start", "on_node_end", "on_edge", etc.
	NodeID   *string                `json:"node_id,omitempty"`
	Data     interface{}            `json:"data"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// LangGraphUsage represents usage information for graph execution
type LangGraphUsage struct {
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	TotalTokens   int `json:"total_tokens"`
	NodesExecuted int `json:"nodes_executed"`
	ToolCallsMade int `json:"tool_calls_made,omitempty"`
}

// State management types

// LangGraphState represents the current state of a graph execution
type LangGraphState struct {
	CurrentNode string                 `json:"current_node"`
	Variables   map[string]interface{} `json:"variables"`
	History     []LangGraphStep        `json:"history"`
	ThreadID    *string                `json:"thread_id,omitempty"`
}

// LangGraphStep represents a single execution step
type LangGraphStep struct {
	NodeID    string          `json:"node_id"`
	Input     interface{}     `json:"input"`
	Output    interface{}     `json:"output"`
	Timestamp string          `json:"timestamp"`
	Duration  *float64        `json:"duration,omitempty"` // In milliseconds
	Usage     *LangGraphUsage `json:"usage,omitempty"`
}

// ConvertToBifrostRequest converts a graph invoke request to a simplified Bifrost request
// This is used when the graph has only a single node or for simplified execution
func (r *LangGraphInvokeRequest) ConvertToBifrostRequest() *schemas.BifrostRequest {
	if r.Graph == nil || len(r.Graph.Nodes) == 0 {
		return nil
	}

	// Use the first node for simplified conversion
	node := r.Graph.Nodes[0]

	provider := schemas.OpenAI // Default
	if node.Provider != nil {
		provider = schemas.ModelProvider(*node.Provider)
	}

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    node.Model,
	}

	// Convert input based on node type and input
	if node.Type == "chat" {
		// Handle chat input
		var messages []schemas.BifrostMessage

		if inputStr, ok := r.Input.(string); ok {
			// Simple string input
			messages = []schemas.BifrostMessage{
				{
					Role:    schemas.ModelChatMessageRoleUser,
					Content: &inputStr,
				},
			}
		} else if inputMap, ok := r.Input.(map[string]interface{}); ok {
			// Structured input
			if messagesArray, ok := inputMap["messages"].([]interface{}); ok {
				for _, msgInterface := range messagesArray {
					if msgMap, ok := msgInterface.(map[string]interface{}); ok {
						msg := schemas.BifrostMessage{
							Role: schemas.ModelChatMessageRoleUser,
						}
						if content, ok := msgMap["content"].(string); ok {
							msg.Content = &content
						}
						if role, ok := msgMap["role"].(string); ok {
							msg.Role = schemas.ModelChatMessageRole(role)
						}
						messages = append(messages, msg)
					}
				}
			} else if content, ok := inputMap["content"].(string); ok {
				messages = []schemas.BifrostMessage{
					{
						Role:    schemas.ModelChatMessageRoleUser,
						Content: &content,
					},
				}
			}
		}

		if len(messages) == 0 {
			// Fallback
			defaultContent := "Start conversation"
			messages = []schemas.BifrostMessage{
				{
					Role:    schemas.ModelChatMessageRoleUser,
					Content: &defaultContent,
				},
			}
		}

		bifrostReq.Input = schemas.RequestInput{
			ChatCompletionInput: &messages,
		}
	} else {
		// Text completion
		var prompt string
		if inputStr, ok := r.Input.(string); ok {
			prompt = inputStr
		} else if inputMap, ok := r.Input.(map[string]interface{}); ok {
			if promptStr, ok := inputMap["prompt"].(string); ok {
				prompt = promptStr
			} else if contentStr, ok := inputMap["content"].(string); ok {
				prompt = contentStr
			} else {
				prompt = "Generate text"
			}
		} else {
			prompt = "Generate text"
		}

		bifrostReq.Input = schemas.RequestInput{
			TextCompletionInput: &prompt,
		}
	}

	// Convert parameters
	if len(node.Parameters) > 0 || r.Config != nil {
		params := &schemas.ModelParameters{}

		// Apply node parameters
		if temp, ok := node.Parameters["temperature"].(float64); ok {
			params.Temperature = &temp
		}
		if maxTokens, ok := node.Parameters["max_tokens"].(float64); ok {
			maxTokensInt := int(maxTokens)
			params.MaxTokens = &maxTokensInt
		}
		if topP, ok := node.Parameters["top_p"].(float64); ok {
			params.TopP = &topP
		}

		// Apply config parameters (override node parameters)
		if r.Config != nil {
			if temp, ok := r.Config["temperature"].(float64); ok {
				params.Temperature = &temp
			}
			if maxTokens, ok := r.Config["max_tokens"].(float64); ok {
				maxTokensInt := int(maxTokens)
				params.MaxTokens = &maxTokensInt
			}
			if topP, ok := r.Config["top_p"].(float64); ok {
				params.TopP = &topP
			}
		}

		bifrostReq.Params = params
	}

	// Convert tools if available
	if node.Tools != nil {
		tools := []schemas.Tool{}
		for _, tool := range *node.Tools {
			// Convert args_schema to FunctionParameters
			params := schemas.FunctionParameters{
				Type: "object",
			}
			if tool.ArgsSchema != nil {
				if typeVal, ok := tool.ArgsSchema["type"].(string); ok {
					params.Type = typeVal
				}
				if desc, ok := tool.ArgsSchema["description"].(string); ok {
					params.Description = &desc
				}
				if required, ok := tool.ArgsSchema["required"].([]interface{}); ok {
					reqStrings := make([]string, len(required))
					for i, req := range required {
						if reqStr, ok := req.(string); ok {
							reqStrings[i] = reqStr
						}
					}
					params.Required = reqStrings
				}
				if properties, ok := tool.ArgsSchema["properties"].(map[string]interface{}); ok {
					params.Properties = properties
				}
				if enum, ok := tool.ArgsSchema["enum"].([]interface{}); ok {
					enumStrings := make([]string, len(enum))
					for i, e := range enum {
						if eStr, ok := e.(string); ok {
							enumStrings[i] = eStr
						}
					}
					params.Enum = &enumStrings
				}
			}

			t := schemas.Tool{
				Type: "function",
				Function: schemas.Function{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  params,
				},
			}
			tools = append(tools, t)
		}
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ModelParameters{}
		}
		bifrostReq.Params.Tools = &tools
	}

	return bifrostReq
}

// Helper functions for graph execution

// GetStartNode returns the starting node of the graph
func (g *LangGraphDefinition) GetStartNode() *LangGraphNode {
	for _, node := range g.Nodes {
		if node.ID == g.StartNode {
			return &node
		}
	}
	return nil
}

// GetNode returns a node by ID
func (g *LangGraphDefinition) GetNode(id string) *LangGraphNode {
	for _, node := range g.Nodes {
		if node.ID == id {
			return &node
		}
	}
	return nil
}

// GetNextNodes returns the next nodes connected to the given node
func (g *LangGraphDefinition) GetNextNodes(nodeID string) []LangGraphNode {
	var nextNodes []LangGraphNode
	for _, edge := range g.Edges {
		if edge.From == nodeID {
			if nextNode := g.GetNode(edge.To); nextNode != nil {
				nextNodes = append(nextNodes, *nextNode)
			}
		}
	}
	return nextNodes
}

// IsEndNode checks if a node is an end node
func (g *LangGraphDefinition) IsEndNode(nodeID string) bool {
	for _, endNodeID := range g.EndNodes {
		if endNodeID == nodeID {
			return true
		}
	}
	return false
}

// Validate checks if the graph definition is valid
func (g *LangGraphDefinition) Validate() error {
	// Check if start node exists
	if g.GetStartNode() == nil {
		return json.NewEncoder(nil).Encode(map[string]string{"error": "Start node not found"})
	}

	// Check if all referenced nodes in edges exist
	for _, edge := range g.Edges {
		if g.GetNode(edge.From) == nil {
			return json.NewEncoder(nil).Encode(map[string]string{"error": "Edge references non-existent 'from' node: " + edge.From})
		}
		if g.GetNode(edge.To) == nil {
			return json.NewEncoder(nil).Encode(map[string]string{"error": "Edge references non-existent 'to' node: " + edge.To})
		}
	}

	// Check if end nodes exist
	for _, endNodeID := range g.EndNodes {
		if g.GetNode(endNodeID) == nil {
			return json.NewEncoder(nil).Encode(map[string]string{"error": "End node not found: " + endNodeID})
		}
	}

	return nil
}
