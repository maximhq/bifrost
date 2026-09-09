package webhooks

import (
	"context"
	"crypto/hmac"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

// --- fakes -----------------------------------------------------------------

type recordingLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *recordingLogger) Debug(string, ...any) {}
func (l *recordingLogger) Info(string, ...any)  {}
func (l *recordingLogger) Warn(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(msg, args...))
}
func (l *recordingLogger) Error(string, ...any)                   {}
func (l *recordingLogger) Fatal(string, ...any)                   {}
func (l *recordingLogger) SetLevel(schemas.LogLevel)              {}
func (l *recordingLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l *recordingLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func (l *recordingLogger) warnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warns)
}

// fakeConfigStore mirrors the real queue semantics — due predicate on claim,
// claimed_by + live-lease fencing on reschedule/delete — over an in-memory map.
type fakeConfigStore struct {
	mu             sync.Mutex
	jobs           map[string]*tables.TableWebhookJob
	failures       map[string]int
	successes      map[string]int
	createFailures int // fail this many CreateWebhookJob calls before succeeding
}

func newFakeConfigStore() *fakeConfigStore {
	return &fakeConfigStore{
		jobs:      make(map[string]*tables.TableWebhookJob),
		failures:  make(map[string]int),
		successes: make(map[string]int),
	}
}

func (f *fakeConfigStore) CreateWebhookJob(ctx context.Context, job *tables.TableWebhookJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createFailures > 0 {
		f.createFailures--
		return fmt.Errorf("simulated storage failure")
	}
	if job.NextAttemptAt.IsZero() {
		job.NextAttemptAt = time.Now()
	}
	job.CreatedAt = time.Now()
	copied := *job
	f.jobs[job.ID] = &copied
	return nil
}

func (f *fakeConfigStore) ListDueWebhookJobs(ctx context.Context, limit int) ([]tables.TableWebhookJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	var due []tables.TableWebhookJob
	for _, job := range f.jobs {
		if job.NextAttemptAt.After(now) {
			continue
		}
		if job.ClaimedUntil != nil && job.ClaimedUntil.After(now) {
			continue
		}
		due = append(due, *job)
	}
	return due, nil
}

func (f *fakeConfigStore) ClaimWebhookJob(ctx context.Context, id, runnerID string, leaseUntil time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	now := time.Now()
	if !ok || job.NextAttemptAt.After(now) || (job.ClaimedUntil != nil && job.ClaimedUntil.After(now)) {
		return false, nil
	}
	job.ClaimedBy = runnerID
	job.ClaimedUntil = &leaseUntil
	return true, nil
}

func (f *fakeConfigStore) RescheduleWebhookJob(ctx context.Context, id, runnerID string, leaseUntil, nextAttemptAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok || job.ClaimedBy != runnerID || job.ClaimedUntil == nil || !job.ClaimedUntil.Equal(leaseUntil) {
		return fmt.Errorf("webhook job not found or no longer owned by caller")
	}
	job.AttemptCount++
	job.NextAttemptAt = nextAttemptAt
	job.ClaimedBy = ""
	job.ClaimedUntil = nil
	return nil
}

func (f *fakeConfigStore) DeleteWebhookJob(ctx context.Context, id, runnerID string, leaseUntil time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok || job.ClaimedBy != runnerID || job.ClaimedUntil == nil || !job.ClaimedUntil.Equal(leaseUntil) {
		return fmt.Errorf("webhook job not found or no longer owned by caller")
	}
	delete(f.jobs, id)
	return nil
}

func (f *fakeConfigStore) RecordWebhookEndpointSuccess(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successes[id]++
	f.failures[id] = 0
	return nil
}

func (f *fakeConfigStore) RecordWebhookEndpointFailure(ctx context.Context, id string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[id]++
	return f.failures[id], nil
}

func (f *fakeConfigStore) job(id string) *tables.TableWebhookJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok {
		return nil
	}
	copied := *job
	return &copied
}

func (f *fakeConfigStore) jobCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.jobs)
}

type fakeLogStore struct {
	mu         sync.Mutex
	asyncJobs  map[string]*logstore.AsyncJob
	deliveries []logstore.WebhookDelivery
	findErr    error // when set, FindAsyncJobByID returns it (simulates a DB outage)
}

func newFakeLogStore() *fakeLogStore {
	return &fakeLogStore{asyncJobs: make(map[string]*logstore.AsyncJob)}
}

