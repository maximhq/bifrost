package bifrost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// A key the caller brought (x-bf-direct-key) is the caller's own, so the provider refusing it is
// the answer whatever max_retries is. Collapsing it into 502 upstream_credentials_exhausted would
// tell the caller the gateway's keys ran out, and invite a retry with the same bad key. A single
// configured key, pinned, still collapses within the budget, since that credential is not the
// caller's.
func TestExecuteRequestWithRetries_DirectKeyRefusalIsTheCallersError(t *testing.T) {
	run := func(t *testing.T, retries int, direct bool) (*schemas.BifrostError, int) {
		t.Helper()
		ctx := rotationTestContext()
		if direct {
			ctx.SetValue(schemas.BifrostContextKeyDirectKey, rotationKeyA)
		}
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
		_, err := executeRequestWithRetries(ctx, createTestConfig(retries, 0, 0), handler, fixed,
			schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
		return err, calls
	}

	for _, retries := range []int{0, 1, 3} {
		err, calls := run(t, retries, true)
		if err == nil || err.StatusCode == nil || *err.StatusCode != 401 || err.ExtraFields.ErrorType == schemas.ErrorTypeProviderCredentialsExhausted {
			t.Errorf("max_retries %d: want the provider's 401 for the caller's own key, got %v", retries, err)
		}
		if calls != 1 {
			t.Errorf("max_retries %d: %d upstream calls, want 1: there is no other key to reach", retries, calls)
		}
	}
	if err, _ := run(t, 1, false); err == nil || err.StatusCode == nil || *err.StatusCode != 502 || err.ExtraFields.ErrorType != schemas.ErrorTypeProviderCredentialsExhausted {
		t.Errorf("a pinned configured key within the budget: want 502 upstream_credentials_exhausted, got %v", err)
	}
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

// TestExecuteRequestWithRetries_BackoffSkippedOnlyForACredentialSwap pins when a retry waits out
// its backoff. A key the provider refused outright is swapped for another with no wait: the new
// credential has nothing to wait for. A rate limit waits even when the key changes, because an
// account-level limit is shared by every key of the account, and a retry on the same key waits
// too. The routing trail says which of the two happened.
//
// The backoff is an hour, so a retry that waits never runs: the request's deadline ends it during the
// wait. A retry that skips the wait runs at once and serves. The test reads which of the two
// happened, not how long anything took, so a slow test worker cannot fail it.
func TestExecuteRequestWithRetries_BackoffSkippedOnlyForACredentialSwap(t *testing.T) {
	const backoff = time.Hour
	rateLimited := func() *schemas.BifrostError {
		return providerError(429, "rate_limit_error", "rate_limit_exceeded", "Rate limit reached")
	}
	refused := func() *schemas.BifrostError {
		return providerError(401, "invalid_request_error", "invalid_api_key", "Incorrect API key provided")
	}
	for _, tc := range []struct {
		name       string
		fail       func() *schemas.BifrostError
		maxRetries int
		keys       []schemas.Key
		wantWait   bool
		wantNote   string
	}{
		{"a refused key swapped for another", refused, 0, []schemas.Key{rotationKeyA, rotationKeyB}, false, "rotated key=Key B"},
		{"a rate-limited key swapped for another", rateLimited, 1, []schemas.Key{rotationKeyA, rotationKeyB}, true, "rotated key=Key B"},
		{"a rate-limited key retried on itself", rateLimited, 1, []schemas.Key{rotationKeyA}, true, "same key=Key A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Long enough for an attempt however slow the worker, far too short for the backoff.
			deadline := 30 * time.Second
			if tc.wantWait {
				deadline = time.Second
			}
			ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(deadline))
			ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
			calls := 0
			handler := func(k schemas.Key) (string, *schemas.BifrostError) {
				calls++
				if calls == 1 {
					return "", tc.fail()
				}
				return "ok", nil
			}
			result, err := executeRequestWithRetries(ctx, createTestConfig(tc.maxRetries, backoff, backoff), handler,
				poolKeyProvider(tc.keys), schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
			message := ""
			if err != nil && err.Error != nil {
				message = err.Error.Message
			}
			if tc.wantWait && (calls != 1 || !strings.Contains(message, "during retry backoff")) {
				t.Fatalf("want the retry to wait out its backoff until the deadline ended it, got %d attempt(s), result %q, error %q", calls, result, message)
			}
			if !tc.wantWait && (calls != 2 || result != "ok") {
				t.Fatalf("want the retry to skip the backoff and serve, got %d attempt(s), result %q, error %q", calls, result, message)
			}
			found := false
			for _, entry := range ctx.GetRoutingEngineLogs() {
				if entry.Engine == schemas.RoutingEngineCore && strings.Contains(entry.Message, tc.wantNote) {
					found = true
				}
			}
			if !found {
				t.Fatalf("the trail does not record %q: %v", tc.wantNote, ctx.GetRoutingEngineLogs())
			}
		})
	}
}

// TestKeyPoolFilterSuppressingEveryKeyIs503 pins the answer when the key pool filter admits none of
// a provider's live keys: 503 no_eligible_keys, which says the keys are held back for now, rather
// than a credentials error that would blame keys nobody refused. The upstream is never called.
func TestKeyPoolFilterSuppressingEveryKeyIs503(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, upstream.URL)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "key-a", Name: "Key A", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "key-b", Name: "Key B", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
	})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
		KeyPoolFilter: func(_ *schemas.BifrostContext, _ schemas.ModelProvider, _ string, _ []schemas.Key) ([]schemas.Key, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(client.Shutdown)

	_, bifrostErr := client.ChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	})
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 503 || bifrostErr.Type == nil || *bifrostErr.Type != "no_eligible_keys" {
		t.Fatalf("want 503 no_eligible_keys, got %+v", bifrostErr)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the upstream was called %d time(s) with every key held back", n)
	}
}

