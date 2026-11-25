package handlers

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// An async job answers 202 before the request pipeline has run, so the submit
// itself asks whether a credential was presented when the deployment requires one.
func TestAsyncJobAdmission(t *testing.T) {
	newCtx := func(t *testing.T) *schemas.BifrostContext {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		t.Cleanup(cancel)
		return ctx
	}
	withVirtualKey := func(t *testing.T) *schemas.BifrostContext {
		ctx := newCtx(t)
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
		lib.RecordCredential(ctx, grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-test"))
		return ctx
	}
	enforced := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: true}}
	open := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: false}}

	t.Run("no credential is refused when auth is enforced", func(t *testing.T) {
		require.Error(t, AsyncJobAdmission(enforced)(newCtx(t)))
	})
	t.Run("a virtual key is admitted when auth is enforced", func(t *testing.T) {
		require.NoError(t, AsyncJobAdmission(enforced)(withVirtualKey(t)))
	})
	t.Run("no credential is admitted when auth is not enforced", func(t *testing.T) {
		require.NoError(t, AsyncJobAdmission(open)(newCtx(t)))
	})
	t.Run("the setting is read per submit", func(t *testing.T) {
		config := &lib.Config{ClientConfig: &configstore.ClientConfig{}}
		admission := AsyncJobAdmission(config)
		require.NoError(t, admission(newCtx(t)))
		config.ClientConfig.EnforceAuthOnInference = true
		require.Error(t, admission(newCtx(t)))
	})
}

func TestSendSubmitJobError_NotAdmittedIsUnauthorized(t *testing.T) {
	var ctx fasthttp.RequestCtx
	sendSubmitJobError(&ctx, logstore.ErrAsyncJobNotAdmitted)
	assert.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
}

func TestGetResultTTLFromHeaderWithDefault_IsBounded(t *testing.T) {
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.Set(schemas.AsyncHeaderResultTTL, "3153600000")
	assert.Equal(t, logstore.MaxAsyncJobResultTTL, getResultTTLFromHeaderWithDefault(&ctx, 3600))
	ctx.Request.Header.Set(schemas.AsyncHeaderResultTTL, "600")
	assert.Equal(t, 600, getResultTTLFromHeaderWithDefault(&ctx, 3600))
}

// A config save replaces ClientConfig under config.Mu. The admission check reads the
// setting on every submit, so it takes the same lock. Run with -race.
func TestAsyncJobAdmission_ReadsTheSettingUnderTheConfigLock(t *testing.T) {
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: true}}
	admission := AsyncJobAdmission(config)
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 2000 {
			config.Mu.Lock()
			config.ClientConfig = &configstore.ClientConfig{EnforceAuthOnInference: i%2 == 0}
			config.Mu.Unlock()
		}
	}()
	for range 2000 {
		_ = admission(ctx)
	}
	<-done
}

func BenchmarkAsyncJobAdmission(b *testing.B) {
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: true}}
	admission := AsyncJobAdmission(config)
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	lib.RecordCredential(ctx, grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-test"))
	b.ReportAllocs()
	for b.Loop() {
		if err := admission(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
