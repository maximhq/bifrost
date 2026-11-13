package traceingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

var (
	// ErrUnauthorized is returned when dashboard auth is on and a request carries no valid credential.
	ErrUnauthorized = errors.New("dashboard credentials are missing or not valid")
	// ErrForbidden is returned when a scoped credential names a project it may not write to.
	ErrForbidden = errors.New("this credential cannot write to this project")
	// ErrRateLimited is returned when spans arrive faster than the configured cap.
	ErrRateLimited = errors.New("trace ingest is over its span rate limit")
	// ErrClosed is returned once the service has been closed.
	ErrClosed = errors.New("trace ingest is closed")
)

// ConfigStore is the part of the config store the ingest path reads and writes.
type ConfigStore interface {
	GetTraceProject(ctx context.Context, id string) (*tables.TableTraceProject, error)
	GetTraceProjectByName(ctx context.Context, name string) (*tables.TableTraceProject, error)
	CreateTraceProject(ctx context.Context, project *tables.TableTraceProject) error
}

// Config tunes the ingest path. The zero value is usable.
type Config struct {
	// CommitWindow is an extra wait for more requests before a batch is written. Zero, the default,
	// writes as soon as nothing more is queued: a lone request is written at once, and requests that
	// arrive during a write share the next one. Set it only for a store that is slow per write.
	CommitWindow time.Duration
	// MaxBatchParts caps one commit. A request is never split across commits.
	MaxBatchParts int
	// RollupInterval is how often traces marked dirty get their rollup rebuilt.
	RollupInterval time.Duration
	// PartRetention is how long a received part is kept for refolds. A span outlives its parts.
	PartRetention time.Duration
	// MaxSpansPerSecond caps ingest across all callers. Zero means no cap.
	MaxSpansPerSecond int
	// DefaultProjectName is the project a request that names none goes to. It is created on first use.
	DefaultProjectName string
	// RetentionDays returns the default trace retention, for projects that set none. Zero keeps forever.
	RetentionDays func() int
	// ContentLoggingDisabled reports the global content switch. When on, no span content is stored.
	ContentLoggingDisabled func() bool
}

func (c Config) withDefaults() Config {
	if c.MaxBatchParts <= 0 {
		c.MaxBatchParts = 2000
	}
	if c.RollupInterval <= 0 {
		c.RollupInterval = 250 * time.Millisecond
	}
	if c.PartRetention <= 0 {
		c.PartRetention = 7 * 24 * time.Hour
	}
	if c.DefaultProjectName == "" {
		c.DefaultProjectName = DefaultProjectName
	}
	return c
}

// DefaultProjectName is the project traces go to when the SDK names none.
const DefaultProjectName = "default"

// Principal is who an ingest request acts as. SDKs authenticate with the dashboard's own
// credentials, which act as the admin: every project, created on first use. Enterprise can narrow a
// principal to some projects, for a scoped API key.
type Principal struct {
	// ProjectIDs are the projects a scoped principal may write to. Ignored when AllProjects is set.
	ProjectIDs        []string
	LogContent        bool
	CanCreateProjects bool
	AllProjects       bool
}

// AdminPrincipal is the principal of a request that passed dashboard auth, or of any request when
// dashboard auth is off.
func AdminPrincipal() *Principal {
	return &Principal{LogContent: true, CanCreateProjects: true, AllProjects: true}
}

func (p *Principal) allows(projectID string) bool {
	if p.AllProjects {
		return true
	}
	for _, id := range p.ProjectIDs {
		if id == projectID {
			return true
		}
	}
	return false
}

// SpanHook runs on every folded span before it is stored. The gateway matcher and the cost
// resolver are hooks.
type SpanHook func(ctx context.Context, span *logstore.TraceSpan)

