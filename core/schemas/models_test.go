package schemas

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Raw types that mirror the real KeyStatus → BifrostError → ExtraFields → KeyStatus
// chain but without any custom MarshalJSON. Used to reproduce the cycle error
// that would occur without the fix.
type rawKeyStatusExtraFields struct {
	KeyStatuses []rawKeyStatus `json:"key_statuses,omitempty"`
}

type rawKeyStatusBifrostError struct {
	IsBifrostError bool                    `json:"is_bifrost_error"`
	Error          *ErrorField             `json:"error"`
	ExtraFields    rawKeyStatusExtraFields `json:"extra_fields"`
}

type rawKeyStatus struct {
	KeyID    string                    `json:"key_id"`
	Status   KeyStatusType             `json:"status"`
	Provider ModelProvider             `json:"provider"`
	Error    *rawKeyStatusBifrostError `json:"error,omitempty"`
}

// TestKeyStatusMarshalJSON_ReproduceCycle proves that without the custom MarshalJSON,
// the circular reference between KeyStatus and BifrostError causes a marshaling failure.
func TestKeyStatusMarshalJSON_ReproduceCycle(t *testing.T) {
	bifrostErr := &rawKeyStatusBifrostError{
		IsBifrostError: true,
		Error:          &ErrorField{Message: "test error"},
	}
	keyStatus := rawKeyStatus{
		KeyID:    "key-1",
		Status:   KeyStatusListModelsFailed,
		Provider: "test-provider",
		Error:    bifrostErr,
	}
	// Create the same cycle that HandleKeylessListModelsRequest creates
	bifrostErr.ExtraFields.KeyStatuses = []rawKeyStatus{keyStatus}

	// Without any custom MarshalJSON, this must fail with a cycle error
	_, err := json.Marshal(keyStatus)
	require.Error(t, err, "expected cycle error without the MarshalJSON fix")
	assert.Contains(t, err.Error(), "cycle", "error should mention a cycle")
}

func TestKeyStatusMarshalJSON_NoCycle(t *testing.T) {
	bifrostErr := &BifrostError{
		IsBifrostError: true,
		Error:          &ErrorField{Message: "test error"},
	}
	keyStatus := KeyStatus{
		KeyID:    "key-1",
		Status:   KeyStatusListModelsFailed,
		Provider: "test-provider",
		Error:    bifrostErr,
	}
	// Create the same cycle that HandleKeylessListModelsRequest creates
	bifrostErr.ExtraFields.KeyStatuses = []KeyStatus{keyStatus}

	data, err := Marshal(keyStatus)
	require.NoError(t, err, "Marshal should not fail on circular KeyStatus")

	// Verify the output doesn't contain nested key_statuses
	assert.False(t, bytes.Contains(data, []byte(`"key_statuses"`)),
		"expected key_statuses to be omitted from nested error")
}

func TestKeyStatusMarshalJSON_NilError(t *testing.T) {
	keyStatus := KeyStatus{
		KeyID:    "key-2",
		Status:   "success",
		Provider: "test-provider",
	}

	data, err := Marshal(keyStatus)
	require.NoError(t, err, "Marshal should not fail on KeyStatus with nil error")
	assert.Contains(t, string(data), `"key_id":"key-2"`)
	assert.NotContains(t, string(data), `"error"`)
}

func TestKeyStatusMarshalJSON_PreservesErrorFields(t *testing.T) {
	statusCode := 401
	bifrostErr := &BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &ErrorField{Message: "unauthorized"},
		ExtraFields: BifrostErrorExtraFields{
			Provider:       "openai",
			ModelRequested: "gpt-4",
		},
	}
	keyStatus := KeyStatus{
		KeyID:    "key-3",
		Status:   KeyStatusListModelsFailed,
		Provider: "openai",
		Error:    bifrostErr,
	}
	// Create cycle
	bifrostErr.ExtraFields.KeyStatuses = []KeyStatus{keyStatus}

	data, err := Marshal(keyStatus)
	require.NoError(t, err)

	// Error fields other than key_statuses should be preserved
	dataStr := string(data)
	assert.Contains(t, dataStr, `"unauthorized"`)
	assert.Contains(t, dataStr, `"model_requested":"gpt-4"`)
	assert.Contains(t, dataStr, `"status_code":401`)
}
