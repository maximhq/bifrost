package lib

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
)

// contextGrant returns the grant a request context carries, installing one when it carries none.
// ConvertToBifrostContext installs it for every request it builds; a request context built without
// one, as a connection that authenticates at upgrade or a token that is verified after the context
// exists builds one, gets it the first time something is recorded on it.
func contextGrant(bifrostCtx *schemas.BifrostContext) schemas.Grant {
	if bifrostCtx == nil {
		return nil
	}
	if g := bifrostCtx.Grant(); g != nil {
		return g
	}
	g := grant.New()
	if !bifrostCtx.SetGrant(g) {
		return nil
	}
	return g
}

// RecordCredential records what the request authenticated by on the request context's grant,
// keeping whatever else its identity already holds. It is for the layers that learn the credential
// only after the request context exists, and so after ConvertToBifrostContext settled the identity:
// a connection that authenticates at upgrade, a token verified in the handler, a session restored
// from a store. Recording a credential after another was recorded replaces it, which is how
// precedence is expressed: the layers run in order, and the last to authenticate is the one the
// request goes by. Recording nothing changes nothing.
func RecordCredential(bifrostCtx *schemas.BifrostContext, credential schemas.Credential) {
	if credential.Kind == "" {
		return
	}
	g := contextGrant(bifrostCtx)
	if g == nil {
		return
	}
	g.SetIdentity(identityWith(g.Identity(), credential, nil))
}

// RecordUser records the user the request is attributed to on the request context's grant, keeping
// whatever else its identity already holds, for the same layers RecordCredential is for. Recording
// nobody changes nothing.
func RecordUser(bifrostCtx *schemas.BifrostContext, user *schemas.UserRef) {
	if user == nil || user.ID == "" {
		return
	}
	g := contextGrant(bifrostCtx)
	if g == nil {
		return
	}
	g.SetIdentity(identityWith(g.Identity(), schemas.Credential{}, user))
}

// settleIdentity records who the request is from what the middlewares left on the context, all at
// once, on a grant installed here. The middlewares run before any request context exists, so they
// hand over what they verified as context values and this is the one place those are read: the
// user under its own keys, and any credential other than a header virtual key under
// BifrostContextKeyAuthCredential. The identity is recorded even when nothing was presented, so
// everything after the transport can tell "settled: nobody" from "not settled".
//
// A virtual key presented in a header goes first, whatever else was recorded: a request that
// presents a key goes by the key, and whoever resolves the request's access decides how the user a
// session named relates to it. A direct key is not a credential at all: it selects which provider
// key serves the request and says nothing about who made it.
func settleIdentity(bifrostCtx *schemas.BifrostContext) {
	g := contextGrant(bifrostCtx)
	if g == nil {
		return
	}
	g.SetIdentity(grant.NewIdentity(credentialFromContext(bifrostCtx), userFromContext(bifrostCtx), nil, nil, nil, nil, nil))
}

// credentialFromContext is the credential the request goes by: a virtual key presented in a header,
// else whatever the middleware that authenticated the request recorded, else nothing.
func credentialFromContext(bifrostCtx *schemas.BifrostContext) schemas.Credential {
	if virtualKey, _ := bifrostCtx.Value(schemas.BifrostContextKeyVirtualKey).(string); virtualKey != "" {
		return grant.NewCredential(grant.CredentialVirtualKey, virtualKey)
	}
	if recorded, ok := bifrostCtx.Value(schemas.BifrostContextKeyAuthCredential).(schemas.Credential); ok {
		return recorded
	}
	return schemas.Credential{}
}

// userFromContext reads the user an authenticating middleware resolved, if any.
func userFromContext(bifrostCtx *schemas.BifrostContext) *schemas.UserRef {
	userID, _ := bifrostCtx.Value(schemas.BifrostContextKeyUserID).(string)
	if userID == "" {
		return nil
	}
	name, _ := bifrostCtx.Value(schemas.BifrostContextKeyUserName).(string)
	email, _ := bifrostCtx.Value(schemas.BifrostContextKeyUserEmail).(string)
	return &schemas.UserRef{ID: userID, Name: name, Email: email}
}

// identityWith is the identity current holds with credential and user replaced where given: an
// identity is replaced whole, never edited, so a layer that learns one thing rebuilds it around
// what the layers before it learned.
func identityWith(current schemas.Identity, credential schemas.Credential, user *schemas.UserRef) schemas.Identity {
	if current == nil {
		return grant.NewIdentity(credential, user, nil, nil, nil, nil, nil)
	}
	if credential.Kind == "" {
		credential = current.Credential()
	}
	if user == nil {
		user = current.User()
	}
	return grant.NewIdentity(credential, user, current.VirtualKey(), current.Teams(), current.Customers(), current.BusinessUnits(), current.Project())
}
