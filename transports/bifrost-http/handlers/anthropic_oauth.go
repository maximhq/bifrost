// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains Anthropic OAuth authentication flow handlers for Claude Pro/Max.
package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// AnthropicOAuthHandler manages HTTP requests for Anthropic OAuth operations
type AnthropicOAuthHandler struct {
	oauthProvider *oauth2.OAuth2Provider
}

// NewAnthropicOAuthHandler creates a new Anthropic OAuth handler instance
func NewAnthropicOAuthHandler(oauthProvider *oauth2.OAuth2Provider) *AnthropicOAuthHandler {
	return &AnthropicOAuthHandler{
		oauthProvider: oauthProvider,
	}
}

// RegisterRoutes registers all Anthropic OAuth-related routes
func (h *AnthropicOAuthHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	if h.oauthProvider == nil {
		return
	}
	r.POST("/api/anthropic-oauth/initiate", lib.ChainMiddlewares(h.handleInitiate, middlewares...))
	r.POST("/api/anthropic-oauth/exchange", lib.ChainMiddlewares(h.handleExchange, middlewares...))
	r.GET("/api/anthropic-oauth/status", lib.ChainMiddlewares(h.handleStatus, middlewares...))
	r.POST("/api/anthropic-oauth/refresh", lib.ChainMiddlewares(h.handleRefresh, middlewares...))
	r.POST("/api/anthropic-oauth/logout", lib.ChainMiddlewares(h.handleLogout, middlewares...))
}

// handleInitiate initiates an Anthropic OAuth PKCE flow
// POST /api/anthropic-oauth/initiate
func (h *AnthropicOAuthHandler) handleInitiate(ctx *fasthttp.RequestCtx) {
	flowInitiation, err := h.oauthProvider.InitiateAnthropicOAuthFlow(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to initiate Anthropic OAuth flow: %v", err))
		return
	}

	SendJSONRaw(ctx, map[string]interface{}{
		"authorize_url":   flowInitiation.AuthorizeURL,
		"oauth_config_id": flowInitiation.OauthConfigID,
		"expires_at":      flowInitiation.ExpiresAt,
	})
}

// anthropicOAuthExchangeRequest represents the request to exchange an authorization code
type anthropicOAuthExchangeRequest struct {
	Code          string `json:"code"`
	OAuthConfigID string `json:"oauth_config_id"`
}

// handleExchange exchanges an Anthropic authorization code for tokens
// POST /api/anthropic-oauth/exchange
func (h *AnthropicOAuthHandler) handleExchange(ctx *fasthttp.RequestCtx) {
	var req anthropicOAuthExchangeRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	if req.Code == "" || req.OAuthConfigID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "code and oauth_config_id are required")
		return
	}

	if err := h.oauthProvider.CompleteAnthropicOAuthFlow(ctx, req.Code, req.OAuthConfigID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to exchange code: %v", err))
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"status": "success",
	})
}

// handleStatus checks the status of an Anthropic OAuth configuration
// GET /api/anthropic-oauth/status?oauth_config_id=xxx
func (h *AnthropicOAuthHandler) handleStatus(ctx *fasthttp.RequestCtx) {
	oauthConfigID := string(ctx.QueryArgs().Peek("oauth_config_id"))
	if oauthConfigID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "oauth_config_id query parameter is required")
		return
	}

	valid, err := h.oauthProvider.ValidateToken(ctx, oauthConfigID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to validate token: %v", err))
		return
	}

	status := "invalid"
	if valid {
		status = "valid"
	}

	SendJSON(ctx, map[string]interface{}{
		"oauth_config_id": oauthConfigID,
		"status":          status,
	})
}

// anthropicOAuthRefreshRequest represents the request to refresh a token
type anthropicOAuthRefreshRequest struct {
	OAuthConfigID string `json:"oauth_config_id"`
}

// handleRefresh refreshes an Anthropic OAuth token
// POST /api/anthropic-oauth/refresh
func (h *AnthropicOAuthHandler) handleRefresh(ctx *fasthttp.RequestCtx) {
	var req anthropicOAuthRefreshRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	if req.OAuthConfigID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "oauth_config_id is required")
		return
	}

	if err := h.oauthProvider.RefreshAccessToken(ctx, req.OAuthConfigID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to refresh token: %v", err))
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"status": "success",
	})
}

// anthropicOAuthLogoutRequest represents the request to revoke a token
type anthropicOAuthLogoutRequest struct {
	OAuthConfigID string `json:"oauth_config_id"`
}

// handleLogout revokes an Anthropic OAuth token
// POST /api/anthropic-oauth/logout
func (h *AnthropicOAuthHandler) handleLogout(ctx *fasthttp.RequestCtx) {
	var req anthropicOAuthLogoutRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	if req.OAuthConfigID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "oauth_config_id is required")
		return
	}

	if err := h.oauthProvider.RevokeToken(ctx, req.OAuthConfigID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to revoke token: %v", err))
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"status": "success",
	})
}
