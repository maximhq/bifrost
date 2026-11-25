package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"net/url"
	"slices"
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
	return schemas.DefaultRelayJobIntervalSeconds
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
	// Token paths are often inherited from the relay and never set the price, so they only count
	// when the job has no units, cost path or fee of its own.
	if u.tokensConfigured() && !u.billsUnits() && len(u.costPaths()) == 0 && u.costPerRequest == 0 && !fields.hasInput && !fields.hasOutput && !fields.hasTotal {
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
func (e *endpoint) statusCheckJobID(ctx context.Context, req *schemas.BifrostRelayRequest) string {
	if candidates := e.statusCheckCandidates(ctx, req); len(candidates) > 0 {
		return candidates[0].id
	}
	return ""
}

// statusCandidate is one job route's reading of a status check: the id its own template reads.
type statusCandidate struct {
	id    string
	match schemas.RelayJobMatch
}

// statusCheckCandidates returns every job route whose poll template fits the request, each with the
// id its own template reads, in the order the endpoint ranked them when it was built (most specific
// template first).
// Templates can read one call differently (/jobs/{{job.id}} reads /jobs/accounts/j1 as accounts/j1,
// /jobs/accounts/{{job.id}} as j1), so none is dropped before the job lookup confirms one. The
// templates are copies, so a hook cannot change the relay's own.
func (e *endpoint) statusCheckCandidates(ctx context.Context, req *schemas.BifrostRelayRequest) []statusCandidate {
	// A dot segment never passes as a status check, as at admission, so a pre-hook rewrite is held to
	// the same rule.
	if hasDotSegment(req.Path) {
		return nil
	}
	// The headers the upstream receives: the caller's own, its x-bf-eh-* overrides beneath them, and
	// the relay's extra headers last (in matchStatusCheck).
	effective := *req
	effective.Headers = e.routeHeaders(req.Headers, callerExtraHeaders(ctx))
	var out []statusCandidate
	for _, i := range e.jobOrder {
		job := e.routes[i].job
		if id := matchStatusCheck(job.Poll, &effective, e.operatorHeaders); id != "" {
			poll := job.Poll
			poll.Headers = maps.Clone(job.Poll.Headers)
			out = append(out, statusCandidate{id: id, match: schemas.RelayJobMatch{RouteKey: e.routes[i].jobRouteKey, Poll: poll}})
		}
	}
	return out
}

// pollFixedText is how much fixed text a poll template's path, headers and body carry, so a more
// specific template is tried first.
func pollFixedText(poll schemas.RelayJobPoll) int {
	n := len(strings.ReplaceAll(poll.Path, schemas.JobIDPlaceholder, "")) + len(strings.ReplaceAll(poll.Body, schemas.JobIDPlaceholder, ""))
	for name, value := range poll.Headers {
		n += len(name) + len(strings.ReplaceAll(value, schemas.JobIDPlaceholder, ""))
	}
	return n
}

// statusCandidatesKey names a candidate set, so an unchanged call keeps its earlier job lookup.
func statusCandidatesKey(candidates []statusCandidate) string {
	var b strings.Builder
	for _, c := range candidates {
		b.WriteString(c.match.RouteKey)
		b.WriteByte(0)
		b.WriteString(c.id)
		b.WriteByte(0)
	}
	return b.String()
}

// jobRouteKey names a job route by its method, path and header matchers, so two job routes on one
// relay never share a job row even when their upstreams hand out the same ids.
func jobRouteKey(route schemas.RelayRouteConfig) string {
	h := sha256.New()
	h.Write([]byte(strings.ToUpper(route.Method) + "\x00" + route.Path))
	names := make([]string, 0, len(route.Headers))
	for name := range route.Headers {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	for _, name := range names {
		h.Write([]byte("\x00" + strings.ToLower(name) + "=" + route.Headers[name]))
	}
	return "r" + hex.EncodeToString(h.Sum(nil))[:12]
}

// idMatch collects the job id each place of a template names, and fails when two disagree.
type idMatch struct{ id string }

func (m *idMatch) agree(found string, ok bool) bool {
	if !ok {
		return false
	}
	if found != "" {
		if m.id != "" && m.id != found {
			return false
		}
		m.id = found
	}
	return true
}

// matchStatusCheck matches a request against one poll template, as the upstream would receive it:
// the method, the path, every query field and header the template sets (a header the relay adds
// itself counts), and every string field of a JSON or form body. Each place naming the job id must
// agree on it. Extra caller query fields, headers and body fields are allowed, and so are other
// numbers in a body (a caller's own MaxResults).
func matchStatusCheck(poll schemas.RelayJobPoll, req *schemas.BifrostRelayRequest, operator map[string]string) string {
	method := strings.TrimSpace(poll.Method)
	if method == "" {
		method = "POST"
	}
	if !strings.EqualFold(method, req.Method) {
		return ""
	}
	var m idMatch
	pathTpl, queryTpl, _ := strings.Cut(poll.Path, "?")
	if !m.agree(templateJobID(pathTpl, req.Path)) {
		return ""
	}
	if queryTpl != "" && !queryMatches(queryTpl, req.RawQuery, &m) {
		return ""
	}
	hasBody := strings.TrimSpace(poll.Body) != ""
	for name, value := range poll.Headers {
		// With a body, Bifrost's own poll sends the body's content type, not this header, so a caller
		// sending what Bifrost sends is not refused over it; the body type is checked with the body.
		if hasBody && strings.EqualFold(strings.TrimSpace(name), "content-type") {
			continue
		}
		sent, ok := headerValue(strings.ToLower(name), req.Headers, operator)
		if !ok || !m.agree(templateJobID(value, sent)) {
			return ""
		}
	}
	sentType, _ := headerValue("content-type", req.Headers, operator)
	if hasBody && !bodyMatches(poll, req.Body, sentType, &m) {
		return ""
	}
	if m.id == "" || !validRelayJobID(m.id) {
		return ""
	}
	return m.id
}

// queryMatches checks every field the template's query sets; the caller may send more.
func queryMatches(tpl, rawQuery string, m *idMatch) bool {
	tplValues, err := parseTemplateQuery(tpl)
	if err != nil {
		return false
	}
	sent, err := parseSentQuery(rawQuery)
	if err != nil {
		return false
	}
	for name, values := range tplValues {
		got := sent[name]
		if len(values) == 0 || len(got) == 0 || caseVariantName(sent, name) {
			return false
		}
		switch {
		case !slices.ContainsFunc(values[1:], func(v string) bool { return v != values[0] }):
			// As many values as the template sets, and every one matching, so ?Id=j1&Id=j2 cannot name two jobs.
			if len(got) < len(values) {
				return false
			}
			for _, value := range got {
				if !m.agree(templateJobID(values[0], value)) {
					return false
				}
			}
		case slices.ContainsFunc(values, func(v string) bool { return strings.Contains(v, schemas.JobIDPlaceholder) }):
			// Different values carrying the id are matched in order, each one checked.
			if len(got) != len(values) {
				return false
			}
			for i, value := range got {
				if !m.agree(templateJobID(values[i], value)) {
					return false
				}
			}
		default:
			// Different fixed values (Include=summary&Include=usage) must all be sent, in any order.
			if !sameValues(got, values) {
				return false
			}
		}
	}
	return true
}

// literalPlaceholder stands for a {{job.id}} that is text, not the placeholder: one written
// URL-encoded in a template (%7B%7Bjob.id%7D%7D), which the poll sends as is, or one a caller sends.
const literalPlaceholder = "\uE000job.id\uE000"

// parseTemplateQuery is url.ParseQuery for a template's query or form body that keeps an encoded
// {{job.id}} as literal text: only a placeholder written as is names the job id.
func parseTemplateQuery(raw string) (url.Values, error) {
	values := url.Values{}
	for part := range strings.SplitSeq(raw, "&") {
		if part == "" {
			continue
		}
		rawName, rawValue, _ := strings.Cut(part, "=")
		name, err := url.QueryUnescape(rawName)
		if err != nil {
			return nil, err
		}
		value, err := url.QueryUnescape(rawValue)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(rawValue, schemas.JobIDPlaceholder) {
			value = strings.ReplaceAll(value, schemas.JobIDPlaceholder, literalPlaceholder)
		}
		values[name] = append(values[name], value)
	}
	return values, nil
}

// parseSentQuery is url.ParseQuery for what a caller sent, with any {{job.id}} text marked literal
// so it can only equal a template's literal one.
func parseSentQuery(raw string) (url.Values, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, err
	}
	for name, list := range values {
		// The marker is only ever produced here, so a caller sending it could stand in for a literal {{job.id}}.
		if strings.Contains(name, "\uE000") {
			return nil, errors.New("reserved character in a field name")
		}
		for i, v := range list {
			if strings.Contains(v, "\uE000") {
				return nil, errors.New("reserved character in a field value")
			}
			list[i] = strings.ReplaceAll(v, schemas.JobIDPlaceholder, literalPlaceholder)
		}
		values[name] = list
	}
	return values, nil
}

// templateJobID returns what {{job.id}} stands for in value; with no placeholder the template must
// equal value, and the id is "". A template naming the id more than once must repeat the same id.
func templateJobID(tpl, value string) (string, bool) {
	parts := strings.Split(tpl, schemas.JobIDPlaceholder)
	if len(parts) == 1 {
		return "", tpl == value
	}
	n := len(parts) - 1
	fixed := len(tpl) - n*len(schemas.JobIDPlaceholder)
	if len(value) <= fixed || (len(value)-fixed)%n != 0 || !strings.HasPrefix(value, parts[0]) {
		return "", false
	}
	id := value[len(parts[0]) : len(parts[0])+(len(value)-fixed)/n]
	if strings.Join(parts, id) != value {
		return "", false
	}
	return id, true
}

// bodyMatches checks a JSON or form body against the template: every string field must match, the
// ones naming the job id agreeing on it. Other body types are not read, so a template naming the id
// in one never matches: the caller's body could name another job than the path or query.
func bodyMatches(poll schemas.RelayJobPoll, body []byte, sentType string, m *idMatch) bool {
	namesID := strings.Contains(poll.Body, schemas.JobIDPlaceholder)
	contentType := poll.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	// The body is read as the poll's type, so the call must be sent as that type too: a JSON-looking
	// body labelled as a form would be read differently by the upstream. No content type reads as JSON.
	switch {
	case isJSONContentType(contentType) && sentType != "" && !isJSONContentType(sentType):
		return false
	case bodyMediaType(contentType) == "application/x-www-form-urlencoded" && bodyMediaType(sentType) != "application/x-www-form-urlencoded":
		return false
	}
	switch {
	case isJSONContentType(contentType):
		tpl := gjson.Parse(poll.Body)
		if !tpl.IsObject() && !tpl.IsArray() {
			// A scalar template ("GetStatus") is compared as written, as an unread body is.
			return !namesID && bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace([]byte(poll.Body)))
		}
		// gjson reads the first of two equal keys and many upstreams the last, so a body naming
		// one job for the relay and another for the upstream is refused.
		// Bounded first: the depth guard is one vectorised count, and the key scan one linear pass.
		// Valid JSON only: gjson reads a field out of a truncated body ({"Id":"j1") without checking the rest.
		// An empty key cannot be read by path either, so a caller body with one is never a status check.
		if jsonTooDeep(body) || !json.Valid(body) || hasDuplicateJSONKey(body) || hasEmptyJSONKey(gjson.ParseBytes(body)) {
			return false
		}
		// A gjson path cannot name an empty key, so a template with one could read the wrong field.
		if hasEmptyJSONKey(tpl) {
			return false
		}
		// The top level is a container too: a batch call's list must be as long as the template's.
		if root := gjson.ParseBytes(body); tpl.IsArray() != root.IsArray() || tpl.IsObject() != root.IsObject() ||
			(tpl.IsArray() && len(tpl.Array()) != len(root.Array())) {
			return false
		}
		ok := true
		walkJSONFields(tpl, "", func(path, name string, value gjson.Result) bool {
			sent := gjson.GetBytes(body, path)
			if value.IsObject() || value.IsArray() {
				// The same shape along the path: Jobs.0 reads an array's first item and an object's "0" key alike.
				ok = value.IsObject() == sent.IsObject() && value.IsArray() == sent.IsArray()
				// A list as long as the template's, so no extra operation rides along in a batch call.
				if ok && value.IsArray() {
					ok = len(value.Array()) == len(sent.Array())
				}
				return ok
			}
			if value.Type == gjson.String {
				// The caller's literal {{job.id}} text is marked as the template's is, as in a query or form.
				ok = sent.Type == gjson.String && !strings.Contains(sent.Str, "\uE000") &&
					m.agree(templateJobID(jsonTemplateString(value.Raw), strings.ReplaceAll(sent.Str, schemas.JobIDPlaceholder, literalPlaceholder)))
			} else if value.Type == gjson.Number {
				if pagingField(name) {
					// A page size or offset is the caller's choice: any number, or none, but nothing else.
					ok = !sent.Exists() || sent.Type == gjson.Number
				} else {
					// Any other number names the operation as a string does ("Action": 1).
					ok = sent.Type == gjson.Number && sameJSONNumber(value.Raw, sent.Raw)
				}
			} else {
				// A fixed true, false or null names the operation as a string does ("Cancel": false).
				// Present and equal: an omitted field is not an explicit null.
				ok = sent.Exists() && sent.Type == value.Type
			}
			return ok
		})
		return ok
	case bodyMediaType(contentType) == "application/x-www-form-urlencoded":
		tplValues, err := parseTemplateQuery(poll.Body)
		if err != nil {
			return !namesID && bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace([]byte(poll.Body)))
		}
		sent, err := parseSentQuery(string(body))
		if err != nil {
			return false
		}
		for name, values := range tplValues {
			got, present := sent[name]
			if caseVariantName(sent, name) {
				return false
			}
			if !slices.ContainsFunc(values[1:], func(v string) bool { return v != values[0] }) {
				// As many values as the template repeats it, each checked below.
				if len(got) < len(values) {
					return false
				}
				values = values[:1]
			} else if slices.ContainsFunc(values, func(v string) bool { return strings.Contains(v, schemas.JobIDPlaceholder) }) {
				// Different values under one name that carry the id are matched in order, each one checked.
				if len(got) != len(values) {
					return false
				}
				for i, value := range got {
					if !m.agree(templateJobID(values[i], value)) {
						return false
					}
				}
				continue
			} else {
				// Different fixed values under one name: the caller must send the same set, in any order.
				if !sameValues(got, values) {
					return false
				}
				continue
			}
			// A page size or offset is the caller's choice, but what is sent in its place must be a
			// number too, so Limit=all is not a status check. Any other number must match exactly.
			if _, err := strconv.ParseFloat(values[0], 64); err == nil && pagingField(name) {
				for _, value := range got {
					// ParseFloat takes NaN and Inf too, which are no page size.
					if f, err := strconv.ParseFloat(value, 64); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
						return false
					}
				}
				continue
			}
			if !present || len(got) == 0 {
				return false
			}
			// Every value sent under the name must match, so Id=jobA&Id=jobB cannot name two jobs.
			for _, value := range got {
				if !m.agree(templateJobID(values[0], value)) {
					return false
				}
			}
		}
	default:
		// A body Bifrost cannot read field by field (XML, plain text) must be the template exactly,
		// or another operation on the same URL (<Action>Delete</Action>) would pass as a status check.
		return !namesID && bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace([]byte(poll.Body)))
	}
	return true
}

