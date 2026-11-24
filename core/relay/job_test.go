package relay

import (
	"context"
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
		"dot segment with params":       "..;x/admin",
		"inner dot segment with params": "a/..;/b",
		"current segment with params":   ".;v=1",
		"parent segment":                "../../admin/keys",
		"current segment":               "a/./b",
		"trailing parent":               "a/..",
		"backslash":                     `a\..\b`,
		"control character":             "a\r\nX-Injected: 1",
		"space":                         "a b",
		"query":                         "a?b=1",
		"fragment":                      "a#b",
		"non-ASCII":                     "jöb",
		"too long":                      strings.Repeat("a", maxRelayJobIDLength+1),
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
		"fee with inherited tokens":   {schemas.RelayUsageConfig{TotalTokensPath: "usage.total", CostPerRequest: schemas.Ptr(0.1)}, `{}`, true},
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
		target, query, _ := strings.Cut(path, "?")
		return &schemas.BifrostRelayRequest{Method: method, Path: target, RawQuery: query, Headers: headers, Body: []byte(body)}
	}
	matchStatusCheck := func(poll schemas.RelayJobPoll, operator map[string]string, r *schemas.BifrostRelayRequest) string {
		return matchStatusCheck(poll, r, operator)
	}
	rest := schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}/status"}
	assert.Equal(t, "acme/2026/j1", matchStatusCheck(rest, nil, req("GET", "/jobs/acme/2026/j1/status", nil, "")), "an id with slashes")
	assert.Empty(t, matchStatusCheck(rest, nil, req("POST", "/jobs/j1/status", nil, "")), "another method")
	assert.Empty(t, matchStatusCheck(rest, nil, req("GET", "/jobs//status", nil, "")), "no id")
	assert.Empty(t, matchStatusCheck(rest, nil, req("GET", "/jobs/../admin/status", nil, "")), "an id the relay would never follow")

	textract := schemas.RelayJobPoll{Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"},
		Body: `{"JobId":"{{job.id}}"}`, ContentType: "application/x-amz-json-1.1"}
	target := map[string]string{"x-amz-target": "Textract.GetDocumentAnalysis"}
	assert.Equal(t, "j-1", matchStatusCheck(textract, nil, req("POST", "/", target, `{"MaxResults":10,"JobId":"j-1"}`)), "the id is read from the body field, whatever else the caller sends")
	assert.Empty(t, matchStatusCheck(textract, nil, req("POST", "/", map[string]string{"x-amz-target": "Textract.StartDocumentAnalysis"}, `{"JobId":"j-1"}`)), "another operation")
	assert.Empty(t, matchStatusCheck(textract, nil, req("POST", "/", target, `{"JobId":7}`)), "an id that is not a string")

	header := schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}", Headers: map[string]string{"X-Job": "id-{{job.id}}"}}
	assert.Equal(t, "j1", matchStatusCheck(header, nil, req("GET", "/jobs/j1", map[string]string{"x-job": "id-j1"}, "")))
	assert.Empty(t, matchStatusCheck(header, nil, req("GET", "/jobs/j1", map[string]string{"x-job": "id-j2"}, "")), "the path and a header must name the same job")

	formType := map[string]string{"content-type": "application/x-www-form-urlencoded"}
	form := schemas.RelayJobPoll{Path: "/status", Body: "Action=Get&Id={{job.id}}", ContentType: "application/x-www-form-urlencoded"}
	assert.Equal(t, "a/b", matchStatusCheck(form, nil, req("POST", "/status", formType, "Id=a%2Fb&Action=Get")), "a form value, decoded")

	// Fixed parts that pick the operation must match too, or another operation's reply could settle the job.
	assert.Empty(t, matchStatusCheck(form, nil, req("POST", "/status", formType, "Action=Other&Id=j1")), "another form operation")
	assert.Equal(t, "j-1", matchStatusCheck(textract, nil, req("POST", "/", target, `{"JobId":"j-1","Action":"x"}`)), "an extra caller field is fine")
	opJSON := schemas.RelayJobPoll{Path: "/", Body: `{"Action":"Get","Id":"{{job.id}}","MaxResults":1}`}
	assert.Equal(t, "j1", matchStatusCheck(opJSON, nil, req("POST", "/", nil, `{"Action":"Get","Id":"j1","MaxResults":50}`)), "a caller's own page size")
	paged := schemas.RelayJobPoll{Path: "/", Body: `{"Id":"{{job.id}}","MaxResults":1}`}
	assert.Equal(t, "j1", matchStatusCheck(paged, nil, req("POST", "/", nil, `{"Id":"j1","MaxResults":50}`)), "another page size")
	assert.Equal(t, "j1", matchStatusCheck(paged, nil, req("POST", "/", nil, `{"Id":"j1"}`)), "no page size")
	assert.Empty(t, matchStatusCheck(paged, nil, req("POST", "/", nil, `{"Id":"j1","MaxResults":"all"}`)), "a page size that is not a number")
	formPaged := schemas.RelayJobPoll{Path: "/status", Body: "Id={{job.id}}&Limit=1", ContentType: "application/x-www-form-urlencoded"}
	assert.Equal(t, "j1", matchStatusCheck(formPaged, nil, req("POST", "/status", formType, "Id=j1&Limit=50")), "another form page size")
	assert.Empty(t, matchStatusCheck(formPaged, nil, req("POST", "/status", formType, "Id=j1&Limit=all")), "a form page size that is not a number")
	assert.Empty(t, matchStatusCheck(formPaged, nil, req("POST", "/status", formType, "Id=j1&Limit=NaN")), "NaN is no page size")
	assert.Empty(t, matchStatusCheck(formPaged, nil, req("POST", "/status", formType, "Id=j1&Limit=Inf")), "nor is infinity")
	// A {{job.id}} spelled with escapes is text the poll sends as it is, never a second placeholder.
	escaped := schemas.RelayJobPoll{Path: "/", Body: `{"Id":"{{job.id}}","Label":"\u007b\u007bjob.id\u007d\u007d"}`}
	rendered := (&JobSnapshot{Job: schemas.RelayJobConfig{Poll: escaped}}).PollRequest("j1")
	assert.Equal(t, "j1", matchStatusCheck(escaped, nil, req("POST", "/", nil, string(rendered.Body))), "the job's own poll matches")
	assert.Empty(t, matchStatusCheck(escaped, nil, req("POST", "/", nil, `{"Id":"j1","Label":"j1"}`)), "the fixed label cannot be changed")
	// Any other number names the operation: a caller cannot swap "Action": 1 (status) for 3 (re-run).
	action := schemas.RelayJobPoll{Path: "/", Body: `{"Id":"{{job.id}}","Action":1,"Paging":{"page_size":10}}`}
	assert.Equal(t, "j1", matchStatusCheck(action, nil, req("POST", "/", nil, `{"Id":"j1","Action":1.0,"Paging":{"page_size":50}}`)), "the same action, and a nested page size of its own")
	assert.Empty(t, matchStatusCheck(action, nil, req("POST", "/", nil, `{"Id":"j1","Action":3,"Paging":{"page_size":10}}`)), "another action")
	assert.Empty(t, matchStatusCheck(action, nil, req("POST", "/", nil, `{"Id":"j1","Paging":{"page_size":10}}`)), "no action")
	assert.Empty(t, matchStatusCheck(action, nil, req("POST", "/", nil, `{"Id":"j1","Action":"1","Paging":{"page_size":10}}`)), "a string in its place")
	exact := schemas.RelayJobPoll{Path: "/", Body: `{"Id":"{{job.id}}","Seq":9007199254740993}`}
	assert.Empty(t, matchStatusCheck(exact, nil, req("POST", "/", nil, `{"Id":"j1","Seq":9007199254740992}`)), "exact past float precision")
	assert.Empty(t, matchStatusCheck(action, nil, req("POST", "/", nil, `{"Id":"j1","Action":1.000000000000000000001,"Paging":{"page_size":10}}`)), "exact past 64-bit precision")
	assert.Empty(t, matchStatusCheck(action, nil, req("POST", "/", nil, `{"Id":"j1","Action":1e999999999,"Paging":{"page_size":10}}`)), "a huge exponent is never expanded")
	assert.Empty(t, matchStatusCheck(action, nil, req("POST", "/", nil, `{"Id":"j1","Action":1,"Paging":{"page_size":10},"":"x"}`)), "an empty key in the caller's body")
	formAction := schemas.RelayJobPoll{Path: "/status", Body: "Id={{job.id}}&Action=1", ContentType: "application/x-www-form-urlencoded"}
	assert.Empty(t, matchStatusCheck(formAction, nil, req("POST", "/status", formType, "Id=j1&Action=3")), "another form action")
	assert.True(t, pagingField("MaxResults") && pagingField("page_size") && pagingField("per-page") && !pagingField("Action"))
	// With a body, a Content-Type in the template's headers is replaced by its body type when Bifrost
	// polls, so a caller sending exactly what Bifrost sends still matches.
	typed := schemas.RelayJobPoll{Method: "POST", Path: "/", Headers: map[string]string{"Content-Type": "application/json", "X-Amz-Target": "Textract.GetDocumentAnalysis"},
		Body: `{"JobId":"{{job.id}}"}`, ContentType: "application/x-amz-json-1.1"}
	sent := (&JobSnapshot{Job: schemas.RelayJobConfig{Poll: typed}}).PollRequest("j1")
	assert.Equal(t, "application/x-amz-json-1.1", sent.Headers["content-type"], "what Bifrost sends")
	assert.Equal(t, "j1", matchStatusCheck(typed, nil, req("POST", "/", sent.Headers, string(sent.Body))), "the caller sending the same")
	assert.Empty(t, matchStatusCheck(typed, nil, req("POST", "/", map[string]string{"content-type": "text/plain", "x-amz-target": "Textract.GetDocumentAnalysis"}, `{"JobId":"j1"}`)), "a body type that is not JSON")

	// The field's own name decides, not one re-read from the escaped path: a parent key ending in a
	// backslash, or one with a dot in it, still leaves Limit a page size.
	slashed := schemas.RelayJobPoll{Path: "/", Body: `{"Id":"{{job.id}}","Paging\\":{"Limit":1},"a.b":{"Limit":1}}`}
	assert.Equal(t, "j1", matchStatusCheck(slashed, nil, req("POST", "/", nil, `{"Id":"j1","Paging\\":{"Limit":50},"a.b":{"Limit":50}}`)))
	// A batch call must carry exactly the template's operations, at every depth.
	batch := schemas.RelayJobPoll{Path: "/rpc", Body: `[{"method":"job.status","params":{"ids":["{{job.id}}"]}}]`}
	assert.Equal(t, "j1", matchStatusCheck(batch, nil, req("POST", "/rpc", nil, `[{"method":"job.status","params":{"ids":["j1"]}}]`)))
	assert.Empty(t, matchStatusCheck(batch, nil, req("POST", "/rpc", nil, `[{"method":"job.status","params":{"ids":["j1"]}},{"method":"doc.analyze"}]`)), "an extra operation")
	assert.Empty(t, matchStatusCheck(batch, nil, req("POST", "/rpc", nil, `[{"method":"job.status","params":{"ids":["j1","j2"]}}]`)), "an extra item deeper in")
	assert.Empty(t, matchStatusCheck(opJSON, nil, req("POST", "/", nil, `{"Action":"Other","Id":"j1"}`)), "another JSON operation")

	query := schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}?view=status"}
	assert.Equal(t, "j1", matchStatusCheck(query, nil, req("GET", "/jobs/j1?view=status&trace=1", nil, "")))
	assert.Empty(t, matchStatusCheck(query, nil, req("GET", "/jobs/j1?view=summary", nil, "")), "another view")
	assert.Empty(t, matchStatusCheck(query, nil, req("GET", "/jobs/j1", nil, "")), "no view")

	twice := schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}/copy/{{job.id}}"}
	assert.Equal(t, "j1", matchStatusCheck(twice, nil, req("GET", "/jobs/j1/copy/j1", nil, "")))
	assert.Empty(t, matchStatusCheck(twice, nil, req("GET", "/jobs/j1/copy/j2", nil, "")))
	bodyTwice := schemas.RelayJobPoll{Path: "/", Body: `{"Id":"{{job.id}}","OtherId":"{{job.id}}"}`}
	assert.Empty(t, matchStatusCheck(bodyTwice, nil, req("POST", "/", nil, `{"Id":"j1","OtherId":"j2"}`)), "two body fields naming different jobs")
	formTwice := schemas.RelayJobPoll{Path: "/", Body: "A={{job.id}}&B={{job.id}}", ContentType: "application/x-www-form-urlencoded"}
	assert.Empty(t, matchStatusCheck(formTwice, nil, req("POST", "/", formType, "A=j1&B=j2")))
	assert.Empty(t, matchStatusCheck(form, nil, req("POST", "/status", formType, "Action=Get&Id=j1&Id=j2")), "a form value repeated with another job")
	formRepeated := schemas.RelayJobPoll{Path: "/jobs/{{job.id}}", Body: "Id={{job.id}}&Id={{job.id}}", ContentType: "application/x-www-form-urlencoded"}
	assert.Equal(t, "j1", matchStatusCheck(formRepeated, nil, req("POST", "/jobs/j1", formType, "Id=j1&Id=j1")))
	assert.Empty(t, matchStatusCheck(formRepeated, nil, req("POST", "/jobs/j1", formType, "Id=j2&Id=j2")), "a repeated template field naming another job than the path")
	formPair := schemas.RelayJobPoll{Path: "/", Body: "Id=job-{{job.id}}&Id=copy-{{job.id}}", ContentType: "application/x-www-form-urlencoded"}
	assert.Equal(t, "j1", matchStatusCheck(formPair, nil, req("POST", "/", formType, "Id=job-j1&Id=copy-j1")))
	assert.Empty(t, matchStatusCheck(formPair, nil, req("POST", "/", formType, "Id=job-j1&Id=copy-j2")), "each repeated template value is checked")

	// A body the matcher cannot read must not name the id, or it could name another job than the query.
	xmlPoll := schemas.RelayJobPoll{Path: "/status?trace={{job.id}}", Body: "<Id>{{job.id}}</Id>", ContentType: "application/xml"}
	assert.Empty(t, matchStatusCheck(xmlPoll, nil, req("POST", "/status?trace=j1", nil, "<Id>j2</Id>")))
	xmlFixed := schemas.RelayJobPoll{Path: "/status?trace={{job.id}}", Body: "<Action>Get</Action>", ContentType: "application/xml"}
	xmlType := map[string]string{"content-type": "application/xml"}
	assert.Equal(t, "j1", matchStatusCheck(xmlFixed, nil, req("POST", "/status?trace=j1", xmlType, "<Action>Get</Action>")), "an unread body equal to the template")
	assert.Empty(t, matchStatusCheck(xmlFixed, nil, req("POST", "/status?trace=j1", xmlType, "<Action>Delete</Action>")), "another operation on the same URL")
	scalar := schemas.RelayJobPoll{Path: "/status/{{job.id}}", Body: `"{{job.id}}"`}
	assert.Empty(t, matchStatusCheck(scalar, nil, req("POST", "/status/j1", nil, `"j2"`)), "a JSON string body naming the id")

	// Repeated fixed query values must all be sent, in any order, and the template matches itself.
	includes := schemas.RelayJobPoll{Method: "GET", Path: "/status?Id={{job.id}}&Include=summary&Include=usage"}
	assert.Equal(t, "j1", matchStatusCheck(includes, nil, req("GET", "/status?Id=j1&Include=summary&Include=usage", nil, "")))
	assert.Equal(t, "j1", matchStatusCheck(includes, nil, req("GET", "/status?Include=usage&Id=j1&Include=summary", nil, "")))
	assert.Empty(t, matchStatusCheck(includes, nil, req("GET", "/status?Id=j1&Include=summary", nil, "")), "a fixed value left out")

	// The call must be sent as the poll's body type, or the upstream could read another job id.
	jsonPoll := schemas.RelayJobPoll{Path: "/", Body: `{"Id":"{{job.id}}"}`}
	assert.Empty(t, matchStatusCheck(jsonPoll, nil, req("POST", "/", formType, `{"Id":"j1","extra":"&Id=j2&"}`)), "a JSON body labelled as a form")
	assert.Equal(t, "j1", matchStatusCheck(jsonPoll, nil, req("POST", "/", map[string]string{"content-type": "application/json"}, `{"Id":"j1"}`)))
	assert.Equal(t, "j1", matchStatusCheck(jsonPoll, nil, req("POST", "/", nil, `{"Id":"j1"}`)), "no content type reads as JSON")
	assert.Empty(t, matchStatusCheck(form, nil, req("POST", "/status", map[string]string{"content-type": "application/json"}, "Action=Get&Id=j1")), "a form poll sent as JSON")

	// Repeated fixed form fields must all be sent, in any order.
	formFixed := schemas.RelayJobPoll{Path: "/", Body: "Include=summary&Include=usage&Id={{job.id}}", ContentType: "application/x-www-form-urlencoded"}
	assert.Equal(t, "j1", matchStatusCheck(formFixed, nil, req("POST", "/", formType, "Include=usage&Include=summary&Id=j1")))
	assert.Empty(t, matchStatusCheck(formFixed, nil, req("POST", "/", formType, "Id=j1")), "repeated fixed fields left out")

	// Fixed booleans and nulls name the operation too; numbers stay the caller's choice.
	cancelPoll := schemas.RelayJobPoll{Path: "/", Body: `{"Id":"{{job.id}}","Cancel":false,"Cursor":null,"Limit":1}`}
	assert.Equal(t, "j1", matchStatusCheck(cancelPoll, nil, req("POST", "/", nil, `{"Id":"j1","Cancel":false,"Cursor":null,"Limit":50}`)))
	assert.Empty(t, matchStatusCheck(cancelPoll, nil, req("POST", "/", nil, `{"Id":"j1","Cancel":true,"Cursor":null}`)), "a changed boolean")
	assert.Empty(t, matchStatusCheck(cancelPoll, nil, req("POST", "/", nil, `{"Id":"j1","Cancel":false,"Cursor":"x"}`)), "a changed null")
	assert.Empty(t, matchStatusCheck(cancelPoll, nil, req("POST", "/", nil, `{"Id":"j1","Cancel":false}`)), "an omitted field is not an explicit null")

	// A field the template repeats must be sent as often.
	twiceQuery := schemas.RelayJobPoll{Method: "GET", Path: "/status?Id={{job.id}}&Id={{job.id}}"}
	assert.Equal(t, "j1", matchStatusCheck(twiceQuery, nil, req("GET", "/status?Id=j1&Id=j1", nil, "")))
	assert.Empty(t, matchStatusCheck(twiceQuery, nil, req("GET", "/status?Id=j1", nil, "")), "one Id for a template that sets two")
	twiceForm := schemas.RelayJobPoll{Path: "/status/{{job.id}}", Body: "Id={{job.id}}&Id={{job.id}}", ContentType: "application/x-www-form-urlencoded"}
	assert.Empty(t, matchStatusCheck(twiceForm, nil, req("POST", "/status/j1", formType, "Id=j1")), "one Id for a form template that sets two")

	// Keys that differ only in letter case are refused, in a body, a query or a form.
	assert.Empty(t, matchStatusCheck(textract, nil, req("POST", "/", target, `{"JobId":"j1","jobid":"j2"}`)), "a case-variant body key")
	caseQuery := schemas.RelayJobPoll{Method: "GET", Path: "/status?Id={{job.id}}"}
	assert.Empty(t, matchStatusCheck(caseQuery, nil, req("GET", "/status?Id=j1&id=j2", nil, "")), "a case-variant query name")
	assert.Empty(t, matchStatusCheck(form, nil, req("POST", "/status", formType, "Action=Get&Id=j1&ID=j2")), "a case-variant form name")

	// A body that would match is refused once it nests past the depth limit (BenchmarkStatusCheckDeepBody tracks the cost).
	deepBody := func(depth int) string {
		return `{"JobId":"j-1","x":` + strings.Repeat("[", depth) + strings.Repeat("]", depth) + `}`
	}
	assert.Equal(t, "j-1", matchStatusCheck(textract, nil, req("POST", "/", target, deepBody(10))), "within the limit")
	assert.Empty(t, matchStatusCheck(textract, nil, req("POST", "/", target, deepBody(1100))), "past the limit")

	// Only valid JSON: a truncated body is not read for its id.
	assert.Empty(t, matchStatusCheck(textract, nil, req("POST", "/", target, `{"JobId":"j-1"`)), "a missing closing brace")

	// Objects and arrays keep the template's shape: an object with a "0" key is not an array.
	arrayPoll := schemas.RelayJobPoll{Path: "/", Body: `{"Jobs":[{"Id":"{{job.id}}"}]}`}
	assert.Equal(t, "j1", matchStatusCheck(arrayPoll, nil, req("POST", "/", nil, `{"Jobs":[{"Id":"j1"}],"Extra":1}`)))
	assert.Empty(t, matchStatusCheck(arrayPoll, nil, req("POST", "/", nil, `{"Jobs":{"0":{"Id":"j1"}}}`)), "an object pretending to be an array")

	// A key gjson would read as syntax is compared as the literal field it is.
	bangPoll := schemas.RelayJobPoll{Path: "/", Body: `{"Id":"{{job.id}}","!true":true}`}
	assert.Empty(t, matchStatusCheck(bangPoll, nil, req("POST", "/", nil, `{"Id":"j1"}`)), "the !true field is missing")
	assert.Equal(t, "j1", matchStatusCheck(bangPoll, nil, req("POST", "/", nil, `{"Id":"j1","!true":true}`)))

	// An encoded {{job.id}} in a template is literal text, sent as is, and never a second id.
	labelPoll := schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}?label=%7B%7Bjob.id%7D%7D"}
	snap := &JobSnapshot{Job: schemas.RelayJobConfig{Poll: labelPoll}}
	own := snap.PollRequest("j1")
	assert.Equal(t, "j1", matchStatusCheck(labelPoll, nil, req("GET", own.Path+"?"+own.RawQuery, nil, "")), "the job's own poll matches")
	assert.Empty(t, matchStatusCheck(labelPoll, nil, req("GET", "/jobs/j1?label=j1", nil, "")), "the label is text, not the id")
	assert.Empty(t, matchStatusCheck(labelPoll, nil, req("GET", "/jobs/j1?label=%EE%80%80job.id%EE%80%80", nil, "")), "the internal marker sent by a caller")

	// A gjson path cannot name an empty key, so a template with one never matches.
	emptyKey := schemas.RelayJobPoll{Path: "/", Body: `{"":{"JobId":"{{job.id}}"}}`}
	assert.Empty(t, matchStatusCheck(emptyKey, nil, req("POST", "/", nil, `{"JobId":"j1","":{"JobId":"j2"}}`)))

	// gjson reads the first of two equal keys and many upstreams the last, so duplicates are refused.
	assert.Empty(t, matchStatusCheck(textract, nil, req("POST", "/", target, `{"JobId":"j1","JobId":"j2"}`)), "a duplicate key")
	nested := schemas.RelayJobPoll{Path: "/", Body: `{"Job":{"Id":"{{job.id}}"}}`}
	assert.Empty(t, matchStatusCheck(nested, nil, req("POST", "/", nil, `{"Job":{"Id":"j1"},"Job":{"Id":"j2"}}`)), "a duplicate parent key")
	assert.Empty(t, matchStatusCheck(nested, nil, req("POST", "/", nil, `{"Job":{"Id":"j1","Id":"j2"}}`)), "a duplicate nested key")
	assert.Equal(t, "j1", matchStatusCheck(nested, nil, req("POST", "/", nil, `{"Job":{"Id":"j1"},"Items":[{"Id":"a"},{"Id":"b"}]}`)), "equal keys in sibling objects are fine")

	// Every value sent under a queried name must name the same job.
	idQuery := schemas.RelayJobPoll{Method: "GET", Path: "/status?Id={{job.id}}"}
	assert.Equal(t, "j1", matchStatusCheck(idQuery, nil, req("GET", "/status?Id=j1", nil, "")))
	assert.Empty(t, matchStatusCheck(idQuery, nil, req("GET", "/status?Id=j1&Id=j2", nil, "")))
	assert.Equal(t, "j1", matchStatusCheck(idQuery, nil, req("GET", "/status?Id=j1&Id=j1", nil, "")))

	// A dotted key in the body template is read as written.
	dotted := schemas.RelayJobPoll{Path: "/", Body: `{"job.id":"{{job.id}}"}`}
	assert.Equal(t, "j1", matchStatusCheck(dotted, nil, req("POST", "/", nil, `{"job.id":"j1"}`)))
	assert.Empty(t, matchStatusCheck(dotted, nil, req("POST", "/", nil, `{"job":{"id":"j1"}}`)))

	// A header the relay adds itself is what the upstream gets, so the caller need not send it.
	tenant := schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}", Headers: map[string]string{"X-Tenant": "acme"}}
	assert.Equal(t, "j1", matchStatusCheck(tenant, map[string]string{"x-tenant": "acme"}, req("GET", "/jobs/j1", nil, "")))
	assert.Empty(t, matchStatusCheck(tenant, map[string]string{"x-tenant": "acme"}, req("GET", "/jobs/j1", map[string]string{"x-tenant": "other"}, "")), "the caller's header wins, as on the wire")
}

