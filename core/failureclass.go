package bifrost

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// Everything the retry loop knows about provider errors lives in this file: the status
// codes it trusts on their own, the codes, types and exception names providers expose,
// and the message phrases used where a provider exposes nothing else. Exact-match lists
// are checked with slices.Contains, phrase lists against the lower-cased message with
// containsAny. The phrase lists are deliberately specific: a phrase that also appears in
// an unrelated error would rotate or exclude a key for the wrong reason.

// transientServerStatusCodes are upstream-side failures unrelated to the credential,
// retried with the *same* key (a different credential gains nothing against a flaky
// server). 529 is Anthropic's overloaded_error ("The API is temporarily overloaded",
// docs.claude.com/en/api/errors), also surfaced by Bedrock Mantle's Claude endpoint. It
// reflects capacity across all callers rather than anything about this credential, so it
// retries on the same key instead of rotating: rotating would burn every key on a
// condition none of them can avoid.
var transientServerStatusCodes = []int{500, 502, 503, 504, 529}

// rateLimitPatterns are the phrases a rate limit is reported with by providers that do
// not always answer 429 (case-insensitive substrings).
var rateLimitPatterns = []string{
	"rate limit",
	"rate_limit",
	"ratelimit",
	"too many requests",
	"quota exceeded",
	"quota_exceeded",
	"request limit",
	"throttled",
	"throttling",
	"rate exceeded",
	"limit exceeded",
	"requests per",
	"rpm exceeded",
	"tpm exceeded",
	"tokens per minute",
	"requests per minute",
	"requests per second",
	"api rate limit",
	"usage limit",
	"concurrent requests limit",
	"burst_rate",
	"rate increased",
}

// quotaMarkers say the account behind the key is out of money or over a spend cap, which
// providers report under a 400 (Anthropic), a 403 (OpenRouter, Vertex) or a 429 (Anthropic,
// Gemini, OpenAI) that would otherwise read as a rate limit.
var quotaMarkers = []string{
	"credit balance",
	"spend limit",
	"api usage limits",
	"prepayment credits",
	"credits are depleted",
	"spending cap",
	"requires billing to be enabled",
	"key limit exceeded",
	"account is not active",
}

// quotaCodes name an exhausted balance or a spend block outright: OpenAI's
// insufficient_quota (a type as well as a code) and Groq's blocked_api_access.
var quotaCodes = []string{"insufficient_quota", "blocked_api_access"}

// regionMarkers say the provider refuses the request's location: Gemini's
// FAILED_PRECONDITION and Anthropic-on-Bedrock's ValidationException wording. Both checks
// also read the key's project or account (Gemini's tier, Bedrock's billing address), so the
// class rotates; see schemas.FailureClassRegionBlocked.
var regionMarkers = []string{
	"unsupported countries",
	"location is not supported",
	"not available in your country",
}

// credentialTypes are the error types that name a rejected key outright: Anthropic's and
// Bedrock Mantle's authentication_error, Google's UNAUTHENTICATED, and the AWS exception
// names for an unknown access key id, a signature that does not match the secret, or an
// expired session token (Bedrock never answers 401; these arrive as 403).
var credentialTypes = []string{
	"authentication_error",
	"UNAUTHENTICATED",
	"UnrecognizedClientException",
	"InvalidSignatureException",
	"ExpiredTokenException",
	"IncompleteSignatureException",
}

// credentialCodes are the codes that name a rejected key: OpenAI-shaped invalid_api_key,
// and Groq's code for an organisation that has been cut off.
var credentialCodes = []string{
	"invalid_api_key",
	"organization_restricted",
}

// credentialSharedTypes are the types providers use both for a rejected key and for a
// model the key cannot reach (Google's INVALID_ARGUMENT, Bedrock's AccessDeniedException,
// Bedrock Mantle's permission_error); credentialMarkers in the message decide.
var credentialSharedTypes = []string{
	"INVALID_ARGUMENT",
	"AccessDeniedException",
	"permission_error",
}

