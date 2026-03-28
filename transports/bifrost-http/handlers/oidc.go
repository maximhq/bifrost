package handlers

import (
	"context"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	oidcpkg "github.com/maximhq/bifrost/framework/oidc"
	"github.com/valyala/fasthttp"
)

// OIDCMiddleware creates a middleware that validates Keycloak OIDC JWT tokens
// and resolves governance entities (Customer/Team) from the claims.
// It slots BEFORE the existing AuthMiddleware in the chain per D-02.
//
// The configStore parameter is used for governance entity resolution (D-08):
//   - organization_id claim -> Customer lookup via configStore.GetCustomer()
//   - groups claim -> Team resolution via configStore.GetTeams()
//
// Flow:
//  1. If oidcProvider is nil (OIDC not configured per D-20): pass through to next
//  2. If no Authorization header: pass through (let AuthMiddleware handle)
//  3. If Bearer token is not a JWT (session UUID): pass through (let AuthMiddleware handle)
//  4. If Bearer token IS a JWT: validate via OIDC provider
//     - Valid: extract claims, resolve governance entities, set context keys (D-03), call next
//     - Invalid/expired: return 401 immediately (do NOT fall through per D-02)
//     - Valid but Customer not found: return 403 (D-08)
func OIDCMiddleware(oidcProvider *oidcpkg.OIDCProvider, configStore configstore.ConfigStore) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// Skip if OIDC not configured (D-20: backward-compatible)
			if oidcProvider == nil {
				next(ctx)
				return
			}

			// Extract Authorization header
			authorization := string(ctx.Request.Header.Peek("Authorization"))
			if authorization == "" {
				// No auth header -- let AuthMiddleware handle
				next(ctx)
				return
			}

			// Parse scheme and token
			scheme, token, found := strings.Cut(authorization, " ")
			if !found || !strings.EqualFold(scheme, "Bearer") {
				// Not Bearer auth (e.g., Basic) -- let AuthMiddleware handle
				next(ctx)
				return
			}

			// Check if token looks like a JWT (3 dot-separated segments)
			// Session UUIDs are 36-char strings with dashes -- they won't match
			if !oidcpkg.IsJWT(token) {
				// Not a JWT (likely session UUID) -- let AuthMiddleware handle
				next(ctx)
				return
			}

			// Token is a JWT -- validate via OIDC provider.
			// Use context.Background() because fasthttp.RequestCtx's context.Context
			// implementation depends on internal server state that may not be available,
			// and go-oidc's JWKS fetch needs a stable context (D-07).
			claims, err := oidcProvider.ValidateToken(context.Background(), token)
			if err != nil {
				// JWT validation failed (expired, bad signature, wrong audience)
				// Return 401 immediately -- do NOT fall through to AuthMiddleware
				if logger != nil {
					logger.Error("OIDC token validation failed: %v", err)
				}
				SendError(ctx, fasthttp.StatusUnauthorized, "Invalid or expired OIDC token")
				return
			}

			// JWT valid -- resolve governance entities per D-08
			// Look up Customer by organization_id claim (matched to Customer.ID)
			if claims.OrgID == "" {
				if logger != nil {
					logger.Error("OIDC token missing organization_id claim for sub=%s", claims.Subject)
				}
				SendError(ctx, fasthttp.StatusForbidden, "Missing organization_id claim")
				return
			}

			customer, err := configStore.GetCustomer(ctx, claims.OrgID)
			if err != nil || customer == nil {
				// D-08: v1 requires pre-provisioned governance entities.
				// Customer not found = 403 Forbidden (not 401 -- the user IS authenticated,
				// but their org is not provisioned in Bifrost governance)
				if logger != nil {
					logger.Error("OIDC: Customer not found for organization_id=%s sub=%s", claims.OrgID, claims.Subject)
				}
				SendError(ctx, fasthttp.StatusForbidden, "Organization not provisioned")
				return
			}

			// Resolve Teams from groups claim by name within the Customer (D-10)
			// Unmapped groups are silently skipped per D-10
			var resolvedTeamIDs []string
			if len(claims.Groups) > 0 && configStore != nil {
				teams, teamErr := configStore.GetTeams(ctx, customer.ID)
				if teamErr == nil {
					// Build name -> ID lookup for the customer's teams
					teamByName := make(map[string]string, len(teams))
					for _, t := range teams {
						teamByName[t.Name] = t.ID
					}
					for _, group := range claims.Groups {
						if teamID, ok := teamByName[group]; ok {
							resolvedTeamIDs = append(resolvedTeamIDs, teamID)
						}
						// D-10: unmapped groups silently skipped
					}
				}
				// If GetTeams fails, continue without team resolution
				// (teams are not strictly required for auth)
			}

			// Set OIDC context keys per D-03
			ctx.SetUserValue(oidcpkg.BifrostContextKeyOIDCAuthenticated, true)
			ctx.SetUserValue(oidcpkg.BifrostContextKeyOIDCSub, claims.Subject)
			ctx.SetUserValue(oidcpkg.BifrostContextKeyOIDCEmail, claims.Email)
			ctx.SetUserValue(oidcpkg.BifrostContextKeyOIDCOrgID, claims.OrgID)
			ctx.SetUserValue(oidcpkg.BifrostContextKeyOIDCGroups, claims.Groups)

			// Set resolved governance entity IDs for downstream governance plugin
			ctx.SetUserValue("BifrostContextKeyOIDCCustomerID", customer.ID)
			if len(resolvedTeamIDs) > 0 {
				ctx.SetUserValue("BifrostContextKeyOIDCTeamIDs", resolvedTeamIDs)
			}

			if logger != nil {
				logger.Info("OIDC auth: sub=%s email=%s org=%s customer=%s teams=%d",
					claims.Subject, claims.Email, claims.OrgID, customer.Name, len(resolvedTeamIDs))
			}

			next(ctx)
		}
	}
}