// Service is the ingest path shared by every vendor adapter.
type Service struct {
	store  logstore.TraceStore
	config ConfigStore
	cfg    Config
	logger schemas.Logger
	hooks  []SpanHook
	pricer Pricer
	// partHooks run on each received part before it is stored; traceHooks on each completed trace.
	partHooks  []PartHook
	traceHooks []TraceHook
	export     *exportState

	requests chan *commitRequest
	done     chan struct{}
	wg       sync.WaitGroup
	closing  sync.Once

	mu       sync.Mutex
	projects map[string]cachedProject
	rate     rateWindow
	// retries schedules another look at traces whose calls are not all linked yet.
	retries map[logstore.TraceKey]matchRetry
}

type cachedProject struct {
	project *tables.TableTraceProject
	expires time.Time
}

type matchRetry struct {
	attempt int
	at      time.Time
}

type rateWindow struct {
	second int64
	count  int
}

type commitRequest struct {
	parts  []*logstore.TraceSpanPart
	spans  []*SpanPart // same order as parts
	hidden bool
	result chan error
}

const projectCacheTTL = 30 * time.Second

// NewService builds the ingest service and starts its committer and rollup worker.
func NewService(store logstore.TraceStore, config ConfigStore, cfg Config, logger schemas.Logger) *Service {
	s := &Service{
		store: store, config: config, cfg: cfg.withDefaults(), logger: logger,
		requests: make(chan *commitRequest, 1024), done: make(chan struct{}),
		projects: make(map[string]cachedProject),
		retries:  make(map[logstore.TraceKey]matchRetry),
	}
	s.wg.Add(2)
	go s.commitLoop()
	go s.rollupLoop()
	return s
}

// AddSpanHook registers a hook. Call it before the first request.
func (s *Service) AddSpanHook(hook SpanHook) {
	s.hooks = append(s.hooks, hook)
}

// PartHook runs on every received span part before anything of it is stored, the kept raw part
// included. It may rewrite the part's content: the enterprise build masks content this way. It does
// not run when the request's content is not stored at all.
type PartHook func(ctx context.Context, project *tables.TableTraceProject, part *SpanPart)

// AddPartHook registers a part hook. Call it before the first request.
func (s *Service) AddPartHook(hook PartHook) {
	s.partHooks = append(s.partHooks, hook)
}

// TraceHook runs each time the rollup of a completed trace is rebuilt: once when the trace
// completes, and again when a late part changes it. It runs on the rollup loop, so a hook that does
// slow work (the enterprise build runs evaluators) hands it off.
type TraceHook func(ctx context.Context, trace *logstore.Trace)

// AddTraceHook registers a trace hook. Call it before the first request.
func (s *Service) AddTraceHook(hook TraceHook) {
	s.traceHooks = append(s.traceHooks, hook)
}

// traceRolledUp runs the trace hooks and the connector export on a trace whose rollup was rebuilt.
// A trace still arriving is left until it completes.
func (s *Service) traceRolledUp(ctx context.Context, trace *logstore.Trace) {
	if trace == nil || trace.InProgress {
		return
	}
	for _, hook := range s.traceHooks {
		hook(ctx, trace)
	}
	s.exportTrace(ctx, trace)
}

// Store returns the trace store the service writes to.
func (s *Service) Store() logstore.TraceStore { return s.store }

// Close stops the workers after the queued requests are written.
func (s *Service) Close() {
	s.closing.Do(func() { close(s.done) })
	s.wg.Wait()
	s.waitForExports()
}

// InvalidateProjects drops every cached project, so an edit in the dashboard takes effect now.
func (s *Service) InvalidateProjects() {
	s.mu.Lock()
	clear(s.projects)
	s.mu.Unlock()
}

