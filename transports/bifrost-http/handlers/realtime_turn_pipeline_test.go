package handlers

import (
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func TestMapRealtimeWireErrorFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		err      *schemas.BifrostError
		wantType string
		wantCode string
	}{
		{name: "an operation the provider does not allow is the caller's mistake, not the server's",
			err: providerUtils.NewUnsupportedOperationError(schemas.LiveRequest, "openai-nolive"), wantType: "invalid_request_error", wantCode: "unsupported_operation"},
		{name: "a wire error keeps its own type",
			err: newRealtimeWireBifrostError(400, "invalid_request_error", "bad frame"), wantType: "invalid_request_error", wantCode: "invalid_request_error"},
		{name: "a spent budget is a quota error",
			err: newRealtimeWireBifrostError(402, "budget_exceeded", "Budget exceeded"), wantType: "insufficient_quota", wantCode: "insufficient_quota"},
		{name: "anything unclassified is a server error",
			err: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "boom"}}, wantType: "server_error", wantCode: "server_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errType, code, message, _ := mapRealtimeWireErrorFields(tc.err)
			assert.Equal(t, tc.wantType, errType)
			assert.Equal(t, tc.wantCode, code)
			assert.Equal(t, tc.err.Error.Message, message)
		})
	}
}
