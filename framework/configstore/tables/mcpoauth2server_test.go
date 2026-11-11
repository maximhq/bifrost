package tables

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

// TestClientGatewayTelemetryConfig_Validate pins what a switched-on telemetry
// block must hold: at least one destination, an http(s) endpoint, header values
// that are env. or vault. references (so no collector credential is stored in
// the config), and no empty attribute keys. A switched-off block is not checked.
func TestClientGatewayTelemetryConfig_Validate(t *testing.T) {
	t.Setenv("BIFROST_TEST_OTEL_TOKEN", "secret")
	dest := func(endpoint string, headers map[string]string) ClientGatewayTelemetryDestination {
		d := ClientGatewayTelemetryDestination{Endpoint: schemas.NewSecretVar(endpoint)}
		if headers != nil {
			d.Headers = map[string]*schemas.SecretVar{}
			for k, v := range headers {
				d.Headers[k] = schemas.NewSecretVar(v)
			}
		}
		return d
	}
	for name, tc := range map[string]struct {
		cfg     *ClientGatewayTelemetryConfig
		wantErr string
	}{
		"nil":               {cfg: nil},
		"disabled, empty":   {cfg: &ClientGatewayTelemetryConfig{Enabled: false}},
		"valid":             {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("https://otel.example.com", map[string]string{"authorization": "env.BIFROST_TEST_OTEL_TOKEN"})}, DropAttributes: []string{"prompt"}}},
		"env endpoint":      {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("env.BIFROST_TEST_OTEL_ENDPOINT", nil)}}},
		"no destinations":   {cfg: &ClientGatewayTelemetryConfig{Enabled: true}, wantErr: "destinations must list at least one collector"},
		"missing endpoint":  {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{{}}}, wantErr: "endpoint is required"},
		"non-http endpoint": {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("ftp://otel.example.com", nil)}}, wantErr: "must be an http or https URL"},
		"plain-text header": {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("https://otel.example.com", map[string]string{"authorization": "Bearer abc"})}}, wantErr: "must be an env. or vault. reference"},
		"empty header name": {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("https://otel.example.com", map[string]string{" ": "env.X"})}}, wantErr: "empty header name"},
		"empty drop key":    {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("https://otel.example.com", nil)}, DropAttributes: []string{""}}, wantErr: "empty attribute key"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.cfg.Validate("telemetry")
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}
