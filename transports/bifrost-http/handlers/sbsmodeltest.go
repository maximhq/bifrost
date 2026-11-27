// SBS fork: "Test" button for models in the Model Catalog.
//
// POST /api/sbs/models/test {"provider": "openai", "model": "gpt-4o-mini"} sends one tiny request
// straight to the provider and reports whether the model answered, how long it took, and the
// provider's error if not. The request is exactly one user message: the plugin pipeline is
// skipped (no governance, logs, cost tracking, prompts or guardrails), and so are the MCP tools,
// MCP server instructions and provider-injected tools core would otherwise add. The body that
// went to the provider is returned as raw_request so the caller can check that.
// Embedding models (name contains "embed") get an embedding request instead of a chat one.
package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// sbsModelTestTimeout bounds one test; slow reasoning models still fit.
const sbsModelTestTimeout = 60 * time.Second

// SBSModelTestHandler serves the model test API.
type SBSModelTestHandler struct {
	client *bifrost.Bifrost
}

// NewSBSModelTestHandler returns the handler.
func NewSBSModelTestHandler(client *bifrost.Bifrost) *SBSModelTestHandler {
	return &SBSModelTestHandler{client: client}
}

// RegisterRoutes registers the model test route.
func (h *SBSModelTestHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.POST("/api/sbs/models/test", lib.ChainMiddlewares(h.testModel, middlewares...))
}

type sbsModelTestRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Prompt   string `json:"prompt,omitempty"`
	Mode     string `json:"mode,omitempty"` // "chat" or "embedding"; guessed from the model name when empty
}

type sbsModelTestResponse struct {
	OK         bool   `json:"ok"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Mode       string `json:"mode"`
	LatencyMs  int64  `json:"latency_ms"`
	Reply      string `json:"reply,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
	Tokens     int    `json:"tokens,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
	RawRequest any    `json:"raw_request,omitempty"` // the exact body sent to the provider
}

func (h *SBSModelTestHandler) testModel(ctx *fasthttp.RequestCtx) {
	var req sbsModelTestRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	if req.Provider == "" || req.Model == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider and model are required")
		return
	}
	if h.client == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "bifrost client not ready")
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "chat"
		if strings.Contains(strings.ToLower(req.Model), "embed") {
			mode = "embedding"
		}
	}
	prompt := req.Prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = "Reply with the single word: OK"
	}

	bctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), sbsModelTestTimeout)
	defer cancel()
	bctx.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true)
	// No MCP tools or server instructions: an empty include list admits no MCP client.
	bctx.SetValue(schemas.MCPContextKeyIncludeClients, []string{})
	bctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{})
	bctx.SetValue(schemas.BifrostContextKeyMCPInstructionsInjected, true)
	bctx.SetValue(bifrost.SBSContextKeySkipInjectedTools, true)
	// Hand back the wire body so the UI can show what was actually sent.
	bctx.SetValue(schemas.BifrostContextKeyAllowPerRequestRawOverride, true)
	bctx.SetValue(schemas.BifrostContextKeySendBackRawRequest, true)

	out := sbsModelTestResponse{Provider: req.Provider, Model: req.Model, Mode: mode}
	provider := schemas.ModelProvider(req.Provider)
	start := time.Now()
	var bErr *schemas.BifrostError

	switch mode {
	case "embedding":
		text := prompt
		resp, e := h.client.EmbeddingRequest(bctx, &schemas.BifrostEmbeddingRequest{
			Provider: provider,
			Model:    req.Model,
			Input: []schemas.EmbeddingInputItem{{
				Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}},
			}},
		})
		bErr = e
		if resp != nil {
			out.RawRequest = resp.ExtraFields.RawRequest
			if b, err := json.Marshal(resp); err == nil {
				var shape struct {
					Data []struct {
						Embedding []float64 `json:"embedding"`
					} `json:"data"`
				}
				if json.Unmarshal(b, &shape) == nil && len(shape.Data) > 0 {
					out.Dimensions = len(shape.Data[0].Embedding)
				}
			}
		}
	default:
		out.Mode = "chat"
		maxTokens := 64
		resp, e := h.client.ChatCompletionRequest(bctx, &schemas.BifrostChatRequest{
			Provider: provider,
			Model:    req.Model,
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &prompt},
			}},
			Params: &schemas.ChatParameters{MaxCompletionTokens: &maxTokens},
		})
		bErr = e
		if resp != nil {
			out.RawRequest = resp.ExtraFields.RawRequest
			for _, c := range resp.Choices {
				if c.ChatNonStreamResponseChoice != nil && c.Message != nil && c.Message.Content != nil {
					if c.Message.Content.ContentStr != nil {
						out.Reply = *c.Message.Content.ContentStr
					} else if b, err := json.Marshal(c.Message.Content); err == nil {
						out.Reply = string(b)
					}
					break
				}
			}
			if resp.Usage != nil {
				out.Tokens = resp.Usage.TotalTokens
			}
		}
	}
	out.LatencyMs = time.Since(start).Milliseconds()

	if bErr != nil {
		out.OK = false
		out.RawRequest = bErr.ExtraFields.RawRequest
		if bErr.StatusCode != nil {
			out.StatusCode = *bErr.StatusCode
		}
		if bErr.Error != nil {
			out.Error = bErr.Error.Message
			if out.Error == "" && bErr.Error.Error != nil {
				out.Error = bErr.Error.Error.Error()
			}
		}
		if out.Error == "" {
			out.Error = "request failed"
		}
	} else {
		out.OK = true
	}
	if len(out.Reply) > 500 {
		out.Reply = out.Reply[:500] + "…"
	}
	SendJSON(ctx, out)
}
