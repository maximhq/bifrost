package traceingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

var (
	// ErrUnauthorized is returned when a request carries no tracing key or an unknown one.
	ErrUnauthorized = errors.New("tracing key is missing or not valid")
	// ErrForbidden is returned when a key names a project it is not bound to.
	ErrForbidden = errors.New("tracing key cannot write to this project")
	// ErrRateLimited is returned when a key sends spans faster than its cap.
	ErrRateLimited = errors.New("tracing key is over its span rate limit")
	// ErrClosed is returned once the service has been closed.
	ErrClosed = errors.New("trace ingest is closed")
)

// ConfigStore is the part of the config store the ingest path reads and writes.
type ConfigStore interface {
	GetTraceProject(ctx context.Context, id string) (*tables.TableTraceProject, error)
	GetTraceProjectByName(ctx context.Context, name string) (*tables.TableTraceProject, error)
	CreateTraceProject(ctx context.Context, project *tables.TableTraceProject) error
	GetTracingKey(ctx context.Context, id string) (*tables.TableTracingKey, error)
	GetTracingKeyByHash(ctx context.Context, hash string) (*tables.TableTracingKey, error)
	UpdateTracingKey(ctx context.Context, key *tables.TableTracingKey) error
	TouchTracingKey(ctx context.Context, id string, at time.Time) error
}

// Config tunes the ingest path. The zero value is usable.
type Config struct {
	// CommitWindow is how long the committer waits for more requests before it writes a batch.
	CommitWindow time.Duration
	// MaxBatchParts caps one commit. A request is never split across commits.
	MaxBatchParts int
	// RollupInterval is how often traces marked dirty get their rollup rebuilt.
	RollupInterval time.Duration
	// PartRetention is how long a received part is kept for refolds. A span outlives its parts.
	PartRetention time.Duration
	// MaxSpansPerSecond caps each key. Zero means no cap.
	MaxSpansPerSecond int
	// RetentionDays returns the default trace retention, for projects that set none. Zero keeps forever.
	RetentionDays func() int
	// ContentLoggingDisabled reports the global content switch. When on, no span content is stored.
	ContentLoggingDisabled func() bool
}

func (c Config) withDefaults() Config {
	if c.CommitWindow <= 0 {
		c.CommitWindow = 5 * time.Millisecond
	}
	if c.MaxBatchParts <= 0 {
		c.MaxBatchParts = 2000
	}
	if c.RollupInterval <= 0 {
		c.RollupInterval = 250 * time.Millisecond
	}
	if c.PartRetention <= 0 {
		c.PartRetention = 7 * 24 * time.Hour
	}
	return c
}

// Principal is who an ingest request acts as: a tracing key, or in enterprise a scoped API key.
type Principal struct {
	KeyID             string
	ProjectIDs        []string
	DefaultProjectID  string
	VirtualKeyIDs     []string
	LogContent        bool
	CanCreateProjects bool
	CanReadPrompts    bool
	CanUseDatasets    bool
	// AllProjects lets the principal write to any project. Tracing keys never have it.
	AllProjects bool
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
	evals  *evaluations

	requests chan *commitRequest
	done     chan struct{}
	wg       sync.WaitGroup
	closing  sync.Once

	mu       sync.Mutex
	keys     map[string]cachedKey  // by key hash
	masks    map[string]cachedMask // by project id
	projects map[string]cachedProject
	rates    map[string]*rateWindow // by key id
	touched  map[string]time.Time   // by key id
	// retries schedules another look at traces whose calls are not all linked yet.
	retries map[logstore.TraceKey]matchRetry
}

type cachedKey struct {
	principal *Principal
	expires   time.Time
}

type cachedProject struct {
	project *tables.TableTraceProject
	expires time.Time
}

type cachedMask struct {
	updatedAt time.Time
	rules     []compiledMask
}

type compiledMask struct {
	pattern     *regexp.Regexp
	replacement string
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
	keyID  string
	hidden bool
	result chan error
}

const (
	keyCacheTTL     = 30 * time.Second
	missCacheTTL    = 5 * time.Second
	projectCacheTTL = 30 * time.Second
	keyTouchEvery   = time.Minute
)

