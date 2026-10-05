package bifrost

import (
	"context"
	"errors"
	"fmt"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// providerError builds the error a provider parser would hand the retry loop: a status,
// the provider's type and code, and its message.
func providerError(status int, errType, code, message string) *schemas.BifrostError {
	err := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     Ptr(status),
		Error:          &schemas.ErrorField{Message: message},
	}
	if errType != "" {
		err.Error.Type = Ptr(errType)
	}
	if code != "" {
		err.Error.Code = Ptr(code)
	}
	return err
}

// poolKeyProvider behaves like the rotating closure in selectKeyFromProviderForModelWithPool:
// first key that is neither dead nor used, then a fresh round over the non-dead keys, then
// errAllKeysDead.
func poolKeyProvider(keys []schemas.Key) func(usedKeyIDs, deadKeyIDs map[string]bool) (schemas.Key, error) {
	return func(usedKeyIDs, deadKeyIDs map[string]bool) (schemas.Key, error) {
		for _, k := range keys {
			if !deadKeyIDs[k.ID] && !usedKeyIDs[k.ID] {
				return k, nil
			}
		}
		for _, k := range keys {
			if !deadKeyIDs[k.ID] {
				for id := range usedKeyIDs {
					delete(usedKeyIDs, id)
				}
				return k, nil
			}
		}
		return schemas.Key{}, errAllKeysDead
	}
}

func rotationTestContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	return ctx
}

func attemptTrail(t *testing.T, ctx *schemas.BifrostContext, want int) []schemas.KeyAttemptRecord {
	t.Helper()
	trail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	if len(trail) != want {
		t.Fatalf("attempt trail has %d record(s), want %d: %+v", len(trail), want, trail)
	}
	return trail
}

var (
	rotationKeyA = schemas.Key{ID: "key-a", Name: "Key A"}
	rotationKeyB = schemas.Key{ID: "key-b", Name: "Key B"}
)

// A rejected credential is walked past even at the default max_retries of 0: the failed
// attempt is granted back because the key left the pool, and the next key serves.
func TestExecuteRequestWithRetries_PermanentPerKeyFailureRotatesAtZeroRetries(t *testing.T) {
	ctx := rotationTestContext()
	var served []string
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		served = append(served, k.ID)
		if k.ID == rotationKeyA.ID {
			return "", providerError(401, "invalid_request_error", "invalid_api_key", "Incorrect API key provided")
		}
		return "ok", nil
	}

	result, err := executeRequestWithRetries(ctx, createTestConfig(0, 0, 0), handler,
		poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("expected the second key to serve, got %v", err)
	}
	if result != "ok" || len(served) != 2 || served[0] != rotationKeyA.ID || served[1] != rotationKeyB.ID {
		t.Fatalf("served %v, want key-a then key-b", served)
	}
	trail := attemptTrail(t, ctx, 2)
	first := trail[0]
	if first.FailureClass != schemas.FailureClassCredential || first.StatusCode == nil || *first.StatusCode != 401 {
		t.Errorf("first attempt class=%q status=%v, want credential 401", first.FailureClass, first.StatusCode)
	}
	if first.FailReason == nil || *first.FailReason != "authentication_error" || !first.TriggeredRotation {
		t.Errorf("first attempt fail_reason=%v triggered_rotation=%v, want authentication_error and true", first.FailReason, first.TriggeredRotation)
	}
	if trail[1].FailReason != nil || trail[1].FailureClass != "" || trail[1].StatusCode != nil {
		t.Errorf("successful attempt carries failure data: %+v", trail[1])
	}
}