func (f *fakeLogStore) FindAsyncJobByID(ctx context.Context, id string) (*logstore.AsyncJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.findErr != nil {
		return nil, f.findErr
	}
	job, ok := f.asyncJobs[id]
	if !ok {
		return nil, logstore.ErrNotFound
	}
	return job, nil
}

func (f *fakeLogStore) CreateWebhookDelivery(ctx context.Context, delivery *logstore.WebhookDelivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries = append(f.deliveries, *delivery)
	return nil
}

func (f *fakeLogStore) deliveryList() []logstore.WebhookDelivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]logstore.WebhookDelivery(nil), f.deliveries...)
}

type fakeResolver struct {
	mu        sync.Mutex
	endpoints map[string]*tables.TableWebhookEndpoint
}

func newFakeResolver(endpoints ...*tables.TableWebhookEndpoint) *fakeResolver {
	r := &fakeResolver{endpoints: make(map[string]*tables.TableWebhookEndpoint)}
	for _, endpoint := range endpoints {
		r.endpoints[endpoint.ID] = endpoint
	}
	return r
}

func (r *fakeResolver) WebhookEndpointByID(id string) (*tables.TableWebhookEndpoint, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	endpoint, ok := r.endpoints[id]
	return endpoint, ok
}

// --- helpers ---------------------------------------------------------------

func testEndpoint(id, url string) *tables.TableWebhookEndpoint {
	return &tables.TableWebhookEndpoint{
		ID:                  id,
		Name:                id,
		URL:                 url,
		Secret:              &schemas.SecretVar{Val: testSecret},
		AllowPrivateNetwork: true, // test receivers listen on loopback
		Events:              []tables.WebhookEvent{tables.WebhookEventAsyncJobCompleted, tables.WebhookEventAsyncJobFailed},
	}
}

func dueWebhookJob(id, endpointID, asyncJobID string) *tables.TableWebhookJob {
	return &tables.TableWebhookJob{
		ID:            id,
		EndpointID:    endpointID,
		AsyncJobID:    asyncJobID,
		Event:         tables.WebhookEventAsyncJobCompleted,
		NextAttemptAt: time.Now().Add(-time.Second),
	}
}

type dispatcherFixture struct {
	dispatcher  *Dispatcher
	configStore *fakeConfigStore
	logStore    *fakeLogStore
	resolver    *fakeResolver
	logger      *recordingLogger
}

func newFixture(t *testing.T, endpoints ...*tables.TableWebhookEndpoint) *dispatcherFixture {
	t.Helper()
	f := &dispatcherFixture{
		configStore: newFakeConfigStore(),
		logStore:    newFakeLogStore(),
		resolver:    newFakeResolver(endpoints...),
		logger:      &recordingLogger{},
	}
	f.dispatcher = NewDispatcher(context.Background(), "", 30*24*time.Hour, f.configStore, f.logStore, f.resolver, f.logger)
	t.Cleanup(f.dispatcher.Stop)
	return f
}

// --- tests -----------------------------------------------------------------

func TestEnqueueJobEvent(t *testing.T) {
	endpoint := testEndpoint("ep-1", "https://receiver.example/hook")
	f := newFixture(t, endpoint)
	endpointID := endpoint.ID

	job := &logstore.AsyncJob{ID: "job-1", Status: schemas.AsyncJobStatusCompleted, WebhookEndpointID: &endpointID}
	f.dispatcher.EnqueueJobEvent(context.Background(), job)

	require.Equal(t, 1, f.configStore.jobCount())
	var queued *tables.TableWebhookJob
	for id := range f.configStore.jobs {
		queued = f.configStore.job(id)
	}
	assert.Equal(t, "ep-1", queued.EndpointID)
	assert.Equal(t, "job-1", queued.AsyncJobID)
	assert.Equal(t, tables.WebhookEventAsyncJobCompleted, queued.Event)

	select {
	case <-f.dispatcher.signal:
	default:
		t.Fatal("enqueue must wake the worker")
	}
}