// sameValues reports whether a and b hold the same values, in any order.
func sameValues(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// hasEmptyJSONKey reports whether any object in node, at any depth, has an empty key.
func hasEmptyJSONKey(node gjson.Result) bool {
	found := false
	node.ForEach(func(key, value gjson.Result) bool {
		found = (node.IsObject() && key.Str == "") || ((value.IsObject() || value.IsArray()) && hasEmptyJSONKey(value))
		return !found
	})
	return found
}

// hasDuplicateJSONKey reports whether any object in a JSON body, at any depth, names a key twice,
// counting keys that differ only in letter case: gjson reads the first, and an upstream may read the
// last or fold case, so the two could name different jobs. One pass over the bytes; a malformed body
// counts as a duplicate, so it never matches.
func hasDuplicateJSONKey(body []byte) bool {
	type frame struct {
		obj, wantKey bool
		keys         map[string]struct{}
	}
	var stack []frame
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '{':
			stack = append(stack, frame{obj: true, wantKey: true})
		case '[':
			stack = append(stack, frame{})
		case '}', ']':
			if len(stack) == 0 {
				return true
			}
			stack = stack[:len(stack)-1]
		case ',':
			if n := len(stack); n > 0 && stack[n-1].obj {
				stack[n-1].wantKey = true
			}
		case '"':
			start, escaped := i, false
			for i++; i < len(body) && body[i] != '"'; i++ {
				if body[i] == '\\' {
					escaped = true
					i++
				}
			}
			if i >= len(body) {
				return true
			}
			n := len(stack)
			if n == 0 || !stack[n-1].obj || !stack[n-1].wantKey {
				continue
			}
			key := string(body[start+1 : i])
			if escaped {
				if json.Unmarshal(body[start:i+1], &key) != nil {
					return true
				}
			}
			key = strings.ToLower(key)
			if stack[n-1].keys == nil {
				stack[n-1].keys = map[string]struct{}{}
			}
			if _, dup := stack[n-1].keys[key]; dup {
				return true
			}
			stack[n-1].keys[key] = struct{}{}
			stack[n-1].wantKey = false
		}
	}
	return false
}

