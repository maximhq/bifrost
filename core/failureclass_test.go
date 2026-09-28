package bifrost

import (
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// The fixtures below are the shapes the provider parsers hand the retry loop, taken from
// provider documentation and captured responses. Where a provider's error format is
// exercised elsewhere in the tree (Bedrock errors_test.go, Gemini and Vertex
// errors_test.go, Mistral ocr_test.go, streamfallback_test.go), the same strings are used
// here so the classifier is pinned to the parsers' output.
func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		name string
		err  *schemas.BifrostError
		want schemas.FailureClass
	}{
		// transient
		{"network error", &schemas.BifrostError{Error: &schemas.ErrorField{Message: schemas.ErrProviderNetworkError}}, schemas.FailureClassTransient},
		{"do request error", &schemas.BifrostError{Error: &schemas.ErrorField{Message: schemas.ErrProviderDoRequest}}, schemas.FailureClassTransient},
		{"503 overloaded", providerError(503, "overloaded_error", "", "Overloaded"), schemas.FailureClassTransient},
		{"529 anthropic overloaded", providerError(529, "overloaded_error", "", "The API is temporarily overloaded"), schemas.FailureClassTransient},
		{"500 empty body", providerError(500, "", "", "provider server error (500)"), schemas.FailureClassTransient},
		{"503 with rate limit text rotates as before", providerError(503, "", "", "Service unavailable: rate limit reached"), schemas.FailureClassRateLimit},
		{"503 with quota text stays transient as before", providerError(503, "", "", "Your credit balance service is unavailable"), schemas.FailureClassTransient},
		{"401 with limit text stays credential as before", providerError(401, "", "", "API key usage limit reached, key disabled"), schemas.FailureClassCredential},
		{"403 with limit text stays credential as before", providerError(403, "invalid_request_error", "", "Your organization's usage limit has been reached"), schemas.FailureClassCredential},
		{"openai blank message synthesized", providerError(400, "", "", "provider API error (status 400)"), schemas.FailureClassUnknown},
		{"bedrock deprecated field is the request's fault", providerError(400, "ValidationException", "", "The parameter textGenerationConfig is deprecated. Remove it and try again."), schemas.FailureClassCallerFault},
		{"openai account flagged 403", providerError(403, "access_terminated", "", "Your account was flagged for potential abuse. If you feel this is an error, please contact us at help.openai.com."), schemas.FailureClassCredential},
		{"azure content policy resource block 403", providerError(403, "", "Forbidden", "Your resource has been temporarily blocked because we detected behavior that may violate our content policy."), schemas.FailureClassCredential},

		// quota, checked before rate limit because its message matches the rate-limit patterns
		{"openai insufficient_quota", providerError(429, "insufficient_quota", "insufficient_quota", "You exceeded your current quota, please check your plan and billing details."), schemas.FailureClassQuota},
		{"402 payment required", providerError(402, "", "", "Payment Required"), schemas.FailureClassQuota},
		{"openrouter 402 in-band", providerError(402, "", "402", "This request requires more credits, or fewer max_tokens."), schemas.FailureClassQuota},
		{"anthropic credit balance", providerError(400, "invalid_request_error", "", "Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."), schemas.FailureClassQuota},

		// rate limit
		{"429 rate_limit_exceeded", providerError(429, "requests", "rate_limit_exceeded", "Rate limit reached for gpt-4o"), schemas.FailureClassRateLimit},
		{"429 empty body", providerError(429, "", "", "rate limit exceeded (429)"), schemas.FailureClassRateLimit},
		{"bedrock ThrottlingException", providerError(429, "ThrottlingException", "", "Too many requests, please wait before trying again."), schemas.FailureClassRateLimit},
		{"gemini RESOURCE_EXHAUSTED", providerError(429, "RESOURCE_EXHAUSTED", "429", "You exceeded your current quota, please check your plan and billing details."), schemas.FailureClassRateLimit},
		{"rate limit text without 429", providerError(400, "", "", "Too many requests, throttled"), schemas.FailureClassRateLimit},

		// region
		{"openai region block", providerError(403, "invalid_request_error", "unsupported_country_region_territory", "Country, region, or territory not supported"), schemas.FailureClassRegionBlocked},

		// credential
		{"openai invalid_api_key", providerError(401, "invalid_request_error", "invalid_api_key", "Incorrect API key provided: sk-abc. You can find your API key at https://platform.openai.com/account/api-keys."), schemas.FailureClassCredential},
		{"mistral authentication_error", providerError(401, "authentication_error", "invalid_api_key", "Unauthorized"), schemas.FailureClassCredential},
		{"anthropic authentication_error", providerError(401, "authentication_error", "", "invalid x-api-key"), schemas.FailureClassCredential},
		{"401 empty body", providerError(401, "", "", "authentication failed: unauthorized (401) - check your API key"), schemas.FailureClassCredential},
		{"gemini bad key is a 400", providerError(400, "INVALID_ARGUMENT", "400", "API key not valid. Please pass a valid API key."), schemas.FailureClassCredential},
		{"vertex UNAUTHENTICATED", providerError(401, "UNAUTHENTICATED", "", "Request had invalid authentication credentials."), schemas.FailureClassCredential},
		{"bedrock UnrecognizedClientException", providerError(403, "UnrecognizedClientException", "", "The security token included in the request is invalid."), schemas.FailureClassCredential},
		{"bedrock InvalidSignatureException", providerError(403, "InvalidSignatureException", "", "The request signature we calculated does not match the signature you provided."), schemas.FailureClassCredential},
		{"bedrock ExpiredTokenException", providerError(403, "ExpiredTokenException", "", "The security token included in the request is expired"), schemas.FailureClassCredential},
		{"bare 403", providerError(403, "", "", "access forbidden (403) - your API key may not have permission for this operation"), schemas.FailureClassCredential},
		{"azure 401 subscription key", providerError(401, "", "401", "Access denied due to invalid subscription key or wrong API endpoint."), schemas.FailureClassCredential},

		// model access
		{"openai model_not_found", providerError(404, "invalid_request_error", "model_not_found", "The model `gpt-4o` does not exist or you do not have access to it."), schemas.FailureClassModelAccess},
		{"azure DeploymentNotFound", providerError(404, "", "DeploymentNotFound", "The API deployment for this resource does not exist. If you created the deployment within the last 5 minutes, please wait a moment and try again."), schemas.FailureClassModelAccess},
		{"groq model_not_found", providerError(404, "invalid_request_error", "model_not_found", "The model `llama3-70b` does not exist or you do not have access to it."), schemas.FailureClassModelAccess},
		{"anthropic permission_error", providerError(403, "permission_error", "", "Your API key does not have permission to use the specified resource."), schemas.FailureClassModelAccess},
		{"anthropic not_found_error model", providerError(404, "not_found_error", "", "model: claude-3-opus-2024022"), schemas.FailureClassModelAccess},
		{"bedrock AccessDeniedException", providerError(403, "AccessDeniedException", "", "You don't have access to the model with the specified model ID."), schemas.FailureClassModelAccess},
		{"bedrock invalid model identifier", providerError(400, "ValidationException", "", "The provided model identifier is invalid."), schemas.FailureClassModelAccess},
		{"bedrock on-demand throughput", providerError(400, "ValidationException", "", "Invocation of model ID anthropic.claude-v2 with on-demand throughput isn't supported. Retry your request with the ID or ARN of an inference profile that contains this model."), schemas.FailureClassModelAccess},
		{"bedrock ResourceNotFoundException", providerError(404, "ResourceNotFoundException", "", "Could not resolve the foundation model from the provided model identifier."), schemas.FailureClassModelAccess},
		{"gemini NOT_FOUND model", providerError(404, "NOT_FOUND", "404", "models/gemini-1.0-pro is not found for API version v1beta, or is not supported for generateContent."), schemas.FailureClassModelAccess},
		{"vertex NOT_FOUND publisher model", providerError(404, "NOT_FOUND", "", "Publisher Model `projects/my-project/locations/us-central1/publishers/google/models/gemini-9` was not found or your project does not have access to it."), schemas.FailureClassModelAccess},
		{"vertex PERMISSION_DENIED on model", providerError(403, "PERMISSION_DENIED", "", "Permission 'aiplatform.endpoints.predict' denied on resource '//aiplatform.googleapis.com/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-pro' (or it may not exist)."), schemas.FailureClassModelAccess},

		// model gone
		{"groq model_decommissioned", providerError(400, "invalid_request_error", "model_decommissioned", "The model `mixtral-8x7b-32768` has been decommissioned and is no longer supported."), schemas.FailureClassModelGone},
		{"openai deprecated model", providerError(404, "invalid_request_error", "model_not_found", "The model `gpt-4-32k` has been deprecated"), schemas.FailureClassModelGone},
		{"bedrock end of life 404", providerError(404, "ResourceNotFoundException", "", "This model version has reached the end of its life"), schemas.FailureClassModelGone},
		{"bedrock end of life 400", providerError(400, "ValidationException", "", "This model version has reached the end of its life"), schemas.FailureClassModelGone},

		// caller fault
		{"openai context length", providerError(400, "invalid_request_error", "context_length_exceeded", "This model's maximum context length is 128000 tokens."), schemas.FailureClassCallerFault},
		{"openai encrypted content", providerError(400, "invalid_request_error", "invalid_encrypted_content", "Encrypted content could not be verified."), schemas.FailureClassCallerFault},
		{"azure content filter", providerError(400, "", "content_filter", "The response was filtered due to the prompt triggering Azure OpenAI's content management policy."), schemas.FailureClassCallerFault},
		{"anthropic invalid_request_error", providerError(400, "invalid_request_error", "", "messages: at least one message is required"), schemas.FailureClassCallerFault},
		{"bedrock unsupported field", providerError(400, "ValidationException", "", "This model doesn't support the reasoningContent.reasoningText.signature field. Remove reasoningContent.reasoningText.signature and try again."), schemas.FailureClassCallerFault},
		{"gemini INVALID_ARGUMENT", providerError(400, "INVALID_ARGUMENT", "400", "Invalid JSON payload received. Unknown name \"foo\"."), schemas.FailureClassCallerFault},
		{"413 payload too large", providerError(413, "invalid_request_error", "", "Request too large"), schemas.FailureClassCallerFault},
		{"422 unprocessable", providerError(422, "validation_error", "", "Input validation error"), schemas.FailureClassCallerFault},
		{"vertex PERMISSION_DENIED on project", providerError(403, "PERMISSION_DENIED", "", "Vertex AI API has not been used in project 123 before or it is disabled."), schemas.FailureClassCredential},

		// shapes captured from provider docs and reports
		{"anthropic spend limit 429", providerError(429, "rate_limit_error", "", "You have reached your API usage limits: your organization has crossed its monthly spend limit."), schemas.FailureClassQuota},
		{"gemini prepayment credits", providerError(429, "RESOURCE_EXHAUSTED", "429", "Your prepayment credits are depleted. Please go to AI Studio to top up."), schemas.FailureClassQuota},
		{"gemini spending cap", providerError(429, "RESOURCE_EXHAUSTED", "429", "Your billing account has exceeded its monthly spending cap."), schemas.FailureClassQuota},
		{"vertex billing disabled", providerError(403, "PERMISSION_DENIED", "", "This API method requires billing to be enabled. Please enable billing on project 123."), schemas.FailureClassQuota},
		{"openrouter key limit", providerError(403, "", "403", "Key limit exceeded (total limit). Manage it using https://openrouter.ai/settings/keys"), schemas.FailureClassQuota},
		{"openai account not active", providerError(429, "", "", "Your account is not active, please check your billing details on our website."), schemas.FailureClassQuota},
		{"openai spend limit code", providerError(429, "insufficient_quota", "organization_spend_limit_exceeded", "Organization spend limit reached"), schemas.FailureClassQuota},
		{"bedrock service quota stays terminal as before", providerError(400, "ServiceQuotaExceededException", "", ""), schemas.FailureClassCallerFault},
		{"bedrock token per day throttle", providerError(429, "ThrottlingException", "", "Too many tokens per day, please wait before trying again."), schemas.FailureClassRateLimit},
		{"groq tpm request too large rotates as before", providerError(413, "tokens", "rate_limit_exceeded", "Request too large for model `llama-3.1-8b-instant` in organization `org` service tier `on_demand` on tokens per minute (TPM): Limit 6000, Requested 7000."), schemas.FailureClassRateLimit},
		{"anthropic self-set usage limit", providerError(400, "invalid_request_error", "", "You have reached your specified API usage limits."), schemas.FailureClassQuota},
		{"groq blocked_api_access", providerError(400, "invalid_request_error", "blocked_api_access", ""), schemas.FailureClassQuota},
		{"account has been flagged stays credential", providerError(403, "", "", "Your account has been flagged for potential abuse."), schemas.FailureClassCredential},
		{"401 with location wording stays credential", providerError(401, "", "", "User location is not supported for the API use."), schemas.FailureClassCredential},
		{"bedrock batch job not found", providerError(404, "ResourceNotFoundException", "", "The requested job was not found."), schemas.FailureClassUnknown},
		{"network error type", &schemas.BifrostError{StatusCode: Ptr(502), Error: &schemas.ErrorField{Message: schemas.ErrProviderNetworkError, Type: Ptr(schemas.ProviderConnectionFailed)}}, schemas.FailureClassTransient},
		{"gemini location", providerError(400, "FAILED_PRECONDITION", "400", "User location is not supported for the API use."), schemas.FailureClassRegionBlocked},
		{"gemini country", providerError(400, "FAILED_PRECONDITION", "400", "Gemini API free tier is not available in your country. Please enable billing on your project in Google AI Studio."), schemas.FailureClassRegionBlocked},
		{"anthropic on bedrock region", providerError(400, "ValidationException", "", "Access to Anthropic models is not allowed from unsupported countries, regions, or territories."), schemas.FailureClassRegionBlocked},
		{"gemini api key expired", providerError(400, "INVALID_ARGUMENT", "400", "API key expired. Please renew the API key."), schemas.FailureClassCredential},
		{"gemini leaked key", providerError(403, "PERMISSION_DENIED", "", "Your API key was reported as leaked. Please use another API key."), schemas.FailureClassCredential},
		{"bedrock api key rejected", providerError(403, "AccessDeniedException", "", "Authentication failed: Please make sure your API Key is valid."), schemas.FailureClassCredential},
		{"bedrock mantle api key format", providerError(403, "permission_error", "", "Invalid API Key format: Must start with pre-defined prefix"), schemas.FailureClassCredential},
		{"groq organization restricted", providerError(400, "invalid_request_error", "organization_restricted", "Organization has been restricted. Please reach out to support if you believe this is a mistake."), schemas.FailureClassCredential},
		{"openai project no access 403", providerError(403, "invalid_request_error", "model_not_found", "Project `proj_abc` does not have access to model `gpt-5.2`"), schemas.FailureClassModelAccess},
		{"groq model blocked at org", providerError(403, "permissions_error", "model_permission_blocked_org", "The model `openai/gpt-oss-120b` is blocked at the organization level."), schemas.FailureClassModelAccess},
		{"groq model terms required", providerError(400, "invalid_request_error", "model_terms_required", "The model `playai-tts` requires terms acceptance."), schemas.FailureClassModelAccess},
		{"mistral unknown_model", providerError(400, "invalid_request_error", "unknown_model", "A human-readable description of the error."), schemas.FailureClassModelAccess},
		{"mistral model_not_found type", providerError(404, "model_not_found", "3040", "The model `mistral-ocr-2411` does not exist for router `ocr`."), schemas.FailureClassModelAccess},
		{"mistral invalid_model type", providerError(400, "invalid_model", "1500", "Invalid model: mistral-medium-3.5"), schemas.FailureClassModelAccess},
		{"anthropic model not available", providerError(404, "not_found_error", "", "Claude Fable 5 is not available. Please use Opus 4.8."), schemas.FailureClassModelAccess},
		{"openrouter no endpoints", providerError(404, "not_found", "404", "No endpoints found for anthropic/claude-3.7-sonnet:thinking."), schemas.FailureClassModelAccess},
		{"openrouter no allowed providers", providerError(404, "", "404", "No allowed providers are available for the selected model."), schemas.FailureClassModelAccess},
		{"cohere model not found", providerError(404, "", "", "model 'xyz' not found, make sure the correct model ID was used and that you have access to the model."), schemas.FailureClassModelAccess},
		{"vertex project not allowed publisher model", providerError(400, "FAILED_PRECONDITION", "", "Project 123 is not allowed to use Publisher Model projects/123/locations/us-central1/publishers/anthropic/models/claude-opus-4-8"), schemas.FailureClassModelAccess},
		{"vertex not servable in region", providerError(400, "FAILED_PRECONDITION", "", "Publisher Model is not servable in region us-central1."), schemas.FailureClassModelAccess},
		{"bedrock use case form", providerError(404, "ResourceNotFoundException", "FTUFormNotFilled", "Model use case details have not been submitted for this account."), schemas.FailureClassModelAccess},
		{"azure 410 gone", providerError(410, "", "", "The model associated with the deployment is deprecated and no longer available."), schemas.FailureClassModelGone},
		{"azure ServiceModelDeprecating", providerError(400, "", "ServiceModelDeprecating", "The model 'Format:OpenAI,Name:gpt-4o,Version:2024-11-20' is in deprecation."), schemas.FailureClassModelGone},
		{"gemini model no longer available", providerError(404, "NOT_FOUND", "404", "This model models/gemini-2.0-flash is no longer available. Please update your code to use models/gemini-2.5-flash."), schemas.FailureClassModelGone},
		{"cohere model removed", providerError(404, "", "", "model 'embed-english-v2.0' was removed on April 4, 2026. See https://docs.cohere.com/docs/deprecations"), schemas.FailureClassModelGone},
		{"mistral guardrail 403 rotates as before", providerError(403, "", "", "Content blocked by guardrail"), schemas.FailureClassCredential},
		{"openrouter moderation 403 rotates as before", providerError(403, "content_policy_violation", "403", "Your chosen model requires moderation and your input was flagged"), schemas.FailureClassCredential},
		{"openrouter moderation flagged only", providerError(403, "", "403", "Moderation: your input was flagged"), schemas.FailureClassCredential},
		{"openrouter per-key guardrail 403 rotates as before", providerError(403, "permission_denied", "403", "Request blocked: prompt injection patterns detected"), schemas.FailureClassCredential},
		{"cohere size limit exceeded rotates as before", providerError(400, "", "", "too many tokens: size limit exceeded by 11326 tokens. Try using shorter or fewer inputs."), schemas.FailureClassRateLimit},
		{"anthropic prompt too long", providerError(400, "invalid_request_error", "", "prompt is too long: 209375 tokens > 200000 maximum"), schemas.FailureClassCallerFault},
		{"bedrock input too long", providerError(400, "ValidationException", "", "Input is too long for requested model."), schemas.FailureClassCallerFault},
		{"gemini input token count", providerError(400, "INVALID_ARGUMENT", "400", "The input token count (185586) exceeds the maximum number of tokens allowed (131072)."), schemas.FailureClassCallerFault},
		{"cohere unknown field 422", providerError(422, "", "", "unknown field: parameter tool_content is not a valid field."), schemas.FailureClassCallerFault},
		{"azure operation not supported", providerError(400, "", "OperationNotSupported", "The completion operation does not work with the specified model, gpt-35-turbo."), schemas.FailureClassCallerFault},
		{"azure network acl 403", providerError(403, "", "403", "Access denied due to Virtual Network/Firewall rules."), schemas.FailureClassCredential},
		{"groq capacity 498", providerError(498, "", "capacity_exceeded", ""), schemas.FailureClassUnknown},

		// unknown
		{"404 empty body", providerError(404, "", "", "resource not found (404)"), schemas.FailureClassUnknown},
		{"404 html", &schemas.BifrostError{StatusCode: Ptr(404), Error: &schemas.ErrorField{Message: schemas.ErrProviderResponseHTML, Error: errors.New("<html>")}}, schemas.FailureClassUnknown},
		{"404 wrong path parsed", providerError(404, "invalid_request_error", "", "Invalid URL (POST /v1/chat/completion)"), schemas.FailureClassUnknown},
		{"405 method not allowed", providerError(405, "", "", "provider API error: method not allowed"), schemas.FailureClassUnknown},
		{"520 unlisted 5xx", providerError(520, "", "", "provider API error"), schemas.FailureClassUnknown},
		{"internal bifrost error", &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "tracer not found"}}, schemas.FailureClassUnknown},
		{"bedrock in-stream validation", &schemas.BifrostError{IsBifrostError: true, Type: Ptr("ValidationException"), Error: &schemas.ErrorField{Type: Ptr("ValidationException"), Message: "This model version has reached the end of its life"}}, schemas.FailureClassUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyFailure(tc.err); got != tc.want {
				t.Errorf("ClassifyFailure = %q, want %q", got, tc.want)
			}
		})
	}
}