func TestEnqueueJobEventSkips(t *testing.T) {
	endpoint := testEndpoint("ep-1", "https://receiver.example/hook")
	f := newFixture(t, endpoint)
	endpointID := endpoint.ID
	missingID := "ep-missing"

	// No endpoint requested on the job.
	f.dispatcher.EnqueueJobEvent(context.Background(), &logstore.AsyncJob{ID: "j1", Status: schemas.AsyncJobStatusCompleted})
	// Non-terminal status.
	f.dispatcher.EnqueueJobEvent(context.Background(), &logstore.AsyncJob{ID: "j2", Status: schemas.AsyncJobStatusProcessing, WebhookEndpointID: &endpointID})
	// Endpoint vanished after submit.
	f.dispatcher.EnqueueJobEvent(context.Background(), &logstore.AsyncJob{ID: "j3", Status: schemas.AsyncJobStatusCompleted, WebhookEndpointID: &missingID})
	// Endpoint disabled after submit.
	endpoint.Disabled = true
	f.dispatcher.EnqueueJobEvent(context.Background(), &logstore.AsyncJob{ID: "j4", Status: schemas.AsyncJobStatusCompleted, WebhookEndpointID: &endpointID})
	// Endpoint unsubscribed from the failure event.
	endpoint.Disabled = false
	endpoint.Events = []tables.WebhookEvent{tables.WebhookEventAsyncJobCompleted}
	f.dispatcher.EnqueueJobEvent(context.Background(), &logstore.AsyncJob{ID: "j5", Status: schemas.AsyncJobStatusFailed, WebhookEndpointID: &endpointID})

	assert.Zero(t, f.configStore.jobCount())
}

func TestEnqueueJobEventWarnsWhenRetriesExhausted(t *testing.T) {
	endpoint := testEndpoint("ep-1", "https://receiver.example/hook")
	f := newFixture(t, endpoint)
	f.configStore.createFailures = enqueueAttempts
	endpointID := endpoint.ID

	f.dispatcher.EnqueueJobEvent(context.Background(), &logstore.AsyncJob{ID: "job-1", Status: schemas.AsyncJobStatusCompleted, WebhookEndpointID: &endpointID})

	assert.Zero(t, f.configStore.jobCount())
	assert.Equal(t, 1, f.logger.warnCount(), "a lost notification must be operator-visible")
}

func TestDeliverySuccessFlow(t *testing.T) {
	var received struct {
		mu      sync.Mutex
		body    []byte
		headers http.Header
	}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.mu.Lock()
		defer received.mu.Unlock()
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		received.body = requestBody
		received.headers = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	f := newFixture(t, endpoint)
	f.logStore.asyncJobs["job-1"] = testAsyncJob()
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	// History first: one delivered attempt.
	deliveries := f.logStore.deliveryList()
	require.Len(t, deliveries, 1)
	assert.Equal(t, logstore.WebhookDeliveryOutcomeDelivered, deliveries[0].Outcome)
	assert.Equal(t, "wh-1", deliveries[0].WebhookID)
	assert.Equal(t, 1, deliveries[0].AttemptNo)
	assert.Equal(t, http.StatusOK, deliveries[0].StatusCode)
	assert.Empty(t, deliveries[0].Error)
	require.NotNil(t, deliveries[0].ExpiresAt)

	// Terminal outcome retires the queue row and resets the failure streak.
	assert.Nil(t, f.configStore.job("wh-1"))
	assert.Equal(t, 1, f.configStore.successes["ep-1"])

	// The wire request is a verifiable Standard Webhooks delivery.
	received.mu.Lock()
	defer received.mu.Unlock()
	assert.Equal(t, "wh-1", received.headers.Get("webhook-id"))
	assert.Equal(t, "async_job.completed", received.headers.Get("X-Bifrost-Event"))
	assert.Equal(t, "application/json", received.headers.Get("Content-Type"))
	require.NotEmpty(t, received.headers.Get("webhook-timestamp"))
	var ts int64
	_, err := fmt.Sscanf(received.headers.Get("webhook-timestamp"), "%d", &ts)
	require.NoError(t, err)
	expected, err := Sign(testSecret, "wh-1", time.Unix(ts, 0), received.body)
	require.NoError(t, err)
	assert.True(t, hmac.Equal([]byte(expected), []byte(received.headers.Get("webhook-signature"))))
}