// cancelledError is the context.Canceled a provider call returns when the caller leaves mid-request:
// not built by Bifrost, no status, but typed request_cancelled like Bifrost's own 499.
func cancelledError() *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error:          &schemas.ErrorField{Type: Ptr(schemas.RequestCancelled), Message: "request cancelled", Error: context.Canceled},
	}
}

// expiredContextError is the 504 newBifrostCtxDoneError builds for a request whose deadline passed.
func expiredContextError(t *testing.T) *schemas.BifrostError {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(-time.Second))
	<-ctx.Done()
	return newBifrostCtxDoneError(ctx, "during the request")
}

// requireEndedAttempt fails the test unless the one record on the trail carries class, status and
// failReason ("" wants none).
func requireEndedAttempt(t *testing.T, ctx *schemas.BifrostContext, class schemas.FailureClass, status int, failReason string) {
	t.Helper()
	got := attemptTrail(t, ctx, 1)[0]
	if got.FailureClass != class || got.StatusCode == nil || *got.StatusCode != status {
		t.Fatalf("attempt class=%q status=%v, want %q %d (%+v)", got.FailureClass, got.StatusCode, class, status, got)
	}
	switch {
	case failReason == "" && got.FailReason != nil:
		t.Fatalf("attempt fail_reason=%q, want none", *got.FailReason)
	case failReason != "" && (got.FailReason == nil || *got.FailReason != failReason):
		t.Fatalf("attempt fail_reason=%v, want %q", got.FailReason, failReason)
	}
}

// An attempt Bifrost ended is labelled on the trail though the retry loop stops on it before it
// classifies: a timeout (the provider's or the request's deadline) is timeout 504 with
// request_timed_out, and a cancellation, Bifrost's 499 or a provider call's context.Canceled, is
// cancelled 499 with no fail_reason, since a fail_reason says the key failed.
func TestExecuteRequestWithRetries_StampsTheAttemptBifrostEnded(t *testing.T) {
	cases := []struct {
		name       string
		err        func(t *testing.T) *schemas.BifrostError
		class      schemas.FailureClass
		status     int
		failReason string
	}{
		{name: "provider timeout", err: func(*testing.T) *schemas.BifrostError {
			return providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, context.DeadlineExceeded)
		}, class: schemas.FailureClassTimeout, status: 504, failReason: schemas.RequestTimedOut},
		{name: "request deadline", err: expiredContextError, class: schemas.FailureClassTimeout, status: 504, failReason: schemas.RequestTimedOut},
		{name: "caller cancel", err: func(*testing.T) *schemas.BifrostError {
			return createBifrostError("request cancelled by caller", Ptr(499), Ptr(schemas.RequestCancelled), true)
		}, class: schemas.FailureClassCancelled, status: 499},
		{name: "context.Canceled from the provider call", err: func(*testing.T) *schemas.BifrostError { return cancelledError() }, class: schemas.FailureClassCancelled, status: 499},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := rotationTestContext()
			calls := 0
			handler := func(schemas.Key) (string, *schemas.BifrostError) {
				calls++
				return "", tc.err(t)
			}
			_, err := executeRequestWithRetries(ctx, createTestConfig(2, time.Millisecond, time.Millisecond), handler,
				poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
				schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
			if err == nil {
				t.Fatal("the attempt Bifrost ended was served")
			}
			if calls != 1 {
				t.Fatalf("the attempt was tried %d times, want once: Bifrost's endings are not retried", calls)
			}
			requireEndedAttempt(t, ctx, tc.class, tc.status, tc.failReason)
		})
	}
}