// NewService builds the ingest service and starts its committer and rollup worker.
func NewService(store logstore.TraceStore, config ConfigStore, cfg Config, logger schemas.Logger) *Service {
	s := &Service{
		store: store, config: config, cfg: cfg.withDefaults(), logger: logger,
		requests: make(chan *commitRequest, 1024), done: make(chan struct{}),
		keys: make(map[string]cachedKey), masks: make(map[string]cachedMask), projects: make(map[string]cachedProject),
		rates: make(map[string]*rateWindow), touched: make(map[string]time.Time), retries: make(map[logstore.TraceKey]matchRetry),
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

// Store returns the trace store the service writes to.
func (s *Service) Store() logstore.TraceStore { return s.store }

// Close stops the workers after the queued requests are written.
func (s *Service) Close() {
	s.closing.Do(func() { close(s.done) })
	s.wg.Wait()
}

// Authenticate resolves a tracing key value to its principal.
func (s *Service) Authenticate(ctx context.Context, value string) (*Principal, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, ErrUnauthorized
	}
	hash := tables.HashTracingKey(value)
	now := time.Now()
	s.mu.Lock()
	cached, ok := s.keys[hash]
	s.mu.Unlock()
	if ok && now.Before(cached.expires) {
		if cached.principal == nil {
			return nil, ErrUnauthorized
		}
		s.touch(cached.principal.KeyID, now)
		return cached.principal, nil
	}
	key, err := s.config.GetTracingKeyByHash(ctx, hash)
	if err != nil && !errors.Is(err, configstore.ErrNotFound) {
		return nil, fmt.Errorf("failed to look up tracing key: %w", err)
	}
	if err != nil || !key.Enabled {
		s.mu.Lock()
		s.keys[hash] = cachedKey{expires: now.Add(missCacheTTL)}
		s.mu.Unlock()
		return nil, ErrUnauthorized
	}
	principal := principalFromKey(key)
	s.mu.Lock()
	s.keys[hash] = cachedKey{principal: principal, expires: now.Add(keyCacheTTL)}
	s.mu.Unlock()
	s.touch(principal.KeyID, now)
	return principal, nil
}

func principalFromKey(key *tables.TableTracingKey) *Principal {
	return &Principal{
		KeyID: key.ID, ProjectIDs: key.ProjectIDs, DefaultProjectID: key.DefaultProjectID, VirtualKeyIDs: key.VirtualKeyIDs,
		LogContent: key.LogContent, CanCreateProjects: key.CanCreateProjects, CanReadPrompts: key.CanReadPrompts, CanUseDatasets: key.CanUseDatasets,
	}
}

// InvalidateKeys drops every cached key, so a key edited or deleted in the dashboard takes effect now.
func (s *Service) InvalidateKeys() {
	s.mu.Lock()
	clear(s.keys)
	clear(s.projects)
	s.mu.Unlock()
}

// touch records key use, at most once a minute per key, off the request path.
func (s *Service) touch(keyID string, now time.Time) {
	s.mu.Lock()
	last := s.touched[keyID]
	if now.Sub(last) < keyTouchEvery {
		s.mu.Unlock()
		return
	}
	s.touched[keyID] = now
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.config.TouchTracingKey(ctx, keyID, now.UTC()); err != nil && s.logger != nil {
			s.logger.Debug("traceingest: failed to record tracing key use: %v", err)
		}
	}()
}