func TestDeliveryRetryableFailureReschedules(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	f := newFixture(t, endpoint)
	f.logStore.asyncJobs["job-1"] = testAsyncJob()
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	before := time.Now()
	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	deliveries := f.logStore.deliveryList()
	require.Len(t, deliveries, 1)
	assert.Equal(t, logstore.WebhookDeliveryOutcomeRetryableFailure, deliveries[0].Outcome)
	assert.Equal(t, http.StatusInternalServerError, deliveries[0].StatusCode)
	assert.Contains(t, deliveries[0].Error, "receiver responded 500")

	job := f.configStore.job("wh-1")
	require.NotNil(t, job, "retryable failures keep the job queued")
	assert.Equal(t, 1, job.AttemptCount)
	assert.Empty(t, job.ClaimedBy)
	assert.Nil(t, job.ClaimedUntil)
	// First backoff step is 30s ±20%.
	delay := job.NextAttemptAt.Sub(before)
	assert.GreaterOrEqual(t, delay, 24*time.Second)
	assert.LessOrEqual(t, delay, 37*time.Second)
	assert.Equal(t, 1, f.configStore.failures["ep-1"])
}

func TestDeliveryExhaustsAttemptBudget(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "still broken", http.StatusServiceUnavailable)
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	f := newFixture(t, endpoint)
	f.logStore.asyncJobs["job-1"] = testAsyncJob()
	job := dueWebhookJob("wh-1", "ep-1", "job-1")
	job.AttemptCount = 4 // the next attempt is the fifth and final one (default: 4 retries + the original attempt)
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), job))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	deliveries := f.logStore.deliveryList()
	require.Len(t, deliveries, 1)
	assert.Equal(t, logstore.WebhookDeliveryOutcomeExhausted, deliveries[0].Outcome)
	assert.Equal(t, 5, deliveries[0].AttemptNo)
	assert.Nil(t, f.configStore.job("wh-1"), "an exhausted delivery is retired")
}

func TestDeliveryPermanentFailure(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	f := newFixture(t, endpoint)
	f.logStore.asyncJobs["job-1"] = testAsyncJob()
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	deliveries := f.logStore.deliveryList()
	require.Len(t, deliveries, 1)
	assert.Equal(t, logstore.WebhookDeliveryOutcomePermanentFailure, deliveries[0].Outcome)
	assert.Nil(t, f.configStore.job("wh-1"))
	assert.Equal(t, 1, f.configStore.failures["ep-1"])
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("redirect target must never be called")
	}))
	defer target.Close()
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	f := newFixture(t, endpoint)
	f.logStore.asyncJobs["job-1"] = testAsyncJob()
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	deliveries := f.logStore.deliveryList()
	require.Len(t, deliveries, 1)
	assert.Equal(t, logstore.WebhookDeliveryOutcomePermanentFailure, deliveries[0].Outcome, "3xx is terminal because redirects are refused")
	assert.Equal(t, http.StatusFound, deliveries[0].StatusCode)
}

func TestPrivateDialerStillBlocksLinkLocal(t *testing.T) {
	dial := newPrivateDialContext()
	_, err := dial(context.Background(), "tcp", "169.254.169.254:80")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "link-local")

	_, err = dial(context.Background(), "tcp", "0.0.0.0:80")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unspecified")
}

func TestStrictClientBlocksPrivateReceivers(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("loopback receiver must not be reachable without allow_private_network")
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	endpoint.AllowPrivateNetwork = false
	f := newFixture(t, endpoint)
	f.logStore.asyncJobs["job-1"] = testAsyncJob()
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	deliveries := f.logStore.deliveryList()
	require.Len(t, deliveries, 1)
	assert.Equal(t, logstore.WebhookDeliveryOutcomeRetryableFailure, deliveries[0].Outcome)
	assert.Zero(t, deliveries[0].StatusCode)
}

func TestExpiredAsyncJobDeliversDegradedPayload(t *testing.T) {
	var body []byte
	var mu sync.Mutex
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		body = requestBody
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	endpoint.IncludeResponse = true // even opted-in endpoints get the thin expired body
	f := newFixture(t, endpoint)
	// No async job seeded: the row expired before this attempt.
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	deliveries := f.logStore.deliveryList()
	require.Len(t, deliveries, 1)
	assert.Equal(t, logstore.WebhookDeliveryOutcomeDelivered, deliveries[0].Outcome)

	mu.Lock()
	defer mu.Unlock()
	data := decodeEnvelope(t, body)["data"].(map[string]any)
	assert.Equal(t, true, data["result_expired"])
	assert.Equal(t, "job-1", data["job_id"])
	assert.NotContains(t, data, "response")
}

