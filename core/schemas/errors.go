package schemas

// Wire-level values Bifrost stamps on BifrostError.Type. These are the client-visible
// error contract, separate from the ErrorType vocabulary below.
const (
	RequestCancelled         = "request_cancelled"
	RequestTimedOut          = "request_timed_out"
	RequestDropped           = "request_dropped"
	ProviderConnectionFailed = "provider_connection_failed"
	// Classification reads the declaration core makes at the same site, not this.
	NoKeySupportsModel = "no_key_supports_model"
)

// ErrorType is the normalized reason a request failed, used as the error_type metric
// label. StatusCode remains the raw fact it was derived from.
//
// The vocabulary is closed and prefixed by fault domain, so a consumer can select or
// exclude a whole family without enumerating its members.
type ErrorType string

const (
	// caller: the request asked for something it could not have.
	ErrorTypeCallerModelNotAvailable ErrorType = "caller_model_not_available" // no configured key serves it
	ErrorTypeCallerModelUnknown      ErrorType = "caller_model_unknown"       // provider rejected the name
	ErrorTypeCallerInvalidRequest    ErrorType = "caller_invalid_request"
	ErrorTypeCallerCancelled         ErrorType = "caller_cancelled"

	// policy: Bifrost's configuration refused it before dispatch.
	ErrorTypePolicyModelBlocked    ErrorType = "policy_model_blocked"
	ErrorTypePolicyProviderBlocked ErrorType = "policy_provider_blocked"
	ErrorTypePolicyAccessDenied    ErrorType = "policy_access_denied"
	ErrorTypePolicyBudgetExceeded  ErrorType = "policy_budget_exceeded"
	ErrorTypePolicyRateLimited     ErrorType = "policy_rate_limited"
	ErrorTypePolicyToolBlocked     ErrorType = "policy_tool_blocked"

	// provider: the upstream failed or refused.
	ErrorTypeProviderAuthFailed           ErrorType = "provider_auth_failed"
	ErrorTypeProviderBilling              ErrorType = "provider_billing"
	ErrorTypeProviderRateLimited          ErrorType = "provider_rate_limited"
	ErrorTypeProviderOverloaded           ErrorType = "provider_overloaded" // 503, and Anthropic's 529
	ErrorTypeProviderServerError          ErrorType = "provider_server_error"
	ErrorTypeProviderTimeout              ErrorType = "provider_timeout"
	ErrorTypeProviderConnectionFailed     ErrorType = "provider_connection_failed"
	ErrorTypeProviderCredentialsExhausted ErrorType = "provider_credentials_exhausted" // every key in the pool was dead

	// bifrost: Bifrost itself failed.
	ErrorTypeBifrostDropped  ErrorType = "bifrost_dropped" // queue full, request shed
	ErrorTypeBifrostInternal ErrorType = "bifrost_internal"

	// Nobody declared and no rule matched. Alarm on it. "_OTHER" is the OTel
	// catch-all, matching the error_type label the MCP metrics emit.
	ErrorTypeOther ErrorType = "_OTHER"
)

// validErrorTypes is ErrorTypes as a set, for validating declarations.
var validErrorTypes = func() map[ErrorType]struct{} {
	set := make(map[ErrorType]struct{}, len(ErrorTypes))
	for _, t := range ErrorTypes {
		set[t] = struct{}{}
	}
	return set
}()

// ErrorTypes is the full vocabulary, for conformance tests and documentation.
var ErrorTypes = []ErrorType{
	ErrorTypeCallerModelNotAvailable,
	ErrorTypeCallerModelUnknown,
	ErrorTypeCallerInvalidRequest,
	ErrorTypeCallerCancelled,
	ErrorTypePolicyModelBlocked,
	ErrorTypePolicyProviderBlocked,
	ErrorTypePolicyAccessDenied,
	ErrorTypePolicyBudgetExceeded,
	ErrorTypePolicyRateLimited,
	ErrorTypePolicyToolBlocked,
	ErrorTypeProviderAuthFailed,
	ErrorTypeProviderBilling,
	ErrorTypeProviderRateLimited,
	ErrorTypeProviderOverloaded,
	ErrorTypeProviderServerError,
	ErrorTypeProviderTimeout,
	ErrorTypeProviderConnectionFailed,
	ErrorTypeProviderCredentialsExhausted,
	ErrorTypeBifrostDropped,
	ErrorTypeBifrostInternal,
	ErrorTypeOther,
}

