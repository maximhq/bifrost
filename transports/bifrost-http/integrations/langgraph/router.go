package langgraph

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// LangGraphRouter holds route registrations for LangGraph endpoints.
type LangGraphRouter struct {
	client *bifrost.Bifrost
}

// NewLangGraphRouter creates a new LangGraphRouter with the given bifrost client.
func NewLangGraphRouter(client *bifrost.Bifrost) *LangGraphRouter {
	return &LangGraphRouter{client: client}
}

// RegisterRoutes registers all LangGraph routes on the given router.
func (lg *LangGraphRouter) RegisterRoutes(r *router.Router) {
	r.POST("/langgraph/invoke", lg.handleInvoke)
	r.POST("/langgraph/stream", lg.handleStream)
	r.POST("/langgraph/batch", lg.handleBatch)
	r.POST("/langgraph/astream", lg.handleAsyncStream)
	r.POST("/langgraph/graph/create", lg.handleCreateGraph)
	r.POST("/langgraph/graph/execute", lg.handleExecuteGraph)
}

// handleInvoke handles POST /langgraph/invoke - execute a graph
func (lg *LangGraphRouter) handleInvoke(ctx *fasthttp.RequestCtx) {
	var req LangGraphInvokeRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		errResponse := map[string]string{"error": err.Error()}
		jsonBytes, _ := json.Marshal(errResponse)
		ctx.SetBody(jsonBytes)
		return
	}

	response := lg.executeGraph(ctx, &req)

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	jsonBytes, _ := json.Marshal(response)
	ctx.SetBody(jsonBytes)
}

// handleStream handles POST /langgraph/stream - streaming graph execution
func (lg *LangGraphRouter) handleStream(ctx *fasthttp.RequestCtx) {
	var req LangGraphStreamRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		errResponse := map[string]string{"error": err.Error()}
		jsonBytes, _ := json.Marshal(errResponse)
		ctx.SetBody(jsonBytes)
		return
	}

	// For now, we'll execute synchronously and return the final result
	// In production, you'd implement streaming using Server-Sent Events
	invokeReq := LangGraphInvokeRequest{
		Graph:  req.Graph,
		Input:  req.Input,
		Config: req.Config,
	}

	response := lg.executeGraph(ctx, &invokeReq)

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	json.NewEncoder(ctx).Encode(response)
}

// handleBatch handles POST /langgraph/batch - batch graph execution
func (lg *LangGraphRouter) handleBatch(ctx *fasthttp.RequestCtx) {
	var req LangGraphBatchRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		json.NewEncoder(ctx).Encode(map[string]string{"error": err.Error()})
		return
	}

	var responses []LangGraphInvokeResponse
	for _, input := range req.Inputs {
		response := lg.executeGraph(ctx, &input)
		responses = append(responses, *response)
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	json.NewEncoder(ctx).Encode(LangGraphBatchResponse{
		Results: responses,
	})
}

// handleAsyncStream handles POST /langgraph/astream - async streaming
func (lg *LangGraphRouter) handleAsyncStream(ctx *fasthttp.RequestCtx) {
	// For simplicity, redirect to regular stream
	lg.handleStream(ctx)
}

// handleCreateGraph handles POST /langgraph/graph/create - create a graph definition
func (lg *LangGraphRouter) handleCreateGraph(ctx *fasthttp.RequestCtx) {
	var req LangGraphCreateRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		json.NewEncoder(ctx).Encode(map[string]string{"error": err.Error()})
		return
	}

	// In production, you'd store the graph definition in a database
	response := LangGraphCreateResponse{
		GraphID: generateGraphID(),
		Status:  "created",
		Message: "Graph created successfully",
		Graph:   req.Graph,
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	json.NewEncoder(ctx).Encode(response)
}

// handleExecuteGraph handles POST /langgraph/graph/execute - execute a stored graph
func (lg *LangGraphRouter) handleExecuteGraph(ctx *fasthttp.RequestCtx) {
	var req LangGraphExecuteRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		json.NewEncoder(ctx).Encode(map[string]string{"error": err.Error()})
		return
	}

	// In production, you'd load the graph from storage using req.GraphID
	// For now, we'll create a simple mock execution
	response := &LangGraphInvokeResponse{
		Output: map[string]interface{}{
			"result":   "Graph execution completed",
			"graph_id": req.GraphID,
		},
		Metadata: map[string]interface{}{
			"execution_id": generateExecutionID(),
			"status":       "completed",
		},
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	json.NewEncoder(ctx).Encode(response)
}