func TestRetryBackoffHighRetryNumbersStayPositive(t *testing.T) {
	// initial<<retryNo used to overflow int64 around retry 30 for second-scale
	// initials, producing a NEGATIVE backoff (an immediate-retry hot loop) on
	// endpoints tuned with a large retry budget.
	tuning := tuningFor(nil) // defaults: initial 30s, max 30m
	for _, retryNo := range []int{30, 40, 1000} {
		delay := retryBackoff(retryNo, tuning)
		assert.Greater(t, delay, time.Duration(0), "retry %d", retryNo)
		assert.LessOrEqual(t, delay, defaultRetryBackoffMax, "retry %d", retryNo)
		assert.GreaterOrEqual(t, delay, time.Duration(float64(defaultRetryBackoffMax)*0.8), "retry %d sits at the jittered cap", retryNo)
	}
}

func TestRenderFailureRetiresJobAsPermanent(t *testing.T) {
	// A stored response that cannot re-serialize makes rendering fail
	// deterministically; the job must be retired with a recorded permanent
	// failure instead of looping on lease expiry forever.
	endpoint := testEndpoint("ep-1", "https://receiver.example/hook")
	endpoint.IncludeResponse = true
	f := newFixture(t, endpoint)
	invalid := testAsyncJob()
	invalid.Response = `{"broken`
	f.logStore.asyncJobs["job-1"] = invalid
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	deliveries := f.logStore.deliveryList()
	require.Len(t, deliveries, 1)
	assert.Equal(t, logstore.WebhookDeliveryOutcomePermanentFailure, deliveries[0].Outcome)
	assert.Contains(t, deliveries[0].Error, "rendering payload failed")
	assert.Nil(t, f.configStore.job("wh-1"), "the job must be retired, not left claimed")
	assert.Zero(t, f.configStore.failures["ep-1"], "no receiver attempt happened, the streak must not move")
}

func TestDeliverRefusesPlaintextHTTPWithoutOptIn(t *testing.T) {
	// Validation ties http to allow_private_network at write time; the client
	// re-checks so a row that bypassed validation never sends signed payloads
	// and custom headers in cleartext.
	endpoint := testEndpoint("ep-1", "http://receiver.example/hook")
	endpoint.AllowPrivateNetwork = false
	c := newDeliveryClient()
	result := c.deliver(context.Background(), endpoint, tables.WebhookEventAsyncJobCompleted, "wh-1", []byte("{}"), time.Now().UTC())
	assert.Zero(t, result.statusCode)
	assert.Contains(t, result.errText, "https is required")
}

func TestEndpointGoneRetiresJobWithoutCounters(t *testing.T) {
	f := newFixture(t) // resolver knows no endpoints
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-gone", "job-1")))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	deliveries := f.logStore.deliveryList()
	require.Len(t, deliveries, 1)
	assert.Equal(t, logstore.WebhookDeliveryOutcomePermanentFailure, deliveries[0].Outcome)
	assert.Contains(t, deliveries[0].Error, "deleted or disabled")
	assert.Nil(t, f.configStore.job("wh-1"))
	assert.Zero(t, f.configStore.failures["ep-gone"], "no receiver attempt happened, the streak must not move")
}

func TestDisabledEndpointRetiresJobKeepingRequestID(t *testing.T) {
	endpoint := testEndpoint("ep-1", "http://127.0.0.1:0")
	endpoint.Disabled = true
	f := newFixture(t, endpoint)
	asyncJob := testAsyncJob()
	asyncJob.RequestID = "req-abc"
	f.logStore.asyncJobs["job-1"] = asyncJob
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	deliveries := f.logStore.deliveryList()
	require.Len(t, deliveries, 1)
	assert.Equal(t, logstore.WebhookDeliveryOutcomePermanentFailure, deliveries[0].Outcome)
	assert.Contains(t, deliveries[0].Error, "deleted or disabled")
	assert.Equal(t, "req-abc", deliveries[0].RequestID,
		"the request id must survive so the terminal history row reconciles with its LLM log")
	assert.Nil(t, f.configStore.job("wh-1"))
}

