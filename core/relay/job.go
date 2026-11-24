package relay

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// JobSnapshot is everything the job accounting sweeper needs to follow and price one job an
// endpoint route started, captured at start so an endpoint config edited later does not change
// how a job already running is settled. It rides on the job row's params column as JSON.
type JobSnapshot struct {
	Job                schemas.RelayJobConfig    `json:"job"`
	Usage              *schemas.RelayUsageConfig `json:"usage,omitempty"`                // the mapping the terminal poll response is priced with, fully merged
	IgnoreUpstreamCost bool                      `json:"ignore_upstream_cost,omitempty"` // the endpoint's ignore_upstream_cost at start
}

// JobPollRequest is the upstream request the sweeper sends to check a job.
type JobPollRequest struct {
	Method      string
	Path        string            // rooted path, without the query
	RawQuery    string            // the query string the configured poll path carried, without the ?
	Headers     map[string]string // lower-cased names, the form the relay forwards
	Body        []byte
	ContentType string
}

// ParseJobSnapshot reads a snapshot back off a job row's params.
func ParseJobSnapshot(params string) (*JobSnapshot, error) {
	if strings.TrimSpace(params) == "" {
		return nil, errors.New("relay job has no params")
	}
	var snap JobSnapshot
	if err := sonic.Unmarshal([]byte(params), &snap); err != nil {
		return nil, err
	}
	if snap.Job.StatusPath == "" || snap.Job.Poll.Path == "" {
		return nil, errors.New("relay job params carry no poll request")
	}
	return &snap, nil
}

// PollRequest builds the poll for jobID: the placeholder is replaced in the path, every header
// value and the body. A query in the configured path is split off, since the relay re-encodes a
// ? inside a path and the upstream would otherwise see the query as path text.
func (s *JobSnapshot) PollRequest(jobID string) JobPollRequest {
	sub := func(v string) string { return strings.ReplaceAll(v, schemas.JobIDPlaceholder, jobID) }
	method := strings.ToUpper(strings.TrimSpace(s.Job.Poll.Method))
	if method == "" {
		method = "POST"
	}
	// The id is escaped for where it lands: query-escaped in the query, so a+b stays a+b, and
	// JSON-escaped in a JSON body, so a quote cannot end the string. The path takes it as is: the
	// relay's URL builder already escapes ?, # and %, and an ARN-shaped id needs its slashes.
	pathTpl, queryTpl, _ := strings.Cut(s.Job.Poll.Path, "?")
	path := sub(pathTpl)
	rawQuery := strings.ReplaceAll(queryTpl, schemas.JobIDPlaceholder, url.QueryEscape(jobID))
	req := JobPollRequest{Method: method, Path: path, RawQuery: rawQuery, Headers: make(map[string]string, len(s.Job.Poll.Headers)+1)}
	for name, value := range s.Job.Poll.Headers {
		req.Headers[strings.ToLower(name)] = sub(value)
	}
	if s.Job.Poll.Body != "" {
		req.ContentType = s.Job.Poll.ContentType
		if req.ContentType == "" {
			req.ContentType = "application/json"
		}
		inBody := jobID
		switch mediaType := bodyMediaType(req.ContentType); {
		case isJSONContentType(req.ContentType):
			quoted, _ := providerUtils.MarshalSorted(jobID)
			inBody = string(quoted[1 : len(quoted)-1])
		case mediaType == "application/x-www-form-urlencoded":
			// x&Action=Delete must stay one value, not add a parameter to the poll.
			inBody = url.QueryEscape(jobID)
		case strings.Contains(mediaType, "xml"):
			var b strings.Builder
			_ = xml.EscapeText(&b, []byte(jobID))
			inBody = b.String()
		}
		req.Body = []byte(strings.ReplaceAll(s.Job.Poll.Body, schemas.JobIDPlaceholder, inBody))
		req.Headers["content-type"] = req.ContentType
	}
	return req
}

// bodyMediaType is the lower-cased media type of a Content-Type value, without parameters.
func bodyMediaType(contentType string) string {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return strings.ToLower(strings.TrimSpace(mediaType))
}

// Status reads the job status a poll response reports, "" when the body has none.
func (s *JobSnapshot) Status(body []byte) string {
	if v := providerUtils.GetJSONField(body, s.Job.StatusPath); v.Exists() {
		return strings.TrimSpace(v.String())
	}
	return ""
}