// ResolveProject finds the project a request names, by id or by name. An empty ref means the
// principal's default project. A name that does not exist is created when the principal may create
// projects, which is how the Braintrust and Langfuse SDKs register a project on first use.
func (s *Service) ResolveProject(ctx context.Context, principal *Principal, ref string) (*tables.TableTraceProject, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = principal.DefaultProjectID
	}
	if ref == "" && len(principal.ProjectIDs) == 1 {
		ref = principal.ProjectIDs[0]
	}
	if ref == "" {
		return nil, fmt.Errorf("%w: the request names no project and the key has no default", ErrForbidden)
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

// createProject makes the project a key named and binds the key to it.
func (s *Service) createProject(ctx context.Context, principal *Principal, name string) (*tables.TableTraceProject, error) {
	if !principal.CanCreateProjects {
		return nil, fmt.Errorf("%w: project %q does not exist and the key may not create projects", ErrForbidden, name)
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
	if !principal.AllProjects && !principal.allows(project.ID) {
		key, err := s.config.GetTracingKey(ctx, principal.KeyID)
		if err != nil {
			return nil, fmt.Errorf("failed to bind tracing key to its new project: %w", err)
		}
		if !key.AllowsProject(project.ID) {
			key.ProjectIDs = append(key.ProjectIDs, project.ID)
			if err := s.config.UpdateTracingKey(ctx, key); err != nil {
				return nil, fmt.Errorf("failed to bind tracing key to its new project: %w", err)
			}
		}
		// The cached principal is shared by every request of this key, so it is replaced, not edited.
		s.InvalidateKeys()
		principal.ProjectIDs = append(append([]string{}, principal.ProjectIDs...), project.ID)
	}
	return project, nil
}

// allowRate counts spans against the key's per-second cap.
func (s *Service) allowRate(keyID string, spans int) bool {
	if s.cfg.MaxSpansPerSecond <= 0 || keyID == "" {
		return true
	}
	second := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	window := s.rates[keyID]
	if window == nil || window.second != second {
		window = &rateWindow{second: second}
		s.rates[keyID] = window
	}
	if window.count+spans > s.cfg.MaxSpansPerSecond && window.count > 0 {
		return false
	}
	window.count += spans
	return true
}

// masksFor returns the project's compiled masking rules. A rule whose pattern does not compile is
// skipped: a bad rule must not stop ingest.
func (s *Service) masksFor(project *tables.TableTraceProject) []compiledMask {
	if len(project.MaskingRules) == 0 {
		return nil
	}
	s.mu.Lock()
	cached, ok := s.masks[project.ID]
	s.mu.Unlock()
	if ok && cached.updatedAt.Equal(project.UpdatedAt) {
		return cached.rules
	}
	rules := make([]compiledMask, 0, len(project.MaskingRules))
	for _, rule := range project.MaskingRules {
		pattern, err := regexp.Compile(rule.Pattern)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("traceingest: masking rule %q of project %s does not compile: %v", rule.Name, project.Name, err)
			}
			continue
		}
		rules = append(rules, compiledMask{pattern: pattern, replacement: rule.Replacement})
	}
	s.mu.Lock()
	s.masks[project.ID] = cachedMask{updatedAt: project.UpdatedAt, rules: rules}
	s.mu.Unlock()
	return rules
}

// maskValue applies the rules to every string inside value.
func maskValue(value any, rules []compiledMask) any {
	switch v := value.(type) {
	case string:
		for _, rule := range rules {
			v = rule.pattern.ReplaceAllString(v, rule.replacement)
		}
		return v
	case map[string]any:
		masked := make(map[string]any, len(v))
		for key, child := range v {
			masked[key] = maskValue(child, rules)
		}
		return masked
	case []any:
		masked := make([]any, len(v))
		for i, child := range v {
			masked[i] = maskValue(child, rules)
		}
		return masked
	}
	return value
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
	if !s.allowRate(principal.KeyID, len(spans)) {
		return ErrRateLimited
	}
	hidden := !principal.LogContent || (s.cfg.ContentLoggingDisabled != nil && s.cfg.ContentLoggingDisabled())
	rules := s.masksFor(project)
	now := time.Now().UTC()
	partExpiry := now.Add(s.cfg.PartRetention)
	request := &commitRequest{keyID: principal.KeyID, hidden: hidden, result: make(chan error, 1)}
	for _, span := range spans {
		if err := span.Validate(); err != nil {
			return err
		}
		if hidden {
			span.Input, span.Output, span.Expected = nil, nil, nil
		} else if len(rules) > 0 {
			span.Input, span.Output, span.Expected = maskValue(span.Input, rules), maskValue(span.Output, rules), maskValue(span.Expected, rules)
			if masked, ok := maskValue(span.Metadata, rules).(map[string]any); ok {
				span.Metadata = masked
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
	for _, request := range batch {
		parts = append(parts, request.parts...)
		for i, span := range request.spans {
			ref := spanRef{request.parts[i].ProjectID, span.TraceID, span.SpanID}
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
		storedParts, err := s.store.ListTraceSpanPartsForSpans(ctx, key.ProjectID, key.TraceID, spanIDs)
		if err != nil {
			return fmt.Errorf("failed to load span parts: %w", err)
		}
		all := make(map[string][]*SpanPart, len(spanIDs))
		for _, stored := range storedParts {
			part, err := partFromStored(stored)
			if err != nil {
				// A part that no longer decodes is skipped, never a reason to fail the batch.
				continue
			}
			all[stored.SpanID] = append(all[stored.SpanID], part)
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
				span.TracingKeyID = request.keyID
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
	h := sha256.New()
	for _, field := range fields {
		fmt.Fprintf(h, "%d:%s", len(field), field)
	}
	return hex.EncodeToString(h.Sum(nil))[:40]
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
			s.evaluateTrace(ctx, rolled)
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