// credentialMarkers are the phrases those shared types carry when it was the key that was
// rejected: Gemini's "API key not valid" / "API key expired", Bedrock's "API Key is valid"
// and "security token", Bedrock Mantle's "Invalid API Key".
var credentialMarkers = []string{
	"api key not valid",
	"api_key_invalid",
	"api key expired",
	"api key is valid",
	"invalid api key",
	"security token",
}

// modelAccessCodes are the codes OpenAI-shaped providers use when this key cannot reach
// the model: OpenAI's and Groq's model_not_found (a 404, or a 403 when a project lacks
// access), Azure's DeploymentNotFound, Mistral's unknown_model, Groq's terms gate.
var modelAccessCodes = []string{
	"model_not_found",
	"DeploymentNotFound",
	"unknown_model",
	"model_terms_required",
}

// modelAccessTypes are the types that name the model as the thing the provider could not
// serve without needing the message: Mistral's model_not_found and invalid_model,
// Anthropic's permission_error (a workspace can restrict models) and Bedrock's
// AccessDeniedException. Bedrock's ResourceNotFoundException also reports a batch job or
// a guardrail that does not exist, so it counts only when its message names a model.
var modelAccessTypes = []string{
	"model_not_found",
	"invalid_model",
	"permission_error",
	"AccessDeniedException",
}

// googleModelTypes need the message to name a model resource (models/... or "publisher
// model"), since the same types also report project and permission problems.
var googleModelTypes = []string{"NOT_FOUND", "PERMISSION_DENIED", "FAILED_PRECONDITION"}

// googleModelMarkers are how a Gemini or Vertex message names a model resource: the
// models/ path segment of a resource name, or "Publisher Model".
var googleModelMarkers = []string{"models/", "publisher model"}

// anthropicModelMarkers pick the not_found_error responses that are about a model.
var anthropicModelMarkers = []string{"model:", "is not available"}

// bedrockModelMarkers are the phrases in a Bedrock ValidationException that make it about
// the model id or the way this key may call it, rather than about the request body: an
// invalid model identifier, or an on-demand call to a model that needs an inference
// profile. A ValidationException about a field the model does not support, or about the
// input being too long, must not match.
var bedrockModelMarkers = []string{
	"model id",
	"on-demand throughput",
	"inference profile",
}

// openRouterModelMarkers are OpenRouter's 404 wordings for a model with no endpoint.
var openRouterModelMarkers = []string{"no endpoints found", "no allowed providers"}

// retiredModelCodes name a withdrawn model outright: Groq's and Azure's.
var retiredModelCodes = []string{"model_decommissioned", "ServiceModelDeprecating"}

// retiredModelMarkers are the phrases providers use when a model has been withdrawn, as
// opposed to never having existed or not being reachable with this key.
var retiredModelMarkers = []string{
	"deprecated",
	"deprecation",
	"decommissioned",
	"retired",
	"end of its life",
	"end-of-life",
	"no longer available",
	"was removed",
	"has been removed",
}

// perKeyStatusCodes are the statuses that on their own say the failure is bound to the key
// or the account rather than the request. They are what a same-key retry has always been
// earned by when no other key exists to move to.
var perKeyStatusCodes = []int{401, 402, 403, 429}

// callerFaultStatusCodes are the statuses that, with a provider error body, say the
// request itself was refused.
var callerFaultStatusCodes = []int{400, 409, 413, 415, 422}

// IsRateLimitErrorMessage checks if an error message indicates a rate limit issue.
func IsRateLimitErrorMessage(errorMessage string) bool {
	if errorMessage == "" {
		return false
	}
	return containsAny(strings.ToLower(errorMessage), rateLimitPatterns)
}