func TestDisabledEndpointDefersRetireOnTransientLookupError(t *testing.T) {
	endpoint := testEndpoint("ep-1", "http://127.0.0.1:0")
	endpoint.Disabled = true
	f := newFixture(t, endpoint)
	// A transient storage error (not ErrNotFound) must not permanently retire
	// the job: leaving it claimed lets a later attempt correlate the request id
	// once the database recovers.
	f.logStore.findErr = fmt.Errorf("database temporarily unavailable")
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	assert.Empty(t, f.logStore.deliveryList(), "no terminal history row on a transient lookup failure")
	assert.NotNil(t, f.configStore.job("wh-1"), "the job stays claimed for a retry after the lease expires")
}

func TestClaimLosersDoNothing(t *testing.T) {
	endpoint := testEndpoint("ep-1", "https://receiver.example/hook")
	f := newFixture(t, endpoint)
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	// Another node holds a live lease on the job.
	lease := time.Now().Add(time.Minute)
	won, err := f.configStore.ClaimWebhookJob(context.Background(), "wh-1", "other-node", lease)
	require.NoError(t, err)
	require.True(t, won)

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	assert.Empty(t, f.logStore.deliveryList(), "a lost claim must not attempt delivery")
	job := f.configStore.job("wh-1")
	assert.Equal(t, "other-node", job.ClaimedBy)
}

func TestRestartRecoveryClaimsExpiredLease(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	f := newFixture(t, endpoint)
	f.logStore.asyncJobs["job-1"] = testAsyncJob()

	// A previous process died mid-attempt: the job is claimed but the lease
	// has already lapsed.
	job := dueWebhookJob("wh-1", "ep-1", "job-1")
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), job))
	expiredLease := time.Now().Add(-time.Second)
	won, err := f.configStore.ClaimWebhookJob(context.Background(), "wh-1", "dead-node", expiredLease)
	require.NoError(t, err)
	require.True(t, won)

	f.dispatcher.Start()
	assert.Eventually(t, func() bool {
		return f.configStore.job("wh-1") == nil && len(f.logStore.deliveryList()) == 1
	}, 2*time.Second, 10*time.Millisecond, "the worker must reclaim and deliver the orphaned job")
	f.dispatcher.Stop()

	deliveries := f.logStore.deliveryList()
	assert.Equal(t, logstore.WebhookDeliveryOutcomeDelivered, deliveries[0].Outcome)
}

func TestDeliverTest(t *testing.T) {
	var headers http.Header
	var mu sync.Mutex
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		headers = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	f := newFixture(t, endpoint)

	status, err := f.dispatcher.DeliverTest(context.Background(), endpoint, tables.WebhookEventAsyncJobCompleted)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, status)

	// The test delivery must not touch the queue, history, or counters.
	assert.Zero(t, f.configStore.jobCount())
	assert.Empty(t, f.logStore.deliveryList())
	assert.Zero(t, f.configStore.successes["ep-1"])

	mu.Lock()
	defer mu.Unlock()
	assert.NotEmpty(t, headers.Get("webhook-signature"), "test deliveries go through the production signing path")
}

func TestRetryBackoffBounds(t *testing.T) {
	tuning := tuningFor(nil) // defaults: initial 30s, max 30m
	expected := []time.Duration{
		30 * time.Second, // retry 1
		time.Minute,      // retry 2
		2 * time.Minute,  // retry 3
		4 * time.Minute,  // retry 4
		8 * time.Minute,  // retry 5
		16 * time.Minute, // retry 6
		30 * time.Minute, // retry 7: 32m capped at the max
		30 * time.Minute, // retry 8: stays at the cap
	}
	for i, base := range expected {
		retryNo := i + 1
		upper := time.Duration(float64(base) * 1.2)
		if upper > defaultRetryBackoffMax {
			upper = defaultRetryBackoffMax
		}
		for range 50 {
			delay := retryBackoff(retryNo, tuning)
			assert.GreaterOrEqual(t, delay, time.Duration(float64(base)*0.8), "retry %d", retryNo)
			assert.LessOrEqual(t, delay, upper, "retry %d", retryNo)
		}
	}
}

