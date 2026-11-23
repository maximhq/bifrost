package schemas

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *RelayConfig
		wantErr string
	}{
		{"nil is valid", nil, ""},
		{"empty auth type defaults", &RelayConfig{}, ""},
		{"bearer", &RelayConfig{Auth: RelayAuthConfig{Type: RelayAuthBearer}}, ""},
		{"unknown auth type", &RelayConfig{Auth: RelayAuthConfig{Type: "magic"}}, "auth.type"},
		{"header needs name", &RelayConfig{Auth: RelayAuthConfig{Type: RelayAuthHeader}}, "auth.header_name"},
		{"header name must be a token", &RelayConfig{Auth: RelayAuthConfig{Type: RelayAuthHeader, HeaderName: "x api"}}, "not a valid header name"},
		{"query needs param", &RelayConfig{Auth: RelayAuthConfig{Type: RelayAuthQuery}}, "auth.query_param"},
		{"sigv4 needs service", &RelayConfig{Auth: RelayAuthConfig{Type: RelayAuthAWSSigV4}}, "auth.aws.service"},
		{"sigv4 service charset", &RelayConfig{Auth: RelayAuthConfig{Type: RelayAuthAWSSigV4, AWS: &RelayAWSAuthConfig{Service: "Text Ract"}}}, "[a-z0-9-]+"},
		{"sigv4 ok", &RelayConfig{Auth: RelayAuthConfig{Type: RelayAuthAWSSigV4, AWS: &RelayAWSAuthConfig{Service: "textract"}}}, ""},
		{"negative fee", &RelayConfig{Usage: &RelayUsageConfig{CostPerRequest: Ptr(-1.0)}}, "usage.cost_per_request"},
		{"route method", &RelayConfig{Routes: []RelayRouteConfig{{Method: "FETCH", Path: "/x"}}}, "routes[0]: unsupported method"},
		{"route path must be rooted", &RelayConfig{Routes: []RelayRouteConfig{{Method: "GET", Path: "x"}}}, "must start with /"},
		{"route glob syntax", &RelayConfig{Routes: []RelayRouteConfig{{Method: "GET", Path: "/[x"}}}, "routes[0]"},
		{"double star only last", &RelayConfig{Routes: []RelayRouteConfig{{Method: "GET", Path: "/**/x"}}}, "only allowed as the last segment"},
		{"header route ok", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.Get*"}}}}, ""},
		{"header name must be a token", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X Amz": "x"}}}}, "not a valid header name"},
		{"header value required", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": " "}}}}, "a value glob is required"},
		{"header glob syntax", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "[x"}}}}, "routes[0].headers[X-Amz-Target]"},
		{"header name with a separator", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X/Amz-Target": "x"}}}}, "not a valid header name"},
		{"header matcher on a credential ok", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"Authorization": "Bearer *"}}}}, ""},
		{"header matcher on a key header ok", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X-Api-Key": "*"}}}}, ""},
		{"header matcher on the injected auth header ok", &RelayConfig{Auth: RelayAuthConfig{Type: RelayAuthHeader, HeaderName: "X-Key"}, Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"x-key": "*"}}}}, ""},
		{"header matcher on an unforwarded x-amz header under sigv4 ok", &RelayConfig{Auth: RelayAuthConfig{Type: RelayAuthAWSSigV4, AWS: &RelayAWSAuthConfig{Service: "s3"}}, Routes: []RelayRouteConfig{{Method: "PUT", Path: "/**", Headers: map[string]string{"X-Amz-Copy-Source": "*"}, Billable: Ptr(false)}}}, ""},
		{"header matcher on a credential x-amz header under sigv4 ok", &RelayConfig{Auth: RelayAuthConfig{Type: RelayAuthAWSSigV4, AWS: &RelayAWSAuthConfig{Service: "s3"}}, Routes: []RelayRouteConfig{{Method: "PUT", Path: "/**", Headers: map[string]string{"X-Amz-Security-Token": "*"}, Billable: Ptr(false)}}}, ""},
		{"header matcher on content-length ok", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"Content-Length": "0"}, Billable: Ptr(false)}}}, ""},
		{"header matcher on a hop-by-hop header ok", &RelayConfig{Routes: []RelayRouteConfig{{Method: "GET", Path: "/ws", Headers: map[string]string{"Upgrade": "websocket"}}}}, ""},
		{"header matcher on an Azure key header ok", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"Ocp-Apim-Subscription-Key": "*"}}}}, ""},
		{"header matcher on Connection ok", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"Connection": "keep-alive"}}}}, ""},
		{"header names differing only by case", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "A*", "x-amz-target": "B*"}}}}, "differs only by case"},
		{"block size above zero", &RelayConfig{Usage: &RelayUsageConfig{UnitsBlockSize: Ptr(0.0)}}, "units_block_size must be above zero"},
		{"minimum not negative", &RelayConfig{Usage: &RelayUsageConfig{UnitsMinimum: Ptr(-1.0)}}, "units_minimum must not be negative"},
		{"model from the query ok", &RelayConfig{Usage: &RelayUsageConfig{ModelPath: "model", ModelSource: RelayModelFromQuery}}, ""},
		{"unknown model source", &RelayConfig{Usage: &RelayUsageConfig{ModelSource: "header"}}, "model_source: unsupported value"},
		{"rounding ok", &RelayConfig{Usage: &RelayUsageConfig{UnitsPath: "Text", UnitsSource: RelayUnitsFromRequest, UnitsMeasure: RelayUnitsChars, UnitsBlockSize: Ptr(100.0), UnitsMinimum: Ptr(1.0), CostPerUnit: Ptr(0.01)}}, ""},
		{"job ok", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: &RelayJobConfig{IDPath: "JobId", Poll: RelayJobPoll{Path: "/", Body: `{"JobId":"{{job.id}}"}`}, StatusPath: "JobStatus", Succeeded: []string{"SUCCEEDED"}, Usage: &RelayUsageConfig{UnitsPath: "DocumentMetadata.Pages", CostPerUnit: Ptr(0.0015)}}}}}, ""},
		{"job needs id path", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: &RelayJobConfig{Poll: RelayJobPoll{Path: "/{{job.id}}"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "routes[0].job.id_path"},
		{"job needs a succeeded status", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/{{job.id}}"}, StatusPath: "s"}}}}, "succeeded"},
		{"job poll path rooted", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "jobs/{{job.id}}"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "poll.path"},
		{"job measure needing a path under an inherited rate", &RelayConfig{Usage: &RelayUsageConfig{UnitsMeasure: RelayUnitsBodyBytes, CostPerUnit: Ptr(0.01)}, Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/{{job.id}}"}, StatusPath: "s", Succeeded: []string{"x"}, Usage: &RelayUsageConfig{UnitsMeasure: RelayUnitsChars}}}}}, "routes[0].job.usage.units_path is required"},
		{"job cannot bill request units", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Usage: &RelayUsageConfig{UnitsPath: "Text", UnitsSource: RelayUnitsFromRequest, UnitsMeasure: RelayUnitsChars, CostPerUnit: Ptr(0.001)}, Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/{{job.id}}"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "request-side units are not billed at settlement"},
		{"job route cannot stream", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Stream: Ptr(true), Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/{{job.id}}"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "served buffered"},
		{"job route must be billable", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Billable: Ptr(false), Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/{{job.id}}"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "must be billable"},
		{"job on a PUT needs billable true", &RelayConfig{Routes: []RelayRouteConfig{{Method: "PUT", Path: "/", Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/{{job.id}}"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "must set billable: true"},
		{"job usage validated", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/{{job.id}}"}, StatusPath: "s", Succeeded: []string{"x"}, Usage: &RelayUsageConfig{CostPerUnit: Ptr(1.0)}}}}}, "routes[0].job.usage.units_path"},
		{"route fee", &RelayConfig{Routes: []RelayRouteConfig{{Method: "GET", Path: "/x", Usage: &RelayUsageConfig{CostPerRequest: Ptr(-0.1)}}}}, "routes[0].usage.cost_per_request"},
		{"streaming glob", &RelayConfig{Streaming: &RelayStreamingConfig{Paths: []string{"v1/*"}}}, "streaming.paths[0]"},
		{"full valid", &RelayConfig{
			Auth:      RelayAuthConfig{Type: RelayAuthHeader, HeaderName: "x-api-key", ValuePrefix: "Key "},
			Usage:     &RelayUsageConfig{ModelPath: "model", CostPath: "data.#.cost", CostPerRequest: Ptr(0.01)},
			Streaming: &RelayStreamingConfig{Paths: []string{"/v1/stream/**"}},
			Routes:    []RelayRouteConfig{{Method: "post", Path: "/v1/*/run", Billable: Ptr(true)}},
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

func TestRelayBodyOverridesValidate(t *testing.T) {
	raw := func(v string) json.RawMessage { return json.RawMessage(v) }
	cases := []struct {
		name     string
		endpoint map[string]json.RawMessage
		route    map[string]json.RawMessage
		wantErr  string
	}{
		{"top-level boolean", map[string]json.RawMessage{"include_usage": raw("true")}, nil, ""},
		{"nested path and any JSON value", nil, map[string]json.RawMessage{"options.depth": raw(`"advanced"`), "limits": raw(`{"max":3}`), "tags": raw(`[1,2]`), "x": raw("null")}, ""},
		{"value must be JSON", map[string]json.RawMessage{"depth": raw("advanced")}, nil, "body_overrides[depth]: the value must be JSON"},
		{"a number past a double's range", map[string]json.RawMessage{"limits": raw(`{"max":1e400}`)}, nil, "too large or too small"},
		{"a number too small for a double", nil, map[string]json.RawMessage{"eps": raw("1e-400")}, "too large or too small"},
		{"an exact whole number past 2^53", map[string]json.RawMessage{"seed": raw("12345678901234567891")}, nil, ""},
		{"empty value", nil, map[string]json.RawMessage{"depth": raw("  ")}, "routes[0].body_overrides[depth]"},
		{"empty segment", map[string]json.RawMessage{"a..b": raw("1")}, nil, "not a path of object keys"},
		{"path syntax refused", map[string]json.RawMessage{"items.#.id": raw("1")}, nil, "not a path of object keys"},
		{"escape refused", map[string]json.RawMessage{`a\.b`: raw("1")}, nil, "not a path of object keys"},
		{"array index refused", map[string]json.RawMessage{"items.0": raw("1")}, nil, "read as an array index"},
		{"array append refused", map[string]json.RawMessage{"options.-1": raw("true")}, nil, "read as an array index"},
		{"a key with a dash is fine", map[string]json.RawMessage{"options.-x": raw("true"), "options.-12a": raw("1")}, nil, ""},
		{"one parent under two spellings", map[string]json.RawMessage{"options.depth": raw("1"), "Options.mode": raw("2")}, nil, "one parent field under different letter case"},
		{"one parent under two spellings across endpoint and route", map[string]json.RawMessage{"options.depth": raw("1")}, map[string]json.RawMessage{"OPTIONS.mode": raw("2")}, "(with the endpoint's)"},
		{"siblings under one spelling", map[string]json.RawMessage{"options.depth": raw("1"), "options.mode": raw("2")}, nil, ""},
		{"case variants in one map", map[string]json.RawMessage{"include_usage": raw("true"), "Include_Usage": raw("true")}, nil, "different letter case"},
		{"field inside a set field", nil, map[string]json.RawMessage{"options": raw("{}"), "options.depth": raw("1")}, "sets a field inside"},
		{"route replaces the endpoint value", map[string]json.RawMessage{"include_usage": raw("true")}, map[string]json.RawMessage{"include_usage": raw("false")}, ""},
		{"route case variant of an endpoint field", map[string]json.RawMessage{"include_usage": raw("true")}, map[string]json.RawMessage{"INCLUDE_USAGE": raw("true")}, "(with the endpoint's)"},
		{"route field inside an endpoint field", map[string]json.RawMessage{"options": raw("{}")}, map[string]json.RawMessage{"Options.depth": raw("1")}, "sets a field inside"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &RelayConfig{BodyOverrides: tc.endpoint, Routes: []RelayRouteConfig{{Method: "POST", Path: "/search", BodyOverrides: tc.route}}}
			err := cfg.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// A route that turns the relay's fields off keeps only its own, and is not checked against the relay's.
func TestRelayRouteInheritBodyOverrides(t *testing.T) {
	relayFields := map[string]json.RawMessage{"include_usage": json.RawMessage("true")}
	own := map[string]json.RawMessage{"INCLUDE_USAGE": json.RawMessage("false")}
	off := RelayRouteConfig{Method: "POST", Path: "/x", InheritBodyOverrides: Ptr(false), BodyOverrides: own}
	assert.Equal(t, own, off.EffectiveBodyOverrides(relayFields))
	assert.Nil(t, RelayRouteConfig{InheritBodyOverrides: Ptr(false)}.EffectiveBodyOverrides(relayFields), "off with no fields of its own sends nothing")
	assert.Equal(t, relayFields, RelayRouteConfig{}.EffectiveBodyOverrides(relayFields), "unset inherits")

	cfg := &RelayConfig{BodyOverrides: relayFields, Routes: []RelayRouteConfig{off}}
	require.NoError(t, cfg.Validate(), "a case variant of a field the route does not inherit is not a conflict")
	dst := cfg.Clone()
	*dst.Routes[0].InheritBodyOverrides = true
	assert.False(t, *cfg.Routes[0].InheritBodyOverrides, "a clone shares no pointer")

	var parsed RelayRouteConfig
	require.NoError(t, Unmarshal([]byte(`{"method":"POST","path":"/x","inherit_body_overrides":false}`), &parsed))
	require.NotNil(t, parsed.InheritBodyOverrides)
	assert.False(t, *parsed.InheritBodyOverrides)
}

func TestRelayBodyOverridesMergeAndClone(t *testing.T) {
	endpoint := map[string]json.RawMessage{"include_usage": json.RawMessage("true"), "depth": json.RawMessage(`"basic"`)}
	route := map[string]json.RawMessage{"depth": json.RawMessage(`"advanced"`)}
	merged := MergeRelayBodyOverrides(endpoint, route)
	assert.Equal(t, map[string]json.RawMessage{"include_usage": json.RawMessage("true"), "depth": json.RawMessage(`"advanced"`)}, merged)
	assert.Equal(t, json.RawMessage(`"basic"`), endpoint["depth"], "the endpoint map is not modified")

	src := &RelayConfig{BodyOverrides: endpoint, Routes: []RelayRouteConfig{{Method: "POST", Path: "/x", BodyOverrides: route}}}
	dst := src.Clone()
	require.Equal(t, src, dst)
	dst.BodyOverrides["include_usage"][0] = 'f'
	dst.Routes[0].BodyOverrides["depth"] = json.RawMessage("1")
	assert.Equal(t, json.RawMessage("true"), src.BodyOverrides["include_usage"], "a clone shares no value bytes")
	assert.Equal(t, json.RawMessage(`"advanced"`), src.Routes[0].BodyOverrides["depth"])
}

func TestRelayConfigClone(t *testing.T) {
	assert.Nil(t, (*RelayConfig)(nil).Clone())
	src := &RelayConfig{
		Auth:      RelayAuthConfig{Type: RelayAuthAWSSigV4, AWS: &RelayAWSAuthConfig{Service: "textract"}},
		Usage:     &RelayUsageConfig{CostPerRequest: Ptr(1.0)},
		Streaming: &RelayStreamingConfig{Paths: []string{"/a"}},
		Routes:    []RelayRouteConfig{{Method: "GET", Path: "/x", Billable: Ptr(false), Stream: Ptr(true), Usage: &RelayUsageConfig{CostPath: "c"}}},
	}
	dst := src.Clone()
	require.Equal(t, src, dst)

	dst.Auth.AWS.Service = "translate"
	*dst.Usage.CostPerRequest = 2
	dst.Streaming.Paths[0] = "/b"
	*dst.Routes[0].Billable = true
	src.Routes[0].Job = &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/", Headers: map[string]string{"X-Amz-Target": "Svc.Get"}}, StatusPath: "s", Succeeded: []string{"DONE"}, Usage: &RelayUsageConfig{CostPerUnit: Ptr(0.5)}}
	withJob := src.Clone()
	withJob.Routes[0].Job.Succeeded[0] = "CHANGED"
	withJob.Routes[0].Job.Poll.Headers["X-Amz-Target"] = "Svc.Other"
	*withJob.Routes[0].Job.Usage.CostPerUnit = 9
	assert.Equal(t, "DONE", src.Routes[0].Job.Succeeded[0], "a clone's job block shares nothing with the source")
	assert.Equal(t, "Svc.Get", src.Routes[0].Job.Poll.Headers["X-Amz-Target"])
	assert.InDelta(t, 0.5, *src.Routes[0].Job.Usage.CostPerUnit, 0, "the job usage's pointer fields are copied too")
	dst.Routes[0].Usage.CostPath = "d"
	assert.Equal(t, "textract", src.Auth.AWS.Service)
	assert.InDelta(t, 1.0, *src.Usage.CostPerRequest, 0)
	assert.Equal(t, "/a", src.Streaming.Paths[0])
	assert.False(t, *src.Routes[0].Billable)
	assert.Equal(t, "c", src.Routes[0].Usage.CostPath)
}

func TestEndpointUsageConfigUnitsValidation(t *testing.T) {
	ok := func(u *RelayUsageConfig) {
		t.Helper()
		assert.NoError(t, (&RelayConfig{Usage: u}).Validate())
	}
	bad := func(u *RelayUsageConfig, msg string) {
		t.Helper()
		err := (&RelayConfig{Usage: u}).Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), msg)
	}
	ok(&RelayUsageConfig{UnitsPath: "DocumentMetadata.Pages", CostPerUnit: Ptr(0.0015)})
	ok(&RelayUsageConfig{UnitsPath: "Text", UnitsSource: RelayUnitsFromRequest, UnitsMeasure: RelayUnitsChars, CostPerUnit: Ptr(0.000015)})
	ok(&RelayUsageConfig{UnitsSource: RelayUnitsFromRequest, UnitsMeasure: RelayUnitsBodyBytes, CostPerUnit: Ptr(0.000016)})
	ok(&RelayUsageConfig{UnitsPath: "pages"})
	bad(&RelayUsageConfig{UnitsPath: "pages", CostPerUnit: Ptr(-1.0)}, "cost_per_unit must not be negative")
	bad(&RelayUsageConfig{UnitsPath: "pages", UnitsSource: "header", CostPerUnit: Ptr(1.0)}, "units_source")
	bad(&RelayUsageConfig{UnitsPath: "pages", UnitsMeasure: "words", CostPerUnit: Ptr(1.0)}, "units_measure")
	bad(&RelayUsageConfig{CostPerUnit: Ptr(1.0)}, "units_path is required")
	// A route inherits units_path and units_measure from the provider block, so a rate alone is
	// enough there; without a provider path it is the same error.
	rate := []RelayRouteConfig{{Method: "POST", Path: "/x", Usage: &RelayUsageConfig{CostPerUnit: Ptr(0.5)}}}
	require.NoError(t, (&RelayConfig{Usage: &RelayUsageConfig{UnitsPath: "pages"}, Routes: rate}).Validate())
	require.NoError(t, (&RelayConfig{Usage: &RelayUsageConfig{UnitsMeasure: RelayUnitsBodyBytes}, Routes: rate}).Validate())
	inherited := []RelayRouteConfig{{Method: "POST", Path: "/x", Usage: &RelayUsageConfig{UnitsMeasure: RelayUnitsChars}}}
	err := (&RelayConfig{Usage: &RelayUsageConfig{UnitsMeasure: RelayUnitsBodyBytes, CostPerUnit: Ptr(0.01)}, Routes: inherited}).Validate()
	require.Error(t, err, "a route switching to a measure that needs a path, under an inherited rate, is refused")
	assert.Contains(t, err.Error(), "routes[0].usage.units_path is required")
	err = (&RelayConfig{Routes: rate}).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "routes[0].usage.units_path is required")
	ok(&RelayUsageConfig{CostPerUnit: Ptr(0.0)})                                                                             // zero is "no unit pricing" and needs no path (a route waiving the provider's)
	ok(&RelayUsageConfig{UnitsSource: RelayUnitsFromRequest, UnitsMeasure: RelayUnitsBodyChars, CostPerUnit: Ptr(0.000016)}) // body_chars reads the whole body and needs no path
}

func TestEndpointScopedJobID(t *testing.T) {
	scoped := RelayScopedJobID("6f1c0b9e-2c11-4c55-9a0e-0d6a3c1f2e44", "arn:aws:textract:job/42")
	assert.Equal(t, "6f1c0b9e-2c11-4c55-9a0e-0d6a3c1f2e44:arn:aws:textract:job/42", scoped)
	assert.Equal(t, "arn:aws:textract:job/42", RelayUpstreamJobID(scoped), "an upstream id may itself contain ':'")
	assert.NotEqual(t, scoped, RelayScopedJobID("a8d2f7c4-0000-4000-8000-000000000001", "arn:aws:textract:job/42"), "two starts of one upstream id are two jobs")
	// The start id can be a caller's x-request-id, which may itself carry ':'.
	fromCaller := RelayScopedJobID("req:1", "arn:aws:textract:job/42")
	assert.Equal(t, "arn:aws:textract:job/42", RelayUpstreamJobID(fromCaller), "a ':' in the start id never leaks into the upstream id")
	assert.NotEqual(t, fromCaller, RelayScopedJobID("req", "1:arn:aws:textract:job/42"), "different starts stay different rows")
	assert.Equal(t, ":job-1", RelayScopedJobID("", "job-1"), "an empty scope still writes the separator")
	assert.Equal(t, "arn:aws:textract:job/42", RelayUpstreamJobID(RelayScopedJobID("", "arn:aws:textract:job/42")), "an unscoped ARN round-trips")
	assert.Equal(t, "job-1", RelayUpstreamJobID("job-1"))
}

func TestRelayValidateEndpoint(t *testing.T) {
	valid := func() *RelayConfig {
		return &RelayConfig{
			Name:       "tavily",
			BaseURL:    "https://search.example.com/v1",
			Credential: &RelayCredential{Value: NewSecretVar("sk")},
		}
	}
	require.NoError(t, valid().ValidateEndpoint())

	cases := map[string]func(c *RelayConfig){
		"name":               func(c *RelayConfig) { c.Name = "a/b" },
		"base_url relative":  func(c *RelayConfig) { c.BaseURL = "/v1" },
		"base_url scheme":    func(c *RelayConfig) { c.BaseURL = "ftp://x" },
		"base_url query":     func(c *RelayConfig) { c.BaseURL = "https://x/?a=1" },
		"base_url user info": func(c *RelayConfig) { c.BaseURL = "https://u:p@x/" },
		"missing credential": func(c *RelayConfig) { c.Credential = nil },
		"extra header case": func(c *RelayConfig) {
			c.ExtraHeaders = map[string]SecretVar{"X-T": *NewSecretVar("a"), "x-t": *NewSecretVar("b")}
		},
		"negative timeout": func(c *RelayConfig) { c.Network = &RelayNetworkConfig{TimeoutSeconds: -1} },
		"sigv4 region": func(c *RelayConfig) {
			c.Auth = RelayAuthConfig{Type: RelayAuthAWSSigV4, AWS: &RelayAWSAuthConfig{Service: "textract"}}
			c.Credential = &RelayCredential{AWS: &RelayAWSCredential{}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := valid()
			mutate(c)
			assert.Error(t, c.ValidateEndpoint())
		})
	}

	none := valid()
	none.Auth.Type, none.Credential = RelayAuthNone, nil
	assert.NoError(t, none.ValidateEndpoint(), "auth none needs no credential")
}

func TestRelayRedactAndMerge(t *testing.T) {
	stored := &RelayConfig{
		Name:         "tavily",
		BaseURL:      "https://search.example.com",
		Credential:   &RelayCredential{Value: NewSecretVar("sk-live-123456789")},
		ExtraHeaders: map[string]SecretVar{"X-Tenant": *NewSecretVar("acme"), "X-Functions-Key": *NewSecretVar("fn-key-literal"), "Subscription-Key": *NewSecretVar("sub-key-literal")},
		ProxyConfig: &ProxyConfig{
			Type:      "http",
			URL:       NewSecretVar("http://user:pw-in-url@proxy:8080"),
			Username:  NewSecretVar("proxy-user-name"),
			Password:  NewSecretVar("hunter2-long-secret"),
			CACertPEM: NewSecretVar("-----BEGIN CERTIFICATE-----abc"),
		},
	}
	redacted := stored.Redacted()
	tenant, functionsKey, subscriptionKey := redacted.ExtraHeaders["X-Tenant"], redacted.ExtraHeaders["X-Functions-Key"], redacted.ExtraHeaders["Subscription-Key"]
	assert.Equal(t, "acme", tenant.GetValue(), "a plain extra header stays readable")
	assert.True(t, functionsKey.IsRedacted(), "a credential header IsSensitiveHeader does not name is still masked")
	assert.True(t, subscriptionKey.IsRedacted())
	assert.NotContains(t, redacted.ProxyConfig.URL.GetValue(), "pw-in-url", "a proxy URL can embed credentials")
	assert.Equal(t, "<REDACTED>", redacted.ProxyConfig.Password.GetValue())
	assert.True(t, redacted.Credential.Value.IsRedacted())
	assert.NotEqual(t, "sk-live-123456789", redacted.Credential.Value.GetValue())
	assert.Equal(t, "sk-live-123456789", stored.Credential.Value.GetValue(), "Redacted leaves the original alone")

	// A UI that only saw the redacted form sends it back unchanged: the stored secrets are kept.
	update := redacted.Clone()
	update.Description = "edited"
	update.MergeRedacted(stored)
	assert.Equal(t, "sk-live-123456789", update.Credential.Value.GetValue())
	assert.Equal(t, "hunter2-long-secret", update.ProxyConfig.Password.GetValue())
	assert.Equal(t, "http://user:pw-in-url@proxy:8080", update.ProxyConfig.URL.GetValue())
	assert.Equal(t, "proxy-user-name", update.ProxyConfig.Username.GetValue())
	assert.Equal(t, "-----BEGIN CERTIFICATE-----abc", update.ProxyConfig.CACertPEM.GetValue())

	// A new value replaces the stored one.
	update.Credential.Value = NewSecretVar("sk-new")
	update.MergeRedacted(stored)
	assert.Equal(t, "sk-new", update.Credential.Value.GetValue())
}

// A clone owns its secrets: editing the original after Upsert must not change what a live relay
// built from the clone sends.
func TestRelayCloneDoesNotShareSecrets(t *testing.T) {
	original := &RelayConfig{
		Name: "tavily", BaseURL: "https://api.tavily.com",
		Credential:   &RelayCredential{Value: NewSecretVar("sk-one"), AWS: &RelayAWSCredential{AccessKey: NewSecretVar("AK1"), Region: NewSecretVar("us-east-1")}},
		ExtraHeaders: map[string]SecretVar{"X-Tenant": *NewSecretVar("acme")},
		ProxyConfig:  &ProxyConfig{Type: "http", URL: NewSecretVar("http://proxy:8080"), Password: NewSecretVar("pw")},
	}
	clone := original.Clone()
	original.Credential.Value = NewSecretVar("sk-two")
	original.Credential.AWS.AccessKey.Val = "AK2"
	original.ProxyConfig.Password.Val = "pw2"
	header := original.ExtraHeaders["X-Tenant"]
	header.Val = "other"
	original.ExtraHeaders["X-Tenant"] = header
	assert.Equal(t, "sk-one", clone.Credential.Value.GetValue())
	assert.Equal(t, "AK1", clone.Credential.AWS.AccessKey.GetValue())
	assert.Equal(t, "pw", clone.ProxyConfig.Password.GetValue())
	tenant := clone.ExtraHeaders["X-Tenant"]
	assert.Equal(t, "acme", tenant.GetValue())
}

func TestRelayRouteJobRoundTrips(t *testing.T) {
	var cfg RelayConfig
	require.NoError(t, Unmarshal([]byte(`{"name":"docs","base_url":"https://docs.example.com","routes":[{"method":"POST","path":"/","job":{"id_path":"JobId","poll":{"path":"/jobs/{{job.id}}","method":"GET"},"status_path":"s","succeeded":["x"],"interval_seconds":10}}]}`), &cfg))
	require.Len(t, cfg.Routes, 1)
	require.NotNil(t, cfg.Routes[0].Job, "a job in config.json or an API body is read")
	assert.Equal(t, "/jobs/{{job.id}}", cfg.Routes[0].Job.Poll.Path)
	assert.Equal(t, 10, cfg.Routes[0].Job.IntervalSeconds)
	out, err := Marshal(cfg.Routes[0])
	require.NoError(t, err)
	assert.Contains(t, string(out), `"job":{"id_path":"JobId"`)
}

func TestRelayJobValidate(t *testing.T) {
	base := func() *RelayJobConfig {
		return &RelayJobConfig{IDPath: "JobId", StatusPath: "JobStatus", Succeeded: []string{"SUCCEEDED"}, Failed: []string{"FAILED"},
			Poll: RelayJobPoll{Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"}, Body: `{"JobId":"{{job.id}}"}`}}
	}
	for name, tc := range map[string]struct {
		edit    func(j *RelayJobConfig)
		wantErr string
	}{
		"valid":                             {func(j *RelayJobConfig) {}, ""},
		"timeout of a week":                 {func(j *RelayJobConfig) { j.TimeoutSeconds = 7 * 24 * 3600 }, ""},
		"timeout over a week":               {func(j *RelayJobConfig) { j.TimeoutSeconds = 7*24*3600 + 1 }, "timeout_seconds"},
		"interval over the timeout":         {func(j *RelayJobConfig) { j.IntervalSeconds, j.TimeoutSeconds = 600, 300 }, "interval_seconds"},
		"credential header in the poll":     {func(j *RelayJobConfig) { j.Poll.Headers["Authorization"] = "Bearer x" }, "poll.headers"},
		"functions key header in the poll":  {func(j *RelayJobConfig) { j.Poll.Headers["X-Functions-Key"] = "k" }, "poll.headers"},
		"api key header in the poll":        {func(j *RelayJobConfig) { j.Poll.Headers["X-Api-Key"] = "k" }, "poll.headers"},
		"status both succeeded and failed":  {func(j *RelayJobConfig) { j.Failed = append(j.Failed, "succeeded") }, "both succeeded and failed"},
		"interval over the default timeout": {func(j *RelayJobConfig) { j.IntervalSeconds = 7200 }, "interval_seconds"},
		"default interval past the timeout": {func(j *RelayJobConfig) { j.IntervalSeconds, j.TimeoutSeconds = 0, 10 }, "30s, the default"},
		"interval equal to the timeout":     {func(j *RelayJobConfig) { j.IntervalSeconds, j.TimeoutSeconds = 30, 30 }, "must be shorter"},
		"interval just under the timeout":   {func(j *RelayJobConfig) { j.IntervalSeconds, j.TimeoutSeconds = 29, 30 }, ""},
		"both left to the defaults":         {func(j *RelayJobConfig) { j.IntervalSeconds, j.TimeoutSeconds = 0, 0 }, ""},
		"blank succeeded status":            {func(j *RelayJobConfig) { j.Succeeded = []string{""} }, "non-empty"},
		"padded failed status":              {func(j *RelayJobConfig) { j.Failed = []string{" FAILED "} }, "non-empty"},
		"poll that names no job":            {func(j *RelayJobConfig) { j.Poll.Body = `{"MaxResults":1}` }, "{{job.id}}"},
		"id only in a content type the body replaces": {func(j *RelayJobConfig) {
			j.Poll.Body = `{}`
			j.Poll.Headers["Content-Type"] = "application/x-job-{{job.id}}"
		}, "{{job.id}}"},
		"header set under two spellings": {func(j *RelayJobConfig) {
			j.Poll.Headers["x-amz-target"] = "Textract.Other"
		}, "same header"},
		"host header in the poll": {func(j *RelayJobConfig) {
			j.Poll.Body = ""
			j.Poll.Path = "/jobs"
			j.Poll.Headers["Host"] = "{{job.id}}"
		}, "never sent"},
		"id in the content type of a bodyless poll": {func(j *RelayJobConfig) {
			j.Poll.Body = ""
			j.Poll.Headers["Content-Type"] = "application/x-job-{{job.id}}"
		}, ""},
		"job id in a header":           {func(j *RelayJobConfig) { j.Poll.Body = ""; j.Poll.Headers["X-Job"] = "{{job.id}}" }, ""},
		"credential in the poll query": {func(j *RelayJobConfig) { j.Poll.Path = "/jobs/{{job.id}}?api_key=k" }, "poll.path"},
		"plain query in the poll":      {func(j *RelayJobConfig) { j.Poll.Path = "/jobs?id={{job.id}}&verbose=1" }, ""},
		"azure key header in the poll": {func(j *RelayJobConfig) { j.Poll.Headers["Ocp-Apim-Subscription-Key"] = "k" }, "poll.headers"},
	} {
		t.Run(name, func(t *testing.T) {
			job := base()
			tc.edit(job)
			cfg := &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: job}}}
			err := cfg.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestRelayMergeRedactedKeepsReferenceChanges(t *testing.T) {
	stored := &RelayConfig{
		Credential:   &RelayCredential{Value: NewSecretVar("sk-live-123456789")},
		ExtraHeaders: map[string]SecretVar{"X-Tenant": *NewSecretVar("acme-tenant-secret")},
	}
	update := &RelayConfig{
		Credential:   &RelayCredential{Value: NewSecretVar("env.NEW_API_KEY")},
		ExtraHeaders: map[string]SecretVar{"X-Tenant": *NewSecretVar("env.TENANT")},
	}
	require.True(t, update.Credential.Value.IsFromSecret())
	update.MergeRedacted(stored)
	assert.True(t, update.Credential.Value.IsFromSecret(), "switching to an env reference sticks")
	assert.NotEqual(t, "sk-live-123456789", update.Credential.Value.GetValue())
	tenant := update.ExtraHeaders["X-Tenant"]
	assert.True(t, tenant.IsFromSecret(), "a header switched to an env reference sticks")

	// The masked form the UI read back still keeps the stored secret.
	masked := stored.Redacted()
	masked.MergeRedacted(stored)
	assert.Equal(t, "sk-live-123456789", masked.Credential.Value.GetValue())
}

func TestRelayRedactedMasksLiteralCredentialHeaders(t *testing.T) {
	cfg := &RelayConfig{ExtraHeaders: map[string]SecretVar{
		"X-Api-Key": *NewSecretVar("literal-upstream-key-123"),
		"X-Tenant":  *NewSecretVar("acme"),
	}}
	out := cfg.Redacted()
	key := out.ExtraHeaders["X-Api-Key"]
	assert.NotEqual(t, "literal-upstream-key-123", key.GetValue(), "a literal credential header is redacted")
	tenant := out.ExtraHeaders["X-Tenant"]
	assert.Equal(t, "acme", tenant.GetValue(), "a plain header stays readable")
}

// The config and Helm schemas must refuse what RelayBodyOverridePath refuses, or a key passes one and fails the other.
func TestRelayBodyOverrideSchemasMatchValidator(t *testing.T) {
	cases := map[string]bool{
		strings.Repeat("a", 128) + ".depth": true,
		strings.Repeat("a", 129) + ".depth": false,
		"options.-1":                        false,
		"-1.options":                        false,
		"options.-1.depth":                  false,
		"options.-2":                        true,
		"options.a-1":                       true,
	}
	for key, want := range cases {
		_, err := RelayBodyOverridePath(key)
		assert.Equal(t, want, err == nil, key)
	}

	for _, file := range []string{"../../transports/config.schema.json", "../../helm-charts/bifrost/values.schema.json"} {
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		var doc any
		require.NoError(t, json.Unmarshal(raw, &doc))
		rules := bodyOverrideNameRules(doc)
		require.NotEmpty(t, rules, file)
		for _, rule := range rules {
			for key, want := range cases {
				assert.Equal(t, want, rule.accepts(key), "%s: %s", file, key)
			}
		}
	}
}

// bodyOverrideNameRule is one propertyNames allOf: patterns a key must match, and patterns it must not.
type bodyOverrideNameRule struct {
	match, not []*regexp.Regexp
}

func (r bodyOverrideNameRule) accepts(key string) bool {
	for _, p := range r.match {
		if !p.MatchString(key) {
			return false
		}
	}
	for _, p := range r.not {
		if p.MatchString(key) {
			return false
		}
	}
	return true
}

// bodyOverrideNameRules returns each propertyNames allOf that carries the 128-character limit.
func bodyOverrideNameRules(node any) []bodyOverrideNameRule {
	var found []bodyOverrideNameRule
	switch v := node.(type) {
	case []any:
		for _, item := range v {
			found = append(found, bodyOverrideNameRules(item)...)
		}
	case map[string]any:
		if names, ok := v["propertyNames"].(map[string]any); ok {
			if all, ok := names["allOf"].([]any); ok {
				var rule bodyOverrideNameRule
				limited := false
				for _, item := range all {
					entry, ok := item.(map[string]any)
					if !ok {
						continue
					}
					if p, ok := entry["pattern"].(string); ok {
						rule.match = append(rule.match, regexp.MustCompile(p))
						limited = limited || strings.Contains(p, "{1,128}")
					}
					if not, ok := entry["not"].(map[string]any); ok {
						if p, ok := not["pattern"].(string); ok {
							rule.not = append(rule.not, regexp.MustCompile(p))
						}
					}
				}
				if limited {
					found = append(found, rule)
				}
			}
		}
		for _, item := range v {
			found = append(found, bodyOverrideNameRules(item)...)
		}
	}
	return found
}

// A poll header needs a value: a blank one would make the job's status route match every call.
func TestRelayJobRefusesABlankPollHeader(t *testing.T) {
	job := &RelayJobConfig{IDPath: "id", StatusPath: "status", Succeeded: []string{"done"},
		Poll: RelayJobPoll{Path: "/jobs/{{job.id}}", Headers: map[string]string{"X-Op": " "}}}
	require.ErrorContains(t, job.validate("job", RelayRouteConfig{Method: "POST", Path: "/jobs"}, nil, RelayAuthConfig{Type: RelayAuthBearer}), `"X-Op" has no value`)
}

// A poll query refuses credential names, but not generic words like code that status APIs use.
func TestRelayJobPollQueryCredentials(t *testing.T) {
	route := RelayRouteConfig{Method: "POST", Path: "/jobs"}
	poll := func(query string) *RelayJobConfig {
		return &RelayJobConfig{IDPath: "id", StatusPath: "status", Succeeded: []string{"done"},
			Poll: RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}?" + query}}
	}
	for _, q := range []string{"api_key=x", "refresh_token=x", "pwd=x", "X-Amz-Signature=x"} {
		require.ErrorContains(t, poll(q).validate("job", route, nil, RelayAuthConfig{Type: RelayAuthBearer}), "carries a credential", q)
	}
	for _, q := range []string{"code=pending", "auth=none", "view=full"} {
		require.NoError(t, poll(q).validate("job", route, nil, RelayAuthConfig{Type: RelayAuthBearer}), q)
	}
}

// A relay can name its own key header, which no built-in credential list knows.
func TestRelayJobRefusesTheRelaysOwnHeaderCredential(t *testing.T) {
	job := &RelayJobConfig{IDPath: "id", StatusPath: "status", Succeeded: []string{"done"},
		Poll: RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}", Headers: map[string]string{"x-tenant-code": "live-key"}}}
	route := RelayRouteConfig{Method: "POST", Path: "/jobs"}
	require.ErrorContains(t, job.validate("job", route, nil, RelayAuthConfig{Type: RelayAuthHeader, HeaderName: "X-Tenant-Code"}), "carries a credential")
	require.NoError(t, job.validate("job", route, nil, RelayAuthConfig{Type: RelayAuthBearer}), "only the relay's own header name is refused")
}

// A relay can name its own key parameter, which no built-in credential list knows.
func TestRelayJobRefusesTheRelaysOwnQueryCredential(t *testing.T) {
	job := &RelayJobConfig{IDPath: "id", StatusPath: "status", Succeeded: []string{"done"},
		Poll: RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}?TenantCode=live-key"}}
	route := RelayRouteConfig{Method: "POST", Path: "/jobs"}

	err := job.validate("job", route, nil, RelayAuthConfig{Type: RelayAuthQuery, QueryParam: "tenantCode"})
	require.ErrorContains(t, err, "carries a credential")

	require.NoError(t, job.validate("job", route, nil, RelayAuthConfig{Type: RelayAuthBearer}), "the name is only a credential on a query-auth relay")
}

// A SigV4 poll keeps its own x-amz-* headers; only the ones the signer sets itself are refused.
func TestRelayJobSigV4PollHeaders(t *testing.T) {
	route := RelayRouteConfig{Method: "POST", Path: "/"}
	sigv4 := RelayAuthConfig{Type: RelayAuthAWSSigV4}
	poll := func(name string) *RelayJobConfig {
		return &RelayJobConfig{IDPath: "JobId", StatusPath: "JobStatus", Succeeded: []string{"SUCCEEDED"},
			Poll: RelayJobPoll{Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis", name: "v"}, Body: `{"JobId":"{{job.id}}"}`}}
	}
	require.NoError(t, poll("X-Amz-User-Agent").validate("job", route, nil, sigv4), "the poll sends it")
	require.ErrorContains(t, poll("X-Amz-Date").validate("job", route, nil, sigv4), "never sent")
	require.ErrorContains(t, poll("X-Amz-Security-Token").validate("job", route, nil, sigv4), "carries a credential")
	require.NoError(t, poll("X-Amz-Date").validate("job", route, nil, RelayAuthConfig{Type: RelayAuthBearer}), "only SigV4 owns it")
}

// The poll fills {{job.id}} into a JSON key too, which a status check could never match, so a save
// refuses it; in a value it is fine.
func TestRelayJobPollPlaceholderInAJSONKey(t *testing.T) {
	route := RelayRouteConfig{Method: "POST", Path: "/"}
	job := func(body string) *RelayJobConfig {
		return &RelayJobConfig{IDPath: "JobId", StatusPath: "JobStatus", Succeeded: []string{"SUCCEEDED"}, Poll: RelayJobPoll{Path: "/", Body: body}}
	}
	require.ErrorContains(t, job(`{"Id":"{{job.id}}","job_{{job.id}}":true}`).validate("job", route, nil, RelayAuthConfig{}), "cannot be part of a JSON key")
	require.ErrorContains(t, job(`{"Jobs":[{"{{job.id}}":1}]}`).validate("job", route, nil, RelayAuthConfig{}), "cannot be part of a JSON key")
	require.NoError(t, job(`{"Id":"{{job.id}}","Label":"job {{job.id}}"}`).validate("job", route, nil, RelayAuthConfig{}))
}
