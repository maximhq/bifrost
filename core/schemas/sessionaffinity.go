package schemas

// SessionAffinity keeps a request that carries a session id on what served that session
// before. Core asks it at defined points and applies the answer; the policy behind the
// answer belongs to the implementation. The one Bifrost ships keeps a session on the key it
// was first served by for as long as that key stays eligible, and is installed when nothing
// else is. A deployment that knows more about its keys, such as their health, registers its
// own through BifrostConfig.SessionAffinity.
//
// Every method receives the request context, which carries the session id
// (BifrostContextKeySessionID), the session TTL (BifrostContextKeySessionTTL) and the
// identifiers that scope a session to its caller.
type SessionAffinity interface {
	// ResolveKey is asked once the eligible key pool for a provider attempt has been built
	// with two or more keys in it. It answers with the key the session's state settles on,
	// looking one up or binding one now, or with ok=false to leave the choice to ordinary
	// key selection. A returned key is used for every attempt of the request.
	ResolveKey(ctx *BifrostContext, provider ModelProvider, model string, eligible []Key) (key Key, ok bool)
}
