package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestValidateDeepSeekV4FlashResponseMetadata covers valid, missing, malformed,
// inconsistent, and wrong-model unary metadata.
func TestValidateDeepSeekV4FlashResponseMetadata(t *testing.T) {
	valid := `{"model":"deepseek-v4-flash","content":[{"type":"text","text":"must not be retained"}],"usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":23,"prompt_tokens":121}}`
	if err := validateDeepSeekV4FlashResponseMetadata([]byte(valid)); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "usage absent", body: `{"model":"deepseek-v4-flash"}`, want: "absent"},
		{name: "usage corrupt", body: `{"model":"deepseek-v4-flash","usage":{"input_tokens":"bad"}}`, want: "decode failed"},
		{name: "negative", body: `{"model":"deepseek-v4-flash","usage":{"input_tokens":-1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}`, want: "negative"},
		{name: "collapsed", body: `{"model":"deepseek-v4-flash","usage":{"input_tokens":121,"output_tokens":1}}`, want: "collapsed"},
		{name: "nonconserving", body: `{"model":"deepseek-v4-flash","usage":{"input_tokens":100,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":1,"prompt_tokens":121}}`, want: "non-conserving"},
		{name: "wrong model", body: `{"model":"deepseek-v4-flash-0731","usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":1}}`, want: "does not match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDeepSeekV4FlashResponseMetadata([]byte(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// TestValidateDeepSeekV4FlashStreamLifecycle verifies a complete ordered stream
// satisfies the usage-fidelity state machine.
func TestValidateDeepSeekV4FlashStreamLifecycle(t *testing.T) {
	state := &deepSeekStreamUsageState{}
	for _, event := range []struct {
		typeName string
		body     string
	}{
		{typeName: "message_start", body: `{"type":"message_start","message":{"model":"deepseek-v4-flash","content":[{"text":"ignored"}],"usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":0,"prompt_tokens":121}}}`},
		{typeName: "content_block_delta", body: `{"type":"content_block_delta","delta":{"text":"ignored"}}`},
		{typeName: "message_delta", body: `{"type":"message_delta","usage":{"output_tokens":23}}`},
		{typeName: "message_stop", body: `{"type":"message_stop"}`},
	} {
		if err := validateDeepSeekV4FlashStreamMetadata(event.typeName, []byte(event.body), state); err != nil {
			t.Fatalf("%s rejected: %v", event.typeName, err)
		}
	}
	if err := validateDeepSeekV4FlashStreamComplete(state); err != nil {
		t.Fatalf("complete stream rejected: %v", err)
	}
}

// TestValidateDeepSeekV4FlashStreamRejectsMalformedUsage covers collapsed,
// inconsistent, absent, corrupt, and negative stream usage metadata.
func TestValidateDeepSeekV4FlashStreamRejectsMalformedUsage(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "collapsed start", body: `{"type":"message_start","message":{"model":"deepseek-v4-flash","usage":{"input_tokens":121,"output_tokens":0}}}`, want: "collapsed"},
		{name: "nonconserving start", body: `{"type":"message_start","message":{"model":"deepseek-v4-flash","usage":{"input_tokens":100,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":0,"prompt_tokens":121}}}`, want: "non-conserving"},
		{name: "wrong model", body: `{"type":"message_start","message":{"model":"deepseek-v4-flash-0731","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`, want: "does not match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDeepSeekV4FlashStreamMetadata("message_start", []byte(tc.body), &deepSeekStreamUsageState{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "delta absent", body: `{"type":"message_delta"}`, want: "absent"},
		{name: "delta corrupt", body: `{"type":"message_delta","usage":{"output_tokens":"bad"}}`, want: "decode failed"},
		{name: "delta negative", body: `{"type":"message_delta","usage":{"output_tokens":-1}}`, want: "negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDeepSeekV4FlashStreamMetadata("message_delta", []byte(tc.body), &deepSeekStreamUsageState{sawMessageStart: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// TestValidateDeepSeekV4FlashStreamRejectsOutOfOrderEvents verifies duplicate
// starts and events after the terminal stop are rejected.
func TestValidateDeepSeekV4FlashStreamRejectsOutOfOrderEvents(t *testing.T) {
	t.Run("duplicate message_start", func(t *testing.T) {
		state := &deepSeekStreamUsageState{sawMessageStart: true}
		body := `{"type":"message_start","message":{"model":"deepseek-v4-flash","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`
		err := validateDeepSeekV4FlashStreamMetadata("message_start", []byte(body), state)
		if err == nil || !strings.Contains(err.Error(), "duplicate message_start") {
			t.Fatalf("error = %v, want duplicate message_start", err)
		}
	})

	t.Run("event after message_stop", func(t *testing.T) {
		state := &deepSeekStreamUsageState{sawMessageStart: true, sawMessageDelta: true, sawMessageStop: true}
		err := validateDeepSeekV4FlashStreamMetadata("message_delta", []byte(`{"type":"message_delta","usage":{"output_tokens":1}}`), state)
		if err == nil || !strings.Contains(err.Error(), "after message_stop") {
			t.Fatalf("error = %v, want after message_stop", err)
		}
	})
}

// TestValidateDeepSeekV4FlashStreamComplete covers every incomplete terminal
// state recognized by the stream validator.
func TestValidateDeepSeekV4FlashStreamComplete(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state *deepSeekStreamUsageState
		want  string
	}{
		{name: "empty", state: &deepSeekStreamUsageState{}, want: "message_start"},
		{name: "no delta", state: &deepSeekStreamUsageState{sawMessageStart: true}, want: "message_delta"},
		{name: "no stop", state: &deepSeekStreamUsageState{sawMessageStart: true, sawMessageDelta: true}, want: "message_stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDeepSeekV4FlashStreamComplete(tc.state)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// TestDeepSeekUsageValidationGateIsExact verifies alias resolution enables the
// exact route while provider and model near misses remain untouched.
func TestDeepSeekUsageValidationGateIsExact(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key:    "workhorse",
		Config: &schemas.AliasConfig{ModelID: "deepseek-v4-flash"},
	})
	if !shouldValidateDeepSeekV4FlashUsage(ctx, schemas.DeepSeek, "workhorse") {
		t.Fatal("canonical exact alias did not enable validation")
	}
	if !shouldValidateDeepSeekV4FlashUsageFromBody(ctx, schemas.DeepSeek, []byte(`{"model":"workhorse"}`)) {
		t.Fatal("canonical exact stream alias did not enable validation")
	}
	for _, tc := range []struct {
		provider schemas.ModelProvider
		model    string
	}{
		{provider: schemas.DeepSeek, model: "deepseek-v4-flash-0731"},
		{provider: schemas.Anthropic, model: "deepseek-v4-flash"},
		{provider: schemas.DeepSeek, model: "DeepSeek-V4-Flash"},
	} {
		if shouldValidateDeepSeekV4FlashUsage(nil, tc.provider, tc.model) {
			t.Fatalf("near miss %s/%s enabled validation", tc.provider, tc.model)
		}
	}
}

// TestResetAnthropicStreamAttemptState verifies connection state cannot leak
// from one fallback attempt into the next.
func TestResetAnthropicStreamAttemptState(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyConnectionClosed, true)
	resetAnthropicStreamAttemptState(ctx)
	closed, _ := ctx.Value(schemas.BifrostContextKeyConnectionClosed).(bool)
	if closed {
		t.Fatal("connection-closed state leaked into the next streaming attempt")
	}
}

// TestDeepSeekUsageFidelityErrorIsTypedFallbackEligible verifies the failure is
// a typed 502 that permits fallback dispatch.
func TestDeepSeekUsageFidelityErrorIsTypedFallbackEligible(t *testing.T) {
	err := newDeepSeekUsageFidelityError(context.Canceled)
	if err.StatusCode == nil || *err.StatusCode != 502 || err.Error == nil || err.Error.Code == nil || *err.Error.Code != "deepseek_usage_fidelity" {
		t.Fatalf("unexpected typed error: %#v", err)
	}
	if err.AllowFallbacks == nil || !*err.AllowFallbacks {
		t.Fatalf("fidelity error must allow fallback: %#v", err.AllowFallbacks)
	}
}
