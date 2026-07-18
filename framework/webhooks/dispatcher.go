package webhooks

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

// ConfigStore is the subset of the configstore the dispatcher needs: the
// webhook job queue plus the endpoint operational counters.
type ConfigStore interface {
	CreateWebhookJob(ctx context.Context, job *tables.TableWebhookJob) error
	ListDueWebhookJobs(ctx context.Context, limit int) ([]tables.TableWebhookJob, error)
	ClaimWebhookJob(ctx context.Context, id, runnerID string, leaseUntil time.Time) (bool, error)
	RescheduleWebhookJob(ctx context.Context, id, runnerID string, leaseUntil, nextAttemptAt time.Time) error
	DeleteWebhookJob(ctx context.Context, id, runnerID string, leaseUntil time.Time) error
	RecordWebhookEndpointSuccess(ctx context.Context, id string) error
	RecordWebhookEndpointFailure(ctx context.Context, id string) (int, error)
}

// LogStore is the subset of the logstore the dispatcher needs: reading the
// async job at send time and appending per-attempt delivery history.
type LogStore interface {
	FindAsyncJobByID(ctx context.Context, id string) (*logstore.AsyncJob, error)
	CreateWebhookDelivery(ctx context.Context, delivery *logstore.WebhookDelivery) error
}

// EndpointResolver serves webhook endpoint configuration from memory. It is
// consulted on every enqueue and every delivery attempt, so changes to an
// endpoint (disable, delete, secret rotation) take effect on the next
// attempt without a database read.
type EndpointResolver interface {
	WebhookEndpointByID(id string) (*tables.TableWebhookEndpoint, bool)
}

// Default delivery tuning, applied when an endpoint does not set its own
// knobs. Retry defaults mirror the provider retry convention (max retries
// plus exponential backoff between an initial and a max step) and are
// deliberately tighter than classic event-bus webhook schedules: the payload
// announces a TTL-bounded inference result, so the default window
// (30s + 1m + 2m + 4m ≈ 7.5m) sits well inside the default 1h result TTL.
// Longer receiver outages are the caller's poll-reconciliation territory.
const (
	defaultMaxRetries              = 4
	defaultRetryBackoffInitial     = 30 * time.Second
	defaultRetryBackoffMax         = 30 * time.Minute
	defaultAttemptTimeout          = 10 * time.Second
	defaultMaxResponsePayloadKBs   = 256
	defaultMaxConcurrentDeliveries = 10
)

// enqueueAttempts bounds the inline insert retries when queueing a delivery
// for a finished job; exhausting them loses the notification (accepted crash
// window) and is surfaced with a Warn log.
const enqueueAttempts = 3

// scanBatchSize bounds how many due jobs one queue scan loads and offers for
// claiming, so a large backlog drains across scans instead of being read
// into memory at once.
const scanBatchSize = 64

// pollInterval is the queue scan fallback cadence; the in-process signal
// channel usually wakes the worker sooner.
const pollInterval = 5 * time.Second

// Dispatcher drains the webhook job queue: it claims due jobs, performs one
// signed delivery attempt per claim, records history, and reschedules or
// retires the job. One Dispatcher runs per node; the atomic claim decides a
// single owner per attempt, so running it everywhere needs no coordination.
type Dispatcher struct {
	runnerID         string
	historyRetention time.Duration
	configStore      ConfigStore
	logStore         LogStore
	resolver         EndpointResolver
	client           *deliveryClient
	logger           schemas.Logger

	signal   chan struct{}
	baseCtx  context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once

	mu          sync.Mutex
	perEndpoint map[string]int
}

// NewDispatcher builds a stopped dispatcher; call Start to launch its worker.
// The dispatcher's lifetime is bounded by ctx as well as by Stop. runnerID
// fences queue claims — the node id in a cluster, empty on a single node.
// historyRetention sets expires_at on delivery history rows and must be
// positive. Delivery tuning (retries, backoff, timeouts, payload caps,
// concurrency) is per endpoint.
func NewDispatcher(ctx context.Context, runnerID string, historyRetention time.Duration, configStore ConfigStore, logStore LogStore, resolver EndpointResolver, logger schemas.Logger) *Dispatcher {
	ctx, cancel := context.WithCancel(ctx)
	return &Dispatcher{
		runnerID:         runnerID,
		historyRetention: historyRetention,
		configStore:      configStore,
		logStore:         logStore,
		resolver:         resolver,
		client:           newDeliveryClient(),
		logger:           logger,
		signal:           make(chan struct{}, 1),
		baseCtx:          ctx,
		cancel:           cancel,
		perEndpoint:      make(map[string]int),
	}
}