// ResolveProject finds the project a request names, by id or by name. An empty ref means the
// default project. A name that does not exist is created when the principal may create projects,
// which is how SDKs register a project on first use.
func (s *Service) ResolveProject(ctx context.Context, principal *Principal, ref string) (*tables.TableTraceProject, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" && !principal.AllProjects && len(principal.ProjectIDs) == 1 {
		ref = principal.ProjectIDs[0]
	}
	if ref == "" {
		ref = s.cfg.DefaultProjectName
	}
	now := time.Now()
	s.mu.Lock()
	cached, ok := s.projects[ref]
	s.mu.Unlock()
	var project *tables.TableTraceProject
	if ok && now.Before(cached.expires) {
		project = cached.project
	} else {
		var err error
		project, err = s.config.GetTraceProject(ctx, ref)
		if errors.Is(err, configstore.ErrNotFound) {
			project, err = s.config.GetTraceProjectByName(ctx, ref)
		}
		if errors.Is(err, configstore.ErrNotFound) {
			return s.createProject(ctx, principal, ref)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to look up trace project: %w", err)
		}
		s.mu.Lock()
		s.projects[ref] = cachedProject{project: project, expires: now.Add(projectCacheTTL)}
		s.mu.Unlock()
	}
	if !principal.allows(project.ID) {
		return nil, ErrForbidden
	}
	return project, nil
}

// createProject makes the project a request named.
func (s *Service) createProject(ctx context.Context, principal *Principal, name string) (*tables.TableTraceProject, error) {
	if !principal.CanCreateProjects {
		return nil, fmt.Errorf("%w: project %q does not exist", ErrForbidden, name)
	}
	project := &tables.TableTraceProject{Name: name}
	if err := s.config.CreateTraceProject(ctx, project); err != nil {
		if !errors.Is(err, configstore.ErrTraceProjectExists) {
			return nil, fmt.Errorf("failed to create trace project: %w", err)
		}
		// Another request created it first.
		existing, findErr := s.config.GetTraceProjectByName(ctx, name)
		if findErr != nil {
			return nil, fmt.Errorf("failed to look up trace project: %w", findErr)
		}
		project = existing
	}
	if !principal.allows(project.ID) {
		return nil, ErrForbidden
	}
	return project, nil
}

// allowRate counts spans against the per-second cap.
func (s *Service) allowRate(spans int) bool {
	if s.cfg.MaxSpansPerSecond <= 0 {
		return true
	}
	second := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rate.second != second {
		s.rate = rateWindow{second: second}
	}
	if s.rate.count+spans > s.cfg.MaxSpansPerSecond && s.rate.count > 0 {
		return false
	}
	s.rate.count += spans
	return true
}

// Ingest stores span parts for one project and returns once they are committed. A nil error means
// the caller may acknowledge the SDK's request: the parts survive a restart.
func (s *Service) Ingest(ctx context.Context, principal *Principal, project *tables.TableTraceProject, source string, spans []*SpanPart) error {
	if len(spans) == 0 {
		return nil
	}
	if !principal.allows(project.ID) {
		return ErrForbidden
	}
	if !s.allowRate(len(spans)) {
		return ErrRateLimited
	}
	hidden := !principal.LogContent || (s.cfg.ContentLoggingDisabled != nil && s.cfg.ContentLoggingDisabled())
	now := time.Now().UTC()
	partExpiry := now.Add(s.cfg.PartRetention)
	request := &commitRequest{hidden: hidden, result: make(chan error, 1)}
	for _, span := range spans {
		if err := span.Validate(); err != nil {
			return err
		}
		if hidden {
			span.Input, span.Output, span.Expected = nil, nil, nil
		} else {
			for _, hook := range s.partHooks {
				hook(ctx, project, span)
			}
		}
		stored, err := span.toStoredPart(project.ID, source, now, &partExpiry)
		if err != nil {
			return err
		}
		span.ReceivedAt, span.ID = now, stored.ID
		request.parts = append(request.parts, stored)
		request.spans = append(request.spans, span)
	}
	select {
	case s.requests <- request:
	case <-s.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		// The commit may still land. The SDK retries, and a resent part is stored once.
		return ctx.Err()
	}
}

