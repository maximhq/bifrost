//go:build !dev

package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
)

// DevPprofHandler is a no-op stub for production builds (built without the "dev" tag).
type DevPprofHandler struct{}

// IsDevMode always returns false in production builds.
func IsDevMode() bool { return false }

// NewDevPprofHandler returns nil in production builds.
func NewDevPprofHandler() *DevPprofHandler { return nil }

// RegisterRoutes is a no-op in production builds.
func (h *DevPprofHandler) RegisterRoutes(_ *router.Router, _ ...schemas.BifrostHTTPMiddleware) {}

// Cleanup is a no-op in production builds.
func (h *DevPprofHandler) Cleanup() {}
