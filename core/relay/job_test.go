package relay

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The job id comes from the upstream's reply and lands in the poll path, so an id that could leave
// the configured path, or overflow the job row, is not recorded as a job.
func TestJobHandleRefusesUnsafeIDs(t *testing.T) {
	route := &compiledRoute{job: &schemas.RelayJobConfig{IDPath: "JobId"}, jobParams: "{}"}
	handle := func(id string) (*schemas.RelayJobHandle, error) {
		body, _ := schemas.MarshalSorted(map[string]string{"JobId": id})
		return route.jobHandle("application/json", body)
	}
	for _, id := range []string{
		"8f14e45fceea167a5a36dedd4bea2543",
		"arn:aws:bedrock:us-east-1:123456789012:async-invoke/abc123",
		"job_01-ABC.v2",
	} {
		h, err := handle(id)
		require.NoError(t, err, id)
		require.NotNil(t, h, id)
		assert.Equal(t, id, h.ID)
	}
	for name, id := range map[string]string{
		"parent segment":    "../../admin/keys",
		"current segment":   "a/./b",
		"trailing parent":   "a/..",
		"backslash":         `a\..\b`,
		"control character": "a\r\nX-Injected: 1",
		"space":             "a b",
		"query":             "a?b=1",
		"fragment":          "a#b",
		"non-ASCII":         "jöb",
		"too long":          strings.Repeat("a", maxRelayJobIDLength+1),
	} {
		h, err := handle(id)
		assert.Error(t, err, name)
		assert.Nil(t, h, name)
	}
	h, err := handle("")
	assert.NoError(t, err, "a reply naming no id is an ordinary call")
	assert.Nil(t, h)
}

// The job id is escaped for where it lands, so an upstream-chosen id cannot add a parameter to a
// form body or markup to an XML one.
func TestJobPollRequestEscapesTheIDPerBodyType(t *testing.T) {
	snap := func(contentType, body string) *JobSnapshot {
		return &JobSnapshot{Job: schemas.RelayJobConfig{Poll: schemas.RelayJobPoll{Path: "/jobs", Body: body, ContentType: contentType}}}
	}
	id := `x&Action=Delete"<a>`
	form := snap("application/x-www-form-urlencoded", "Action=Get&JobId={{job.id}}").PollRequest(id)
	assert.Equal(t, "Action=Get&JobId=x%26Action%3DDelete%22%3Ca%3E", string(form.Body))
	xmlReq := snap("text/xml; charset=utf-8", "<Get><Id>{{job.id}}</Id></Get>").PollRequest(id)
	assert.Equal(t, "<Get><Id>x&amp;Action=Delete&#34;&lt;a&gt;</Id></Get>", string(xmlReq.Body))
	jsonReq := snap("application/x-amz-json-1.1", `{"JobId":"{{job.id}}"}`).PollRequest(id)
	assert.Equal(t, `{"JobId":"x&Action=Delete\"<a>"}`, string(jsonReq.Body))
}