// spanExpiry is when a span of the project stops being kept, nil for never.
func (s *Service) spanExpiry(project *tables.TableTraceProject, from time.Time) *time.Time {
	days := 0
	if project != nil && project.RetentionDays != nil {
		days = *project.RetentionDays
	} else if s.cfg.RetentionDays != nil {
		days = s.cfg.RetentionDays()
	}
	if days <= 0 {
		return nil
	}
	return new(from.AddDate(0, 0, days))
}

// commitLoop is the group commit: requests that arrive within one commit window share one write.
func (s *Service) commitLoop() {
	defer s.wg.Done()
	for {
		var batch []*commitRequest
		select {
		case request := <-s.requests:
			batch = append(batch, request)
		case <-s.done:
			s.drain()
			return
		}
		parts := len(batch[0].parts)
		// Take what is already queued. Requests that arrived while the last batch was being written
		// are waiting here, so batches grow with load by themselves and an idle service adds no wait.
	queued:
		for parts < s.cfg.MaxBatchParts {
			select {
			case request := <-s.requests:
				batch = append(batch, request)
				parts += len(request.parts)
			default:
				break queued
			}
		}
		if s.cfg.CommitWindow > 0 {
			timer := time.NewTimer(s.cfg.CommitWindow)
		collect:
			for parts < s.cfg.MaxBatchParts {
				select {
				case request := <-s.requests:
					batch = append(batch, request)
					parts += len(request.parts)
				case <-timer.C:
					break collect
				}
			}
			timer.Stop()
		}
		s.commit(batch)
	}
}

// drain writes whatever was queued when the service closed.
func (s *Service) drain() {
	for {
		select {
		case request := <-s.requests:
			s.commit([]*commitRequest{request})
		default:
			return
		}
	}
}

func (s *Service) commit(batch []*commitRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := s.write(ctx, batch)
	if err != nil && s.logger != nil {
		s.logger.Error("traceingest: failed to commit %d requests: %v", len(batch), err)
	}
	for _, request := range batch {
		request.result <- err
	}
}

type spanRef struct {
	projectID, traceID, spanID string
}

