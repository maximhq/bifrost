package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestValidateDeepSeekV4ResponseMetadata covers valid, missing, malformed,
// inconsistent, and wrong-model unary metadata.
func TestValidateDeepSeekV4ResponseMetadata(t *testing.T) {
	pro := `{"model":"deepseek-v4-pro","content":[{"type":"text","text":"must not be retained"}],"usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":23,"prompt_tokens":121}}`
	if err := validateDeepSeekV4ResponseMetadata([]byte(pro), deepSeekV4ProModel); err != nil {
		t.Fatalf("valid Pro metadata rejected: %v", err)
	}
	flash := `{"model":"deepseek-v4-flash","usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":23,"prompt_tokens":121}}`
	if err := validateDeepSeekV4ResponseMetadata([]byte(flash), deepSeekV4FlashModel); err != nil {
		t.Fatalf("valid Flash metadata regressed: %v", err)
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
		{name: "wrong exact model", body: `{"model":"deepseek-v4-pro","usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":1}}`, want: "does not match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDeepSeekV4ResponseMetadata([]byte(tc.body), deepSeekV4FlashModel)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}

	t.Run("decode error is sanitized", func(t *testing.T) {
		err := validateDeepSeekV4ResponseMetadata([]byte(`{"model":"deepseek-v4-flash","usage":{"input_tokens":"sensitive-upstream-fragment"}}`), deepSeekV4FlashModel)
		if err == nil || err.Error() != "usage metadata decode failed" {
			t.Fatalf("decode error = %v, want fixed sanitized message", err)
		}
	})
}

// TestValidateDeepSeekV4StreamLifecycle verifies a complete ordered stream
// satisfies the usage-fidelity state machine.
func TestValidateDeepSeekV4StreamLifecycle(t *testing.T) {
	state := &deepSeekStreamUsageState{}
	for _, event := range []struct {
		typeName string
		body     string
	}{
		{typeName: "message_start", body: `{"type":"message_start","message":{"model":"deepseek-v4-pro","content":[{"text":"ignored"}],"usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":0,"prompt_tokens":121}}}`},
		{typeName: "content_block_delta", body: `{"type":"content_block_delta","delta":{"text":"ignored"}}`},
		{typeName: "message_delta", body: `{"type":"message_delta","usage":{"output_tokens":23}}`},
		{typeName: "message_stop", body: `{"type":"message_stop"}`},
	} {
		if err := validateDeepSeekV4StreamMetadata(event.typeName, []byte(event.body), deepSeekV4ProModel, state); err != nil {
			t.Fatalf("%s rejected: %v", event.typeName, err)
		}
	}
	if err := validateDeepSeekV4StreamComplete(state); err != nil {
		t.Fatalf("complete stream rejected: %v", err)
	}
}

// TestValidateDeepSeekV4StreamRejectsMalformedUsage covers collapsed,
// inconsistent, absent, corrupt, and negative stream usage metadata.
func TestValidateDeepSeekV4StreamRejectsMalformedUsage(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "message absent", body: `{"type":"message_start"}`, want: "missing message"},
		{name: "usage absent", body: `{"type":"message_start","message":{"model":"deepseek-v4-flash"}}`, want: "absent"},
		{name: "usage corrupt", body: `{"type":"message_start","message":{"model":"deepseek-v4-flash","usage":{"input_tokens":"sensitive-upstream-fragment"}}}`, want: "stream usage metadata decode failed"},
		{name: "usage negative", body: `{"type":"message_start","message":{"model":"deepseek-v4-flash","usage":{"input_tokens":-1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`, want: "negative"},
		{name: "collapsed start", body: `{"type":"message_start","message":{"model":"deepseek-v4-flash","usage":{"input_tokens":121,"output_tokens":0}}}`, want: "collapsed"},
		{name: "nonconserving start", body: `{"type":"message_start","message":{"model":"deepseek-v4-flash","usage":{"input_tokens":100,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":0,"prompt_tokens":121}}}`, want: "non-conserving"},
		{name: "wrong exact model", body: `{"type":"message_start","message":{"model":"deepseek-v4-pro","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`, want: "does not match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDeepSeekV4StreamMetadata("message_start", []byte(tc.body), deepSeekV4FlashModel, &deepSeekStreamUsageState{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}

	t.Run("stream decode error is sanitized", func(t *testing.T) {
		err := validateDeepSeekV4StreamMetadata(
			"message_start",
			[]byte(`{"type":"message_start","message":{"model":"deepseek-v4-flash","usage":{"input_tokens":"sensitive-upstream-fragment"}}}`),
			deepSeekV4FlashModel,
			&deepSeekStreamUsageState{},
		)
		if err == nil || err.Error() != "stream usage metadata decode failed" {
			t.Fatalf("decode error = %v, want fixed sanitized message", err)
		}
	})

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
			err := validateDeepSeekV4StreamMetadata("message_delta", []byte(tc.body), deepSeekV4FlashModel, &deepSeekStreamUsageState{sawMessageStart: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// TestValidateDeepSeekV4StreamRejectsOutOfOrderEvents verifies duplicate
// starts and events after the terminal stop are rejected.
func TestValidateDeepSeekV4StreamRejectsOutOfOrderEvents(t *testing.T) {
	t.Run("duplicate message_start", func(t *testing.T) {
		state := &deepSeekStreamUsageState{sawMessageStart: true}
		body := `{"type":"message_start","message":{"model":"deepseek-v4-flash","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`
		err := validateDeepSeekV4StreamMetadata("message_start", []byte(body), deepSeekV4FlashModel, state)
		if err == nil || !strings.Contains(err.Error(), "duplicate message_start") {
			t.Fatalf("error = %v, want duplicate message_start", err)
		}
	})

	t.Run("event after message_stop", func(t *testing.T) {
		state := &deepSeekStreamUsageState{sawMessageStart: true, sawMessageDelta: true, sawMessageStop: true}
		err := validateDeepSeekV4StreamMetadata("message_delta", []byte(`{"type":"message_delta","usage":{"output_tokens":1}}`), deepSeekV4FlashModel, state)
		if err == nil || !strings.Contains(err.Error(), "after message_stop") {
			t.Fatalf("error = %v, want after message_stop", err)
		}
	})
}

// TestValidateDeepSeekV4StreamComplete covers every incomplete terminal
// state recognized by the stream validator.
func TestValidateDeepSeekV4StreamComplete(t *testing.T) {
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
			err := validateDeepSeekV4StreamComplete(tc.state)
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
		Key:    "frontier",
		Config: &schemas.AliasConfig{ModelID: "deepseek-v4-pro"},
	})
	if got, ok := expectedDeepSeekV4UsageModel(ctx, schemas.DeepSeek, "frontier"); !ok || got != deepSeekV4ProModel {
		t.Fatalf("canonical exact alias resolved to %q, enabled=%t", got, ok)
	}
	if got, ok := expectedDeepSeekV4UsageModelFromBody(ctx, schemas.DeepSeek, []byte(`{"model":"frontier"}`)); !ok || got != deepSeekV4ProModel {
		t.Fatalf("canonical exact stream alias resolved to %q, enabled=%t", got, ok)
	}
	if got, ok := expectedDeepSeekV4UsageModel(nil, schemas.DeepSeek, deepSeekV4FlashModel); !ok || got != deepSeekV4FlashModel {
		t.Fatalf("exact Flash request resolved to %q, enabled=%t", got, ok)
	}
	for _, tc := range []struct {
		provider schemas.ModelProvider
		model    string
	}{
		{provider: schemas.DeepSeek, model: "deepseek-v4-flash-0731"},
		{provider: schemas.DeepSeek, model: "deepseek-v4-pro-0813"},
		{provider: schemas.DeepSeek, model: "deepseek-v4"},
		{provider: schemas.Anthropic, model: "deepseek-v4-pro"},
		{provider: schemas.DeepSeek, model: "DeepSeek-V4-Pro"},
	} {
		if _, ok := expectedDeepSeekV4UsageModel(nil, tc.provider, tc.model); ok {
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