// A cancellation during the backoff after a refusal ends the request before another attempt is
// dispatched: the refused attempt keeps its own class and reason, and no record is added for the
// attempt that never ran.
func TestExecuteRequestWithRetries_CancelDuringBackoffKeepsTheRefusal(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	handler := func(schemas.Key) (string, *schemas.BifrostError) {
		// The caller leaves while the retry waits out its backoff.
		cancel()
		return "", providerError(429, "requests", "rate_limit_exceeded", "Rate limit reached for requests")
	}
	_, err := executeRequestWithRetries(ctx, createTestConfig(2, time.Second, time.Second), handler,
		poolKeyProvider([]schemas.Key{rotationKeyA, rotationKeyB}),
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))
	if err == nil || err.Error == nil || err.Error.Type == nil || *err.Error.Type != schemas.RequestCancelled {
		t.Fatalf("want the request to end as cancelled, got %+v", err)
	}
	got := attemptTrail(t, ctx, 1)[0]
	if got.FailureClass != schemas.FailureClassRateLimit || got.FailReason == nil || *got.FailReason != "rate_limit_error" {
		t.Fatalf("the refused attempt should keep rate_limit / rate_limit_error, got %+v", got)
	}
}

// A TTFT miss keeps the ttft_timeout settleAttemptAbort wrote and gains the timeout class and its
// status. A record already classified is left alone, and an error Bifrost did not end is not stamped.
func TestStampEndedAttempt(t *testing.T) {
	trailWith := func(record schemas.KeyAttemptRecord) *schemas.BifrostContext {
		ctx := rotationTestContext()
		ctx.SetValue(schemas.BifrostContextKeyAttemptTrail, []schemas.KeyAttemptRecord{record})
		return ctx
	}
	t.Run("a TTFT miss keeps its reason", func(t *testing.T) {
		ctx := trailWith(schemas.KeyAttemptRecord{KeyID: rotationKeyA.ID, FailReason: Ptr(schemas.FirstTokenTimeoutErrorCode)})
		stampEndedAttempt(ctx, providerUtils.NewFirstTokenTimeoutError(time.Second))
		requireEndedAttempt(t, ctx, schemas.FailureClassTimeout, 504, schemas.FirstTokenTimeoutErrorCode)
	})
	t.Run("a classified record is left alone", func(t *testing.T) {
		ctx := trailWith(schemas.KeyAttemptRecord{KeyID: rotationKeyA.ID, FailureClass: schemas.FailureClassRateLimit, StatusCode: Ptr(429), FailReason: Ptr("rate_limit_error")})
		stampEndedAttempt(ctx, cancelledError())
		requireEndedAttempt(t, ctx, schemas.FailureClassRateLimit, 429, "rate_limit_error")
	})
	t.Run("another Bifrost error is not stamped", func(t *testing.T) {
		ctx := trailWith(schemas.KeyAttemptRecord{KeyID: rotationKeyA.ID})
		stampEndedAttempt(ctx, createBifrostError("tracer not found in context", nil, nil, true))
		if got := attemptTrail(t, ctx, 1)[0]; got.FailureClass != "" || got.StatusCode != nil || got.FailReason != nil {
			t.Fatalf("an internal error was stamped: %+v", got)
		}
	})
}

// endedAttemptRecorder is an LLM plugin that keeps the last trail record each error post-hook sees,
// which is what logging and telemetry read about the attempt.
type endedAttemptRecorder struct {
	mu      sync.Mutex
	records []schemas.KeyAttemptRecord
}

// GetName names the recorder plugin.
func (r *endedAttemptRecorder) GetName() string { return "ended-attempt-recorder" }

// Cleanup has nothing to release.
func (r *endedAttemptRecorder) Cleanup() error { return nil }

// PreRequestHook leaves the request alone.
func (r *endedAttemptRecorder) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook leaves the request alone.
func (r *endedAttemptRecorder) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook keeps the trail's last record when the hook carries an error.
func (r *endedAttemptRecorder) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if trail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord); bifrostErr != nil && len(trail) > 0 {
		r.mu.Lock()
		r.records = append(r.records, trail[len(trail)-1])
		r.mu.Unlock()
	}
	return resp, bifrostErr, nil
}

// errorHookRecords returns the records kept so far.
func (r *endedAttemptRecorder) errorHookRecords() []schemas.KeyAttemptRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]schemas.KeyAttemptRecord(nil), r.records...)
}

