package integrations

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ExtensionRouter defines the interface that all integration routers must implement
// to register their routes with the main HTTP router.
type ExtensionRouter interface {
	RegisterRoutes(r *router.Router)
}

// RequestConverter is a function that converts integration-specific requests to Bifrost format.
// It takes the parsed request object and returns a BifrostRequest ready for processing.
type RequestConverter func(req interface{}) (*schemas.BifrostRequest, error)

// ResponseConverter is a function that converts Bifrost responses to integration-specific format.
// It takes a BifrostResponse and returns the format expected by the specific integration.
type ResponseConverter func(*schemas.BifrostResponse) (interface{}, error)

// PreRequestCallback is called before processing the request.
// It can be used to modify the request object (e.g., extract model from URL parameters)
// or perform validation. If it returns an error, the request processing stops.
type PreRequestCallback func(ctx *fasthttp.RequestCtx, req interface{}) error

// PostRequestCallback is called after processing the request but before sending the response.
// It can be used to modify the response or perform additional logging/metrics.
// If it returns an error, an error response is sent instead of the success response.
type PostRequestCallback func(ctx *fasthttp.RequestCtx, req interface{}, resp *schemas.BifrostResponse) error

// RouteConfig defines configuration for a single HTTP route in an integration.
// Each route specifies how to handle requests for a specific endpoint.
type RouteConfig struct {
	Path                   string              // HTTP path pattern (e.g., "/openai/v1/chat/completions")
	Method                 string              // HTTP method (POST, GET, PUT, DELETE)
	GetRequestTypeInstance func() interface{}  // Factory function to create request instance (SHOULD NOT BE NIL)
	RequestConverter       RequestConverter    // Function to convert request to BifrostRequest (SHOULD NOT BE NIL)
	ResponseConverter      ResponseConverter   // Function to convert BifrostResponse to integration format (SHOULD NOT BE NIL)
	PreCallback            PreRequestCallback  // Optional: called before request processing
	PostCallback           PostRequestCallback // Optional: called after request processing
}

// GenericRouter provides a reusable router implementation for all integrations.
// It handles the common flow of: parse request → convert to Bifrost → execute → convert response.
// Integration-specific logic is handled through the RouteConfig callbacks and converters.
type GenericRouter struct {
	client *bifrost.Bifrost // Bifrost client for executing requests
	routes []RouteConfig    // List of route configurations
}

// NewGenericRouter creates a new generic router with the given bifrost client and route configurations.
// Each integration should create their own routes and pass them to this constructor.
func NewGenericRouter(client *bifrost.Bifrost, routes []RouteConfig) *GenericRouter {
	return &GenericRouter{
		client: client,
		routes: routes,
	}
}

// RegisterRoutes registers all configured routes on the given fasthttp router.
// This method implements the ExtensionRouter interface.
func (g *GenericRouter) RegisterRoutes(r *router.Router) {
	for _, route := range g.routes {
		// Validate route configuration at startup to fail fast
		if route.GetRequestTypeInstance == nil {
			log.Println("[WARN] route configuration is invalid: GetRequestTypeInstance cannot be nil for route " + route.Path)
			continue
		}
		if route.RequestConverter == nil {
			log.Println("[WARN] route configuration is invalid: RequestConverter cannot be nil for route " + route.Path)
			continue
		}
		if route.ResponseConverter == nil {
			log.Println("[WARN] route configuration is invalid: ResponseConverter cannot be nil for route " + route.Path)
			continue
		}

		// Test that GetRequestTypeInstance returns a valid instance
		if testInstance := route.GetRequestTypeInstance(); testInstance == nil {
			log.Println("[WARN] route configuration is invalid: GetRequestTypeInstance returned nil for route " + route.Path)
			continue
		}

		handler := g.createHandler(route)
		switch strings.ToUpper(route.Method) {
		case fasthttp.MethodPost:
			r.POST(route.Path, handler)
		case fasthttp.MethodGet:
			r.GET(route.Path, handler)
		case fasthttp.MethodPut:
			r.PUT(route.Path, handler)
		case fasthttp.MethodDelete:
			r.DELETE(route.Path, handler)
		default:
			r.POST(route.Path, handler) // Default to POST
		}
	}
}