// caseVariantName reports whether values names name under another letter case, which an upstream
// that folds case would read as the same field.
func caseVariantName(values url.Values, name string) bool {
	for sent := range values {
		if sent != name && strings.EqualFold(sent, name) {
			return true
		}
	}
	return false
}

// escapeGJSONKey makes a JSON key a literal gjson path component: every ASCII character other than a
// letter, digit, _ or - is escaped, so a key like !true or a|b is read as written, not as syntax.
func escapeGJSONKey(key string) string {
	var b strings.Builder
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c < 0x80 && !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

// walkJSONFields calls visit with the gjson path and value of every object, array, string, boolean
// and null in a JSON template (a container before its fields), until visit returns false.
// visit gets each field's gjson path, for lookup, and its name as written (empty for a list item),
// so a name is never re-read from the escaped path.
func walkJSONFields(node gjson.Result, prefix string, visit func(path, name string, value gjson.Result) bool) bool {
	cont := true
	node.ForEach(func(key, value gjson.Result) bool {
		name := ""
		k := key.String()
		if node.IsArray() {
			k = strconv.Itoa(int(key.Int()))
		} else {
			name = k
			// Escaped so gjson reads the key as written, not as path syntax.
			k = escapeGJSONKey(k)
		}
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch {
		case value.Type == gjson.String || value.Type == gjson.Number || value.Type == gjson.True || value.Type == gjson.False || value.Type == gjson.Null:
			cont = visit(path, name, value)
		case value.IsObject() || value.IsArray():
			// The container itself is visited first, so its shape is checked before its fields are read.
			if cont = visit(path, name, value); cont {
				cont = walkJSONFields(value, path, visit)
			}
		}
		return cont
	})
	return cont
}

// pagingFields are the field names, lowercased with _ and - removed, whose numbers only page through
// a job's results, so a caller may send its own. Any other number in a template must match exactly.
var pagingFields = map[string]struct{}{
	"limit": {}, "offset": {}, "page": {}, "pagesize": {}, "perpage": {}, "pagenumber": {}, "pageindex": {},
	"maxresults": {}, "maxitems": {}, "maxkeys": {}, "maxrecords": {}, "count": {}, "top": {}, "skip": {},
}

func pagingField(name string) bool {
	name = strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(name))
	_, ok := pagingFields[name]
	return ok
}

