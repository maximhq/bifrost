package schemas

import (
	"context"
	"testing"
)

// contentLoggingTestTracer records every attribute written to the root span, so a test can tell an
// attribute that was never written apart from one written as false.
type contentLoggingTestTracer struct {
	NoOpTracer
	attrs map[string]any
}

func (t *contentLoggingTestTracer) GetSpanHandleByID(traceID string, spanID *string) SpanHandle {
	return struct{}{}
}

func (t *contentLoggingTestTracer) SetAttribute(handle SpanHandle, key string, value any) {
	t.attrs[key] = value
}

func newContentLoggingTestContext() (*BifrostContext, *contentLoggingTestTracer) {
	tracer := &contentLoggingTestTracer{attrs: map[string]any{}}
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.SetValue(BifrostContextKeyTracer, tracer)
	ctx.SetValue(BifrostContextKeyTraceID, "trace-content-logging")
	return ctx, tracer
}

// The tiers decide top down: the first tier holding any decision wins, and a tier below it is never
// consulted. Inside a tier an "off" from any layer wins over an "on" from another.
func TestResolveAdminContentLogging(t *testing.T) {
	type stamp struct {
		layer    BifrostContextKey
		disabled bool
	}
	for _, tc := range []struct {
		name         string
		stamps       []stamp
		wantDisabled bool
		wantDecided  bool
	}{
		{name: "nothing stamped is undecided"},
		{
			name:         "a lone credential decides",
			stamps:       []stamp{{BifrostContextKeyGovernanceDisableContentLogging, true}},
			wantDisabled: true, wantDecided: true,
		},
		{
			name: "business unit outranks team",
			stamps: []stamp{
				{BifrostContextKeyBusinessUnitDisableContentLogging, false},
				{BifrostContextKeyTeamDisableContentLogging, true},
			},
			wantDisabled: false, wantDecided: true,
		},
		{
			name: "team outranks user",
			stamps: []stamp{
				{BifrostContextKeyTeamDisableContentLogging, true},
				{BifrostContextKeyUserDisableContentLogging, false},
			},
			wantDisabled: true, wantDecided: true,
		},
		{
			name: "user outranks the credential tier",
			stamps: []stamp{
				{BifrostContextKeyUserDisableContentLogging, false},
				{BifrostContextKeyGovernanceDisableContentLogging, true},
				{BifrostContextKeyProviderKeyDisableContentLogging, true},
			},
			wantDisabled: false, wantDecided: true,
		},
		{
			name: "team off cannot be reopened by a virtual key",
			stamps: []stamp{
				{BifrostContextKeyTeamDisableContentLogging, true},
				{BifrostContextKeyGovernanceDisableContentLogging, false},
			},
			wantDisabled: true, wantDecided: true,
		},
		{
			name:         "an inheriting org tier falls through to the credential tier",
			stamps:       []stamp{{BifrostContextKeyProviderKeyDisableContentLogging, false}},
			wantDisabled: false, wantDecided: true,
		},
		{
			name: "any off wins inside the credential tier",
			stamps: []stamp{
				{BifrostContextKeyGovernanceDisableContentLogging, false},
				{BifrostContextKeyProviderKeyDisableContentLogging, true},
				{BifrostContextKeyAccessProfileDisableContentLogging, false},
			},
			wantDisabled: true, wantDecided: true,
		},
		{
			name: "all on inside the credential tier is on",
			stamps: []stamp{
				{BifrostContextKeyGovernanceDisableContentLogging, false},
				{BifrostContextKeyAccessProfileDisableContentLogging, false},
			},
			wantDisabled: false, wantDecided: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := newContentLoggingTestContext()
			for _, s := range tc.stamps {
				StampContentLoggingDecision(ctx, s.layer, s.disabled)
			}
			disabled, decided := ResolveAdminContentLogging(ctx)
			if decided != tc.wantDecided || disabled != tc.wantDisabled {
				t.Fatalf("ResolveAdminContentLogging = (disabled %v, decided %v), want (%v, %v)", disabled, decided, tc.wantDisabled, tc.wantDecided)
			}
		})
	}
}

// A layer stamped more than once keeps "off" once it has it: a user in two business units, a user
// holding two access profiles, and a request retried onto a second provider key all merge this way.
func TestStampContentLoggingDecisionMergesAnyOff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []bool
		want   bool
	}{
		{name: "off then on stays off", values: []bool{true, false}, want: true},
		{name: "on then off becomes off", values: []bool{false, true}, want: true},
		{name: "on then on stays on", values: []bool{false, false}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := newContentLoggingTestContext()
			for _, v := range tc.values {
				StampContentLoggingDecision(ctx, BifrostContextKeyProviderKeyDisableContentLogging, v)
			}
			got, _ := ctx.Value(BifrostContextKeyProviderKeyDisableContentLogging).(bool)
			if got != tc.want {
				t.Fatalf("layer value = %v, want %v", got, tc.want)
			}
		})
	}
}

// A key that is not a content-logging layer is never written, so the helper cannot be used to set
// an arbitrary context value.
func TestStampContentLoggingDecisionIgnoresUnknownLayers(t *testing.T) {
	ctx, tracer := newContentLoggingTestContext()
	StampContentLoggingDecision(ctx, BifrostContextKeySelectedKeyID, true)
	if v := ctx.Value(BifrostContextKeySelectedKeyID); v != nil {
		t.Fatalf("unknown layer was written: %v", v)
	}
	if _, ok := tracer.attrs[AttrBifrostContentLoggingDisabled]; ok {
		t.Fatal("unknown layer marked the trace")
	}
}

