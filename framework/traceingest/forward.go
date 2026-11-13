package traceingest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

// ForwardRequest is one received ingest request, kept to replay to a project's upstream vendor.
type ForwardRequest struct {
	Source      string // which adapter received it
	Path        string // the path below the adapter's base, with its leading slash
	ContentType string
	Headers     map[string]string // request headers the upstream needs to see again (x-bt-parent)
	Body        []byte
	// ProjectID and ProjectName are the Bifrost project the request was stored under.
	ProjectID   string
	ProjectName string
}

type forwardJob struct {
	request ForwardRequest
	target  tables.TraceForwardTarget
	attempt int
	due     time.Time
}

// forwardDelays are the waits before each retry of a failed replay. After the last one the request
// is dropped: forwarding is best effort and never holds up Bifrost's own ingest.
var forwardDelays = []time.Duration{2 * time.Second, 10 * time.Second, time.Minute, 5 * time.Minute}

const forwardQueueLimit = 5000

// Forwarder replays ingest requests to an upstream vendor after Bifrost has stored them. It lets a
// team try Bifrost without leaving the vendor they use: the SDK has one URL, so Bifrost passes the
// data on. A replay runs after the SDK was answered and cannot change that answer.
//
// The upstream gets the request as the SDK sent it. Masking rules shape what Bifrost stores, not
// what is forwarded.
type Forwarder struct {
	client *http.Client
	logger schemas.Logger

	mu     sync.Mutex
	queue  []*forwardJob
	wake   chan struct{}
	done   chan struct{}
	wg     sync.WaitGroup
	closed sync.Once
	// Braintrust state: where each upstream's API lives, and the upstream's id for each project.
	braintrustAPI      map[string]string
	braintrustProjects map[string]string
}

// NewForwarder starts the replay worker.
func NewForwarder(client *http.Client, logger schemas.Logger) *Forwarder {
	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
			// A redirect is not followed: it would carry the upstream credential to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	f := &Forwarder{
		client: client, logger: logger, wake: make(chan struct{}, 1), done: make(chan struct{}),
		braintrustAPI: make(map[string]string), braintrustProjects: make(map[string]string),
	}
	f.wg.Add(1)
	go f.loop()
	return f
}

// Close stops the worker. Requests still queued are dropped.
func (f *Forwarder) Close() {
	f.closed.Do(func() { close(f.done) })
	f.wg.Wait()
}

// Enqueue queues a request for replay when the project forwards this source. It never blocks: a
// full queue drops the request.
func (f *Forwarder) Enqueue(project *tables.TableTraceProject, request ForwardRequest) {
	if f == nil || project == nil || project.Forward == nil || !project.Forward.Enabled || project.Forward.Vendor != request.Source {
		return
	}
	request.ProjectID, request.ProjectName = project.ID, project.Name
	f.mu.Lock()
	if len(f.queue) >= forwardQueueLimit {
		f.mu.Unlock()
		if f.logger != nil {
			f.logger.Warn("traceingest: forward queue is full, dropping a request for project %s", project.Name)
		}
		return
	}
	f.queue = append(f.queue, &forwardJob{request: request, target: *project.Forward, due: time.Now()})
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// Pending reports how many requests wait for replay.
func (f *Forwarder) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queue)
}

func (f *Forwarder) loop() {
	defer f.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-f.done:
			return
		case <-f.wake:
		case <-ticker.C:
		}
		f.drain(time.Now())
	}
}

// drain replays every job that is due and requeues the ones that fail.
func (f *Forwarder) drain(now time.Time) {
	f.mu.Lock()
	var due, later []*forwardJob
	for _, job := range f.queue {
		if job.due.After(now) {
			later = append(later, job)
		} else {
			due = append(due, job)
		}
	}
	f.queue = later
	f.mu.Unlock()
	for _, job := range due {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := f.send(ctx, job)
		cancel()
		if err == nil {
			continue
		}
		if job.attempt >= len(forwardDelays) {
			if f.logger != nil {
				f.logger.Warn("traceingest: giving up forwarding a request of project %s to %s: %v", job.request.ProjectName, job.target.BaseURL, err)
			}
			continue
		}
		job.due = time.Now().Add(forwardDelays[job.attempt])
		job.attempt++
		f.mu.Lock()
		f.queue = append(f.queue, job)
		f.mu.Unlock()
	}
}

func (f *Forwarder) send(ctx context.Context, job *forwardJob) error {
	if job.request.Source == logstore.TraceSourceBraintrust {
		return f.sendBraintrust(ctx, job)
	}
	headers := map[string]string{"Content-Type": job.request.ContentType}
	for name, value := range job.request.Headers {
		headers[name] = value
	}
	_, err := f.post(ctx, strings.TrimRight(job.target.BaseURL, "/")+job.request.Path, headers, job.target.Headers, job.request.Body)
	return finalOrRetry(err)
}