// The depth guard refuses a 100,000-level body with one count, before any key scan.
func BenchmarkStatusCheckDeepBody(b *testing.B) {
	poll := schemas.RelayJobPoll{Path: "/", Body: `{"JobId":"{{job.id}}"}`}
	req := &schemas.BifrostRelayRequest{Method: "POST", Path: "/", Body: []byte(strings.Repeat("[", 100000))}
	b.ReportAllocs()
	for b.Loop() {
		_ = matchStatusCheck(poll, req, nil)
	}
}

// A dot segment never yields a status check candidate, so a pre-hook rewrite into one is held to the
// same rule as admission, even against a template with a fixed .. segment.
func TestStatusCheckCandidatesRefuseDotSegments(t *testing.T) {
	job := &schemas.RelayJobConfig{IDPath: "id", StatusPath: "status", Succeeded: []string{"DONE"}, Poll: schemas.RelayJobPoll{Method: "GET", Path: "/jobs/../status/{{job.id}}"}}
	e := &endpoint{routes: []compiledRoute{{job: job}}, hasJobs: true, jobOrder: []int{0}}
	assert.Empty(t, e.statusCheckCandidates(context.Background(), &schemas.BifrostRelayRequest{Method: "GET", Path: "/jobs/../status/j-1"}))
	plain := &schemas.RelayJobConfig{IDPath: "id", StatusPath: "status", Succeeded: []string{"DONE"}, Poll: schemas.RelayJobPoll{Method: "GET", Path: "/status/{{job.id}}"}}
	e = &endpoint{routes: []compiledRoute{{job: plain}}, hasJobs: true, jobOrder: []int{0}}
	assert.NotEmpty(t, e.statusCheckCandidates(context.Background(), &schemas.BifrostRelayRequest{Method: "GET", Path: "/status/j-1"}))
}