// ClassifyFailure says what a failed attempt tells Bifrost about the key and the route it
// used: whether the same key can be retried, another key should be tried, or nothing will
// help. executeRequestWithRetries stamps the result on the attempt trail and drives key
// rotation from it. A nil error has no class and yields "".
//
// The rules read the provider's own error code, type or exception name wherever the
// provider exposes one, then its message where it exposes nothing else, and fall back to
// the status only for the facts a status can carry on its own (401, 402, 403, 429 and the
// transient 5xx set). Providers disagree on which status carries which fact: Gemini
// rejects a bad key with a 400, Bedrock reports a retired model as a 400 or a 404 under
// the same exception name, Anthropic reports an empty credit balance as a 400
// invalid_request_error, OpenAI and Gemini report one as a 429. A failure the rules do
// not recognise is FailureClassUnknown, which keeps the existing behaviour: no retry, no
// rotation.
func ClassifyFailure(err *schemas.BifrostError) schemas.FailureClass {
	if err == nil {
		return ""
	}
	if err.IsBifrostError {
		return schemas.FailureClassUnknown
	}
	status := 0
	if err.StatusCode != nil {
		status = *err.StatusCode
	}
	var message, errType, code string
	hasParam := false
	if err.Error != nil {
		message = err.Error.Message
		if err.Error.Type != nil {
			errType = *err.Error.Type
		}
		if err.Error.Code != nil {
			code = *err.Error.Code
		}
		if s, ok := err.Error.Param.(string); ok {
			hasParam = s != ""
		} else {
			hasParam = err.Error.Param != nil
		}
	}
	if errType == "" && err.Type != nil {
		errType = *err.Type
	}
	lower := strings.ToLower(message)
	body := hasBody(message)
	transient := slices.Contains(transientServerStatusCodes, status)

	// Transport failures: the key played no part.
	if message == schemas.ErrProviderDoRequest || message == schemas.ErrProviderNetworkError {
		return schemas.FailureClassTransient
	}

	// Quota before rate limit: OpenAI, Gemini and Anthropic report an exhausted balance or
	// a spend cap under a 429 whose message also matches the rate-limit patterns. A
	// transient 5xx stays transient whatever its body says, as it always has.
	if !transient && (status == 402 || slices.Contains(quotaCodes, code) || errType == "insufficient_quota" || containsAny(lower, quotaMarkers)) {
		return schemas.FailureClassQuota
	}

	// Rate limit: the status, or the patterns the retry loop has always honoured for
	// providers that report a limit without a 429, at any status (a 5xx whose body says the
	// limit was hit included) except 401 and 403, which mark the key dead whatever their
	// body says, as they always have.
	if status == 429 || (status != 401 && status != 403 && hasRateLimitSignal(err)) {
		return schemas.FailureClassRateLimit
	}

	// The transient 5xx set: retried on the same key.
	if transient {
		return schemas.FailureClassTransient
	}

	if code == "unsupported_country_region_territory" || ((status == 400 || status == 403) && containsAny(lower, regionMarkers)) {
		return schemas.FailureClassRegionBlocked
	}

	// Credential: a 401 from anyone, the types and codes that name a rejected key, and
	// the shared types whose message says it was the key.
	if status == 401 || slices.Contains(credentialTypes, errType) || slices.Contains(credentialCodes, code) ||
		(slices.Contains(credentialSharedTypes, errType) && containsAny(lower, credentialMarkers)) {
		return schemas.FailureClassCredential
	}

	// The model is unreachable with this key, or withdrawn altogether. The two share their
	// signals and differ only in what the message says about the model.
	if isModel, retired := modelSignals(status, errType, code, lower, body); isModel {
		if retired {
			return schemas.FailureClassModelGone
		}
		return schemas.FailureClassModelAccess
	}

	// Any other 403 is a permission problem with the key, as the retry loop has always
	// treated it. That includes a guardrail or moderation refusal: guardrails can be set
	// per key or per workspace, so another key may serve the same request.
	if status == 403 {
		return schemas.FailureClassCredential
	}

	// The request itself was refused. A 404 counts only when the provider named the
	// request field it could not resolve (a file, a previous response); a 404 for the
	// endpoint itself carries no such signal and stays unknown.
	if slices.Contains(callerFaultStatusCodes, status) && (errType != "" || code != "" || body) {
		return schemas.FailureClassCallerFault
	}
	if status == 404 && hasParam {
		return schemas.FailureClassCallerFault
	}
	return schemas.FailureClassUnknown
}