// executeGraph executes a graph by processing nodes sequentially
func (lg *LangGraphRouter) executeGraph(ctx *fasthttp.RequestCtx, req *LangGraphInvokeRequest) *LangGraphInvokeResponse {
	if req.Graph == nil {
		return &LangGraphInvokeResponse{
			Error: "Graph definition is required",
		}
	}

	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	// Start with the initial input
	currentState := req.Input
	var finalOutput interface{}
	var totalUsage *LangGraphUsage

	// Execute nodes in sequence (simplified graph execution)
	for _, node := range req.Graph.Nodes {
		// Convert node to bifrost request
		bifrostReq := lg.nodeToRequest(node, currentState, req.Config)

		var result *schemas.BifrostResponse
		var err *schemas.BifrostError

		// Execute based on node type
		if node.Type == "chat" {
			result, err = lg.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
		} else {
			result, err = lg.client.TextCompletionRequest(*bifrostCtx, bifrostReq)
		}

		if err != nil {
			return &LangGraphInvokeResponse{
				Error: err.Error.Message,
			}
		}

		// Update state with result
		if len(result.Choices) > 0 && result.Choices[0].Message.Content != nil {
			currentState = map[string]interface{}{
				"content":        *result.Choices[0].Message.Content,
				"previous_state": currentState,
			}
			finalOutput = currentState
		}

		// Accumulate usage
		if result.Usage != (schemas.LLMUsage{}) {
			if totalUsage == nil {
				totalUsage = &LangGraphUsage{}
			}
			totalUsage.InputTokens += result.Usage.PromptTokens
			totalUsage.OutputTokens += result.Usage.CompletionTokens
			totalUsage.TotalTokens += result.Usage.TotalTokens
		}
	}

	return &LangGraphInvokeResponse{
		Output: finalOutput,
		Metadata: map[string]interface{}{
			"execution_id":   generateExecutionID(),
			"nodes_executed": len(req.Graph.Nodes),
		},
		Usage: totalUsage,
	}
}

// nodeToRequest converts a graph node to a Bifrost request
func (lg *LangGraphRouter) nodeToRequest(node LangGraphNode, state interface{}, config map[string]interface{}) *schemas.BifrostRequest {
	provider := schemas.OpenAI // Default
	if node.Provider != nil {
		provider = schemas.ModelProvider(*node.Provider)
	}

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    node.Model,
	}

	// Convert input based on node type
	if node.Type == "chat" {
		// Create a user message from the current state
		var content string
		if stateMap, ok := state.(map[string]interface{}); ok {
			if contentStr, ok := stateMap["content"].(string); ok {
				content = contentStr
			} else {
				content = "Continue the conversation"
			}
		} else if stateStr, ok := state.(string); ok {
			content = stateStr
		} else {
			content = "Start conversation"
		}

		messages := []schemas.BifrostMessage{
			{
				Role:    schemas.ModelChatMessageRoleUser,
				Content: &content,
			},
		}

		bifrostReq.Input = schemas.RequestInput{
			ChatCompletionInput: &messages,
		}
	} else {
		// Text completion
		var prompt string
		if stateMap, ok := state.(map[string]interface{}); ok {
			if contentStr, ok := stateMap["content"].(string); ok {
				prompt = contentStr
			} else {
				prompt = "Complete the following:"
			}
		} else if stateStr, ok := state.(string); ok {
			prompt = stateStr
		} else {
			prompt = "Generate text"
		}

		bifrostReq.Input = schemas.RequestInput{
			TextCompletionInput: &prompt,
		}
	}

	// Apply node-specific parameters
	if len(node.Parameters) > 0 || config != nil {
		params := &schemas.ModelParameters{}

		// Apply node parameters
		if temp, ok := node.Parameters["temperature"].(float64); ok {
			params.Temperature = &temp
		}
		if maxTokens, ok := node.Parameters["max_tokens"].(float64); ok {
			maxTokensInt := int(maxTokens)
			params.MaxTokens = &maxTokensInt
		}

		// Apply config parameters
		if config != nil {
			if temp, ok := config["temperature"].(float64); ok {
				params.Temperature = &temp
			}
			if maxTokens, ok := config["max_tokens"].(float64); ok {
				maxTokensInt := int(maxTokens)
				params.MaxTokens = &maxTokensInt
			}
		}

		bifrostReq.Params = params
	}

	return bifrostReq
}

// Helper functions
func generateGraphID() string {
	// In production, use UUID or similar
	return "graph_" + randomString(8)
}

func generateExecutionID() string {
	// In production, use UUID or similar
	return "exec_" + randomString(8)
}

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[i%len(charset)]
	}
	return string(b)
}
