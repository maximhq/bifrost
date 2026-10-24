package generic

import (
	"errors"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// JobSnapshot is everything the job accounting sweeper needs to follow and price one job a
// generic route started, captured at start so a provider config edited later does not change
// how a job already running is settled. It rides on the job row's params column as JSON.
type JobSnapshot struct {
	Job                schemas.GenericJobConfig    `json:"job"`
	Usage              *schemas.GenericUsageConfig `json:"usage,omitempty"`                // the mapping the terminal poll response is priced with, fully merged
	IgnoreUpstreamCost bool                        `json:"ignore_upstream_cost,omitempty"` // the provider's ignore_provider_cost at start
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
		return nil, errors.New("generic job has no params")
	}
	var snap JobSnapshot
	if err := sonic.Unmarshal([]byte(params), &snap); err != nil {
		return nil, err
	}
	if snap.Job.StatusPath == "" || snap.Job.Poll.Path == "" {
		return nil, errors.New("generic job params carry no poll request")
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
	path, rawQuery, _ := strings.Cut(sub(s.Job.Poll.Path), "?")
	req := JobPollRequest{Method: method, Path: path, RawQuery: rawQuery, Headers: make(map[string]string, len(s.Job.Poll.Headers)+1)}
	for name, value := range s.Job.Poll.Headers {
		req.Headers[strings.ToLower(name)] = sub(value)
	}
	if s.Job.Poll.Body != "" {
		req.Body = []byte(sub(s.Job.Poll.Body))
		req.ContentType = s.Job.Poll.ContentType
		if req.ContentType == "" {
			req.ContentType = "application/json"
		}
		req.Headers["content-type"] = req.ContentType
	}
	return req
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
	return 3600
}

// Price runs the terminal poll response through the job's usage mapping, the same way a
// buffered relay prices a reply, and returns the usage plus the detail the settlement row keeps.
func (s *JobSnapshot) Price(jobID, status, contentType string, body []byte) (*schemas.BifrostPassthroughUsage, *schemas.PassthroughJobAccounting) {
	u := resolveUsage(s.Usage, nil)
	u.ignoreUpstreamCost = s.IgnoreUpstreamCost
	fields := u.parse(contentType, body)
	usage := u.build(fields)
	detail := &schemas.PassthroughJobAccounting{JobID: jobID, Status: status, CostPerUnit: u.costPerUnit, CostPerRequest: u.costPerRequest}
	if fields.hasUnits {
		detail.Units = schemas.Ptr(fields.units)
		detail.UnitsBilled = schemas.Ptr(u.billedUnits(fields.units))
	}
	if fields.hasCost {
		detail.ExtractedCost = schemas.Ptr(fields.cost)
	}
	return usage, detail
}

// mergeUsageConfig overlays override on base field by field, the schema-level twin of
// resolveUsage, so a snapshot carries one self-contained mapping.
func mergeUsageConfig(base, override *schemas.GenericUsageConfig) *schemas.GenericUsageConfig {
	if base == nil && override == nil {
		return nil
	}
	out := schemas.GenericUsageConfig{}
	apply := func(u *schemas.GenericUsageConfig) {
		if u == nil {
			return
		}
		if u.ModelPath != "" {
			out.ModelPath = u.ModelPath
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
		if u.DisableOptimisticParsing {
			out.DisableOptimisticParsing = true
		}
	}
	apply(base)
	apply(override)
	return &out
}

// jobSnapshotFor builds the snapshot a route's job block is settled with: the job block, the
// mapping merged provider → route → job, and the provider's cost policy.
func jobSnapshotFor(cfg *schemas.GenericProviderConfig, route schemas.GenericRouteConfig, ignoreUpstreamCost bool) (string, error) {
	if route.Job == nil {
		return "", nil
	}
	snap := JobSnapshot{
		Job:                *route.Job,
		Usage:              mergeUsageConfig(mergeUsageConfig(cfg.Usage, route.Usage), route.Job.Usage),
		IgnoreUpstreamCost: ignoreUpstreamCost,
	}
	// The job's own usage block, once merged, is not needed twice.
	snap.Job.Usage = nil
	data, err := sonic.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// jobHandle reads the job id a start response names and returns the handle the logging plugin
// records, or nil when the response names none (the call is then billed as an ordinary one).
func (route *compiledRoute) jobHandle(contentType string, body []byte) *schemas.PassthroughProviderJob {
	if route == nil || route.job == nil || len(body) == 0 {
		return nil
	}
	if !looksJSON(contentType, body) {
		return nil
	}
	v := providerUtils.GetJSONField(body, route.job.IDPath)
	if !v.Exists() {
		return nil
	}
	id := strings.TrimSpace(v.String())
	if id == "" {
		return nil
	}
	return &schemas.PassthroughProviderJob{ID: id, IntervalSeconds: route.jobInterval, Params: route.jobParams}
}