// Start launches the queue worker. The first scan runs immediately, which is
// also what recovers deliveries left claimed or due by a previous process.
func (d *Dispatcher) Start() {
	d.wg.Add(1)
	go d.run()
}

// Stop cancels the worker and waits for in-flight deliveries to finish.
func (d *Dispatcher) Stop() {
	d.stopOnce.Do(func() {
		d.cancel()
		d.wg.Wait()
	})
}

// EnqueueJobEvent queues the webhook delivery for a job that just reached a
// terminal state. Callers invoke it right after the terminal job update; it
// performs no receiver I/O — just the queue insert plus a worker wake-up.
func (d *Dispatcher) EnqueueJobEvent(ctx context.Context, job *logstore.AsyncJob) {
	if job == nil || job.WebhookEndpointID == nil {
		return
	}
	event, ok := EventForJobStatus(job.Status)
	if !ok {
		return
	}
	endpointID := *job.WebhookEndpointID
	endpoint, ok := d.resolver.WebhookEndpointByID(endpointID)
	if !ok || endpoint.Disabled || !subscribesTo(endpoint, event) {
		d.logger.Debug("webhooks: skipping enqueue for job %s: endpoint %s is gone, disabled, or unsubscribed", job.ID, endpointID)
		return
	}
	webhookJob := &tables.TableWebhookJob{
		ID:         uuid.NewString(),
		EndpointID: endpoint.ID,
		AsyncJobID: job.ID,
		Event:      event,
	}
	var err error
	for attempt := range enqueueAttempts {
		if err = d.configStore.CreateWebhookJob(ctx, webhookJob); err == nil {
			break
		}
		if errors.Is(err, configstore.ErrAlreadyExists) {
			err = nil
			break
		}
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
	if err != nil {
		// The notification is lost; make that operator-visible. This is a
		// rare storage-failure path, not request-path noise.
		d.logger.Warn("webhooks: dropping notification for job %s to endpoint %s: enqueue failed after %d attempts: %v", job.ID, endpointID, enqueueAttempts, err)
		return
	}
	d.Wake()
}

// DeliverTest sends one signed sample of the given event through the exact
// production delivery path — same rendering, signing, client, and
// redirect/TLS policy — without touching the queue, history, or failure
// counters. It returns the receiver's status code.
func (d *Dispatcher) DeliverTest(ctx context.Context, endpoint *tables.TableWebhookEndpoint, event tables.WebhookEvent) (int, error) {
	now := time.Now().UTC()
	sample := &logstore.AsyncJob{
		ID:          "test-" + uuid.NewString(),
		Status:      statusForEvent(event),
		RequestType: schemas.ChatCompletionRequest,
		StatusCode:  200,
		CreatedAt:   now,
		CompletedAt: &now,
	}
	if event == tables.WebhookEventAsyncJobFailed {
		sample.StatusCode = 500
	}
	tuning := tuningFor(endpoint)
	body, err := renderPayload(sample, event, false, tuning.maxResponsePayloadBytes, now)
	if err != nil {
		return 0, err
	}
	ctx, cancelAttempt := context.WithTimeout(ctx, tuning.attemptTimeout)
	defer cancelAttempt()
	result := d.client.deliver(ctx, endpoint, event, uuid.NewString(), body, now)
	if result.statusCode == 0 {
		return 0, errors.New(result.errText)
	}
	return result.statusCode, nil
}

// Wake nudges the worker to scan the queue now instead of waiting for the
// next poll tick — non-blocking; a full signal buffer means a scan is already
// pending. Callers use it after inserting queue rows outside EnqueueJobEvent
// (e.g. redelivery).
func (d *Dispatcher) Wake() {
	select {
	case d.signal <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) run() {
	defer d.wg.Done()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	d.dispatchOnce()
	for {
		select {
		case <-d.baseCtx.Done():
			return
		case <-d.signal:
			d.dispatchOnce()
		case <-ticker.C:
			d.dispatchOnce()
		}
	}
}

// dispatchOnce scans for due jobs and hands each to a delivery goroutine,
// bounded by the per-endpoint cap. Jobs skipped by the cap stay due and are
// picked up by a later scan.
func (d *Dispatcher) dispatchOnce() {
	jobs, err := d.configStore.ListDueWebhookJobs(d.baseCtx, scanBatchSize)
	if err != nil {
		d.logger.Warn("webhooks: listing due jobs failed: %v", err)
		return
	}
	for _, job := range jobs {
		if !d.reserveEndpointSlot(job.EndpointID, d.concurrencyLimitFor(job.EndpointID)) {
			continue
		}
		d.wg.Add(1)
		go func(job tables.TableWebhookJob) {
			defer d.wg.Done()
			defer d.releaseEndpointSlot(job.EndpointID)
			d.processDue(job)
		}(job)
	}
}

// concurrencyLimitFor resolves an endpoint's concurrent-delivery cap; a
// missing endpoint takes the default (its jobs get retired on attempt).
func (d *Dispatcher) concurrencyLimitFor(endpointID string) int {
	endpoint, _ := d.resolver.WebhookEndpointByID(endpointID)
	return tuningFor(endpoint).maxConcurrentDeliveries
}

func (d *Dispatcher) reserveEndpointSlot(endpointID string, limit int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.perEndpoint[endpointID] >= limit {
		return false
	}
	d.perEndpoint[endpointID]++
	return true
}

func (d *Dispatcher) releaseEndpointSlot(endpointID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.perEndpoint[endpointID] <= 1 {
		delete(d.perEndpoint, endpointID)
	} else {
		d.perEndpoint[endpointID]--
	}
}

// endpointTuning holds the effective delivery knobs for one attempt:
// per-endpoint values where the endpoint sets them, dispatcher defaults
// otherwise.
type endpointTuning struct {
	maxRetries              int
	retryBackoffInitial     time.Duration
	retryBackoffMax         time.Duration
	attemptTimeout          time.Duration
	maxResponsePayloadBytes int
	maxConcurrentDeliveries int
}

// tuningFor resolves the effective knobs for an endpoint; a nil endpoint
// yields the defaults.
func tuningFor(endpoint *tables.TableWebhookEndpoint) endpointTuning {
	tuning := endpointTuning{
		maxRetries:              defaultMaxRetries,
		retryBackoffInitial:     defaultRetryBackoffInitial,
		retryBackoffMax:         defaultRetryBackoffMax,
		attemptTimeout:          defaultAttemptTimeout,
		maxResponsePayloadBytes: defaultMaxResponsePayloadKBs * 1024,
		maxConcurrentDeliveries: defaultMaxConcurrentDeliveries,
	}
	if endpoint == nil {
		return tuning
	}
	if endpoint.MaxRetries > 0 {
		tuning.maxRetries = endpoint.MaxRetries
	}
	if endpoint.RetryBackoffInitialSeconds > 0 {
		tuning.retryBackoffInitial = time.Duration(endpoint.RetryBackoffInitialSeconds) * time.Second
	}
	if endpoint.RetryBackoffMaxSeconds > 0 {
		tuning.retryBackoffMax = time.Duration(endpoint.RetryBackoffMaxSeconds) * time.Second
	}
	if endpoint.AttemptTimeoutSeconds > 0 {
		tuning.attemptTimeout = time.Duration(endpoint.AttemptTimeoutSeconds) * time.Second
	}
	if endpoint.MaxResponsePayloadKBs > 0 {
		tuning.maxResponsePayloadBytes = endpoint.MaxResponsePayloadKBs * 1024
	}
	if endpoint.MaxConcurrentDeliveries > 0 {
		tuning.maxConcurrentDeliveries = endpoint.MaxConcurrentDeliveries
	}
	return tuning
}

// processDue claims one due job and, if this node wins, performs a single
// delivery attempt.
func (d *Dispatcher) processDue(job tables.TableWebhookJob) {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Warn("webhooks: delivery attempt for job %s panicked: %v", job.ID, r)
		}
	}()
	// Resolve once; the same endpoint state sizes the lease and drives the
	// attempt. A missing endpoint still claims, so the job can be retired.
	endpoint, ok := d.resolver.WebhookEndpointByID(job.EndpointID)
	if !ok {
		endpoint = nil
	}
	tuning := tuningFor(endpoint)
	lease := max(2*tuning.attemptTimeout, 30*time.Second)
	// The lease expiry doubles as this claim's fencing token: terminal queue
	// mutations must present it, so an attempt that outlived its lease cannot
	// clobber the state of whoever reclaimed the job.
	leaseUntil := time.Now().UTC().Add(lease)
	won, err := d.configStore.ClaimWebhookJob(d.baseCtx, job.ID, d.runnerID, leaseUntil)
	if err != nil {
		d.logger.Warn("webhooks: claiming job %s failed: %v", job.ID, err)
		return
	}
	if !won {
		return
	}
	d.attempt(job, endpoint, tuning, leaseUntil)
}

