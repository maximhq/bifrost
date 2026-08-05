package modelark

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testNoopLogger struct{}

func (testNoopLogger) Debug(string, ...any)                   {}
func (testNoopLogger) Info(string, ...any)                    {}
func (testNoopLogger) Warn(string, ...any)                    {}
func (testNoopLogger) Error(string, ...any)                   {}
func (testNoopLogger) Fatal(string, ...any)                   {}
func (testNoopLogger) SetLevel(schemas.LogLevel)              {}
func (testNoopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testNoopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestVideoRetrieveValidatesTaskID(t *testing.T) {
	tests := []struct {
		name    string
		taskID  string
		wantErr bool
	}{
		{"documented_format", "cgt-20260507095207-ktz64", false},
		{"format_from_existing_tests", "cgt-20260803171320-r6rlg", false},
		{"empty", "", true},
		{"path_traversal", "cgt-x/../../secrets", true},
		{"query_injection", "cgt-x?admin=1", true},
		{"dot_segment", "cgt-x.json", true},
		{"double_encoded_dot_segment", "cgt-x%252e%252e", true},
		{"uppercase_rejected", "CGT-20260507095207-KTZ64", true},
		{"missing_prefix", "20260507095207-ktz64", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var called bool
			var gotPath, gotRawQuery string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				called = true
				gotPath = r.URL.Path
				gotRawQuery = r.URL.RawQuery
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"` + tt.taskID + `","status":"succeeded"}`))
			}))
			defer server.Close()

			provider, err := NewModelArkProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:                        server.URL,
					DefaultRequestTimeoutInSeconds: 5,
				},
			}, testNoopLogger{})
			require.NoError(t, err)

			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			resp, bifrostErr := provider.VideoRetrieve(ctx, schemas.Key{}, &schemas.BifrostVideoRetrieveRequest{
				ID: tt.taskID,
			})

			mu.Lock()
			defer mu.Unlock()

			if tt.wantErr {
				require.NotNil(t, bifrostErr, "expected an error for task id %q", tt.taskID)
				assert.False(t, called, "malicious/invalid task id must be rejected before any request reaches the wire")
				return
			}

			require.Nil(t, bifrostErr)
			require.True(t, called, "valid task id must still reach ModelArk")
			assert.Equal(t, "/contents/generations/tasks/"+tt.taskID, gotPath)
			assert.Empty(t, gotRawQuery)
			require.NotNil(t, resp)
		})
	}
}

func TestNewModelArkProviderBoundsVideoDownloadSize(t *testing.T) {
	provider, err := NewModelArkProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 5},
	}, testNoopLogger{})
	require.NoError(t, err)
	assert.Equal(t, maxVideoDownloadBytes, provider.client.MaxResponseBodySize)
}

func TestVideoDownloadRejectsAdvertisedOversizedBody(t *testing.T) {
	assert.False(t, videoDownloadExceedsLimit(-1), "unknown Content-Length is allowed and bounded by fasthttp")
	assert.False(t, videoDownloadExceedsLimit(maxVideoDownloadBytes))
	assert.True(t, videoDownloadExceedsLimit(maxVideoDownloadBytes+1))
}
