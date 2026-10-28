package schemas

import (
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
		{"job needs id path", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: &RelayJobConfig{Poll: RelayJobPoll{Path: "/"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "routes[0].job.id_path"},
		{"job needs a succeeded status", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/"}, StatusPath: "s"}}}}, "succeeded"},
		{"job poll path rooted", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "jobs"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "poll.path"},
		{"job measure needing a path under an inherited rate", &RelayConfig{Usage: &RelayUsageConfig{UnitsMeasure: RelayUnitsBodyBytes, CostPerUnit: Ptr(0.01)}, Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/"}, StatusPath: "s", Succeeded: []string{"x"}, Usage: &RelayUsageConfig{UnitsMeasure: RelayUnitsChars}}}}}, "routes[0].job.usage.units_path is required"},
		{"job cannot bill request units", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Usage: &RelayUsageConfig{UnitsPath: "Text", UnitsSource: RelayUnitsFromRequest, UnitsMeasure: RelayUnitsChars, CostPerUnit: Ptr(0.001)}, Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "request-side units are not billed at settlement"},
		{"job route cannot stream", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Stream: Ptr(true), Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "served buffered"},
		{"job route must be billable", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Billable: Ptr(false), Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "must be billable"},
		{"job on a PUT needs billable true", &RelayConfig{Routes: []RelayRouteConfig{{Method: "PUT", Path: "/", Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/"}, StatusPath: "s", Succeeded: []string{"x"}}}}}, "must set billable: true"},
		{"job usage validated", &RelayConfig{Routes: []RelayRouteConfig{{Method: "POST", Path: "/", Job: &RelayJobConfig{IDPath: "id", Poll: RelayJobPoll{Path: "/"}, StatusPath: "s", Succeeded: []string{"x"}, Usage: &RelayUsageConfig{CostPerUnit: Ptr(1.0)}}}}}, "routes[0].job.usage.units_path"},
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

func TestRelayRouteJobIsNotConfigurable(t *testing.T) {
	var cfg RelayConfig
	require.NoError(t, Unmarshal([]byte(`{"name":"docs","base_url":"https://docs.example.com","routes":[{"method":"POST","path":"/","job":{"id_path":"JobId","poll":{"path":"/"},"status_path":"s","succeeded":["x"]}}]}`), &cfg))
	require.Len(t, cfg.Routes, 1)
	assert.Nil(t, cfg.Routes[0].Job, "a job in config.json or an API body is ignored until async jobs are exposed")
	out, err := Marshal(RelayRouteConfig{Method: "POST", Path: "/", Job: &RelayJobConfig{IDPath: "JobId"}})
	require.NoError(t, err)
	assert.NotContains(t, string(out), "job")
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
