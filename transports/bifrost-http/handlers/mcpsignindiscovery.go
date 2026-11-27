package handlers

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
)

// signInDiscoveryTimeout bounds the discovery a sign-in callback runs before it
// redirects the browser.
const signInDiscoveryTimeout = 10 * time.Second

// storeSignInDiscovery stores, as the caller's discovery of config, what
// discovering with the credential they just signed in with found, so a client
// with per_user_discovery on serves them its tools without a call to its connect
// tool. It does nothing for any other client, or for a session-mode caller, who
// keeps no discovery.
//
// The discovery is stored under mode and identity, the ones the credential was
// written under, never under the identity of the request that completed sign-in.
// Callers check that the credential is still active just before calling, so one
// revoked while discovery ran leaves no discovery behind.
func storeSignInDiscovery(ctx context.Context,
	store schemas.MCPDiscoveryStore,
	config *schemas.MCPClientConfig,
	mode schemas.MCPAuthMode,
	identity string,
	discovery *schemas.MCPDiscovery,
) error {
	if store == nil || !keepsSignInDiscovery(config, mode, identity) {
		return nil
	}
	return store.SetDiscovery(signInCallerContext(ctx, mode, identity), config, discovery)
}

// dropSignInDiscovery deletes the discovery of config an earlier sign-in by mode
// and identity left, once discovering with the credential that replaced it has
// failed. The earlier tools may be another account's; without them the caller
// is offered the client's connect tool, which discovers with the new credential.
// Failure is logged: the sign-in itself has succeeded.
func dropSignInDiscovery(ctx context.Context,
	store schemas.MCPDiscoveryStore,
	config *schemas.MCPClientConfig,
	mode schemas.MCPAuthMode,
	identity string,
) {
	if store == nil || !keepsSignInDiscovery(config, mode, identity) {
		return
	}
	if err := store.DeleteDiscovery(signInCallerContext(ctx, mode, identity), config); err != nil {
		logger.Warn("[mcp] failed to delete the earlier discovery of MCP client %s after a sign-in whose discovery failed: %v", config.Name, err)
	}
}

// keepsSignInDiscovery reports whether a sign-in by mode and identity leaves a
// discovery of config: config has per_user_discovery on, and the caller is a user
// or a virtual key. Callers check it before discovering, so a sign-in that keeps
// none dials nothing.
func keepsSignInDiscovery(config *schemas.MCPClientConfig, mode schemas.MCPAuthMode, identity string) bool {
	if config == nil || config.PerUserDiscovery == nil || !*config.PerUserDiscovery {
		return false
	}
	return (mode == schemas.MCPAuthModeUser || mode == schemas.MCPAuthModeVK) && identity != ""
}

// signInCallerContext returns a context naming exactly mode and identity, the way
// the per-user discovery store reads a caller. It carries none of parent's
// values: a dashboard user completing a virtual key's flow must not have the
// discovery stored under their own user ID, which the store would prefer.
func signInCallerContext(parent context.Context, mode schemas.MCPAuthMode, identity string) *schemas.BifrostContext {
	deadline, ok := parent.Deadline()
	if !ok {
		deadline = schemas.NoDeadline
	}
	ctx := schemas.NewBifrostContext(context.Background(), deadline)
	switch mode {
	case schemas.MCPAuthModeUser:
		ctx.SetValue(schemas.BifrostContextKeyUserID, identity)
	case schemas.MCPAuthModeVK:
		ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, identity)
	}
	return ctx
}

// perUserDiscoveryStore returns cfg's per-user discovery store, or nil when
// there is none (an interface holding a nil store would not read as unset).
func perUserDiscoveryStore(cfg *lib.Config) schemas.MCPDiscoveryStore {
	if cfg == nil || cfg.MCPPerUserDiscoveryStore == nil {
		return nil
	}
	return cfg.MCPPerUserDiscoveryStore
}