// attempt runs one claimed delivery attempt end to end.
func (d *Dispatcher) attempt(job tables.TableWebhookJob, endpoint *tables.TableWebhookEndpoint, tuning endpointTuning, leaseUntil time.Time) {
	now := time.Now().UTC()
	attemptNo := job.AttemptCount + 1

	if endpoint == nil || endpoint.Disabled {
		// Nothing to deliver to anymore; retire the job with a terminal history
		// record. No counter updates: there was no receiver attempt, and the
		// endpoint is already gone or disabled. Carry the async job's request id
		// so the history row still reconciles with its LLM log.
		var requestID string
		asyncJob, findErr := d.logStore.FindAsyncJobByID(d.baseCtx, job.AsyncJobID)
		switch {
		case findErr == nil:
			requestID = asyncJob.RequestID
		case errors.Is(findErr, logstore.ErrNotFound):
			// The job's result TTL lapsed; retire without a request id.
		default:
			// Transient storage error — leave the row claimed so a retry after
			// the lease expiry can retire it with correlation intact, matching
			// the delivery path below.
			d.logger.Warn("webhooks: reading async job %s failed: %v", job.AsyncJobID, findErr)
			return
		}
		d.finalize(job, attemptNo, requestID, attemptResult{errText: "webhook endpoint deleted or disabled"}, logstore.WebhookDeliveryOutcomePermanentFailure, now, false, tuning, leaseUntil)
		return
	}

	var body []byte
	var err error
	var requestID string
	asyncJob, findErr := d.logStore.FindAsyncJobByID(d.baseCtx, job.AsyncJobID)
	switch {
	case findErr == nil:
		requestID = asyncJob.RequestID
		body, err = renderPayload(asyncJob, job.Event, endpoint.IncludeResponse, tuning.maxResponsePayloadBytes, now)
	case errors.Is(findErr, logstore.ErrNotFound):
		// The job row must have existed for this delivery to be queued, so
		// not-found can only mean its result TTL lapsed: deliver the
		// degraded body that says so instead of dropping the notification.
		body, err = renderExpiredPayload(&job, now)
	default:
		// Storage hiccup — not an attempt. Leave the row claimed; the lease
		// expiry re-offers it to any node.
		d.logger.Warn("webhooks: reading async job %s failed: %v", job.AsyncJobID, findErr)
		return
	}
	if err != nil {
		// Rendering is deterministic on stored state, so retrying cannot
		// succeed — leaving the job claimed would rerun the same failure on
		// every lease expiry, forever and without history. Retire it as a
		// recorded permanent failure instead (no receiver was attempted, so
		// the endpoint counters must not move).
		d.finalize(job, attemptNo, requestID, attemptResult{errText: fmt.Sprintf("rendering payload failed: %v", err)}, logstore.WebhookDeliveryOutcomePermanentFailure, now, false, tuning, leaseUntil)
		return
	}

	attemptCtx, cancelAttempt := context.WithTimeout(d.baseCtx, tuning.attemptTimeout)
	result := d.client.deliver(attemptCtx, endpoint, job.Event, job.ID, body, now)
	cancelAttempt()

	outcome := classify(result)
	if outcome == logstore.WebhookDeliveryOutcomeRetryableFailure && attemptNo > tuning.maxRetries {
		outcome = logstore.WebhookDeliveryOutcomeExhausted
	}
	d.finalize(job, attemptNo, requestID, result, outcome, now, true, tuning, leaseUntil)
}

