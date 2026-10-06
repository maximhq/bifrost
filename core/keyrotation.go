package bifrost

import (
	"time"

	"github.com/maximhq/bifrost/core/keyselectors"
	"github.com/maximhq/bifrost/core/schemas"
)

// keyAttemptObserverContextKey carries the *keyAttemptObserver of the request's current
// provider attempt loop. It is unexported: only requestWorker sets it, only
// executeRequestWithRetries reads it.
type keyAttemptObserverContextKey struct{}

// keyAttemptObserver feeds attempt outcomes back into the provider's key rotation state so
// strategies see in-flight counts and later requests skip keys that are cooling down.
type keyAttemptObserver struct {
	rotator  *keyselectors.Rotator
	provider schemas.ModelProvider
	model    string
	cfg      *schemas.KeySelectionConfig
}

// newKeyAttemptObserver returns an observer when the provider has a key_selection config,
// nil otherwise (the historical stateless weighted-random path records nothing).
func (bifrost *Bifrost) newKeyAttemptObserver(config *schemas.ProviderConfig, provider schemas.ModelProvider, model string) *keyAttemptObserver {
	if config == nil || config.KeySelection == nil {
		return nil
	}
	return &keyAttemptObserver{rotator: bifrost.keyRotator, provider: provider, model: model, cfg: config.KeySelection}
}

// selectKeyWithStrategy picks one key from a pool of two or more eligible keys, applying the
// provider's key_selection strategy when configured and the configured KeySelector otherwise.
func (bifrost *Bifrost) selectKeyWithStrategy(ctx *schemas.BifrostContext, config *schemas.ProviderConfig, keys []schemas.Key, provider schemas.ModelProvider, model string) (schemas.Key, error) {
	if config == nil || config.KeySelection == nil {
		return bifrost.keySelector(ctx, keys, provider, model)
	}
	return bifrost.keyRotator.Select(ctx, provider, model, config.KeySelection, keys, bifrost.keySelector)
}

func keyAttemptObserverFromContext(ctx *schemas.BifrostContext) *keyAttemptObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(keyAttemptObserverContextKey{}).(*keyAttemptObserver)
	return observer
}

// begin marks an attempt in flight on key and returns the function that ends it.
func (o *keyAttemptObserver) begin(key schemas.Key) func() {
	if o == nil || key.ID == "" {
		return func() {}
	}
	return o.rotator.Begin(o.provider, key.ID)
}

func (o *keyAttemptObserver) succeeded(key schemas.Key) {
	if o == nil || key.ID == "" {
		return
	}
	o.rotator.ReportSuccess(o.provider, key.ID, o.model)
}

func (o *keyAttemptObserver) failed(key schemas.Key, class schemas.FailureClass, retryAfterMs int64) {
	if o == nil || key.ID == "" {
		return
	}
	o.rotator.ReportFailure(o.provider, key.ID, o.model, class, time.Duration(retryAfterMs)*time.Millisecond, o.cfg)
}