// A model the first key cannot reach is tried on the next key, and when no key serves it
// the caller gets the provider's own 404, not a credentials error.
func TestExecuteRequestWithRetries_ModelNotFoundRotatesAndKeepsUpstreamError(t *testing.T) {
	for _, maxRetries := range []int{0, 3} {
		ctx := rotationTestContext()
		calls := 0
		handler := func(k schemas.Key) (string, *schemas.BifrostError) {
			calls++
			return "", providerError(404, "invalid_request_error", "model_not_found", "The model `gpt-4o` does not exist or you do not have access to it.")
		}

		_, err := executeRequestWithRetries(ctx, createTestConfig(maxRetries, 0, 0), handler,
			poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
			schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
		if err == nil || err.StatusCode == nil || *err.StatusCode != 404 || err.Error == nil || err.Error.Code == nil || *err.Error.Code != "model_not_found" {
			t.Fatalf("max_retries=%d: want the upstream 404 model_not_found back, got %v", maxRetries, err)
		}
		if calls != 2 {
			t.Errorf("max_retries=%d: %d upstream calls, want one per key", maxRetries, calls)
		}
		trail := attemptTrail(t, ctx, 2)
		for i, record := range trail {
			if record.FailureClass != schemas.FailureClassModelAccess || record.FailReason == nil || *record.FailReason != "model_access_error" {
				t.Errorf("max_retries=%d attempt %d: class=%q fail_reason=%v, want model_access / model_access_error", maxRetries, i, record.FailureClass, record.FailReason)
			}
		}
		if !trail[0].TriggeredRotation || trail[1].TriggeredRotation {
			t.Errorf("max_retries=%d: triggered_rotation = %v,%v, want true,false", maxRetries, trail[0].TriggeredRotation, trail[1].TriggeredRotation)
		}
	}
}

// When the pool holds both a rejected credential and a key that cannot reach the model,
// the model error is what comes back, whichever key died last: it is the fact the caller
// can act on, and the raw 401 is exactly what the 502 collapse exists to hide.
func TestExecuteRequestWithRetries_MixedExhaustionReturnsModelError(t *testing.T) {
	for _, order := range [][]schemas.Key{{rotationKeyA, rotationKeyB}, {rotationKeyB, rotationKeyA}} {
		ctx := rotationTestContext()
		handler := func(k schemas.Key) (string, *schemas.BifrostError) {
			if k.ID == rotationKeyA.ID {
				return "", providerError(401, "invalid_request_error", "invalid_api_key", "Incorrect API key provided")
			}
			return "", providerError(404, "invalid_request_error", "model_not_found", "The model `gpt-4o` does not exist or you do not have access to it.")
		}
		_, err := executeRequestWithRetries(ctx, createTestConfig(3, 0, 0), handler, poolKeyProvider(order),
			schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
		if err == nil || err.StatusCode == nil || *err.StatusCode != 404 {
			t.Fatalf("order %s then %s: want the 404 model_not_found back, got %v", order[0].ID, order[1].ID, err)
		}
	}
}

// A model error stays a model error when the pool filter suppresses the other keys: the
// caller gets the provider's 404, not a 503 about suppressed keys.
func TestExecuteRequestWithRetries_FilteredPoolKeepsModelError(t *testing.T) {
	ctx := rotationTestContext()
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		return "", providerError(404, "invalid_request_error", "model_not_found", "The model `gpt-4o` does not exist or you do not have access to it.")
	}
	keyProvider := func(_, deadKeyIDs map[string]bool) (schemas.Key, error) {
		if deadKeyIDs[rotationKeyA.ID] {
			return schemas.Key{}, errAllKeysFiltered
		}
		return rotationKeyA, nil
	}
	_, err := executeRequestWithRetries(ctx, createTestConfig(2, 0, 0), handler, keyProvider,
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
	if err == nil || err.StatusCode == nil || *err.StatusCode != 404 {
		t.Fatalf("want the 404 back, got %v", err)
	}
}

// A keyless provider keeps the same-key retries a 401 has always earned under max_retries,
// and gets none for a model error, which it never did.
func TestExecuteRequestWithRetries_KeylessRetriesAsBefore(t *testing.T) {
	cases := []struct {
		name  string
		err   *schemas.BifrostError
		calls int
	}{
		{"401 is retried within the budget", providerError(401, "invalid_request_error", "invalid_api_key", "Incorrect API key provided"), 3},
		{"429 quota is retried within the budget", providerError(429, "insufficient_quota", "insufficient_quota", "You exceeded your current quota, please check your plan and billing details."), 3},
		{"404 model_not_found is not retried", providerError(404, "invalid_request_error", "model_not_found", "The model `x` does not exist or you do not have access to it."), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := rotationTestContext()
			calls := 0
			handler := func(k schemas.Key) (string, *schemas.BifrostError) {
				calls++
				return "", tc.err
			}
			_, _ = executeRequestWithRetries(ctx, createTestConfig(2, 0, 0), handler, nil,
				schemas.ChatCompletionRequest, schemas.OpenAI, "x", nil, NewDefaultLogger(schemas.LogLevelError))
			if calls != tc.calls {
				t.Errorf("%d upstream calls, want %d", calls, tc.calls)
			}
		})
	}
}

// A pool filter that admits only the dead key leaves nothing to walk to. On a granted
// attempt that is the same as the pool running out: the upstream error comes back, not a
// 503 blaming the filter.
func TestExecuteRequestWithRetries_FilteredPoolOnGrantedAttemptKeepsUpstreamError(t *testing.T) {
	ctx := rotationTestContext()
	calls := 0
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		calls++
		return "", providerError(401, "invalid_request_error", "invalid_api_key", "Incorrect API key provided")
	}
	keyProvider := func(_, deadKeyIDs map[string]bool) (schemas.Key, error) {
		if deadKeyIDs[rotationKeyA.ID] {
			return schemas.Key{}, errAllKeysFiltered
		}
		return rotationKeyA, nil
	}
	_, err := executeRequestWithRetries(ctx, createTestConfig(0, 0, 0), handler, keyProvider,
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
	if err == nil || err.StatusCode == nil || *err.StatusCode != 401 {
		t.Fatalf("want the raw 401 back, got %v", err)
	}
	if calls != 1 {
		t.Errorf("%d upstream calls, want 1", calls)
	}
}

// A selector that ignores the exclusion and hands back a dead key cannot extend the loop:
// the walk is granted once per key, so the request ends within the pool size.
func TestExecuteRequestWithRetries_DeadKeyRepickedDoesNotExtendLoop(t *testing.T) {
	ctx := rotationTestContext()
	calls := 0
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		calls++
		return "", providerError(401, "invalid_request_error", "invalid_api_key", "Incorrect API key provided")
	}
	stubborn := func(_, _ map[string]bool) (schemas.Key, error) { return rotationKeyA, nil }
	_, err := executeRequestWithRetries(ctx, createTestConfig(0, 0, 0), handler, stubborn,
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
	if err == nil || err.StatusCode == nil || *err.StatusCode != 401 {
		t.Fatalf("want the raw 401 back, got %v", err)
	}
	if calls != 2 {
		t.Errorf("%d upstream calls, want 2: one grant for the one key, then the budget is spent", calls)
	}
}

// Within the configured budget, a pool in which every credential was rejected still
// collapses into 502 upstream_credentials_exhausted, so the caller does not mistake the
// upstream 401 for a problem with its own Bifrost key.
func TestExecuteRequestWithRetries_AllCredentialsDeadWithinBudgetIs502(t *testing.T) {
	ctx := rotationTestContext()
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		return "", providerError(401, "invalid_request_error", "invalid_api_key", "Incorrect API key provided")
	}

	_, err := executeRequestWithRetries(ctx, createTestConfig(3, 0, 0), handler,
		poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
	if err == nil || err.StatusCode == nil || *err.StatusCode != 502 || err.ExtraFields.ErrorType != schemas.ErrorTypeProviderCredentialsExhausted {
		t.Fatalf("want 502 upstream_credentials_exhausted, got %v", err)
	}
	attemptTrail(t, ctx, 2)
}

// Beyond the configured budget the granted attempts only exist to reach another key; when
// none is left, the last upstream error stands. A single key at max_retries 0 therefore
// keeps returning its raw 401, as it always has.
func TestExecuteRequestWithRetries_PermanentFailureBeyondBudgetKeepsUpstreamError(t *testing.T) {
	t.Run("two keys, both rejected", func(t *testing.T) {
		ctx := rotationTestContext()
		calls := 0
		handler := func(k schemas.Key) (string, *schemas.BifrostError) {
			calls++
			return "", providerError(401, "invalid_request_error", "invalid_api_key", "Incorrect API key provided for "+k.ID)
		}
		_, err := executeRequestWithRetries(ctx, createTestConfig(0, 0, 0), handler,
			poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
			schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
		if err == nil || err.StatusCode == nil || *err.StatusCode != 401 || err.Error == nil || err.Error.Message != "Incorrect API key provided for key-b" {
			t.Fatalf("want the second key's raw 401 back, got %v", err)
		}
		if calls != 2 {
			t.Errorf("%d upstream calls, want 2", calls)
		}
		if retries, _ := ctx.Value(schemas.BifrostContextKeyNumberOfRetries).(int); retries != 1 {
			t.Errorf("number_of_retries = %d, want 1: one retry ran, the key selection that found nothing left is not a retry", retries)
		}
	})
	t.Run("fixed key", func(t *testing.T) {
		ctx := rotationTestContext()
		calls := 0
		handler := func(k schemas.Key) (string, *schemas.BifrostError) {
			calls++
			return "", providerError(401, "invalid_request_error", "invalid_api_key", "Incorrect API key provided")
		}
		fixed := func(_, deadKeyIDs map[string]bool) (schemas.Key, error) {
			if deadKeyIDs[rotationKeyA.ID] {
				return schemas.Key{}, errAllKeysDead
			}
			return rotationKeyA, nil
		}
		_, err := executeRequestWithRetries(ctx, createTestConfig(0, 0, 0), handler, fixed,
			schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
		if err == nil || err.StatusCode == nil || *err.StatusCode != 401 {
			t.Fatalf("want the raw 401 back, got %v", err)
		}
		if calls != 1 {
			t.Errorf("%d upstream calls, want exactly 1: there is no other key to reach", calls)
		}
		trail := attemptTrail(t, ctx, 1)
		if trail[0].TriggeredRotation {
			t.Errorf("a fixed key cannot rotate, yet triggered_rotation is true")
		}
		if retries, _ := ctx.Value(schemas.BifrostContextKeyNumberOfRetries).(int); retries != 0 {
			t.Errorf("number_of_retries = %d, want 0: no retry ran", retries)
		}
	})
}

// OpenAI reports an exhausted balance as a 429 with code insufficient_quota. That is a
// billing fact about the account, not a rate limit: the key is dead for the request, the
// next key is tried without backoff, and the trail says billing, not rate limit.
func TestExecuteRequestWithRetries_InsufficientQuotaIsPermanent(t *testing.T) {
	ctx := rotationTestContext()
	var sawDead bool
	keyProvider := func(usedKeyIDs, deadKeyIDs map[string]bool) (schemas.Key, error) {
		if deadKeyIDs[rotationKeyA.ID] {
			sawDead = true
			return rotationKeyB, nil
		}
		if usedKeyIDs[rotationKeyA.ID] {
			t.Fatalf("insufficient_quota landed in usedKeyIDs: the key would be retried later in the request")
		}
		return rotationKeyA, nil
	}
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		if k.ID == rotationKeyA.ID {
			return "", providerError(429, "insufficient_quota", "insufficient_quota", "You exceeded your current quota, please check your plan and billing details.")
		}
		return "ok", nil
	}

	result, err := executeRequestWithRetries(ctx, createTestConfig(0, 0, 0), handler, keyProvider,
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
	if err != nil || result != "ok" || !sawDead {
		t.Fatalf("result=%q err=%v sawDead=%v, want ok on the second key with the first marked dead", result, err, sawDead)
	}
	trail := attemptTrail(t, ctx, 2)
	if trail[0].FailureClass != schemas.FailureClassQuota || trail[0].FailReason == nil || *trail[0].FailReason != "billing_error" {
		t.Errorf("class=%q fail_reason=%v, want quota / billing_error", trail[0].FailureClass, trail[0].FailReason)
	}
}

// A keyless provider has no key to rotate to, so a permanent per-key failure ends the
// request at once instead of repeating the same refused call for the whole budget.
func TestExecuteRequestWithRetries_KeylessProviderDoesNotRetryPermanentFailure(t *testing.T) {
	cases := []struct {
		name   string
		err    *schemas.BifrostError
		status int
	}{
		{"model not found", providerError(404, "invalid_request_error", "model_not_found", "The model `x` does not exist or you do not have access to it."), 404},
		{"region block", providerError(403, "invalid_request_error", "unsupported_country_region_territory", "Country, region, or territory not supported"), 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := rotationTestContext()
			calls := 0
			handler := func(k schemas.Key) (string, *schemas.BifrostError) {
				calls++
				return "", tc.err
			}
			_, err := executeRequestWithRetries(ctx, createTestConfig(3, 0, 0), handler, nil,
				schemas.ChatCompletionRequest, schemas.OpenAI, "x", nil, NewDefaultLogger(schemas.LogLevelError))
			if err == nil || err.StatusCode == nil || *err.StatusCode != tc.status {
				t.Fatalf("want the %d back, got %v", tc.status, err)
			}
			if calls != 1 {
				t.Errorf("%d upstream calls, want 1", calls)
			}
		})
	}
}

// A region block can be bound to the key rather than the gateway: Gemini blocks the free
// tier of a project where a billed project is served, and Bedrock checks the account's
// billing address. So the next key is tried, even at max_retries 0, and the trail records
// the rotation.
func TestExecuteRequestWithRetries_RegionBlockWalksPool(t *testing.T) {
	ctx := rotationTestContext()
	var served []string
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		served = append(served, k.ID)
		if k.ID == rotationKeyA.ID {
			return "", providerError(400, "FAILED_PRECONDITION", "400", "Gemini API free tier is not available in your country. Please enable billing on your project in Google AI Studio.")
		}
		return "ok", nil
	}
	result, err := executeRequestWithRetries(ctx, createTestConfig(0, 0, 0), handler,
		poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
		schemas.ChatCompletionRequest, schemas.Gemini, "gemini-2.5-pro", nil, NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("expected the billed project's key to serve, got %v", err)
	}
	if result != "ok" || len(served) != 2 || served[0] != rotationKeyA.ID || served[1] != rotationKeyB.ID {
		t.Fatalf("served %v, want key-a then key-b", served)
	}
	trail := attemptTrail(t, ctx, 2)
	first := trail[0]
	if first.FailureClass != schemas.FailureClassRegionBlocked || first.StatusCode == nil || *first.StatusCode != 400 {
		t.Errorf("first attempt class=%q status=%v, want region_blocked 400", first.FailureClass, first.StatusCode)
	}
	if first.FailReason == nil || *first.FailReason != "region_blocked_error" || !first.TriggeredRotation {
		t.Errorf("first attempt fail_reason=%v triggered_rotation=%v, want region_blocked_error and true", first.FailReason, first.TriggeredRotation)
	}
	if trail[1].FailReason != nil || trail[1].FailureClass != "" {
		t.Errorf("successful attempt carries failure data: %+v", trail[1])
	}
}

// When every key is blocked, the caller gets the provider's own error, never the 502
// credentials collapse: a block on the gateway's location is the fact they can act on.
// Same answer whether the pool ran out beyond the budget or within it.
func TestExecuteRequestWithRetries_RegionBlockOnEveryKeyReturnsTheProviderError(t *testing.T) {
	for _, maxRetries := range []int{0, 3} {
		t.Run(fmt.Sprintf("max_retries %d", maxRetries), func(t *testing.T) {
			ctx := rotationTestContext()
			calls := 0
			handler := func(k schemas.Key) (string, *schemas.BifrostError) {
				calls++
				return "", providerError(400, "ValidationException", "", "Access to Anthropic models is not allowed from unsupported countries, regions, or territories.")
			}
			_, err := executeRequestWithRetries(ctx, createTestConfig(maxRetries, 0, 0), handler,
				poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
				schemas.ChatCompletionRequest, schemas.Bedrock, "anthropic.claude-sonnet-4", nil, NewDefaultLogger(schemas.LogLevelError))
			if err == nil || err.StatusCode == nil || *err.StatusCode != 400 || err.Error == nil || err.Error.Type == nil || *err.Error.Type != "ValidationException" {
				t.Fatalf("want the provider's 400 ValidationException back, got %v", err)
			}
			if calls != 2 {
				t.Errorf("%d upstream calls, want 2 (one per key)", calls)
			}
			trail := attemptTrail(t, ctx, 2)
			if trail[0].FailureClass != schemas.FailureClassRegionBlocked || !trail[0].TriggeredRotation {
				t.Errorf("first record = %+v, want region_blocked with rotation", trail[0])
			}
			if trail[1].FailureClass != schemas.FailureClassRegionBlocked || trail[1].TriggeredRotation {
				t.Errorf("last record = %+v, want region_blocked without rotation", trail[1])
			}
		})
	}
}

// A rejected request is nobody's key's fault: no retry, no rotation, one attempt, and the
// provider's own type on the trail.
func TestExecuteRequestWithRetries_CallerFaultDoesNotRotate(t *testing.T) {
	cases := []struct {
		name string
		err  *schemas.BifrostError
		want schemas.FailureClass
	}{
		{"context length", providerError(400, "invalid_request_error", "context_length_exceeded", "This model's maximum context length is 128000 tokens."), schemas.FailureClassCallerFault},
		{"bodyless 404", providerError(404, "", "", "resource not found (404)"), schemas.FailureClassUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := rotationTestContext()
			calls := 0
			handler := func(k schemas.Key) (string, *schemas.BifrostError) {
				calls++
				return "", tc.err
			}
			_, err := executeRequestWithRetries(ctx, createTestConfig(3, 0, 0), handler,
				poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
				schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
			if err != tc.err {
				t.Fatalf("want the provider error back unchanged, got %v", err)
			}
			if calls != 1 {
				t.Errorf("%d upstream calls, want 1", calls)
			}
			trail := attemptTrail(t, ctx, 1)
			if trail[0].FailureClass != tc.want || trail[0].TriggeredRotation {
				t.Errorf("class=%q triggered_rotation=%v, want %q and false", trail[0].FailureClass, trail[0].TriggeredRotation, tc.want)
			}
			if tc.err.Error.Type != nil && (trail[0].FailReason == nil || *trail[0].FailReason != *tc.err.Error.Type) {
				t.Errorf("fail_reason=%v, want the provider type %q", trail[0].FailReason, *tc.err.Error.Type)
			}
		})
	}
}

// A transport failure (the provider never answered) is stamped on the trail exactly like a
// provider-answered failure: failure_class "transient" and the synthetic 502 that
// NewBifrostUpstreamConnectionError carries, with fail_reason falling back to Bifrost's own
// provider_connection_failed type. The retry reuses the same key, so no rotation is recorded.
func TestExecuteRequestWithRetries_TransportFailureStampsTransientAndSyntheticStatus(t *testing.T) {
	ctx := rotationTestContext()
	var served []string
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		served = append(served, k.ID)
		if len(served) == 1 {
			return "", providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, errors.New("dial tcp: connection refused"))
		}
		return "ok", nil
	}

	result, err := executeRequestWithRetries(ctx, createTestConfig(1, 0, 0), handler,
		poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("expected the retry on the same key to serve, got %v", err)
	}
	if result != "ok" || len(served) != 2 || served[0] != rotationKeyA.ID || served[1] != rotationKeyA.ID {
		t.Fatalf("served %v, want key-a twice (transport failures do not rotate)", served)
	}
	trail := attemptTrail(t, ctx, 2)
	first := trail[0]
	if first.FailureClass != schemas.FailureClassTransient || first.StatusCode == nil || *first.StatusCode != 502 {
		t.Errorf("transport failure attempt class=%q status=%v, want transient 502 (stamped even though the provider never answered)", first.FailureClass, first.StatusCode)
	}
	if first.FailReason == nil || *first.FailReason != schemas.ProviderConnectionFailed || first.TriggeredRotation {
		t.Errorf("first attempt fail_reason=%v triggered_rotation=%v, want provider_connection_failed and false", first.FailReason, first.TriggeredRotation)
	}
	if trail[1].FailReason != nil || trail[1].FailureClass != "" || trail[1].StatusCode != nil {
		t.Errorf("successful attempt carries failure data: %+v", trail[1])
	}
}