// What a status decides on its own, with no provider body behind it: the per-key statuses
// rotate (401/403 as a rejected credential, 402 as quota, 429 as a rate limit), the
// transient 5xx set retries the same key (529 included: an overloaded provider is not this
// key's fault, so burning the other keys on it would just multiply the failures), and a
// request-bound 4xx or an unlisted status does neither.
func TestClassifyFailure_StatusOnly(t *testing.T) {
	want := map[int]schemas.FailureClass{
		401: schemas.FailureClassCredential,
		402: schemas.FailureClassQuota,
		403: schemas.FailureClassCredential,
		429: schemas.FailureClassRateLimit,
		500: schemas.FailureClassTransient,
		502: schemas.FailureClassTransient,
		503: schemas.FailureClassTransient,
		504: schemas.FailureClassTransient,
		529: schemas.FailureClassTransient,
		400: schemas.FailureClassUnknown,
		404: schemas.FailureClassUnknown,
		422: schemas.FailureClassUnknown,
		520: schemas.FailureClassUnknown,
	}
	for status, class := range want {
		err := &schemas.BifrostError{StatusCode: Ptr(status), Error: &schemas.ErrorField{Message: schemas.ErrProviderResponseEmpty + " (HTTP 0)"}}
		if got := ClassifyFailure(err); got != class {
			t.Errorf("status %d alone: got %q, want %q", status, got, class)
		}
		if got := ClassifyFailure(err); got.IsPerKey() != (status == 401 || status == 402 || status == 403 || status == 429) {
			t.Errorf("status %d alone: IsPerKey = %v", status, got.IsPerKey())
		}
	}
}