// Outcome classifies a status: done and billable (succeeded), done at no charge (failed), or
// still running. Values compare case-insensitively.
func (s *JobSnapshot) Outcome(status string) (done, succeeded bool) {
	for _, v := range s.Job.Succeeded {
		if strings.EqualFold(v, status) {
			return true, true
		}
	}
	for _, v := range s.Job.Failed {
		if strings.EqualFold(v, status) {
			return true, false
		}
	}
	return false, false
}

// Interval is the configured poll spacing, 30s when unset.
func (s *JobSnapshot) Interval() int {
	if s.Job.IntervalSeconds > 0 {
		return s.Job.IntervalSeconds
	}
	return 30
}

// Timeout is how long a job may run before polling stops, an hour when unset.
func (s *JobSnapshot) Timeout() int {
	if s.Job.TimeoutSeconds > 0 {
		return s.Job.TimeoutSeconds
	}
	return schemas.DefaultRelayJobTimeoutSeconds
}

// Price runs the terminal poll response through the job's usage mapping, the same way a
// buffered relay prices a reply. priced is false when the mapping reads units, a cost or tokens
// and the reply carries none of that field, so a job is never settled as free because its reply
// changed shape.
func (s *JobSnapshot) Price(contentType string, body []byte) (usage *schemas.RelayUsage, priced bool) {
	u := resolveUsage(s.Usage, nil)
	u.ignoreUpstreamCost = s.IgnoreUpstreamCost
	fields := u.parse(contentType, body)
	priced = true
	if u.billsUnits() && !fields.hasUnits {
		priced = false
	}
	if len(u.costPaths()) > 0 && !fields.hasCost {
		priced = false
	}
	// Token paths are often inherited from the relay; they only decide the price when the job reads
	// no units or cost of its own, so a per-page job is not unpriced for lacking tokens.
	if u.tokensConfigured() && !u.billsUnits() && len(u.costPaths()) == 0 && !fields.hasInput && !fields.hasOutput && !fields.hasTotal {
		priced = false
	}
	return u.build(fields), priced
}

// mergeUsageConfig overlays override on base field by field, the schema-level twin of
// resolveUsage, so a snapshot carries one self-contained mapping.
func mergeUsageConfig(base, override *schemas.RelayUsageConfig) *schemas.RelayUsageConfig {
	if base == nil && override == nil {
		return nil
	}
	out := schemas.RelayUsageConfig{}
	apply := func(u *schemas.RelayUsageConfig) {
		if u == nil {
			return
		}
		if u.ModelPath != "" {
			out.ModelPath = u.ModelPath
		}
		if u.ModelSource != "" {
			out.ModelSource = u.ModelSource
		}
		if u.InputTokensPath != "" {
			out.InputTokensPath = u.InputTokensPath
		}
		if u.OutputTokensPath != "" {
			out.OutputTokensPath = u.OutputTokensPath
		}
		if u.TotalTokensPath != "" {
			out.TotalTokensPath = u.TotalTokensPath
		}
		if u.CostPath != "" {
			out.CostPath = u.CostPath
		}
		if u.CostPerRequest != nil {
			out.CostPerRequest = schemas.Ptr(*u.CostPerRequest)
		}
		if u.UnitsPath != "" {
			out.UnitsPath = u.UnitsPath
		}
		if u.UnitsSource != "" {
			out.UnitsSource = u.UnitsSource
		}
		if u.UnitsMeasure != "" {
			out.UnitsMeasure = u.UnitsMeasure
		}
		if u.CostPerUnit != nil {
			out.CostPerUnit = schemas.Ptr(*u.CostPerUnit)
		}
		if u.UnitsBlockSize != nil {
			out.UnitsBlockSize = schemas.Ptr(*u.UnitsBlockSize)
		}
		if u.UnitsMinimum != nil {
			out.UnitsMinimum = schemas.Ptr(*u.UnitsMinimum)
		}
	}
	apply(base)
	apply(override)
	return &out
}