// write stores a batch: the parts first, then every span they touch, refolded.
func (s *Service) write(ctx context.Context, batch []*commitRequest) error {
	var parts []*logstore.TraceSpanPart
	fresh := make(map[spanRef][]*SpanPart)
	meta := make(map[spanRef]*commitRequest)
	source := make(map[spanRef]string)
	// held maps a part id to the part this batch already holds decoded.
	held := make(map[string]*SpanPart)
	for _, request := range batch {
		parts = append(parts, request.parts...)
		for i, span := range request.spans {
			ref := spanRef{request.parts[i].ProjectID, span.TraceID, span.SpanID}
			held[span.ID] = span
			fresh[ref] = append(fresh[ref], span)
			meta[ref] = request
			if source[ref] == "" {
				source[ref] = request.parts[i].Source
			}
		}
	}
	if _, err := s.store.BatchCreateTraceSpanParts(ctx, parts); err != nil {
		return fmt.Errorf("failed to store span parts: %w", err)
	}

	byTrace := make(map[logstore.TraceKey][]string)
	for ref := range fresh {
		key := logstore.TraceKey{ProjectID: ref.projectID, TraceID: ref.traceID}
		byTrace[key] = append(byTrace[key], ref.spanID)
	}
	var folded []*logstore.TraceSpan
	var scores []*logstore.TraceScore
	var comments []*logstore.TraceComment
	dirty := make([]logstore.TraceKey, 0, len(byTrace))
	for key, spanIDs := range byTrace {
		dirty = append(dirty, key)
		// The parts are read back so a part another request or node stored is folded in too. Only
		// their headers: a part this batch just wrote is already in memory, and decoding its JSON
		// again was a third of ingest allocation. Payloads are fetched for the other parts alone.
		headers, err := s.store.ListTraceSpanPartHeadersForSpans(ctx, key.ProjectID, key.TraceID, spanIDs)
		if err != nil {
			return fmt.Errorf("failed to load span parts: %w", err)
		}
		var missing []string
		for _, header := range headers {
			if held[header.ID] == nil {
				missing = append(missing, header.ID)
			}
		}
		earlier, err := s.store.FindTraceSpanParts(ctx, missing)
		if err != nil {
			return fmt.Errorf("failed to load span parts: %w", err)
		}
		decoded := make(map[string]*SpanPart, len(earlier))
		for _, stored := range earlier {
			// A part that no longer decodes is skipped, never a reason to fail the batch.
			if part, err := partFromStored(stored); err == nil {
				decoded[stored.ID] = part
			}
		}
		all := make(map[string][]*SpanPart, len(spanIDs))
		for _, header := range headers {
			part := held[header.ID]
			if part != nil {
				// The stored receive time is the first one when the same part was sent before.
				part.ReceivedAt = header.ReceivedAt
			} else if part = decoded[header.ID]; part == nil {
				continue
			}
			all[header.SpanID] = append(all[header.SpanID], part)
		}
		spanKeys := make([]logstore.TraceSpanKey, len(spanIDs))
		for i, id := range spanIDs {
			spanKeys[i] = logstore.TraceSpanKey{TraceID: key.TraceID, SpanID: id}
		}
		existing, err := s.store.FindTraceSpans(ctx, key.ProjectID, spanKeys)
		if err != nil {
			return fmt.Errorf("failed to load stored spans: %w", err)
		}
		storedSpans := make(map[string]*logstore.TraceSpan, len(existing))
		for _, span := range existing {
			storedSpans[span.SpanID] = span
		}
		project, _ := s.cachedProject(ctx, key.ProjectID)
		for _, spanID := range spanIDs {
			ref := spanRef{key.ProjectID, key.TraceID, spanID}
			stored := storedSpans[spanID]
			inputs := all[spanID]
			hasBase := false
			for _, part := range inputs {
				hasBase = hasBase || !part.IsMerge
			}
			// Without the base among the stored parts, the stored span already holds the older
			// parts, so only this batch's parts are laid over it.
			if !hasBase && stored != nil {
				inputs = fresh[ref]
			}
			if len(inputs) == 0 {
				inputs = fresh[ref]
			}
			part := Fold(stored, inputs)
			span := part.Span(key.ProjectID, source[ref], stored)
			request := meta[ref]
			if stored == nil {
				span.ExpiresAt = s.spanExpiry(project, time.Now().UTC())
			} else if stored.Source != "" {
				span.Source = stored.Source
			}
			span.ContentHidden = span.ContentHidden || request.hidden
			for _, hook := range s.hooks {
				hook(ctx, span)
			}
			folded = append(folded, span)
			for name, value := range part.Scores {
				scores = append(scores, &logstore.TraceScore{
					ID: rowID("score", key.ProjectID, key.TraceID, spanID, name), ProjectID: key.ProjectID, TraceID: key.TraceID, SpanID: spanID,
					Name: name, Value: new(value), DataType: "numeric", Source: logstore.TraceAuthorSDK, ExpiresAt: span.ExpiresAt,
				})
			}
			for _, text := range part.Comments {
				comments = append(comments, &logstore.TraceComment{
					ID: rowID("comment", key.ProjectID, key.TraceID, spanID, text), ProjectID: key.ProjectID, TraceID: key.TraceID, SpanID: spanID,
					Text: text, Source: logstore.TraceAuthorSDK, ExpiresAt: span.ExpiresAt,
				})
			}
		}
	}
	if err := s.store.UpsertTraceSpans(ctx, folded); err != nil {
		return fmt.Errorf("failed to store spans: %w", err)
	}
	if err := s.store.UpsertTraceScores(ctx, scores); err != nil {
		return fmt.Errorf("failed to store span scores: %w", err)
	}
	for _, comment := range comments {
		if err := s.store.CreateTraceComment(ctx, comment); err != nil {
			return fmt.Errorf("failed to store span comment: %w", err)
		}
	}
	if err := s.store.MarkTracesDirty(ctx, dirty); err != nil {
		return fmt.Errorf("failed to queue trace rollups: %w", err)
	}
	return nil
}