func TestPerEndpointTuningOverrides(t *testing.T) {
	// Defaults apply when the endpoint sets nothing.
	endpoint := testEndpoint("ep-1", "https://receiver.example/hook")
	tuning := tuningFor(endpoint)
	assert.Equal(t, defaultMaxRetries, tuning.maxRetries)
	assert.Equal(t, defaultRetryBackoffInitial, tuning.retryBackoffInitial)
	assert.Equal(t, defaultRetryBackoffMax, tuning.retryBackoffMax)
	assert.Equal(t, 10*time.Second, tuning.attemptTimeout)
	assert.Equal(t, 256*1024, tuning.maxResponsePayloadBytes)
	assert.Equal(t, defaultMaxConcurrentDeliveries, tuning.maxConcurrentDeliveries)

	// Endpoint-set knobs win over the defaults.
	endpoint.MaxRetries = 1
	endpoint.RetryBackoffInitialSeconds = 5
	endpoint.RetryBackoffMaxSeconds = 60
	endpoint.AttemptTimeoutSeconds = 3
	endpoint.MaxResponsePayloadKBs = 1
	endpoint.MaxConcurrentDeliveries = 4
	tuning = tuningFor(endpoint)
	assert.Equal(t, 1, tuning.maxRetries)
	assert.Equal(t, 5*time.Second, tuning.retryBackoffInitial)
	assert.Equal(t, time.Minute, tuning.retryBackoffMax)
	assert.Equal(t, 3*time.Second, tuning.attemptTimeout)
	assert.Equal(t, 1024, tuning.maxResponsePayloadBytes)
	assert.Equal(t, 4, tuning.maxConcurrentDeliveries)
}

func TestPerEndpointMaxRetriesExhaustsEarlier(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	endpoint.MaxRetries = 1 // two attempts total for this endpoint
	f := newFixture(t, endpoint)
	f.logStore.asyncJobs["job-1"] = testAsyncJob()
	job := dueWebhookJob("wh-1", "ep-1", "job-1")
	job.AttemptCount = 1 // the next attempt is the second and final one
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), job))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	deliveries := f.logStore.deliveryList()
	require.Len(t, deliveries, 1)
	assert.Equal(t, logstore.WebhookDeliveryOutcomeExhausted, deliveries[0].Outcome)
	assert.Nil(t, f.configStore.job("wh-1"))
}

func TestPerEndpointPayloadCap(t *testing.T) {
	var body []byte
	var mu sync.Mutex
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		body = requestBody
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	endpoint.IncludeResponse = true
	endpoint.MaxResponsePayloadKBs = 1
	f := newFixture(t, endpoint)
	job := testAsyncJob()
	job.Response = `{"padding":"` + strings.Repeat("x", 2048) + `"}`
	f.logStore.asyncJobs["job-1"] = job
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	mu.Lock()
	defer mu.Unlock()
	data := decodeEnvelope(t, body)["data"].(map[string]any)
	assert.NotContains(t, data, "response", "the endpoint's own cap applies")
	assert.Equal(t, true, data["response_omitted"])
}

func TestPerEndpointConcurrencyLimit(t *testing.T) {
	endpoint := testEndpoint("ep-1", "https://receiver.example/hook")
	endpoint.MaxConcurrentDeliveries = 1
	f := newFixture(t, endpoint)

	require.Equal(t, 1, f.dispatcher.concurrencyLimitFor("ep-1"), "the endpoint's own cap applies")
	assert.Equal(t, defaultMaxConcurrentDeliveries, f.dispatcher.concurrencyLimitFor("ep-unknown"))

	require.True(t, f.dispatcher.reserveEndpointSlot("ep-1", 1))
	assert.False(t, f.dispatcher.reserveEndpointSlot("ep-1", 1), "no second slot below the cap")
	f.dispatcher.releaseEndpointSlot("ep-1")
	assert.True(t, f.dispatcher.reserveEndpointSlot("ep-1", 1), "released slots become available again")
}

func TestDeliverySendsCustomHeaders(t *testing.T) {
	var received http.Header
	var mu sync.Mutex
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		received = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	endpoint := testEndpoint("ep-1", receiver.URL)
	endpoint.Headers = map[string]schemas.SecretVar{
		"Authorization": {Val: "Bearer receiver-token"},
		// A reserved name that slipped past validation must not reach the wire.
		"webhook-signature": {Val: "forged"},
	}
	f := newFixture(t, endpoint)
	f.logStore.asyncJobs["job-1"] = testAsyncJob()
	require.NoError(t, f.configStore.CreateWebhookJob(context.Background(), dueWebhookJob("wh-1", "ep-1", "job-1")))

	f.dispatcher.processDue(*f.configStore.job("wh-1"))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "Bearer receiver-token", received.Get("Authorization"))
	assert.NotEqual(t, "forged", received.Get("webhook-signature"), "reserved headers always come from the delivery client")
}