// jobSnapshotFor builds the snapshot a route's job block is settled with: the job block, the
// mapping merged endpoint → route → job, and the endpoint's cost policy.
func jobSnapshotFor(cfg *schemas.RelayConfig, route schemas.RelayRouteConfig, ignoreUpstreamCost bool) (string, error) {
	if route.Job == nil {
		return "", nil
	}
	snap := JobSnapshot{
		Job:                *route.Job,
		Usage:              mergeUsageConfig(mergeUsageConfig(cfg.Usage, route.Usage), route.Job.Usage),
		IgnoreUpstreamCost: ignoreUpstreamCost,
	}
	// The start call already charged the endpoint or route fee; the settlement charges only a fee the
	// job block sets itself.
	if snap.Usage != nil {
		snap.Usage.CostPerRequest = nil
		if route.Job.Usage != nil && route.Job.Usage.CostPerRequest != nil {
			snap.Usage.CostPerRequest = schemas.Ptr(*route.Job.Usage.CostPerRequest)
		}
	}
	// The job's own usage block, once merged, is not needed twice.
	snap.Job.Usage = nil
	// Sorted so the same settings always give the same bytes: the snapshot carries header maps.
	data, err := providerUtils.MarshalSorted(snap)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// maxRelayJobIDLength keeps the job row key (relay-job:{relay}:g{n}:{id}) within its column.
const maxRelayJobIDLength = 160

// validRelayJobID accepts printable ASCII without spaces, backslashes, ? or #, and no . or ..
// path segment: the id is substituted into the poll path as is (an ARN keeps its slashes), so an
// id the upstream returns must not be able to leave the configured path.
func validRelayJobID(id string) bool {
	if len(id) > maxRelayJobIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c <= ' ' || c >= 0x7f || c == '\\' || c == '?' || c == '#' {
			return false
		}
	}
	// A ;param does not stop some servers (Tomcat) reading "..;x" as "..", so it is stripped first,
	// as gapSegmentOK does for a ** segment.
	for _, segment := range strings.Split(id, "/") {
		if name, _, _ := strings.Cut(segment, ";"); name == "." || name == ".." {
			return false
		}
	}
	return true
}

// statusCheckJobID reads the job id out of a caller's request when it is a status check for one
// of the relay's jobs: the reverse of PollRequest, so the reply can settle the job. It returns ""
// for any other request.
func (e *endpoint) statusCheckJobID(req *schemas.BifrostRelayRequest) string {
	for i := range e.routes {
		if job := e.routes[i].job; job != nil {
			if id := matchStatusCheck(job.Poll, req); id != "" {
				return id
			}
		}
	}
	return ""
}

// matchStatusCheck matches a request against one poll template. Every place the template names
// the job id must agree on it, and every literal part must match as PollRequest would send it.
func matchStatusCheck(poll schemas.RelayJobPoll, req *schemas.BifrostRelayRequest) string {
	method := strings.TrimSpace(poll.Method)
	if method == "" {
		method = "POST"
	}
	if !strings.EqualFold(method, req.Method) {
		return ""
	}
	id := ""
	agree := func(found string, ok bool) bool {
		if !ok {
			return false
		}
		if found != "" {
			if id != "" && id != found {
				return false
			}
			id = found
		}
		return true
	}
	pathTpl, _, _ := strings.Cut(poll.Path, "?")
	if !agree(templateJobID(pathTpl, req.Path)) {
		return ""
	}
	for name, value := range poll.Headers {
		sent, ok := req.Headers[strings.ToLower(name)]
		if !ok || !agree(templateJobID(value, sent)) {
			return ""
		}
	}
	if strings.Contains(poll.Body, schemas.JobIDPlaceholder) && !agree(bodyJobID(poll, req.Body)) {
		return ""
	}
	if id == "" || !validRelayJobID(id) {
		return ""
	}
	return id
}

// templateJobID returns what {{job.id}} stands for in value; with no placeholder the template must
// equal value, and the id is "".
func templateJobID(tpl, value string) (string, bool) {
	before, after, found := strings.Cut(tpl, schemas.JobIDPlaceholder)
	if !found {
		return "", tpl == value
	}
	if len(value) <= len(before)+len(after) || !strings.HasPrefix(value, before) || !strings.HasSuffix(value, after) {
		return "", false
	}
	id := value[len(before) : len(value)-len(after)]
	// A template naming the id twice must repeat the same id; one naming it once is matched already.
	if strings.Contains(after, schemas.JobIDPlaceholder) && strings.ReplaceAll(tpl, schemas.JobIDPlaceholder, id) != value {
		return "", false
	}
	return id, true
}