// cachedProject loads a project by id through the short project cache.
func (s *Service) cachedProject(ctx context.Context, id string) (*tables.TableTraceProject, error) {
	now := time.Now()
	s.mu.Lock()
	cached, ok := s.projects[id]
	s.mu.Unlock()
	if ok && now.Before(cached.expires) {
		return cached.project, nil
	}
	project, err := s.config.GetTraceProject(ctx, id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.projects[id] = cachedProject{project: project, expires: now.Add(projectCacheTTL)}
	s.mu.Unlock()
	return project, nil
}

// rowID derives a stable id, so a score or comment sent again replaces its first copy.
func rowID(fields ...string) string {
	size := 0
	for _, field := range fields {
		size += len(field) + 4
	}
	// The hashed bytes are "<len>:<field>" for each field, in order. Stored ids depend on them.
	buf := make([]byte, 0, size)
	for _, field := range fields {
		buf = strconv.AppendInt(buf, int64(len(field)), 10)
		buf = append(buf, ':')
		buf = append(buf, field...)
	}
	sum := sha256.Sum256(buf)
	var encoded [sha256.Size * 2]byte
	hex.Encode(encoded[:], sum[:])
	return string(encoded[:40])
}

// rollupLoop rebuilds the rollup row of every trace marked dirty.
func (s *Service) rollupLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.cfg.RollupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := s.FlushRollups(ctx); err != nil && s.logger != nil {
				s.logger.Warn("traceingest: failed to rebuild trace rollups: %v", err)
			}
			cancel()
		case <-s.done:
			return
		}
	}
}

// FlushRollups links, prices and rolls up every trace waiting for it.
func (s *Service) FlushRollups(ctx context.Context) error {
	if err := s.requeueDueRetries(ctx, time.Now()); err != nil {
		return err
	}
	for {
		dirty, err := s.store.ListDirtyTraces(ctx, 200)
		if err != nil {
			return err
		}
		for _, trace := range dirty {
			retry, err := s.enrichTrace(ctx, trace.TraceKey)
			if err != nil {
				return err
			}
			rolled, err := s.store.RecomputeTraceRollup(ctx, trace.ProjectID, trace.TraceID)
			if err != nil {
				return err
			}
			if err := s.store.ClearTraceDirty(ctx, trace); err != nil {
				return err
			}
			s.traceRolledUp(ctx, rolled)
			s.scheduleRetry(trace.TraceKey, retry)
		}
		if len(dirty) < 200 {
			return nil
		}
	}
}

// scheduleRetry books the next look at a trace, or forgets it once every call is linked or the
// retries are used up.
func (s *Service) scheduleRetry(key logstore.TraceKey, retry bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, scheduled := s.retries[key]
	if !retry {
		delete(s.retries, key)
		return
	}
	if scheduled && current.at.After(time.Now()) {
		return // already booked: a new part of the trace does not restart the schedule
	}
	if current.attempt >= len(matchRetryDelays) {
		delete(s.retries, key)
		return
	}
	s.retries[key] = matchRetry{attempt: current.attempt + 1, at: time.Now().Add(matchRetryDelays[current.attempt])}
}

// requeueDueRetries marks the traces whose retry time has come.
func (s *Service) requeueDueRetries(ctx context.Context, now time.Time) error {
	var due []logstore.TraceKey
	s.mu.Lock()
	for key, retry := range s.retries {
		if !retry.at.After(now) {
			due = append(due, key)
		}
	}
	s.mu.Unlock()
	return s.store.MarkTracesDirty(ctx, due)
}
