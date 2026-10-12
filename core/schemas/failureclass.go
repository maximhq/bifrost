package schemas

// FailureClass says what a failed attempt tells Bifrost about the key and the route it
// used, which is what the retry loop needs to decide whether to retry the same key, move
// to another key, or stop. It is stamped on every KeyAttemptRecord that reached the
// provider.
//
// It is deliberately separate from ErrorType. That vocabulary attributes fault for the
// error_type metric label and is derived from the status code alone; this one drives
// retry and exclusion policy and is derived from the provider's own error code, type or
// exception name wherever the provider exposes one. A status alone decides only what it
// has always decided for the retry loop (401, 402, 403, 429 and the transient 5xx set);
// every other class needs a parsed signal, since providers disagree on which status
// carries which fact.
type FailureClass string

const (
	// FailureClassCredential: the key itself was rejected. Revoked, malformed, or the
	// account behind it is locked. Nothing on this key will work until it is changed.
	FailureClassCredential FailureClass = "credential"
	// FailureClassModelAccess: this key cannot reach the requested model. No access, no
	// such deployment, no inference profile. Another key may.
	FailureClassModelAccess FailureClass = "model_access"
	// FailureClassQuota: the account behind the key is out of credit or over a billing
	// limit. Unlike a rate limit, waiting does not help.
	FailureClassQuota FailureClass = "quota"
	// FailureClassModelGone: the provider retired the model.
	FailureClassModelGone FailureClass = "model_gone"
	// FailureClassRegionBlocked: the provider refuses the request's location. The block can
	// be bound to the key rather than the gateway: Gemini refuses a free-tier project where
	// a billed project is served from the same address, and Bedrock checks the account's
	// billing address as well as the caller's. So the next key is tried, and when the block
	// is the gateway's own address (OpenAI checks only that) the walk ends with the
	// provider's error after one refused call per key.
	FailureClassRegionBlocked FailureClass = "region_blocked"
	// FailureClassRateLimit: the key or the model is rate limited right now. The same
	// key may have capacity again shortly.
	FailureClassRateLimit FailureClass = "rate_limit"
	// FailureClassCallerFault: the request itself was refused. Bad parameters, too long,
	// content filtered. No key would have served it.
	FailureClassCallerFault FailureClass = "caller_fault"
	// FailureClassTransient: the upstream failed in a way unrelated to the key or the
	// request. The same key is retried.
	FailureClassTransient FailureClass = "transient"
	// FailureClassUnknown: a failure carrying no signal Bifrost recognises. Neither
	// retried nor rotated away from.
	FailureClassUnknown FailureClass = "unknown"
	// FailureClassTimeout: the attempt ran out of time before it was answered: the provider's
	// own timeout, the request's deadline, or a stream that produced no first token in time.
	// Bifrost ended the attempt, so it is neither retried nor rotated away from, and the next
	// fallback runs unless the request's own deadline has passed.
	FailureClassTimeout FailureClass = "timeout"
	// FailureClassCancelled: the caller abandoned the request while the attempt was in flight.
	// It says nothing about the key or the route, so its attempt carries no FailReason.
	FailureClassCancelled FailureClass = "cancelled"
)

// IsPerKey reports whether the failure is bound to the key that made the attempt, so a
// retry should use a different key.
func (c FailureClass) IsPerKey() bool {
	return c == FailureClassRateLimit || c.IsPermanentPerKey()
}

// IsPermanentPerKey reports whether the key cannot serve the request at all for the rest
// of it: waiting changes nothing, so the key is excluded without backoff.
func (c FailureClass) IsPermanentPerKey() bool {
	switch c {
	case FailureClassCredential, FailureClassModelAccess, FailureClassQuota, FailureClassModelGone, FailureClassRegionBlocked:
		return true
	}
	return false
}

// CoversAllModels reports whether the failure holds for every model on the key, not only the
// one that was refused: the key itself was rejected, or the account behind it is out of credit
// or over a billing cap. It measures how broad the failure is, where IsPerKey says whose fault
// it is: model access, a retired model and a rate limit are the key's fault but bind it only for
// one model, and a per-model quota is classified as a rate limit. A consumer that holds keys
// across requests can hold the whole key on this alone.
func (c FailureClass) CoversAllModels() bool {
	return c == FailureClassCredential || c == FailureClassQuota
}

// FailReason returns the attempt-trail label for the classes that name what the provider
// refused, and "" for the others, whose label is the provider's own error type.
func (c FailureClass) FailReason() string {
	switch c {
	case FailureClassCredential:
		return "authentication_error"
	case FailureClassQuota:
		return "billing_error"
	case FailureClassRateLimit:
		return "rate_limit_error"
	case FailureClassModelAccess:
		return "model_access_error"
	case FailureClassModelGone:
		return "model_retired_error"
	case FailureClassRegionBlocked:
		return "region_blocked_error"
	}
	return ""
}