// bodyJobID reads the job id from a JSON or form body at the field the template puts it in. Other
// body types are not read, so the id must come from the path or a header.
func bodyJobID(poll schemas.RelayJobPoll, body []byte) (string, bool) {
	contentType := poll.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	switch {
	case isJSONContentType(contentType):
		field, tpl, ok := jsonTemplateField(gjson.Parse(poll.Body), "")
		if !ok {
			return "", true
		}
		sent := gjson.GetBytes(body, field)
		if sent.Type != gjson.String {
			return "", false
		}
		return templateJobID(tpl, sent.Str)
	case bodyMediaType(contentType) == "application/x-www-form-urlencoded":
		tplValues, err := url.ParseQuery(poll.Body)
		if err != nil {
			return "", true
		}
		sentValues, err := url.ParseQuery(string(body))
		if err != nil {
			return "", false
		}
		for name, values := range tplValues {
			if len(values) == 1 && strings.Contains(values[0], schemas.JobIDPlaceholder) {
				return templateJobID(values[0], sentValues.Get(name))
			}
		}
	}
	return "", true
}

// jsonTemplateField finds the string field of a JSON template that holds {{job.id}}, as a gjson
// path. A key gjson would read as syntax is not followed.
func jsonTemplateField(node gjson.Result, prefix string) (field, tpl string, ok bool) {
	node.ForEach(func(key, value gjson.Result) bool {
		k := key.String()
		if node.IsArray() {
			k = strconv.Itoa(int(key.Int()))
		} else if strings.ContainsAny(k, ".*?|#@\\") {
			return true
		}
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch {
		case value.Type == gjson.String && strings.Contains(value.Str, schemas.JobIDPlaceholder):
			field, tpl, ok = path, value.Str, true
		case value.IsObject() || value.IsArray():
			field, tpl, ok = jsonTemplateField(value, path)
		}
		return !ok
	})
	return field, tpl, ok
}

// jobHandle reads the job id a start response names and returns the handle the logging plugin
// records, or nil when the response names none (the call is then billed as an ordinary one). An
// id that is unsafe to poll with is refused with an error, and the call is billed as an ordinary one.
func (route *compiledRoute) jobHandle(contentType string, body []byte) (*schemas.RelayJobHandle, error) {
	if route == nil || route.job == nil || len(body) == 0 {
		return nil, nil
	}
	if !looksJSON(contentType, body) {
		return nil, nil
	}
	v := providerUtils.GetJSONField(body, route.job.IDPath)
	if !v.Exists() {
		return nil, nil
	}
	id := strings.TrimSpace(v.String())
	if id == "" {
		return nil, nil
	}
	if !validRelayJobID(id) {
		return nil, fmt.Errorf("the job id at %s is not safe to poll with (%d bytes; printable ASCII, no spaces, \\, ?, # or . segments, at most %d bytes)", route.job.IDPath, len(id), maxRelayJobIDLength)
	}
	return &schemas.RelayJobHandle{ID: id, IntervalSeconds: route.jobInterval, Params: route.jobParams, ReusedIDIsNewJob: route.job.ReusedIDIsNewJob}, nil
}

// checkJobStatusRoutes refuses a job whose status check would be charged when a caller sends it:
// the job is billed once when it settles, so a billable route (or a billable method default on a
// relay that relays any path) matching the poll request would charge every check on top.
func (e *endpoint) checkJobStatusRoutes() error {
	for i := range e.routes {
		job := e.routes[i].job
		if job == nil {
			continue
		}
		// Several ids of different shapes, so a free route that matches one literal id cannot pass
		// the check while real ids reach a billable route.
		for _, id := range jobStatusSampleIDs {
			poll := (&JobSnapshot{Job: *job}).PollRequest(id)
			p := e.plan(poll.Method, poll.Path, poll.Headers)
			if p.route != nil && p.route.disabled {
				return fmt.Errorf("routes[%d].job: a caller's status check (%s %s) matches routes[%d], which is disabled, so it would get a 404; enable or replace that route, or add a more specific enabled route with billable: false", e.routes[i].configIndex, poll.Method, job.Poll.Path, p.index)
			}
			if !p.allowed || !p.billing {
				continue
			}
			matched := "no route matches it and skip_route_check relays it as a billable " + poll.Method
			if p.index >= 0 {
				matched = fmt.Sprintf("it matches routes[%d], which is billable", p.index)
			}
			return fmt.Errorf("routes[%d].job: a caller's status check (%s %s) would be charged on every call as well as the job: %s; add a more specific route for it with billable: false", e.routes[i].configIndex, poll.Method, job.Poll.Path, matched)
		}
	}
	return nil
}

// jobStatusSampleIDs are the job ids a status check is tried with: a word, a UUID, a long hex id
// and ids with slashes, as upstreams return.
var jobStatusSampleIDs = []string{"job-id", "0f3c9a1e-7b2d-4c8e-9f10-2a4b6c8d0e1f", "9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f9a8b", "jobs/1", "a/b/c"}