// Request types whose only addressable resource is the model, so a 404 can mean nothing
// else. Excludes anything that can also address a resource: OCR documents, image
// edit/variation sources, image-to-video inputs, and chat/responses, which carry file
// ids and previous_response_id. TestEveryRequestTypeIsCategorized fails on an undecided
// request type.
//
// Not plugins/governance.IsModelRequiredForRequest: that asks whether a model is
// carried at all, has the opposite default, and governs authorization.
var modelAddressedRequestTypes = map[RequestType]struct{}{
	TextCompletionRequest:        {},
	TextCompletionStreamRequest:  {},
	EmbeddingRequest:             {},
	SpeechRequest:                {},
	SpeechStreamRequest:          {},
	TranscriptionRequest:         {},
	TranscriptionStreamRequest:   {},
	ImageGenerationRequest:       {},
	ImageGenerationStreamRequest: {},
	RerankRequest:                {},
	CountTokensRequest:           {},
}

// RequestTypeAddressesModel reports whether the model is the only resource addressed.
func RequestTypeAddressesModel(requestType RequestType) bool {
	_, ok := modelAddressedRequestTypes[requestType]
	return ok
}

// ClassifyErrorType maps a failure onto the vocabulary. "" for nil, else always a
// value, falling back to ErrorTypeOther.
//
// requestType is passed, not read off the error: ExtraFields is empty until the
// request settles. Pass what you label metrics with.
//
// Order matters: declaration beats marker beats status, so a governance 403 is not
// re-attributed as provider_auth_failed.
func ClassifyErrorType(err *BifrostError, requestType RequestType) ErrorType {
	if err == nil {
		return ""
	}

	if declared := err.ExtraFields.ErrorType; declared != "" {
		// Validated: ErrorType is a string type, so a producer typo would otherwise
		// become an undocumented label instead of surfacing in ErrorTypeOther.
		if _, ok := validErrorTypes[declared]; ok {
			return declared
		}
		return ErrorTypeOther
	}

	for _, candidate := range []*string{err.Type, nestedErrorType(err)} {
		if candidate == nil {
			continue
		}
		switch *candidate {
		case RequestCancelled:
			return ErrorTypeCallerCancelled
		case RequestTimedOut:
			return ErrorTypeProviderTimeout
		case RequestDropped:
			return ErrorTypeBifrostDropped
		case ProviderConnectionFailed:
			return ErrorTypeProviderConnectionFailed
		}
	}

	if err.StatusCode != nil {
		if classified, ok := classifyProviderStatus(*err.StatusCode, requestType); ok {
			return classified
		}
	}

	if err.IsBifrostError {
		return ErrorTypeBifrostInternal
	}

	return ErrorTypeOther
}

// nestedErrorType returns err.Error.Type without dereferencing a nil Error.
func nestedErrorType(err *BifrostError) *string {
	if err.Error == nil {
		return nil
	}
	return err.Error.Type
}

// classifyProviderStatus buckets an upstream status code.
//
// Status, not the provider's error type/code: those disagree across providers and
// mislabel unrelated failures — OpenAI sends invalid_request_error for a 401.
//
// Not derived from perKeyFailureStatusCodes / transientServerStatusCodes despite the
// overlap: those encode retry policy, this encodes fault attribution.
func classifyProviderStatus(status int, requestType RequestType) (ErrorType, bool) {
	switch {
	case status == 401 || status == 403:
		return ErrorTypeProviderAuthFailed, true
	case status == 402:
		return ErrorTypeProviderBilling, true
	case status == 429:
		return ErrorTypeProviderRateLimited, true
	case status == 404:
		// Imprecise: a 400-rejecting provider lands in caller_invalid_request, and
		// Vertex uses 404 for "not found OR no access".
		if RequestTypeAddressesModel(requestType) {
			return ErrorTypeCallerModelUnknown, true
		}
		return ErrorTypeCallerInvalidRequest, true
	case status == 499:
		// Non-standard "client went away". Must precede the 4xx catch-all.
		return ErrorTypeCallerCancelled, true
	case status == 503 || status == 529:
		return ErrorTypeProviderOverloaded, true
	case status == 504:
		return ErrorTypeProviderTimeout, true
	case status >= 400 && status < 500:
		// Keeps ErrorTypeOther meaning "nobody declared and no rule matched".
		return ErrorTypeCallerInvalidRequest, true
	case status >= 500:
		return ErrorTypeProviderServerError, true
	}
	return "", false
}
