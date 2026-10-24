package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenericProviderConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *GenericProviderConfig
		wantErr string
	}{
		{"nil is valid", nil, ""},
		{"empty auth type defaults", &GenericProviderConfig{}, ""},
		{"bearer", &GenericProviderConfig{Auth: GenericAuthConfig{Type: GenericAuthBearer}}, ""},
		{"unknown auth type", &GenericProviderConfig{Auth: GenericAuthConfig{Type: "magic"}}, "auth.type"},
		{"header needs name", &GenericProviderConfig{Auth: GenericAuthConfig{Type: GenericAuthHeader}}, "auth.header_name"},
		{"header name must be a token", &GenericProviderConfig{Auth: GenericAuthConfig{Type: GenericAuthHeader, HeaderName: "x api"}}, "not a valid header name"},
		{"query needs param", &GenericProviderConfig{Auth: GenericAuthConfig{Type: GenericAuthQuery}}, "auth.query_param"},
		{"negative fee", &GenericProviderConfig{Usage: &GenericUsageConfig{CostPerRequest: Ptr(-1.0)}}, "usage.cost_per_request"},
		{"route method", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "FETCH", Path: "/x"}}}, "routes[0]: unsupported method"},
		{"route path must be rooted", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "GET", Path: "x"}}}, "must start with /"},
		{"route glob syntax", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "GET", Path: "/[x"}}}, "routes[0]"},
		{"double star only last", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "GET", Path: "/**/x"}}}, "only allowed as the last segment"},
		{"route fee", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "GET", Path: "/x", Usage: &GenericUsageConfig{CostPerRequest: Ptr(-0.1)}}}}, "routes[0].usage.cost_per_request"},
		{"streaming glob", &GenericProviderConfig{Streaming: &GenericStreamingConfig{Paths: []string{"v1/*"}}}, "streaming.paths[0]"},
		{"full valid", &GenericProviderConfig{
			Auth:      GenericAuthConfig{Type: GenericAuthHeader, HeaderName: "x-api-key", ValuePrefix: "Key "},
			Usage:     &GenericUsageConfig{ModelPath: "model", CostPath: "data.#.cost", CostPerRequest: Ptr(0.01)},
			Streaming: &GenericStreamingConfig{Paths: []string{"/v1/stream/**"}, DetectFromAcceptHeader: true},
			Routes:    []GenericRouteConfig{{Method: "post", Path: "/v1/*/run", Billable: Ptr(true)}},
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestGenericProviderConfigClone(t *testing.T) {
	assert.Nil(t, (*GenericProviderConfig)(nil).Clone())
	src := &GenericProviderConfig{
		Auth:      GenericAuthConfig{Type: GenericAuthHeader, HeaderName: "x-api-key"},
		Usage:     &GenericUsageConfig{CostPerRequest: Ptr(1.0)},
		Streaming: &GenericStreamingConfig{Paths: []string{"/a"}},
		Routes:    []GenericRouteConfig{{Method: "GET", Path: "/x", Billable: Ptr(false), Stream: Ptr(true), Usage: &GenericUsageConfig{CostPath: "c"}}},
	}
	dst := src.Clone()
	require.Equal(t, src, dst)

	dst.Auth.HeaderName = "x-other"
	*dst.Usage.CostPerRequest = 2
	dst.Streaming.Paths[0] = "/b"
	*dst.Routes[0].Billable = true
	dst.Routes[0].Usage.CostPath = "d"
	assert.Equal(t, "x-api-key", src.Auth.HeaderName)
	assert.InDelta(t, 1.0, *src.Usage.CostPerRequest, 0)
	assert.Equal(t, "/a", src.Streaming.Paths[0])
	assert.False(t, *src.Routes[0].Billable)
	assert.Equal(t, "c", src.Routes[0].Usage.CostPath)
}
