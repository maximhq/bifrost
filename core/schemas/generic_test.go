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
		{"sigv4 needs service", &GenericProviderConfig{Auth: GenericAuthConfig{Type: GenericAuthAWSSigV4}}, "auth.aws.service"},
		{"sigv4 service charset", &GenericProviderConfig{Auth: GenericAuthConfig{Type: GenericAuthAWSSigV4, AWS: &GenericAWSAuthConfig{Service: "Text Ract"}}}, "[a-z0-9-]+"},
		{"sigv4 ok", &GenericProviderConfig{Auth: GenericAuthConfig{Type: GenericAuthAWSSigV4, AWS: &GenericAWSAuthConfig{Service: "textract"}}}, ""},
		{"negative fee", &GenericProviderConfig{Usage: &GenericUsageConfig{CostPerRequest: Ptr(-1.0)}}, "usage.cost_per_request"},
		{"route method", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "FETCH", Path: "/x"}}}, "routes[0]: unsupported method"},
		{"route path must be rooted", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "GET", Path: "x"}}}, "must start with /"},
		{"route glob syntax", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "GET", Path: "/[x"}}}, "routes[0]"},
		{"double star only last", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "GET", Path: "/**/x"}}}, "only allowed as the last segment"},
		{"header route ok", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.Get*"}}}}, ""},
		{"header name must be a token", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X Amz": "x"}}}}, "not a valid header name"},
		{"header value required", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": " "}}}}, "a value glob is required"},
		{"header glob syntax", &GenericProviderConfig{Routes: []GenericRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "[x"}}}}, "routes[0].headers[X-Amz-Target]"},
		{"block size above zero", &GenericProviderConfig{Usage: &GenericUsageConfig{UnitsBlockSize: Ptr(0.0)}}, "units_block_size must be above zero"},
		{"minimum not negative", &GenericProviderConfig{Usage: &GenericUsageConfig{UnitsMinimum: Ptr(-1.0)}}, "units_minimum must not be negative"},
		{"rounding ok", &GenericProviderConfig{Usage: &GenericUsageConfig{UnitsPath: "Text", UnitsSource: GenericUnitsFromRequest, UnitsMeasure: GenericUnitsChars, UnitsBlockSize: Ptr(100.0), UnitsMinimum: Ptr(1.0), CostPerUnit: Ptr(0.01)}}, ""},
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
		Auth:      GenericAuthConfig{Type: GenericAuthAWSSigV4, AWS: &GenericAWSAuthConfig{Service: "textract"}},
		Usage:     &GenericUsageConfig{CostPerRequest: Ptr(1.0)},
		Streaming: &GenericStreamingConfig{Paths: []string{"/a"}},
		Routes:    []GenericRouteConfig{{Method: "GET", Path: "/x", Billable: Ptr(false), Stream: Ptr(true), Usage: &GenericUsageConfig{CostPath: "c"}}},
	}
	dst := src.Clone()
	require.Equal(t, src, dst)

	dst.Auth.AWS.Service = "translate"
	*dst.Usage.CostPerRequest = 2
	dst.Streaming.Paths[0] = "/b"
	*dst.Routes[0].Billable = true
	dst.Routes[0].Usage.CostPath = "d"
	assert.Equal(t, "textract", src.Auth.AWS.Service)
	assert.InDelta(t, 1.0, *src.Usage.CostPerRequest, 0)
	assert.Equal(t, "/a", src.Streaming.Paths[0])
	assert.False(t, *src.Routes[0].Billable)
	assert.Equal(t, "c", src.Routes[0].Usage.CostPath)
}

func TestGenericUsageConfigUnitsValidation(t *testing.T) {
	ok := func(u *GenericUsageConfig) {
		t.Helper()
		assert.NoError(t, (&GenericProviderConfig{Usage: u}).Validate())
	}
	bad := func(u *GenericUsageConfig, msg string) {
		t.Helper()
		err := (&GenericProviderConfig{Usage: u}).Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), msg)
	}
	ok(&GenericUsageConfig{UnitsPath: "DocumentMetadata.Pages", CostPerUnit: Ptr(0.0015)})
	ok(&GenericUsageConfig{UnitsPath: "Text", UnitsSource: GenericUnitsFromRequest, UnitsMeasure: GenericUnitsChars, CostPerUnit: Ptr(0.000015)})
	ok(&GenericUsageConfig{UnitsSource: GenericUnitsFromRequest, UnitsMeasure: GenericUnitsBodyBytes, CostPerUnit: Ptr(0.000016)})
	ok(&GenericUsageConfig{UnitsPath: "pages"})
	bad(&GenericUsageConfig{UnitsPath: "pages", CostPerUnit: Ptr(-1.0)}, "cost_per_unit must not be negative")
	bad(&GenericUsageConfig{UnitsPath: "pages", UnitsSource: "header", CostPerUnit: Ptr(1.0)}, "units_source")
	bad(&GenericUsageConfig{UnitsPath: "pages", UnitsMeasure: "words", CostPerUnit: Ptr(1.0)}, "units_measure")
	bad(&GenericUsageConfig{CostPerUnit: Ptr(1.0)}, "units_path is required")
	// A route inherits units_path and units_measure from the provider block, so a rate alone is
	// enough there; without a provider path it is the same error.
	rate := []GenericRouteConfig{{Method: "POST", Path: "/x", Usage: &GenericUsageConfig{CostPerUnit: Ptr(0.5)}}}
	require.NoError(t, (&GenericProviderConfig{Usage: &GenericUsageConfig{UnitsPath: "pages"}, Routes: rate}).Validate())
	require.NoError(t, (&GenericProviderConfig{Usage: &GenericUsageConfig{UnitsMeasure: GenericUnitsBodyBytes}, Routes: rate}).Validate())
	err := (&GenericProviderConfig{Routes: rate}).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "routes[0].usage.units_path is required")
	ok(&GenericUsageConfig{CostPerUnit: Ptr(0.0)})                                                                                 // zero is "no unit pricing" and needs no path (a route waiving the provider's)
	ok(&GenericUsageConfig{UnitsSource: GenericUnitsFromRequest, UnitsMeasure: GenericUnitsBodyChars, CostPerUnit: Ptr(0.000016)}) // body_chars reads the whole body and needs no path
}