// The root-span mark follows the resolved decision so connectors, which only see the finished trace,
// strip content exactly when the layers say off. It is written as false only to undo an earlier
// true; a request whose layers never resolve to off leaves the span without the attribute.
func TestStampContentLoggingDecisionMarksTrace(t *testing.T) {
	type stamp struct {
		layer    BifrostContextKey
		disabled bool
	}
	for _, tc := range []struct {
		name        string
		stamps      []stamp
		wantPresent bool
		wantValue   bool
	}{
		{
			name:        "off marks the span",
			stamps:      []stamp{{BifrostContextKeyGovernanceDisableContentLogging, true}},
			wantPresent: true, wantValue: true,
		},
		{
			name:   "on leaves the span alone",
			stamps: []stamp{{BifrostContextKeyGovernanceDisableContentLogging, false}},
		},
		{
			name: "a lower tier off under a higher tier on never marks",
			stamps: []stamp{
				{BifrostContextKeyTeamDisableContentLogging, false},
				{BifrostContextKeyProviderKeyDisableContentLogging, true},
			},
		},
		{
			name: "a higher tier on over an earlier off clears the mark",
			stamps: []stamp{
				{BifrostContextKeyProviderKeyDisableContentLogging, true},
				{BifrostContextKeyTeamDisableContentLogging, false},
			},
			wantPresent: true, wantValue: false,
		},
		{
			name: "a higher tier off over an earlier on marks",
			stamps: []stamp{
				{BifrostContextKeyGovernanceDisableContentLogging, false},
				{BifrostContextKeyBusinessUnitDisableContentLogging, true},
			},
			wantPresent: true, wantValue: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, tracer := newContentLoggingTestContext()
			for _, s := range tc.stamps {
				StampContentLoggingDecision(ctx, s.layer, s.disabled)
			}
			value, present := tracer.attrs[AttrBifrostContentLoggingDisabled]
			if present != tc.wantPresent {
				t.Fatalf("attribute present = %v (value %v), want %v", present, value, tc.wantPresent)
			}
			if present && value != tc.wantValue {
				t.Fatalf("attribute = %v, want %v", value, tc.wantValue)
			}
		})
	}
}

// A layer that has not been stamped yet can still change the decision only when no tier above it has
// decided and no peer in its own tier already says off (any off wins inside a tier).
func TestContentLoggingLayerCanChange(t *testing.T) {
	type stamp struct {
		layer    BifrostContextKey
		disabled bool
	}
	for _, tc := range []struct {
		name   string
		stamps []stamp
		layer  BifrostContextKey
		want   bool
	}{
		{name: "nothing stamped", layer: BifrostContextKeyProviderKeyDisableContentLogging, want: true},
		{
			name:   "a higher tier decided on",
			stamps: []stamp{{BifrostContextKeyTeamDisableContentLogging, false}},
			layer:  BifrostContextKeyProviderKeyDisableContentLogging,
			want:   false,
		},
		{
			name:   "a higher tier decided off",
			stamps: []stamp{{BifrostContextKeyUserDisableContentLogging, true}},
			layer:  BifrostContextKeyProviderKeyDisableContentLogging,
			want:   false,
		},
		{
			name:   "a peer already says off",
			stamps: []stamp{{BifrostContextKeyGovernanceDisableContentLogging, true}},
			layer:  BifrostContextKeyProviderKeyDisableContentLogging,
			want:   false,
		},
		{
			name:   "a peer says on, so an off can still win",
			stamps: []stamp{{BifrostContextKeyGovernanceDisableContentLogging, false}},
			layer:  BifrostContextKeyProviderKeyDisableContentLogging,
			want:   true,
		},
		{
			name:   "a lower tier decided, so a higher layer can still change it",
			stamps: []stamp{{BifrostContextKeyGovernanceDisableContentLogging, true}},
			layer:  BifrostContextKeyTeamDisableContentLogging,
			want:   true,
		},
		{name: "not a layer", layer: BifrostContextKeySelectedKeyID, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := newContentLoggingTestContext()
			for _, s := range tc.stamps {
				StampContentLoggingDecision(ctx, s.layer, s.disabled)
			}
			if got := ContentLoggingLayerCanChange(ctx, tc.layer); got != tc.want {
				t.Fatalf("ContentLoggingLayerCanChange = %v, want %v", got, tc.want)
			}
		})
	}
	if ContentLoggingLayerCanChange(nil, BifrostContextKeyTeamDisableContentLogging) {
		t.Fatal("nil context reports a layer that can change")
	}
}

// The resolved marker tells the logging plugin that governance has finished stamping the caller,
// including the case where every layer inherits and nothing else was written.
func TestCallerContentLoggingResolved(t *testing.T) {
	ctx, _ := newContentLoggingTestContext()
	if CallerContentLoggingResolved(ctx) {
		t.Fatal("a fresh context reports resolved")
	}
	MarkCallerContentLoggingResolved(ctx)
	if !CallerContentLoggingResolved(ctx) {
		t.Fatal("marked context does not report resolved")
	}
	if CallerContentLoggingResolved(nil) {
		t.Fatal("nil context reports resolved")
	}
	MarkCallerContentLoggingResolved(nil)
	StampContentLoggingDecision(nil, BifrostContextKeyTeamDisableContentLogging, true)
	if _, decided := ResolveAdminContentLogging(nil); decided {
		t.Fatal("nil context resolves to a decision")
	}
}