// A key that was rotated away from keeps the wait the provider asked for. The request ends on
// another key, so its final error carries that key's answer; the trail is the only place the
// rate-limited key's own hint survives, and it is what a load balancer needs to know how long
// to leave that key alone.
func TestExecuteRequestWithRetries_TrailKeepsThePerAttemptRetryHint(t *testing.T) {
	ctx := rotationTestContext()
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		if k.ID == rotationKeyA.ID {
			limited := providerError(429, "rate_limit_error", "rate_limit_exceeded", "Rate limit reached")
			limited.ExtraFields.RetryAfter = 12000
			return "", limited
		}
		return "ok", nil
	}

	result, err := executeRequestWithRetries(ctx, createTestConfig(1, 0, 0), handler,
		poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
	if err != nil || result != "ok" {
		t.Fatalf("expected the second key to serve, got %q %v", result, err)
	}

	trail := attemptTrail(t, ctx, 2)
	if trail[0].RetryAfter != 12000 {
		t.Errorf("rotated-away attempt kept retry_after_ms=%d, want 12000", trail[0].RetryAfter)
	}
	if trail[1].RetryAfter != 0 {
		t.Errorf("the successful attempt carries a hint: %+v", trail[1])
	}
}

// A failure the provider gave no hint for leaves the field at zero rather than inventing one.
func TestExecuteRequestWithRetries_TrailHasNoHintWhenTheProviderGaveNone(t *testing.T) {
	ctx := rotationTestContext()
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		if k.ID == rotationKeyA.ID {
			return "", providerError(401, "invalid_request_error", "invalid_api_key", "Incorrect API key provided")
		}
		return "ok", nil
	}

	if _, err := executeRequestWithRetries(ctx, createTestConfig(0, 0, 0), handler,
		poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError)); err != nil {
		t.Fatalf("expected the second key to serve, got %v", err)
	}

	if trail := attemptTrail(t, ctx, 2); trail[0].RetryAfter != 0 {
		t.Errorf("retry_after_ms=%d without a provider hint, want 0", trail[0].RetryAfter)
	}
}