// createHandler creates a fasthttp handler for the given route configuration.
// The handler follows this flow:
// 1. Parse JSON request body into the configured request type (for methods that expect bodies)
// 2. Execute pre-callback (if configured) for request modification/validation
// 3. Convert request to BifrostRequest using the configured converter
// 4. Execute the request through Bifrost
// 5. Execute post-callback (if configured) for response modification
// 6. Convert and send the response using the configured response converter
func (g *GenericRouter) createHandler(config RouteConfig) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		// Parse request body into the integration-specific request type
		// Note: config validation is performed at startup in RegisterRoutes
		req := config.GetRequestTypeInstance()

		method := string(ctx.Method())

		if method != fasthttp.MethodGet && method != fasthttp.MethodDelete {
			// Use ctx.Request.Body() instead of ctx.PostBody() to support all HTTP methods
			body := ctx.Request.Body()
			if len(body) > 0 {
				if err := json.Unmarshal(body, req); err != nil {
					g.sendError(ctx, newBifrostError(err, "Invalid JSON"))
					return
				}
			}
		}

		// Execute pre-request callback if configured
		// This is typically used for extracting data from URL parameters
		// or performing request-specific validation
		if config.PreCallback != nil {
			if err := config.PreCallback(ctx, req); err != nil {
				g.sendError(ctx, newBifrostError(err, "failed to execute pre-request callback"))
				return
			}
		}

		// Convert the integration-specific request to Bifrost format
		bifrostReq, err := config.RequestConverter(req)
		if err != nil {
			g.sendError(ctx, newBifrostError(err, "failed to convert request to Bifrost format"))
			return
		}
		if bifrostReq == nil {
			g.sendError(ctx, newBifrostError(nil, "Invalid request"))
			return
		}
		if bifrostReq.Model == "" {
			g.sendError(ctx, newBifrostError(nil, "Model parameter is required"))
			return
		}

		// Execute the request through Bifrost
		bifrostCtx := lib.ConvertToBifrostContext(ctx)
		result, bifrostErr := g.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostErr)
			return
		}
		// Execute post-request callback if configured
		// This is typically used for response modification or additional processing
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, result); err != nil {
				g.sendError(ctx, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}

		if result == nil {
			g.sendError(ctx, newBifrostError(nil, "Bifrost response is nil after post-request callback"))
			return
		}

		// Convert Bifrost response to integration-specific format and send
		response, err := config.ResponseConverter(result)
		if err != nil {
			g.sendError(ctx, newBifrostError(err, "failed to encode response"))
			return
		}
		g.sendSuccess(ctx, response)
	}
}

// sendError sends an error response with the appropriate status code and JSON body.
// It handles different error types (string, error interface, or arbitrary objects).
func (g *GenericRouter) sendError(ctx *fasthttp.RequestCtx, err *schemas.BifrostError) {
	if err.StatusCode != nil {
		ctx.SetStatusCode(*err.StatusCode)
	} else {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
	}

	ctx.SetContentType("application/json")
	if encodeErr := json.NewEncoder(ctx).Encode(err); encodeErr != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetBodyString(fmt.Sprintf("failed to encode error response: %v", encodeErr))
	}
}

// sendSuccess sends a successful response with HTTP 200 status and JSON body.
func (g *GenericRouter) sendSuccess(ctx *fasthttp.RequestCtx, response interface{}) {
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")

	responseBody, err := json.Marshal(response)
	if err != nil {
		g.sendError(ctx, newBifrostError(err, "failed to encode response"))
		return
	}

	ctx.SetBody(responseBody)
}