// finalize persists an attempt: history first, then the queue row, then the
// endpoint counters. If the history insert fails the queue row is left
// claimed on purpose — the lease expires, the attempt reruns, and the
// receiver's webhook-id dedupe absorbs the duplicate. touchCounters is false
// when no receiver was attempted (endpoint gone), which must not move the
// failure streak.
func (d *Dispatcher) finalize(job tables.TableWebhookJob, attemptNo int, requestID string, result attemptResult, outcome logstore.WebhookDeliveryOutcome, now time.Time, touchCounters bool, tuning endpointTuning, leaseUntil time.Time) {
	expiresAt := now.Add(d.historyRetention)
	history := &logstore.WebhookDelivery{
		ID:         uuid.NewString(),
		WebhookID:  job.ID,
		EndpointID: job.EndpointID,
		AsyncJobID: job.AsyncJobID,
		RequestID:  requestID,
		Event:      job.Event,
		AttemptNo:  attemptNo,
		Outcome:    outcome,
		StatusCode: result.statusCode,
		Error:      result.errText,
		CreatedAt:  now,
		ExpiresAt:  &expiresAt,
	}
	if err := d.logStore.CreateWebhookDelivery(d.baseCtx, history); err != nil {
		d.logger.Warn("webhooks: recording delivery history for job %s failed, attempt will rerun after lease expiry: %v", job.ID, err)
		return
	}

	if outcome == logstore.WebhookDeliveryOutcomeRetryableFailure {
		next := now.Add(retryBackoff(attemptNo, tuning))
		if err := d.configStore.RescheduleWebhookJob(d.baseCtx, job.ID, d.runnerID, leaseUntil, next); err != nil {
			d.logger.Warn("webhooks: rescheduling job %s failed: %v", job.ID, err)
		}
	} else {
		if err := d.configStore.DeleteWebhookJob(d.baseCtx, job.ID, d.runnerID, leaseUntil); err != nil {
			d.logger.Warn("webhooks: retiring job %s failed: %v", job.ID, err)
		}
	}

	if !touchCounters {
		return
	}
	if outcome == logstore.WebhookDeliveryOutcomeDelivered {
		if err := d.configStore.RecordWebhookEndpointSuccess(d.baseCtx, job.EndpointID); err != nil {
			d.logger.Warn("webhooks: recording success for endpoint %s failed: %v", job.EndpointID, err)
		}
		return
	}
	if _, err := d.configStore.RecordWebhookEndpointFailure(d.baseCtx, job.EndpointID); err != nil {
		d.logger.Warn("webhooks: recording failure for endpoint %s failed: %v", job.EndpointID, err)
	}
}

// retryBackoff returns the delay before retry number retryNo (1-based, so
// the retry after the first failed attempt is retry 1): exponential
// initial * 2^(retryNo-1) capped at the effective max, with ±20% jitter —
// the same shape the core request path uses for provider retries.
func retryBackoff(retryNo int, tuning endpointTuning) time.Duration {
	// Double stepwise instead of shifting: initial<<n overflows int64 around
	// n=30 for second-scale initials, which would turn the backoff negative
	// (a hot retry loop) once an endpoint allows enough retries.
	backoff := tuning.retryBackoffInitial
	for i := 1; i < retryNo && backoff < tuning.retryBackoffMax; i++ {
		if backoff > tuning.retryBackoffMax/2 {
			backoff = tuning.retryBackoffMax
			break
		}
		backoff *= 2
	}
	backoff = min(backoff, tuning.retryBackoffMax)
	jitter := float64(backoff) * (0.8 + 0.4*rand.Float64())
	return min(time.Duration(jitter), tuning.retryBackoffMax)
}

func subscribesTo(endpoint *tables.TableWebhookEndpoint, event tables.WebhookEvent) bool {
	return slices.Contains(endpoint.Events, event)
}