// A selector failure that is neither errNoEligibleKeys nor errAllKeysDead is Bifrost's
// own machinery failing. Carrying no status it resolved to 400, blaming the caller for
// an operational fault and hiding it from 5xx dashboards.
func TestExecuteRequestWithRetries_SelectorFailureIsInternal(t *testing.T) {
	ctx := rotationTestContext()
	called := false
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		called = true
		return "ok", nil
	}
	selector := func(usedKeyIDs, deadKeyIDs map[string]bool) (schemas.Key, error) {
		return schemas.Key{}, errors.New("selector exploded")
	}

	_, err := executeRequestWithRetries(ctx, createTestConfig(0, 0, 0), handler, selector,
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
	if err == nil {
		t.Fatal("expected a selector failure to be returned")
	}
	if called {
		t.Error("provider was called despite key selection failing")
	}
	if !err.IsBifrostError {
		t.Error("selector failure is not attributed to Bifrost")
	}
	if got := err.EffectiveHTTPStatus(); got != 500 {
		t.Errorf("EffectiveHTTPStatus() = %d, want 500", got)
	}
	if err.ExtraFields.ErrorType != schemas.ErrorTypeBifrostInternal {
		t.Errorf("ErrorType = %q, want %q", err.ExtraFields.ErrorType, schemas.ErrorTypeBifrostInternal)
	}
	if got := schemas.ClassifyErrorType(err, schemas.ChatCompletionRequest); got != schemas.ErrorTypeBifrostInternal {
		t.Errorf("ClassifyErrorType() = %q, want %q", got, schemas.ErrorTypeBifrostInternal)
	}
}