// A 404 that names the request field it could not resolve is the request's fault; a 404
// that names nothing is not classified, so the retry loop leaves it alone as before.
func TestClassifyFailure_404Param(t *testing.T) {
	withParam := providerError(404, "invalid_request_error", "", "No response found with id 'resp_123'.")
	withParam.Error.Param = "previous_response_id"
	if got := ClassifyFailure(withParam); got != schemas.FailureClassCallerFault {
		t.Errorf("404 naming a request field: got %q, want caller_fault", got)
	}
	withParam.Error.Param = ""
	if got := ClassifyFailure(withParam); got != schemas.FailureClassUnknown {
		t.Errorf("404 naming no field: got %q, want unknown", got)
	}
}

func TestClassifyFailure_NilAndTopLevelType(t *testing.T) {
	if got := ClassifyFailure(nil); got != "" {
		t.Errorf("nil error classified as %q", got)
	}
	// Bedrock, Mistral and Cohere set the top-level Type as well as Error.Type; a parser
	// that sets only the top-level one must still be read.
	topLevel := &schemas.BifrostError{StatusCode: Ptr(403), Type: Ptr("AccessDeniedException"), Error: &schemas.ErrorField{Message: "You don't have access to the model with the specified model ID."}}
	if got := ClassifyFailure(topLevel); got != schemas.FailureClassModelAccess {
		t.Errorf("top-level Type ignored: got %q, want model_access", got)
	}
}