// jsonTemplateString decodes a template's JSON string, keeping apart what the poll fills in and what
// is text: the poll replaces {{job.id}} in the body as written, so one spelled with escapes
// ("\u007b\u007bjob.id\u007d\u007d") is sent as it is and stays text here too.
func jsonTemplateString(raw string) string {
	const active = "\uE001job.id\uE001"
	decoded := gjson.Parse(strings.ReplaceAll(raw, schemas.JobIDPlaceholder, active)).Str
	decoded = strings.ReplaceAll(decoded, schemas.JobIDPlaceholder, literalPlaceholder)
	return strings.ReplaceAll(decoded, active, schemas.JobIDPlaceholder)
}

// sameJSONNumber compares two JSON numbers by value, exactly: 1 and 1.0 agree, 1 and
// 1.000000000000000000001 do not. A huge exponent is compared as text, so it cannot make the
// exact comparison build an enormous number.
func sameJSONNumber(a, b string) bool {
	if a == b {
		return true
	}
	if hugeExponent(a) || hugeExponent(b) {
		return false
	}
	x, okA := new(big.Rat).SetString(a)
	y, okB := new(big.Rat).SetString(b)
	return okA && okB && x.Cmp(y) == 0
}

// hugeExponent reports a number written with an exponent past ±400, beyond any double.
func hugeExponent(n string) bool {
	i := strings.IndexAny(n, "eE")
	if i < 0 {
		return false
	}
	exp, err := strconv.Atoi(strings.TrimPrefix(n[i+1:], "+"))
	return err != nil || exp > 400 || exp < -400
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
	return &schemas.RelayJobHandle{ID: id, IntervalSeconds: route.jobInterval, Params: route.jobParams, ReusedIDIsNewJob: route.job.ReusedIDIsNewJob, RouteKey: route.jobRouteKey}, nil
}
