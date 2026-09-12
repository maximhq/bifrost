package schemas

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetErrorStringWithCause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *BifrostError
		want string
	}{
		{
			name: "appends the cause to the message",
			err: &BifrostError{Error: &ErrorField{
				Message: ErrProviderResponseUnmarshal,
				Error:   errors.New("Syntax error at index 1: invalid char"),
			}},
			want: ErrProviderResponseUnmarshal + ": Syntax error at index 1: invalid char",
		},
		{
			name: "message without a cause",
			err:  &BifrostError{Error: &ErrorField{Message: "rate limit exceeded"}},
			want: "rate limit exceeded",
		},
		{
			name: "cause already part of the message",
			err:  &BifrostError{Error: &ErrorField{Message: "upstream said no", Error: errors.New("upstream said no")}},
			want: "upstream said no",
		},
		{
			name: "status code fallback keeps the cause",
			err: &BifrostError{
				StatusCode: Ptr(404),
				Error:      &ErrorField{Error: errors.New("unknown path /v1/models")},
			},
			want: "endpoint not found: unknown path /v1/models",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.want, test.err.GetErrorStringWithCause())
		})
	}

	require.Equal(t, "", (*BifrostError)(nil).GetErrorStringWithCause())
}