// modelSignals reports whether the error names the model as the thing the provider could
// not serve, and whether it says the model was withdrawn. A 410, which Azure uses for a
// retired deployment, is a withdrawn model whenever it carries a body. A 404 whose
// message names a model counts too, since Cohere sends nothing else.
func modelSignals(status int, errType, code, lower string, body bool) (isModel, retired bool) {
	retired = slices.Contains(retiredModelCodes, code) || containsAny(lower, retiredModelMarkers)
	if status == 410 {
		return body || retired, true
	}
	if slices.Contains(modelAccessCodes, code) || slices.Contains(retiredModelCodes, code) || strings.HasPrefix(code, "model_permission_blocked") {
		return true, retired
	}
	switch {
	case slices.Contains(modelAccessTypes, errType):
		return true, retired
	case errType == "ResourceNotFoundException":
		return strings.Contains(lower, "model"), retired
	case errType == "ValidationException":
		// Only a retirement notice about the model counts; a field the model no longer
		// accepts is the request's problem.
		retired = retired && strings.Contains(lower, "model")
		return retired || containsAny(lower, bedrockModelMarkers), retired
	case errType == "not_found_error":
		return containsAny(lower, anthropicModelMarkers), retired
	case slices.Contains(googleModelTypes, errType):
		return containsAny(lower, googleModelMarkers), retired
	}
	if status == 404 {
		if containsAny(lower, openRouterModelMarkers) {
			return true, retired
		}
		if strings.Contains(lower, "model") && (strings.Contains(lower, "not found") || retired) {
			return true, retired
		}
	}
	return false, false
}

// hasRateLimitSignal reports whether the error's message, type or code carries rate-limit
// wording.
func hasRateLimitSignal(err *schemas.BifrostError) bool {
	if err == nil || err.Error == nil {
		return false
	}
	if IsRateLimitErrorMessage(err.Error.Message) {
		return true
	}
	if err.Error.Type != nil && IsRateLimitErrorMessage(*err.Error.Type) {
		return true
	}
	return err.Error.Code != nil && IsRateLimitErrorMessage(*err.Error.Code)
}

// retriesWithoutRotation reports whether a failure earns a same-key retry when there is no
// other key to move to: a transient failure, or one whose status or rate-limit wording
// binds it to the key or the account. The classes that rotate only because another key
// might serve (a model this key cannot reach, a retired model, a region block on this key's
// project or account) earn nothing here: the same key gets the same answer.
func retriesWithoutRotation(err *schemas.BifrostError, class schemas.FailureClass) bool {
	switch class {
	case schemas.FailureClassTransient, schemas.FailureClassRateLimit:
		return true
	case schemas.FailureClassRegionBlocked:
		return false
	}
	if err == nil || !class.IsPermanentPerKey() {
		return false
	}
	return (err.StatusCode != nil && slices.Contains(perKeyStatusCodes, *err.StatusCode)) || hasRateLimitSignal(err)
}

// hasBody reports whether the message came from a provider error body rather than from
// Bifrost's own description of an empty, non-JSON or HTML response.
func hasBody(message string) bool {
	return message != "" &&
		message != schemas.ErrProviderResponseHTML &&
		!strings.HasPrefix(message, schemas.ErrProviderResponseEmpty) &&
		!strings.HasPrefix(message, "provider API error")
}

// containsAny reports whether the lower-cased message contains any of the markers.
func containsAny(lower string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