// stallingStreamUpstream is an OpenAI-compatible upstream that streams one content chunk of a chat
// completion and then holds the connection until the client leaves. The hold is bounded, so the
// server can close if the client never leaves.
func stallingStreamUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"o"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A stream Bifrost ends after it has produced output is labelled by the time its post-hooks see
// the error, which is when logging and telemetry read the trail: a caller who leaves is cancelled
// with 499 and no fail_reason, and a request deadline is a timeout with 504 and request_timed_out.
// The retry loop has returned by then, so only the stream's post-hook runner can label it. The
// caller leaves once the first chunk has reached it, and the deadline leaves two seconds for that
// chunk, so a slow start cannot end the request before it has produced output.
func TestStreamEndedAfterFirstOutputIsLabelled(t *testing.T) {
	const provider schemas.ModelProvider = "ended-stream"
	cases := []struct {
		name       string
		cancel     bool
		class      schemas.FailureClass
		status     int
		failReason string
	}{
		{name: "the caller leaves after the first chunk", cancel: true, class: schemas.FailureClassCancelled, status: 499},
		{name: "the request deadline passes after the first chunk", class: schemas.FailureClassTimeout, status: 504, failReason: schemas.RequestTimedOut},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := NewMockAccount()
			account.AddProviderWithBaseURL(provider, 1, 16, stallingStreamUpstream(t).URL)
			account.configs[provider].NetworkConfig.MaxRetries = 0
			account.SetCustomProviderConfig(provider, &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI})
			account.SetKeysForProvider(provider, []schemas.Key{{ID: "k1", Value: *schemas.NewSecretVar("sk-k1"), Models: schemas.WhiteList{"*"}, Weight: 100}})
			recorder := &endedAttemptRecorder{}
			client, err := Init(context.Background(), schemas.BifrostConfig{Account: account, Logger: NewDefaultLogger(schemas.LogLevelError), LLMPlugins: []schemas.LLMPlugin{recorder}})
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Cleanup(client.Shutdown)

			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			deadline := time.Now().Add(2 * time.Second)
			if tc.cancel {
				deadline = schemas.NoDeadline
			}
			ctx := schemas.NewBifrostContext(parent, deadline)
			stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
				Provider: provider,
				Model:    "m",
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: Ptr("hi")}}},
			})
			if bifrostErr != nil {
				t.Fatalf("the stream should start, got %s", bifrostErr.GetErrorString())
			}
			chunks := 0
			for chunk := range stream {
				if chunk.BifrostError == nil {
					chunks++
					if tc.cancel && chunks == 1 {
						cancel()
					}
				}
			}
			if chunks == 0 {
				t.Fatal("the stream ended before its first chunk reached the caller")
			}

			var records []schemas.KeyAttemptRecord
			for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
				if records = recorder.errorHookRecords(); len(records) > 0 {
					break
				}
			}
			if len(records) == 0 {
				t.Fatal("no post-hook saw the stream's error")
			}
			record := records[len(records)-1]
			failReason, status := "", "nil"
			if record.FailReason != nil {
				failReason = *record.FailReason
			}
			if record.StatusCode != nil {
				status = fmt.Sprint(*record.StatusCode)
			}
			if record.FailureClass != tc.class || status != fmt.Sprint(tc.status) || failReason != tc.failReason {
				t.Fatalf("the error post-hook saw class %q, status %s, fail_reason %q; want %q, %d, %q", record.FailureClass, status, failReason, tc.class, tc.status, tc.failReason)
			}
		})
	}
}

// A stream's post-hook labels an attempt that ended after its first output, and leaves alone one
// whose TTFT abort is still armed: until the first-chunk check settles, the retry loop labels the
// attempt, and a TTFT cut that surfaces in a post-hook as a closed socket's cancellation would
// otherwise be frozen as cancelled before the loop could call it the ttft_timeout it is.
func TestStampEndedStreamAttempt(t *testing.T) {
	trailWith := func(record schemas.KeyAttemptRecord) *schemas.BifrostContext {
		ctx := rotationTestContext()
		ctx.SetValue(schemas.BifrostContextKeyAttemptTrail, []schemas.KeyAttemptRecord{record})
		return ctx
	}
	t.Run("an attempt that ended after its first output is labelled", func(t *testing.T) {
		ctx := trailWith(schemas.KeyAttemptRecord{KeyID: rotationKeyA.ID})
		stampEndedStreamAttempt(ctx, cancelledError())
		requireEndedAttempt(t, ctx, schemas.FailureClassCancelled, 499, "")
	})
	t.Run("an attempt whose TTFT abort is still armed is left to the retry loop", func(t *testing.T) {
		ctx := trailWith(schemas.KeyAttemptRecord{KeyID: rotationKeyA.ID})
		ctx.SetValue(schemas.BifrostContextKeyStreamAttemptAbort, providerUtils.NewAttemptAbort(time.Second))
		stampEndedStreamAttempt(ctx, cancelledError())
		if record := attemptTrail(t, ctx, 1)[0]; record.FailureClass != "" || record.StatusCode != nil || record.FailReason != nil {
			t.Fatalf("the post-hook labelled an attempt the retry loop still owns: %+v", record)
		}
	})
}