// post sends one request upstream. A 4xx other than 408 and 429 is final: replaying the same bytes
// would fail the same way, so it is reported as done.
func (f *Forwarder) post(ctx context.Context, url string, headers, targetHeaders map[string]string, body []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil // a URL that cannot be built will never work
	}
	for name, value := range headers {
		if value != "" {
			request.Header.Set(name, value)
		}
	}
	// The target's own headers go last: they carry the upstream credential.
	for name, value := range targetHeaders {
		request.Header.Set(name, value)
	}
	response, err := f.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	switch {
	case response.StatusCode < 300:
		return data, nil
	case response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusRequestTimeout && response.StatusCode != http.StatusTooManyRequests:
		if f.logger != nil {
			f.logger.Warn("traceingest: upstream %s refused a forwarded request with %d", url, response.StatusCode)
		}
		return nil, errForwardRefused
	}
	return nil, fmt.Errorf("upstream answered %d", response.StatusCode)
}

// errForwardRefused marks a replay the upstream rejected for good.
var errForwardRefused = fmt.Errorf("upstream refused the request")

// sendBraintrust replays a logs3 flush to Braintrust. Rows carry the project id Bifrost issued,
// which Braintrust does not know, so the same project is registered upstream by name and each
// row's project id is rewritten to the upstream's. Rows of an experiment are not forwarded: an
// experiment's upstream id cannot be derived from the id Bifrost issued.
func (f *Forwarder) sendBraintrust(ctx context.Context, job *forwardJob) error {
	if job.request.Path != "/logs3" {
		return nil
	}
	base := strings.TrimRight(job.target.BaseURL, "/")
	f.mu.Lock()
	api, upstreamProject := f.braintrustAPI[base], f.braintrustProjects[base+"\x00"+job.request.ProjectID]
	f.mu.Unlock()
	jsonHeaders := map[string]string{"Content-Type": "application/json"}
	if api == "" {
		data, err := f.post(ctx, base+"/api/apikey/login", jsonHeaders, job.target.Headers, nil)
		if err != nil {
			return finalOrRetry(err)
		}
		var login struct {
			OrgInfo []struct {
				APIURL string `json:"api_url"`
			} `json:"org_info"`
		}
		if err := sonic.Unmarshal(data, &login); err != nil || len(login.OrgInfo) == 0 || login.OrgInfo[0].APIURL == "" {
			return fmt.Errorf("upstream login returned no api url")
		}
		api = strings.TrimRight(login.OrgInfo[0].APIURL, "/")
		f.mu.Lock()
		f.braintrustAPI[base] = api
		f.mu.Unlock()
	}
	if upstreamProject == "" {
		body, _ := schemas.MarshalSorted(map[string]any{"project_name": job.request.ProjectName})
		data, err := f.post(ctx, base+"/api/project/register", jsonHeaders, job.target.Headers, body)
		if err != nil {
			return finalOrRetry(err)
		}
		var registered struct {
			Project struct {
				ID string `json:"id"`
			} `json:"project"`
		}
		if err := sonic.Unmarshal(data, &registered); err != nil || registered.Project.ID == "" {
			return fmt.Errorf("upstream project register returned no id")
		}
		upstreamProject = registered.Project.ID
		f.mu.Lock()
		f.braintrustProjects[base+"\x00"+job.request.ProjectID] = upstreamProject
		f.mu.Unlock()
	}
	var flush map[string]any
	if err := sonic.Unmarshal(job.request.Body, &flush); err != nil {
		return nil
	}
	rows, _ := flush["rows"].([]any)
	forwarded := make([]any, 0, len(rows))
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if experiment, _ := row["experiment_id"].(string); experiment != "" {
			continue
		}
		if project, _ := row["project_id"].(string); project != job.request.ProjectID {
			continue // a row of another project in the same flush: that project's own target handles it
		}
		row["project_id"] = upstreamProject
		forwarded = append(forwarded, row)
	}
	if len(forwarded) == 0 {
		return nil
	}
	flush["rows"] = forwarded
	body, err := schemas.MarshalSorted(flush)
	if err != nil {
		return nil
	}
	_, err = f.post(ctx, api+"/logs3", jsonHeaders, job.target.Headers, body)
	return finalOrRetry(err)
}

// finalOrRetry turns a refusal into "done" and passes every other error on for a retry.
func finalOrRetry(err error) error {
	if err == errForwardRefused {
		return nil
	}
	return err
}