// A job priced by a cost field or tokens whose terminal reply lacks them is unpriced, not a $0 success.
func TestJobPriceNeedsEveryConfiguredMeasure(t *testing.T) {
	for name, tc := range map[string]struct {
		usage  schemas.RelayUsageConfig
		body   string
		priced bool
	}{
		"cost present":   {schemas.RelayUsageConfig{CostPath: "usage.cost"}, `{"usage":{"cost":0.4}}`, true},
		"cost missing":   {schemas.RelayUsageConfig{CostPath: "usage.cost"}, `{"status":"DONE"}`, false},
		"tokens present": {schemas.RelayUsageConfig{TotalTokensPath: "usage.total"}, `{"usage":{"total":10}}`, true},
		"tokens missing": {schemas.RelayUsageConfig{TotalTokensPath: "usage.total"}, `{}`, false},
		"units missing":  {schemas.RelayUsageConfig{UnitsPath: "pages", CostPerUnit: schemas.Ptr(0.05)}, `{}`, false},
		"flat fee only":  {schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.01)}, `{}`, true},
		// Token paths inherited from the relay do not unprice a job that charges per page.
		"units with inherited tokens": {schemas.RelayUsageConfig{InputTokensPath: "usage.input", UnitsPath: "pages", CostPerUnit: schemas.Ptr(0.05)}, `{"pages":10}`, true},
		"cost with inherited tokens":  {schemas.RelayUsageConfig{TotalTokensPath: "usage.total", CostPath: "usage.cost"}, `{"usage":{"cost":0.4}}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			usage := tc.usage
			_, priced := (&JobSnapshot{Usage: &usage}).Price("application/json", []byte(tc.body))
			assert.Equal(t, tc.priced, priced)
		})
	}
}

// reused_id_is_new_job rides on the handle, so the logging plugin knows whether a settled id may
// open a new job.
func TestJobHandleCarriesIDReusePolicy(t *testing.T) {
	route := &compiledRoute{job: &schemas.RelayJobConfig{IDPath: "id", ReusedIDIsNewJob: true}, jobParams: "{}"}
	h, err := route.jobHandle("application/json", []byte(`{"id":"j-1"}`))
	require.NoError(t, err)
	assert.True(t, h.ReusedIDIsNewJob)
}

// A caller's status check is recognized by reversing the poll template, so its reply can settle the job.
func TestStatusCheckJobIDReversesThePollTemplate(t *testing.T) {
	req := func(method, path string, headers map[string]string, body string) *schemas.BifrostRelayRequest {
		return &schemas.BifrostRelayRequest{Method: method, Path: path, Headers: headers, Body: []byte(body)}
	}
	rest := schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}/status"}
	assert.Equal(t, "acme/2026/j1", matchStatusCheck(rest, req("GET", "/jobs/acme/2026/j1/status", nil, "")), "an id with slashes")
	assert.Empty(t, matchStatusCheck(rest, req("POST", "/jobs/j1/status", nil, "")), "another method")
	assert.Empty(t, matchStatusCheck(rest, req("GET", "/jobs//status", nil, "")), "no id")
	assert.Empty(t, matchStatusCheck(rest, req("GET", "/jobs/../admin/status", nil, "")), "an id the relay would never follow")

	textract := schemas.RelayJobPoll{Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"},
		Body: `{"JobId":"{{job.id}}"}`, ContentType: "application/x-amz-json-1.1"}
	target := map[string]string{"x-amz-target": "Textract.GetDocumentAnalysis"}
	assert.Equal(t, "j-1", matchStatusCheck(textract, req("POST", "/", target, `{"MaxResults":10,"JobId":"j-1"}`)), "the id is read from the body field, whatever else the caller sends")
	assert.Empty(t, matchStatusCheck(textract, req("POST", "/", map[string]string{"x-amz-target": "Textract.StartDocumentAnalysis"}, `{"JobId":"j-1"}`)), "another operation")
	assert.Empty(t, matchStatusCheck(textract, req("POST", "/", target, `{"JobId":7}`)), "an id that is not a string")

	header := schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}", Headers: map[string]string{"X-Job": "id-{{job.id}}"}}
	assert.Equal(t, "j1", matchStatusCheck(header, req("GET", "/jobs/j1", map[string]string{"x-job": "id-j1"}, "")))
	assert.Empty(t, matchStatusCheck(header, req("GET", "/jobs/j1", map[string]string{"x-job": "id-j2"}, "")), "the path and a header must name the same job")

	form := schemas.RelayJobPoll{Path: "/status", Body: "Action=Get&Id={{job.id}}", ContentType: "application/x-www-form-urlencoded"}
	assert.Equal(t, "a/b", matchStatusCheck(form, req("POST", "/status", nil, "Id=a%2Fb&Action=Get")), "a form value, decoded")
}