func TestFailureClass_Policy(t *testing.T) {
	permanent := []schemas.FailureClass{schemas.FailureClassCredential, schemas.FailureClassModelAccess, schemas.FailureClassQuota, schemas.FailureClassModelGone}
	for _, c := range permanent {
		if !c.IsPermanentPerKey() || !c.IsPerKey() {
			t.Errorf("%s should be a permanent per-key class", c)
		}
		if c.FailReason() == "" {
			t.Errorf("%s has no fail_reason label", c)
		}
	}
	if !schemas.FailureClassRateLimit.IsPerKey() || schemas.FailureClassRateLimit.IsPermanentPerKey() {
		t.Errorf("rate_limit should be per-key but not permanent")
	}
	// A location block can be bound to the key's project or account (a Gemini free-tier
	// project, a Bedrock account with an unsupported billing address), so it walks the pool
	// like the other permanent per-key failures.
	if !schemas.FailureClassRegionBlocked.IsPerKey() || !schemas.FailureClassRegionBlocked.IsPermanentPerKey() || schemas.FailureClassRegionBlocked.FailReason() != "region_blocked_error" {
		t.Errorf("region_blocked should be a permanent per-key failure carrying its label")
	}
	for _, c := range []schemas.FailureClass{schemas.FailureClassTransient, schemas.FailureClassCallerFault, schemas.FailureClassUnknown} {
		if c.IsPerKey() || c.IsPermanentPerKey() || c.FailReason() != "" {
			t.Errorf("%s should carry no per-key policy and no label", c)
		}
	}
}
