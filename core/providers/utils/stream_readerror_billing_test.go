package utils

import (
	"context"
	"errors"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// classifiedReadError is a stream-reader error that already carries its
// classified BifrostError (BifrostErrorCarrier).
type classifiedReadError struct{ typed *schemas.BifrostError }

func (e classifiedReadError) Error() string                       { return "classified read error" }
func (e classifiedReadError) BifrostError() *schemas.BifrostError { return e.typed }

func passthroughPostHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return resp, err
}

// A read error on a stream that already consumed tokens bills them when the
// loop asks for it (ProcessAndSendErrorWithBilledUsage), on both the plain and
// the classified-carrier arm, and only then: ProcessAndSendError stays as it was.
func TestProcessAndSendErrorWithBilledUsage(t *testing.T) {
	tier := schemas.BifrostServiceTierPriority
	for _, tc := range []struct {
		name     string
		err      error
		billed   bool
		send     func(*schemas.BifrostContext, error, chan *schemas.BifrostStreamChunk)
		isBifErr bool
	}{
		{
			name: "plain/billed", err: errors.New("connection reset"), billed: true, isBifErr: true,
			send: func(ctx *schemas.BifrostContext, err error, ch chan *schemas.BifrostStreamChunk) {
				ProcessAndSendErrorWithBilledUsage(ctx, passthroughPostHook, err, ch, nil, nil)
			},
		},
		{
			name: "carrier/billed", err: classifiedReadError{typed: &schemas.BifrostError{StatusCode: schemas.Ptr(503)}}, billed: true,
			send: func(ctx *schemas.BifrostContext, err error, ch chan *schemas.BifrostStreamChunk) {
				ProcessAndSendErrorWithBilledUsage(ctx, passthroughPostHook, err, ch, nil, nil)
			},
		},
		{
			name: "plain/unbilled", err: errors.New("connection reset"), billed: false, isBifErr: true,
			send: func(ctx *schemas.BifrostContext, err error, ch chan *schemas.BifrostStreamChunk) {
				ProcessAndSendError(ctx, passthroughPostHook, err, ch, nil, nil)
			},
		},
		{
			name: "carrier/unbilled", err: classifiedReadError{typed: &schemas.BifrostError{StatusCode: schemas.Ptr(503)}}, billed: false,
			send: func(ctx *schemas.BifrostContext, err error, ch chan *schemas.BifrostStreamChunk) {
				ProcessAndSendError(ctx, passthroughPostHook, err, ch, nil, nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, &schemas.BifrostLLMUsage{
				PromptTokens: 7, CompletionTokens: 2, TotalTokens: 9, ServiceTier: &tier,
			})
			ch := make(chan *schemas.BifrostStreamChunk, 1)
			tc.send(ctx, tc.err, ch)
			chunk := <-ch
			if chunk == nil || chunk.BifrostError == nil {
				t.Fatalf("expected an error chunk, got %+v", chunk)
			}
			if chunk.BifrostError.IsBifrostError != tc.isBifErr {
				t.Errorf("IsBifrostError = %v, want %v (classification must not change)", chunk.BifrostError.IsBifrostError, tc.isBifErr)
			}
			billed := chunk.BifrostError.ExtraFields.BilledUsage
			if !tc.billed {
				if billed != nil {
					t.Fatalf("ProcessAndSendError must not attach usage, got %+v", billed)
				}
				return
			}
			if billed == nil {
				t.Fatal("expected the consumed usage as BilledUsage")
			}
			if billed.PromptTokens != 7 || billed.CompletionTokens != 2 || billed.TotalTokens != 9 {
				t.Errorf("billed = %d/%d/%d, want 7/2/9", billed.PromptTokens, billed.CompletionTokens, billed.TotalTokens)
			}
			if billed.ServiceTier == nil || *billed.ServiceTier != tier {
				t.Errorf("billed service tier = %v, want %q", billed.ServiceTier, tier)
			}
		})
	}
}