// GetProviderFromModel determines the appropriate provider based on model name patterns
// This function uses comprehensive pattern matching to identify the correct provider
// for various model naming conventions used across different AI providers.
func GetProviderFromModel(model string) schemas.ModelProvider {
	// Normalize model name for case-insensitive matching
	modelLower := strings.ToLower(strings.TrimSpace(model))

	// Azure OpenAI Models - check first to prevent false positives from OpenAI "gpt" patterns
	if isAzureModel(modelLower) {
		return schemas.Azure
	}

	// OpenAI Models - comprehensive pattern matching
	if isOpenAIModel(modelLower) {
		return schemas.OpenAI
	}

	// Anthropic Models - Claude family
	if isAnthropicModel(modelLower) {
		return schemas.Anthropic
	}

	// Google Vertex AI Models - Gemini and Palm family
	if isVertexModel(modelLower) {
		return schemas.Vertex
	}

	// AWS Bedrock Models - various model providers through Bedrock
	if isBedrockModel(modelLower) {
		return schemas.Bedrock
	}

	// Cohere Models - Command and Embed family
	if isCohereModel(modelLower) {
		return schemas.Cohere
	}

	// Default to OpenAI for unknown models (most LiteLLM compatible)
	return schemas.OpenAI
}

// isOpenAIModel checks for OpenAI model patterns
func isOpenAIModel(model string) bool {
	// Exclude Azure models to prevent overlap
	if strings.Contains(model, "azure/") {
		return false
	}

	openaiPatterns := []string{
		"gpt", "davinci", "curie", "babbage", "ada", "o1", "o3", "o4",
		"text-embedding", "dall-e", "whisper", "tts", "chatgpt",
	}

	return matchesAnyPattern(model, openaiPatterns)
}

// isAzureModel checks for Azure OpenAI specific patterns
func isAzureModel(model string) bool {
	azurePatterns := []string{
		"azure", "model-router", "computer-use-preview",
	}

	return matchesAnyPattern(model, azurePatterns)
}

// isAnthropicModel checks for Anthropic Claude model patterns
func isAnthropicModel(model string) bool {
	anthropicPatterns := []string{
		"claude", "anthropic/",
	}

	return matchesAnyPattern(model, anthropicPatterns)
}

// isVertexModel checks for Google Vertex AI model patterns
func isVertexModel(model string) bool {
	vertexPatterns := []string{
		"gemini", "palm", "bison", "gecko", "vertex/", "google/",
	}

	return matchesAnyPattern(model, vertexPatterns)
}

// isBedrockModel checks for AWS Bedrock model patterns
func isBedrockModel(model string) bool {
	bedrockPatterns := []string{
		"bedrock", "bedrock.amazonaws.com/", "bedrock/",
		"amazon.titan", "amazon.nova", "aws/amazon.",
		"ai21.jamba", "ai21.j2", "aws/ai21.",
		"meta.llama", "aws/meta.",
		"stability.stable-diffusion", "stability.sd3", "aws/stability.",
		"anthropic.claude", "aws/anthropic.",
		"cohere.command", "cohere.embed", "aws/cohere.",
		"mistral.mistral", "mistral.mixtral", "aws/mistral.",
		"titan-text", "titan-embed", "nova-micro", "nova-lite", "nova-pro",
		"jamba-instruct", "j2-ultra", "j2-mid",
		"llama-2", "llama-3", "llama-3.1", "llama-3.2",
		"stable-diffusion-xl", "sd3-large",
	}

	return matchesAnyPattern(model, bedrockPatterns)
}

// isCohereModel checks for Cohere model patterns
func isCohereModel(model string) bool {
	coherePatterns := []string{
		"command-", "embed-", "cohere",
	}

	return matchesAnyPattern(model, coherePatterns)
}

// matchesAnyPattern checks if the model matches any of the given patterns
func matchesAnyPattern(model string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(model, pattern) {
			return true
		}
	}
	return false
}

// newBifrostError wraps a standard error into a BifrostError with IsBifrostError set to false.
// This helper function reduces code duplication when handling non-Bifrost errors.
func newBifrostError(err error, message string) *schemas.BifrostError {
	if err == nil {
		return &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: message,
			},
		}
	}

	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: schemas.ErrorField{
			Message: message,
			Error:   err,
		},
	}
}
