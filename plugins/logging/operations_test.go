package logging

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/jobaccounting"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/streaming"
)

type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// fakeBatchStore is an in-memory jobaccounting.SweepStore for logging tests.
type fakeBatchStore struct {
	jobs map[string]*cstables.TableProviderJob
}

func newFakeBatchStore() *fakeBatchStore {
	return &fakeBatchStore{jobs: make(map[string]*cstables.TableProviderJob)}
}

func (s *fakeBatchStore) UpsertProviderJob(ctx context.Context, job *cstables.TableProviderJob) error {
	if job.ID == "" {
		job.ID = cstables.ProviderJobID(cstables.ProviderJobKindBatch, job.Provider, job.JobID)
	}
	existing, ok := s.jobs[job.ID]
	if !ok {
		copied := *job
		if copied.AccountingStatus == "" {
			copied.AccountingStatus = cstables.ProviderJobAccountingStatusPending
		}
		s.jobs[job.ID] = &copied
		return nil
	}
	if job.Model != "" {
		existing.Model = job.Model
	}
	if job.ProviderStatus != "" {
		existing.ProviderStatus = job.ProviderStatus
	}
	if job.InputFileID != "" {
		existing.InputFileID = job.InputFileID
	}
	if job.OutputFileID != nil {
		existing.OutputFileID = job.OutputFileID
	}
	if job.NextCheckAt != nil {
		existing.NextCheckAt = job.NextCheckAt
	}
	if cstables.IsTerminalBatchProviderStatus(job.ProviderStatus) &&
		job.ProviderStatus != string(schemas.BatchStatusCompleted) &&
		job.ProviderStatus != string(schemas.BatchStatusEnded) {
		existing.NextCheckAt = nil
	}
	return nil
}

func (s *fakeBatchStore) GetProviderJob(ctx context.Context, jobID string) (*cstables.TableProviderJob, error) {
	job, ok := s.jobs[jobID]
	if !ok {
		return nil, errors.New("missing batch job")
	}
	copied := *job
	return &copied, nil
}

func (s *fakeBatchStore) ListDueProviderJobs(ctx context.Context, kind, provider string, now time.Time, limit int) ([]*cstables.TableProviderJob, error) {
	var jobs []*cstables.TableProviderJob
	if kind == "" {
		kind = cstables.ProviderJobKindBatch
	}
	for _, job := range s.jobs {
		// Mirror the real store: a sweeper only ever sees its own kind's rows.
		jobKind := job.Kind
		if jobKind == "" {
			jobKind = cstables.ProviderJobKindBatch
		}
		if jobKind != kind {
			continue
		}
		if provider != "" && job.Provider != provider {
			continue
		}
		if job.NextCheckAt == nil || job.NextCheckAt.After(now) {
			continue
		}
		if job.AccountingStatus == cstables.ProviderJobAccountingStatusAccounted || job.AccountingStatus == cstables.ProviderJobAccountingStatusUnpriceable {
			continue
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *fakeBatchStore) ClaimProviderJob(ctx context.Context, jobID, runnerID string, staleBefore time.Time, allowUnpriceable bool) (bool, error) {
	entry, ok := s.jobs[jobID]
	if !ok {
		return false, errors.New("missing batch job")
	}
	if entry.AccountingStatus == cstables.ProviderJobAccountingStatusAccounted {
		return false, nil
	}
	// "unpriceable" is a stop-polling marker, not a refusal of money: a caller
	// holding real results may re-drive it. Mirrors the real store's allowUnpriceable,
	// which this fake previously ignored — hiding the result-driven re-accounting path
	// from every test using it.
	if entry.AccountingStatus == cstables.ProviderJobAccountingStatusUnpriceable && !allowUnpriceable {
		return false, nil
	}
	// Claim staleness reads claimed_at, not updated_at — updated_at is refreshed by
	// the unfenced UpsertProviderJob, so it cannot represent claim age.
	if entry.AccountingStatus == cstables.ProviderJobAccountingStatusProcessing &&
		entry.ClaimedAt != nil && entry.ClaimedAt.After(staleBefore) {
		return false, nil
	}
	now := time.Now().UTC()
	rid := runnerID
	entry.AccountingStatus = cstables.ProviderJobAccountingStatusProcessing
	entry.RunnerID = &rid
	entry.ClaimedAt = &now
	entry.UpdatedAt = now
	return true, nil
}

func (s *fakeBatchStore) markTerminal(id, status, reason string) error {
	entry, ok := s.jobs[id]
	if !ok {
		return errors.New("missing batch job")
	}
	entry.AccountingStatus = status
	entry.RunnerID = nil
	entry.ClaimedAt = nil
	if reason != "" {
		entry.UnpriceableReason = &reason
	}
	return nil
}

func (s *fakeBatchStore) MarkProviderJobAggregateLogWritten(ctx context.Context, id, runnerID string) error {
	entry, ok := s.jobs[id]
	if !ok {
		return errors.New("missing batch job")
	}
	now := time.Now().UTC()
	entry.AggregateLogWrittenAt = &now
	return nil
}

func (s *fakeBatchStore) MarkProviderJobGovernanceReported(ctx context.Context, id, runnerID string) error {
	entry, ok := s.jobs[id]
	if !ok {
		return errors.New("missing batch job")
	}
	now := time.Now().UTC()
	entry.GovernanceReportedAt = &now
	return nil
}

func (s *fakeBatchStore) CompleteProviderJob(ctx context.Context, id, runnerID string) error {
	return s.markTerminal(id, cstables.ProviderJobAccountingStatusAccounted, "")
}

func (s *fakeBatchStore) MarkProviderJobUnpriceable(ctx context.Context, id, runnerID, reason string, err error) error {
	return s.markTerminal(id, cstables.ProviderJobAccountingStatusUnpriceable, reason)
}

func (s *fakeBatchStore) FailProviderJob(ctx context.Context, id, runnerID string, err error) error {
	return s.markTerminal(id, cstables.ProviderJobAccountingStatusError, "")
}

func newTestStore(t *testing.T) logstore.LogStore {
	t.Helper()

	store, err := logstore.NewLogStore(context.Background(), &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{
			Path: filepath.Join(t.TempDir(), "logging.db"),
		},
	}, testLogger{})
	if err != nil {
		t.Fatalf("NewLogStore() error = %v", err)
	}
	return store
}

func TestUserAgentFromContextUsesRequestHeaders(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"user-agent": "codex-tui/1.0",
	})
	ctx.SetValue(schemas.BifrostContextKeyUserAgent, "fallback/1.0")

	if got := userAgentFromContext(ctx); got != "codex-tui/1.0" {
		t.Fatalf("expected request-header user agent, got %q", got)
	}
}

func TestUserAgentFromContextUsesCanonicalRequestHeader(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"User-Agent": "Claude-Code/1.0",
	})

	if got := userAgentFromContext(ctx); got != "Claude-Code/1.0" {
		t.Fatalf("expected canonical request-header user agent, got %q", got)
	}
}

func TestUserAgentFromContextUsesUppercaseRequestHeader(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"USER-AGENT": "Cursor/0.47",
	})

	if got := userAgentFromContext(ctx); got != "Cursor/0.47" {
		t.Fatalf("expected uppercase request-header user agent, got %q", got)
	}
}

func TestUserAgentFromContextFallsBackWhenHeaderValueIsEmpty(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"user-agent": "",
	})
	ctx.SetValue(schemas.BifrostContextKeyUserAgent, "fallback/1.0")

	if got := userAgentFromContext(ctx); got != "fallback/1.0" {
		t.Fatalf("expected fallback user agent for empty header, got %q", got)
	}
}

func TestUserAgentFromContextFallsBackToUserAgentKey(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUserAgent, "cursor/0.47")

	if got := userAgentFromContext(ctx); got != "cursor/0.47" {
		t.Fatalf("expected fallback user agent, got %q", got)
	}
}

func TestPreLLMHookSetsAppContextFromDetectedApp(t *testing.T) {
	store := newTestStore(t)
	defer store.Close(context.Background())
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := plugin.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup() error = %v", cleanupErr)
		}
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-app-context")
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"user-agent":   "claude-cli/2.1.168 (external, cli)",
		"x-bf-vk":      "vk-test",
		"x-bf-user-id": "user-test",
	})

	_, _, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Params:   &schemas.ChatParameters{},
		},
	})
	if err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	if got, _ := ctx.Value(schemas.BifrostContextKeyApp).(string); got != "claude-code" {
		t.Fatalf("app context = %q, want claude-code", got)
	}
}

// TestPreLLMHookContextKeyComesFromUserAgentNotAgentHeader replays the header
// shape the desktop agent stamps for a Claude Cowork request: Cowork's runtime
// shares Claude Code's CLI User-Agent, so the logging plugin derives
// claude-code for the context key. Header-based app enforcement (X-Bf-App)
// lives in the enterprise agent policy plugin, which prefers the header over
// this context key.
func TestPreLLMHookContextKeyComesFromUserAgentNotAgentHeader(t *testing.T) {
	store := newTestStore(t)
	defer store.Close(context.Background())
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := plugin.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup() error = %v", cleanupErr)
		}
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-cowork-context")
	// Captured from the agent's MITM → GATEWAY forwarding of a Cowork request.
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"user-agent":                "claude-cli/2.1.170 (external, local-agent, agent-sdk/0.3.170)",
		"anthropic-client-platform": "desktop_app",
		"x-app":                     "cli",
		"x-bf-app":                  "claude-cowork",
		"x-bifrost-agent":           "bifrost-agent/0.1.0",
	})

	_, _, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-opus-4-8",
			Params:   &schemas.ChatParameters{},
		},
	})
	if err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	if got, _ := ctx.Value(schemas.BifrostContextKeyApp).(string); got != "claude-code" {
		t.Fatalf("app context = %q, want claude-code (derived from User-Agent)", got)
	}
}

func TestCustomUserAgentMappingOverridesBuiltInDetection(t *testing.T) {
	store := newTestStore(t)
	defer store.Close(context.Background())
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := plugin.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup() error = %v", cleanupErr)
		}
	})

	_, err = plugin.CreateUserAgentMapping(context.Background(), &logstore.UserAgentMapping{
		Pattern:   `claude-cli/\d+\.\d+`,
		MatchType: string(schemas.UserAgentMappingMatchTypeRegex),
		App:       "Internal Claude Wrapper",
		IsActive:  true,
	})
	if err != nil {
		t.Fatalf("CreateUserAgentMapping() error = %v", err)
	}

	if got := plugin.detectAppFromUserAgent("claude-cli/2.1.168 (external, cli)"); got != "Internal Claude Wrapper" {
		t.Fatalf("expected custom app mapping, got %q", got)
	}
}

func TestPreLLMHookSetsAppContextFromCustomMapping(t *testing.T) {
	store := newTestStore(t)
	defer store.Close(context.Background())
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := plugin.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup() error = %v", cleanupErr)
		}
	})
	_, err = plugin.CreateUserAgentMapping(context.Background(), &logstore.UserAgentMapping{
		Pattern:   `custom-wrapper/\d+`,
		MatchType: string(schemas.UserAgentMappingMatchTypeRegex),
		App:       "Internal Claude Wrapper",
		IsActive:  true,
	})
	if err != nil {
		t.Fatalf("CreateUserAgentMapping() error = %v", err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-custom-app-context")
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"user-agent": "custom-wrapper/42",
	})

	_, _, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Params:   &schemas.ChatParameters{},
		},
	})
	if err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	if got, _ := ctx.Value(schemas.BifrostContextKeyApp).(string); got != "internal-claude-wrapper" {
		t.Fatalf("app context = %q, want internal-claude-wrapper", got)
	}
}

func TestPostLLMHookNoPendingErrorPreservesMetadata(t *testing.T) {
	store := newTestStore(t)
	loggingHeaders := []string{"x-custom-log"}
	plugin, err := Init(context.Background(), &Config{LoggingHeaders: &loggingHeaders}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-error-no-pending")
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"x-bf-lh-tenant": "acme",
		"x-custom-log":   "custom-value",
	})
	ctx.SetValue(schemas.BifrostContextKeyDimensions, map[string]string{
		"region": "us-east",
	})
	ctx.SetValue(schemas.BifrostIsAsyncRequest, true)

	statusCode := 500
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{Message: "provider failed"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ChatCompletionRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: "gpt-4o",
			ResolvedModelUsed:      "gpt-4o",
		},
	}

	_, _, err = plugin.PostLLMHook(ctx, nil, bifrostErr)
	if err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), "req-error-no-pending")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.Status != "error" {
		t.Fatalf("expected error status, got %q", logEntry.Status)
	}
	if logEntry.MetadataParsed == nil {
		t.Fatalf("expected metadata to be persisted")
	}
	if got := logEntry.MetadataParsed["tenant"]; got != "acme" {
		t.Fatalf("expected tenant metadata acme, got %#v", got)
	}
	if got := logEntry.MetadataParsed["x-custom-log"]; got != "custom-value" {
		t.Fatalf("expected configured header metadata custom-value, got %#v", got)
	}
	if got := logEntry.MetadataParsed["region"]; got != "us-east" {
		t.Fatalf("expected dimension metadata us-east, got %#v", got)
	}
	if got := logEntry.MetadataParsed["isAsyncRequest"]; got != true {
		t.Fatalf("expected async metadata true, got %#v", got)
	}
}

func TestEmitAggregateLogRunsCallback(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		ctx:   context.Background(),
		store: store,
	}
	callbacks := 0
	plugin.SetLogCallback(func(ctx context.Context, logEntry *logstore.Log) {
		callbacks++
	})

	entry := &logstore.Log{
		ID:        "batch-cost:openai:callback",
		Timestamp: time.Now().UTC(),
		Object:    string(schemas.BatchResultsRequest),
		Provider:  string(schemas.OpenAI),
		Model:     "gpt-4o-mini",
		Status:    "success",
	}
	plugin.EmitAggregateLog(context.Background(), entry)
	if callbacks != 1 {
		t.Fatalf("callback count after emit = %d, want 1", callbacks)
	}
}

func TestPostLLMHookStreamingErrorPreservesHeaderMetadata(t *testing.T) {
	store := newTestStore(t)
	loggingHeaders := []string{"x-custom-log"}
	plugin, err := Init(context.Background(), &Config{LoggingHeaders: &loggingHeaders}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-stream-error-metadata")
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"x-custom-log":   "custom-value",
		"x-bf-lh-user":   `{"device_id":"device-1","session_id":"session-1"}`,
		"x-bf-lh-tag":    "from-header",
		"x-bf-lh-shared": "from-header",
	})
	ctx.SetValue(schemas.BifrostContextKeyDimensions, map[string]string{
		"environment": "staging",
	})

	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesStreamRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.Bedrock,
			Model:    "us.anthropic.claude-opus-4-7",
			Params:   &schemas.ResponsesParameters{},
		},
	}
	if _, _, err = plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	statusCode := 500
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{Message: "stream failed"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ResponsesStreamRequest,
			Provider:               schemas.Bedrock,
			OriginalModelRequested: "us.anthropic.claude-opus-4-7",
			ResolvedModelUsed:      "us.anthropic.claude-opus-4-7",
		},
	}
	if _, _, err = plugin.PostLLMHook(ctx, nil, bifrostErr); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), "req-stream-error-metadata")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.Status != "error" {
		t.Fatalf("expected error status, got %q", logEntry.Status)
	}
	if logEntry.MetadataParsed == nil {
		t.Fatalf("expected metadata to be persisted")
	}
	if got := logEntry.MetadataParsed["user"]; got != `{"device_id":"device-1","session_id":"session-1"}` {
		t.Fatalf("expected user metadata from header, got %#v", got)
	}
	if got := logEntry.MetadataParsed["tag"]; got != "from-header" {
		t.Fatalf("expected tag metadata from header, got %#v", got)
	}
	if got := logEntry.MetadataParsed["x-custom-log"]; got != "custom-value" {
		t.Fatalf("expected configured header metadata custom-value, got %#v", got)
	}
	if got := logEntry.MetadataParsed["shared"]; got != "from-header" {
		t.Fatalf("expected shared metadata from header, got %#v", got)
	}
	if got := logEntry.MetadataParsed["environment"]; got != "staging" {
		t.Fatalf("expected dimension metadata staging, got %#v", got)
	}
}

// TestPostLLMHookCancelledStreamLogsCost verifies #3357 at the logging layer: a
// streaming request cancelled mid-flight (including when a chunk is returned)
// whose error carries the
// partial usage the provider already processed (BifrostError.ExtraFields.BilledUsage)
// must produce a log row with status="cancelled", the consumed tokens, AND an
// accurate cost computed from the datasheet rates.
func TestPostLLMHookCancelledStreamLogsCost(t *testing.T) {
	store := newTestStore(t)

	// Pricing manager loaded from the committed datasheet testdata via an
	// offline file:// URL (no network).
	abs, err := filepath.Abs("../../framework/modelcatalog/datasheet/testdata/pricing.json")
	if err != nil {
		t.Fatalf("resolve testdata path: %v", err)
	}
	ds := datasheet.New(nil, testLogger{}, datasheet.Config{URL: "file://" + abs})
	if err := ds.LoadFromURLIntoMemory(context.Background()); err != nil {
		t.Fatalf("load pricing datasheet: %v", err)
	}
	pricingManager := modelcatalog.NewTestCatalogWithDatasheet(ds)

	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, pricingManager, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-cancel-cost")

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionStreamRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Params:   &schemas.ChatParameters{},
		},
	}
	if _, _, err = plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	embeddingProvider := "openai"
	embeddingModel := "text-embedding-3-small"
	embeddingTokens := 12
	if !schemas.SetCacheDebugOnContext(ctx, &schemas.BifrostCacheDebug{
		ProviderUsed: &embeddingProvider,
		ModelUsed:    &embeddingModel,
		InputTokens:  &embeddingTokens,
	}) {
		t.Fatal("expected semantic cache debug to be stored on context")
	}
	if !schemas.AppendGuardrailJudgeCallOnContext(ctx, schemas.BifrostGuardrailJudgeCall{
		JudgeProvider:    schemas.OpenAI,
		JudgeModel:       "gpt-4o",
		JudgeRequestType: schemas.ChatCompletionRequest,
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}) {
		t.Fatal("expected guardrail judge call to be stored on context")
	}

	const promptTokens, completionTokens = 100, 50
	statusCode := 499 // client closed request (mid-stream cancel)
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{Message: "client disconnected"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ChatCompletionStreamRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: "gpt-4o",
			ResolvedModelUsed:      "gpt-4o",
			// Provider processed these tokens before the client disconnected.
			BilledUsage: &schemas.BifrostLLMUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      promptTokens + completionTokens,
			},
		},
	}
	streamChunk := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionStreamRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4o",
				ResolvedModelUsed:      "gpt-4o",
			},
		},
	}
	if _, _, err = plugin.PostLLMHook(ctx, streamChunk, bifrostErr); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	entry, err := store.FindByID(context.Background(), "req-cancel-cost")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if entry.Status != "cancelled" {
		t.Fatalf("expected cancelled status, got %q", entry.Status)
	}
	if entry.TokenUsageParsed == nil {
		t.Fatalf("expected token usage recorded from BilledUsage on the cancel path")
	}
	if entry.TotalTokens != promptTokens+completionTokens {
		t.Fatalf("expected total_tokens %d, got %d", promptTokens+completionTokens, entry.TotalTokens)
	}
	if entry.Cost == nil {
		t.Fatalf("expected a cost to be logged for a cancelled request that consumed tokens (#3357)")
	}
	// gpt-4o testdata rates: input 2.5e-6/token, output 1e-5/token.
	// text-embedding-3-small is 2e-8/token. The cache lookup and the judge call
	// must both be added even though the stream ended with an error chunk.
	want := float64(promptTokens)*2.5e-6 + float64(completionTokens)*1e-5 +
		float64(embeddingTokens)*2e-8 + float64(10)*2.5e-6 + float64(5)*1e-5
	if diff := *entry.Cost - want; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("logged cost %v does not match datasheet-computed cost %v", *entry.Cost, want)
	}
}

func TestPostLLMHookContextTimeoutLogsCancelledStatus(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-context-timeout")

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Params:   &schemas.ChatParameters{},
		},
	}
	if _, _, err = plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}

	statusCode := 504
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: "Request timed out by context: context deadline exceeded",
			Type:    schemas.Ptr(schemas.RequestTimedOut),
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ChatCompletionRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: "gpt-4o",
			ResolvedModelUsed:      "gpt-4o",
		},
	}
	if _, _, err = plugin.PostLLMHook(ctx, nil, bifrostErr); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	entry, err := store.FindByID(context.Background(), "req-context-timeout")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if entry.Status != "cancelled" {
		t.Fatalf("expected cancelled status, got %q", entry.Status)
	}
}

func TestPostLLMHookProviderTimeoutRemainsErrorStatus(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-provider-timeout")

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Params:   &schemas.ChatParameters{},
		},
	}
	if _, _, err = plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}

	statusCode := 504
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: schemas.ErrProviderRequestTimedOut,
			Type:    schemas.Ptr(schemas.RequestTimedOut),
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ChatCompletionRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: "gpt-4o",
			ResolvedModelUsed:      "gpt-4o",
		},
	}
	if _, _, err = plugin.PostLLMHook(ctx, nil, bifrostErr); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	entry, err := store.FindByID(context.Background(), "req-provider-timeout")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if entry.Status != "error" {
		t.Fatalf("expected error status, got %q", entry.Status)
	}
}

func TestLogStatusForErrorDoesNotTreatGenericDeadlineMessageAsCancelled(t *testing.T) {
	statusCode := 504
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: "provider request hit project deadline exceeded limit",
			Type:    schemas.Ptr(schemas.RequestTimedOut),
		},
	}

	if got := logStatusForError(bifrostErr); got != "error" {
		t.Fatalf("expected generic provider deadline message to remain error, got %q", got)
	}
}

func TestApplyStreamingOutputToEntryPreservesAccumulatorCancelledStatus(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}
	statusCode := 499
	streamResponse := &streaming.ProcessedStreamResponse{
		Data: &streaming.AccumulatedData{
			ErrorDetails: &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Error: &schemas.ErrorField{
					Message: "Request cancelled: client disconnected",
					Type:    schemas.Ptr(schemas.RequestCancelled),
				},
			},
			Latency: 42,
		},
	}

	plugin.applyStreamingOutputToEntry(entry, streamResponse, false, true)
	if entry.Status != "cancelled" {
		t.Fatalf("expected initial cancelled status, got %q", entry.Status)
	}
	if entry.ErrorDetailsParsed == nil {
		t.Fatalf("expected parsed error details to be set")
	}
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error: %v", err)
	}
	if entry.ErrorDetails == "" {
		t.Fatalf("expected serialized error details after SerializeFields")
	}

	// Match the downstream Path B re-derivation in PostLLMHook. If
	// ErrorDetailsParsed is missing, this collapses accumulator-originated
	// cancellations back to "error".
	if entry.ErrorDetailsParsed != nil {
		entry.Status = logStatusForError(entry.ErrorDetailsParsed)
	}
	if entry.Status != "cancelled" {
		t.Fatalf("expected downstream reclassification to preserve cancelled status, got %q", entry.Status)
	}
}

func TestApplyStreamingOutputToEntryPreservesServiceTier(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}
	priority := schemas.BifrostServiceTierPriority

	plugin.applyStreamingOutputToEntry(entry, &streaming.ProcessedStreamResponse{
		Data: &streaming.AccumulatedData{
			ServiceTier: &priority,
			TokenUsage:  &schemas.BifrostLLMUsage{TotalTokens: 1},
		},
	}, false, true)

	if entry.ServiceTier == nil || *entry.ServiceTier != string(schemas.BifrostServiceTierPriority) {
		t.Fatalf("expected priority service tier, got %v", entry.ServiceTier)
	}
}

// newTestPricingManager builds a ModelCatalog backed by the committed pricing
// testdata via an offline file:// URL (no network).
func newTestPricingManager(t *testing.T) *modelcatalog.ModelCatalog {
	t.Helper()
	abs, err := filepath.Abs("../../framework/modelcatalog/datasheet/testdata/pricing.json")
	if err != nil {
		t.Fatalf("resolve testdata path: %v", err)
	}
	ds := datasheet.New(nil, testLogger{}, datasheet.Config{URL: "file://" + abs})
	if err := ds.LoadFromURLIntoMemory(context.Background()); err != nil {
		t.Fatalf("load pricing datasheet: %v", err)
	}
	return modelcatalog.NewTestCatalogWithDatasheet(ds)
}

// TestApplyErrorBillingFromBilledUsage_ComputesCostWhenTokensAlreadyParsed guards
// the case where stream accumulation already captured token usage on a failed
// request but no cost was computed: cost must still be backfilled, and the
// already-parsed token counters must be left untouched (not double-applied).
func TestApplyErrorBillingFromBilledUsage_ComputesCostWhenTokensAlreadyParsed(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, newTestPricingManager(t), nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const promptTokens, completionTokens = 100, 50
	entry := &logstore.Log{
		Provider:         string(schemas.OpenAI),
		Model:            "gpt-4o",
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}
	billed := entry.TokenUsageParsed

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	plugin.applyErrorBillingFromBilledUsage(ctx, entry, billed, schemas.ChatCompletionStreamRequest)

	if entry.Cost == nil {
		t.Fatal("expected cost to be computed even though token usage was already parsed")
	}
	// gpt-4o testdata rates: input 2.5e-6/token, output 1e-5/token.
	want := float64(promptTokens)*2.5e-6 + float64(completionTokens)*1e-5
	if diff := *entry.Cost - want; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("cost %v does not match datasheet-computed %v", *entry.Cost, want)
	}
	if entry.PromptTokens != promptTokens || entry.TotalTokens != promptTokens+completionTokens {
		t.Fatalf("token counters mutated: prompt=%d total=%d", entry.PromptTokens, entry.TotalTokens)
	}
	// The breakdown must ride on the usage so the denormalized split populates,
	// not just the scalar total (Discrepancy B: failed/cancelled billing).
	if entry.TokenUsageParsed.Cost == nil {
		t.Fatal("expected cost breakdown attached to usage for the denormalized split")
	}
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields: %v", err)
	}
	wantIn := float64(promptTokens) * 2.5e-6
	wantOut := float64(completionTokens) * 1e-5
	if d := entry.InputCost - wantIn; d < -1e-9 || d > 1e-9 {
		t.Fatalf("input_cost %v != %v", entry.InputCost, wantIn)
	}
	if d := entry.OutputCost - wantOut; d < -1e-9 || d > 1e-9 {
		t.Fatalf("output_cost %v != %v", entry.OutputCost, wantOut)
	}
}

// TestApplyInternalCallCosts_DenormalizesGuardrailToAdditional guards Discrepancy
// B for internal-only sidecar costs: a guardrail judge call with no provider
// response must land on the additional side of the split, not just the total,
// and must not leak into input/output.
func TestApplyInternalCallCosts_DenormalizesGuardrailToAdditional(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, newTestPricingManager(t), nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	entry := &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-4o"}
	guardrail := &schemas.BifrostGuardrailDebug{
		JudgeCalls: []schemas.BifrostGuardrailJudgeCall{{
			JudgeProvider:    schemas.OpenAI,
			JudgeModel:       "gpt-4o",
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		}},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	plugin.applyInternalCallCosts(ctx, entry, guardrail)

	want := float64(100)*2.5e-6 + float64(50)*1e-5
	if entry.Cost == nil {
		t.Fatal("expected guardrail cost on entry.Cost")
	}
	if d := *entry.Cost - want; d < -1e-9 || d > 1e-9 {
		t.Fatalf("entry.Cost %v != guardrail %v", *entry.Cost, want)
	}
	// No usage carrier exists, so the split is written directly on the columns.
	if d := entry.AdditionalCost - want; d < -1e-9 || d > 1e-9 {
		t.Fatalf("additional_cost %v != guardrail %v", entry.AdditionalCost, want)
	}
	if entry.InputCost != 0 || entry.OutputCost != 0 {
		t.Fatalf("guardrail-only cost must not populate input/output: in=%v out=%v", entry.InputCost, entry.OutputCost)
	}
}

// TestApplyErrorBillingFromBilledUsage_FillsTokensAndCostWhenUnparsed pins the
// original behaviour: when no usage was captured yet, both tokens and cost are
// backfilled from BilledUsage.
func TestApplyErrorBillingFromBilledUsage_FillsTokensAndCostWhenUnparsed(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, newTestPricingManager(t), nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const promptTokens, completionTokens = 100, 50
	entry := &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-4o"}
	billed := &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	plugin.applyErrorBillingFromBilledUsage(ctx, entry, billed, schemas.ChatCompletionStreamRequest)

	if entry.TokenUsageParsed == nil || entry.TotalTokens != promptTokens+completionTokens {
		t.Fatalf("expected tokens backfilled, got %+v", entry.TokenUsageParsed)
	}
	if entry.Cost == nil {
		t.Fatal("expected cost computed on the unparsed path")
	}
}

func TestRecalculateCostsProcessesAllBatches(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:          store,
		pricingManager: newTestPricingManager(t),
		logger:         testLogger{},
	}

	now := time.Now().UTC()
	for i := range 5 {
		if err := store.Create(context.Background(), testRecalculateCostLog(
			"req-recalc-batch-"+string(rune('a'+i)),
			now.Add(time.Duration(i)*time.Second),
			100+i,
			50,
		)); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}

	result, err := plugin.RecalculateCosts(context.Background(), logstore.SearchFilters{}, 2)
	if err != nil {
		t.Fatalf("RecalculateCosts() error = %v", err)
	}
	if result.TotalMatched != 5 || result.Updated != 5 || result.Skipped != 0 || result.Remaining != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	for _, id := range []string{"req-recalc-batch-a", "req-recalc-batch-b", "req-recalc-batch-c", "req-recalc-batch-d", "req-recalc-batch-e"} {
		entry, err := store.FindByID(context.Background(), id)
		if err != nil {
			t.Fatalf("FindByID(%s) error = %v", id, err)
		}
		if entry.Cost == nil || *entry.Cost <= 0 {
			t.Fatalf("expected positive cost for %s, got %v", id, entry.Cost)
		}
	}
}

func TestRecalculateCostsSkipsUnresolvableRowsAndContinues(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:          store,
		pricingManager: newTestPricingManager(t),
		logger:         testLogger{},
	}

	now := time.Now().UTC()
	if err := store.Create(context.Background(), &logstore.Log{
		ID:        "req-recalc-skip",
		Timestamp: now,
		Object:    string(schemas.ChatCompletionRequest),
		Provider:  string(schemas.OpenAI),
		Model:     "gpt-4o",
		Status:    "success",
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	for i := range 3 {
		if err := store.Create(context.Background(), testRecalculateCostLog(
			"req-recalc-continue-"+string(rune('a'+i)),
			now.Add(time.Duration(i+1)*time.Second),
			100+i,
			50,
		)); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}

	result, err := plugin.RecalculateCosts(context.Background(), logstore.SearchFilters{}, 2)
	if err != nil {
		t.Fatalf("RecalculateCosts() error = %v", err)
	}
	if result.TotalMatched != 4 || result.Updated != 3 || result.Skipped != 1 || result.Remaining != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}

	skipped, err := store.FindByID(context.Background(), "req-recalc-skip")
	if err != nil {
		t.Fatalf("FindByID(req-recalc-skip) error = %v", err)
	}
	if skipped.Cost != nil {
		t.Fatalf("expected skipped row cost to remain nil, got %v", skipped.Cost)
	}
	for _, id := range []string{"req-recalc-continue-a", "req-recalc-continue-b", "req-recalc-continue-c"} {
		entry, err := store.FindByID(context.Background(), id)
		if err != nil {
			t.Fatalf("FindByID(%s) error = %v", id, err)
		}
		if entry.Cost == nil || *entry.Cost <= 0 {
			t.Fatalf("expected positive cost for %s, got %v", id, entry.Cost)
		}
	}
}

func TestRecalculateCostsDoesNotWriteZeroForUnresolvedPricing(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:          store,
		pricingManager: newTestPricingManager(t),
		logger:         testLogger{},
	}

	entry := testRecalculateCostLog("req-recalc-unpriced", time.Now().UTC(), 100, 50)
	entry.Model = "unknown-model"
	if err := store.Create(context.Background(), entry); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	result, err := plugin.RecalculateCosts(context.Background(), logstore.SearchFilters{}, 2)
	if err != nil {
		t.Fatalf("RecalculateCosts() error = %v", err)
	}
	if result.TotalMatched != 1 || result.Updated != 0 || result.Skipped != 1 || result.Remaining != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}

	logEntry, err := store.FindByID(context.Background(), "req-recalc-unpriced")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.Cost != nil {
		t.Fatalf("expected unresolved pricing to leave cost nil, got %v", *logEntry.Cost)
	}
}

func TestRecalculateCostsNormalizesProviderObjectForPricing(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:          store,
		pricingManager: newTestPricingManager(t),
		logger:         testLogger{},
	}

	entry := testRecalculateCostLog("req-recalc-provider-object", time.Now().UTC(), 100, 50)
	entry.Object = "chat.completion"
	zeroCost := 0.0
	entry.Cost = &zeroCost
	if err := store.Create(context.Background(), entry); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	result, err := plugin.RecalculateCosts(context.Background(), logstore.SearchFilters{}, 2)
	if err != nil {
		t.Fatalf("RecalculateCosts() error = %v", err)
	}
	if result.TotalMatched != 1 || result.Updated != 1 || result.Skipped != 0 || result.Remaining != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	logEntry, err := store.FindByID(context.Background(), "req-recalc-provider-object")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.Cost == nil || *logEntry.Cost <= 0 {
		t.Fatalf("expected legacy provider object to recalculate positive cost, got %v", logEntry.Cost)
	}
}

func testRecalculateCostLog(id string, timestamp time.Time, promptTokens int, completionTokens int) *logstore.Log {
	totalTokens := promptTokens + completionTokens
	return &logstore.Log{
		ID:               id,
		Timestamp:        timestamp,
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.OpenAI),
		Model:            "gpt-4o",
		Status:           "success",
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
		},
	}
}

func TestBuildInitialLogEntryPreservesMetadata(t *testing.T) {
	metadata := map[string]any{"tenant": "acme"}
	entry := buildInitialLogEntry(&PendingLogData{
		RequestID:     "req-initial-metadata",
		Timestamp:     time.Now().UTC(),
		FallbackIndex: 1,
		InitialData: &InitialLogData{
			Provider: string(schemas.OpenAI),
			Model:    "gpt-4o",
			Object:   string(schemas.ChatCompletionRequest),
			Metadata: metadata,
		},
	})

	if entry.MetadataParsed == nil {
		t.Fatalf("expected metadata on initial log entry")
	}
	if got := entry.MetadataParsed["tenant"]; got != "acme" {
		t.Fatalf("expected tenant metadata acme, got %#v", got)
	}
}

func TestBuildInitialLogEntryPreservesUserAgent(t *testing.T) {
	userAgent := "codex-cli/1.0"
	entry := buildInitialLogEntry(&PendingLogData{
		RequestID:     "req-initial-user-agent",
		Timestamp:     time.Now().UTC(),
		FallbackIndex: 1,
		InitialData: &InitialLogData{
			Provider:  string(schemas.OpenAI),
			Model:     "gpt-4o",
			Object:    string(schemas.ChatCompletionRequest),
			UserAgent: userAgent,
		},
	})

	if entry.UserAgent == nil {
		t.Fatalf("expected user agent on initial log entry")
	}
	if got := *entry.UserAgent; got != userAgent {
		t.Fatalf("expected user agent %q, got %q", userAgent, got)
	}
	if entry.App == nil {
		t.Fatalf("expected app on initial log entry")
	}
	if got := *entry.App; got != "Codex CLI" {
		t.Fatalf("expected app Codex CLI, got %q", got)
	}
}

func TestBuildCompleteLogEntryPreservesUserAgent(t *testing.T) {
	userAgent := "cursor/0.47"
	entry := buildCompleteLogEntryFromPending(&PendingLogData{
		RequestID:     "req-complete-user-agent",
		Timestamp:     time.Now().UTC(),
		FallbackIndex: 1,
		InitialData: &InitialLogData{
			Provider:  string(schemas.OpenAI),
			Model:     "gpt-4o",
			Object:    string(schemas.ChatCompletionRequest),
			UserAgent: userAgent,
		},
	})

	if entry.UserAgent == nil {
		t.Fatalf("expected user agent on complete log entry")
	}
	if got := *entry.UserAgent; got != userAgent {
		t.Fatalf("expected user agent %q, got %q", userAgent, got)
	}
	if entry.App == nil {
		t.Fatalf("expected app on complete log entry")
	}
	if got := *entry.App; got != "Cursor" {
		t.Fatalf("expected app Cursor, got %q", got)
	}
}

func TestBuildLogEntriesOmitEmptyUserAgent(t *testing.T) {
	pending := &PendingLogData{
		RequestID:     "req-empty-user-agent",
		Timestamp:     time.Now().UTC(),
		FallbackIndex: 1,
		InitialData: &InitialLogData{
			Provider: string(schemas.OpenAI),
			Model:    "gpt-4o",
			Object:   string(schemas.ChatCompletionRequest),
		},
	}

	if entry := buildInitialLogEntry(pending); entry.UserAgent != nil {
		t.Fatalf("expected nil initial user agent, got %#v", entry.UserAgent)
	} else if entry.App != nil {
		t.Fatalf("expected nil initial app, got %#v", entry.App)
	}
	if entry := buildCompleteLogEntryFromPending(pending); entry.UserAgent != nil {
		t.Fatalf("expected nil complete user agent, got %#v", entry.UserAgent)
	} else if entry.App != nil {
		t.Fatalf("expected nil complete app, got %#v", entry.App)
	}
}

// TestMCPHooksPersistPluginLogs verifies PostMCPHook stores the plugin-log
// snapshot accumulated before logging's post-hook runs.
func TestMCPHooksPersistPluginLogs(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-mcp-batch")
	ctx.SetValue(schemas.BifrostContextKeyMCPLogID, "mcp-batch-flow")
	ctx.SetValue(schemas.BifrostContextKeyUserID, "user-1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, "team-1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, "customer-1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceBusinessUnitID, "bu-1")
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		ReversibleMappings: schemas.RedactionMapsByPhase{
			Input:  map[string]string{"EMAIL-1": "private@example.com"},
			Output: map[string]string{"EMAIL-2": "result@example.com"},
		},
	})

	toolName := "docs-search"
	_, _, err = plugin.PreMCPHook(ctx, &schemas.BifrostMCPRequest{
		RequestType: schemas.MCPRequestTypeChatToolCall,
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      &toolName,
				Arguments: `{"query":"find this"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("PreMCPHook() error = %v", err)
	}

	if _, err := store.FindMCPToolLog(context.Background(), "mcp-batch-flow"); !errors.Is(err, logstore.ErrNotFound) {
		t.Fatalf("expected MCP log to stay in memory before PostMCPHook, got err=%v", err)
	}
	pendingValue, ok := plugin.pendingMCPLogsToInject.Load("mcp-batch-flow")
	if !ok {
		t.Fatal("expected pending MCP log entry")
	}
	pendingEntry, ok := pendingValue.(*logstore.MCPToolLog)
	if !ok {
		t.Fatalf("pending MCP log entry has type %T", pendingValue)
	}
	if pendingEntry.RedactionData != nil {
		t.Fatal("expected redaction data to be attached only after PostMCPHook")
	}

	result := `{"answer":"done"}`
	guardrailsName := "guardrails"
	guardrailsCtx := ctx.WithPluginScope(&guardrailsName)
	guardrailsCtx.Log(schemas.LogLevelInfo, "MCP tool arguments redacted")
	guardrailsCtx.ReleasePluginScope()

	_, _, err = plugin.PostMCPHook(ctx, &schemas.BifrostMCPResponse{
		ChatMessage: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleTool,
			Content: &schemas.ChatMessageContent{ContentStr: &result},
		},
		ExtraFields: schemas.BifrostMCPResponseExtraFields{
			MCPRequestType: schemas.MCPRequestTypeChatToolCall,
			ClientName:     "docs",
			ToolName:       "search",
			Latency:        42,
		},
	}, nil)
	if err != nil {
		t.Fatalf("PostMCPHook() error = %v", err)
	}
	if pendingEntry.RedactionData == nil {
		t.Fatal("expected PostMCPHook to attach redaction data")
	}
	if got := pendingEntry.RedactionData.ReversibleMappings.Output["EMAIL-2"]; got != "result@example.com" {
		t.Fatalf("output redaction mapping = %q, want %q", got, "result@example.com")
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	logEntry, err := store.FindMCPToolLog(context.Background(), "mcp-batch-flow")
	if err != nil {
		t.Fatalf("FindMCPToolLog() error = %v", err)
	}
	if logEntry.Status != "success" {
		t.Fatalf("expected status success, got %q", logEntry.Status)
	}
	if logEntry.ArgumentsParsed == nil {
		t.Fatalf("expected arguments to be persisted")
	}
	resultMap, ok := logEntry.ResultParsed.(map[string]interface{})
	if !ok || resultMap["answer"] != "done" {
		t.Fatalf("expected parsed result to be persisted, got %#v", logEntry.ResultParsed)
	}
	if logEntry.Latency == nil || *logEntry.Latency != 42 {
		t.Fatalf("expected latency 42, got %#v", logEntry.Latency)
	}
	var pluginLogs map[string][]schemas.PluginLogEntry
	if err := json.Unmarshal([]byte(logEntry.PluginLogs), &pluginLogs); err != nil {
		t.Fatalf("expected valid plugin logs JSON, got %q: %v", logEntry.PluginLogs, err)
	}
	if got := pluginLogs[guardrailsName]; len(got) != 1 || got[0].Message != "MCP tool arguments redacted" {
		t.Fatalf("expected guardrails plugin log to be persisted, got %#v", pluginLogs)
	}
	assertMCPLogGovernanceFields(t, logEntry, "user-1", "team-1", "customer-1", "bu-1")
}

// TestPostMCPHookFallbackStampsGovernanceFields verifies fallback MCP logs
// created without a pending pre-hook entry still carry DAC ownership fields.
func TestPostMCPHookFallbackStampsGovernanceFields(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-mcp-fallback")
	ctx.SetValue(schemas.BifrostContextKeyMCPLogID, "mcp-fallback-flow")
	ctx.SetValue(schemas.BifrostContextKeyUserID, "user-fallback")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, "team-fallback")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, "customer-fallback")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceBusinessUnitID, "bu-fallback")

	result := `{"answer":"fallback"}`
	_, _, err = plugin.PostMCPHook(ctx, &schemas.BifrostMCPResponse{
		ChatMessage: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleTool,
			Content: &schemas.ChatMessageContent{ContentStr: &result},
		},
		ExtraFields: schemas.BifrostMCPResponseExtraFields{
			MCPRequestType: schemas.MCPRequestTypeChatToolCall,
			ClientName:     "docs",
			ToolName:       "search",
			Latency:        7,
		},
	}, nil)
	if err != nil {
		t.Fatalf("PostMCPHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	logEntry, err := store.FindMCPToolLog(context.Background(), "mcp-fallback-flow")
	if err != nil {
		t.Fatalf("FindMCPToolLog() error = %v", err)
	}
	assertMCPLogGovernanceFields(t, logEntry, "user-fallback", "team-fallback", "customer-fallback", "bu-fallback")
}

// TestCleanupStalePendingMCPLogsPersistsErrorFallback verifies stale pending
// MCP logs are committed as terminal errors instead of being silently dropped.
func TestCleanupStalePendingMCPLogsPersistsErrorFallback(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	staleCreatedAt := time.Now().Add(-pendingLogTTL - time.Minute)
	plugin.pendingMCPLogsToInject.Store("mcp-stale", &logstore.MCPToolLog{
		ID:          "mcp-stale",
		RequestID:   "req-stale",
		Timestamp:   staleCreatedAt,
		ToolName:    "search",
		ServerLabel: "docs",
		Status:      "processing",
		CreatedAt:   staleCreatedAt,
		ArgumentsParsed: map[string]interface{}{
			"query": "stale input",
		},
	})

	plugin.cleanupStalePendingLogs()

	if _, ok := plugin.pendingMCPLogsToInject.Load("mcp-stale"); ok {
		t.Fatal("expected stale MCP pending log to be removed from memory")
	}
	if _, err := store.FindMCPToolLog(context.Background(), "mcp-stale"); !errors.Is(err, logstore.ErrNotFound) {
		t.Fatalf("expected stale MCP log to be queued before batch flush, got err=%v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	logEntry, err := store.FindMCPToolLog(context.Background(), "mcp-stale")
	if err != nil {
		t.Fatalf("FindMCPToolLog() error = %v", err)
	}
	if logEntry.Status != "error" {
		t.Fatalf("expected status error, got %q", logEntry.Status)
	}
	if logEntry.ArgumentsParsed == nil {
		t.Fatal("expected stale MCP input arguments to be persisted")
	}
	if logEntry.ResultParsed != nil || logEntry.Result != "" {
		t.Fatalf("expected stale MCP log to have no result, got parsed=%#v raw=%q", logEntry.ResultParsed, logEntry.Result)
	}
	if logEntry.ErrorDetailsParsed == nil || logEntry.ErrorDetailsParsed.Error == nil {
		t.Fatalf("expected stale MCP error details, got %#v", logEntry.ErrorDetailsParsed)
	}
	if !strings.Contains(logEntry.ErrorDetailsParsed.Error.Message, "pending log TTL") {
		t.Fatalf("expected stale MCP timeout message, got %q", logEntry.ErrorDetailsParsed.Error.Message)
	}
}

// TestActiveStreamSurvivesCleanup is the regression test for the prod issue where
// streaming requests running longer than the pending TTL had their in-memory
// pending entry evicted mid-flight (causing the final log row to be lost and a
// per-chunk "no pending log data found" warning). An entry whose CreatedAt is
// older than the TTL but whose LastActivity is recent must NOT be reaped.
func TestActiveStreamSurvivesCleanup(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := plugin.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup() error = %v", cleanupErr)
		}
	})

	oldCreatedAt := time.Now().Add(-pendingLogTTL - time.Minute)
	pending := &PendingLogData{
		RequestID:   "req-active-stream",
		Timestamp:   oldCreatedAt,
		Status:      "processing",
		InitialData: &InitialLogData{Object: "chat.completion.chunk"},
		CreatedAt:   oldCreatedAt,
	}
	// Simulate a chunk that arrived just now: request started long ago but is
	// still actively streaming.
	pending.LastActivity.Store(time.Now().UnixNano())
	plugin.pendingLogsEntries.Store("req-active-stream", pending)

	plugin.cleanupStalePendingLogs()

	if _, ok := plugin.pendingLogsEntries.Load("req-active-stream"); !ok {
		t.Fatal("expected actively-streaming pending entry to survive cleanup")
	}
}

// TestIdlePendingEntryEvicted verifies the reaper still removes genuinely dead
// requests: an entry whose CreatedAt AND LastActivity are both older than the
// TTL (no chunk activity for the whole idle window) must be deleted.
func TestIdlePendingEntryEvicted(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := plugin.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup() error = %v", cleanupErr)
		}
	})

	stale := time.Now().Add(-pendingLogTTL - time.Minute)
	pending := &PendingLogData{
		RequestID:   "req-idle",
		Timestamp:   stale,
		Status:      "processing",
		InitialData: &InitialLogData{Object: "chat.completion.chunk"},
		CreatedAt:   stale,
	}
	pending.LastActivity.Store(stale.UnixNano())
	plugin.pendingLogsEntries.Store("req-idle", pending)

	plugin.cleanupStalePendingLogs()

	if _, ok := plugin.pendingLogsEntries.Load("req-idle"); ok {
		t.Fatal("expected idle pending entry to be evicted by cleanup")
	}
}

// TestPreMCPHookSkipsPrefixedCodemodeTool verifies that PreMCP skips codemode
// meta-tools invoked with a client prefix (e.g. "myclient-executeToolCode"),
// not just bare names. Otherwise PostMCP — which sees the stripped bare name —
// would silently skip and leave the pending row to expire as a fake TTL error.
func TestPreMCPHookSkipsPrefixedCodemodeTool(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := plugin.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup() error = %v", cleanupErr)
		}
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-prefixed-codemode")
	ctx.SetValue(schemas.BifrostContextKeyMCPLogID, "mcp-prefixed-codemode")

	toolName := "myclient-executeToolCode"
	_, _, err = plugin.PreMCPHook(ctx, &schemas.BifrostMCPRequest{
		RequestType: schemas.MCPRequestTypeChatToolCall,
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      &toolName,
				Arguments: `{}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("PreMCPHook() error = %v", err)
	}

	if _, ok := plugin.pendingMCPLogsToInject.Load("mcp-prefixed-codemode"); ok {
		t.Fatal("expected PreMCPHook to skip prefixed codemode tool, but a pending row was created")
	}
}

func assertMCPLogGovernanceFields(t *testing.T, logEntry *logstore.MCPToolLog, userID, teamID, customerID, businessUnitID string) {
	t.Helper()
	if logEntry.UserID == nil || *logEntry.UserID != userID {
		t.Fatalf("expected user_id %q, got %#v", userID, logEntry.UserID)
	}
	if logEntry.TeamID == nil || *logEntry.TeamID != teamID {
		t.Fatalf("expected team_id %q, got %#v", teamID, logEntry.TeamID)
	}
	if logEntry.CustomerID == nil || *logEntry.CustomerID != customerID {
		t.Fatalf("expected customer_id %q, got %#v", customerID, logEntry.CustomerID)
	}
	if logEntry.BusinessUnitID == nil || *logEntry.BusinessUnitID != businessUnitID {
		t.Fatalf("expected business_unit_id %q, got %#v", businessUnitID, logEntry.BusinessUnitID)
	}
}

func TestUpdateLogEntryPreservesResponsesInputContentSummary(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:  store,
		logger: testLogger{},
	}

	requestID := "req-1"
	now := time.Now().UTC()
	inputText := "request-side text"
	initial := &InitialLogData{
		Object:   "responses",
		Provider: "openai",
		Model:    "gpt-4o-mini",
		ResponsesInputHistory: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{
				ContentStr: &inputText,
			},
		}},
	}

	if err := plugin.insertInitialLogEntry(context.Background(), requestID, "", now, 0, nil, initial); err != nil {
		t.Fatalf("insertInitialLogEntry() error = %v", err)
	}

	responsesText := "responses output"
	update := &UpdateLogData{
		Status: "success",
		ResponsesOutput: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{
				ContentStr: &responsesText,
			},
		}},
	}

	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update, true); err != nil {
		t.Fatalf("updateLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if !strings.Contains(logEntry.ContentSummary, inputText) {
		t.Fatalf("expected content summary to preserve responses input, got %q", logEntry.ContentSummary)
	}
	if strings.Contains(logEntry.ContentSummary, responsesText) {
		t.Fatalf("expected content summary to avoid overwriting with responses output-only data, got %q", logEntry.ContentSummary)
	}
}

func TestUpdateLogEntryUpdatesContentSummaryForChatOutput(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:  store,
		logger: testLogger{},
	}

	requestID := "req-chat"
	now := time.Now().UTC()
	initial := &InitialLogData{
		Object:   "chat_completion",
		Provider: "openai",
		Model:    "gpt-4o-mini",
	}

	if err := plugin.insertInitialLogEntry(context.Background(), requestID, "", now, 0, nil, initial); err != nil {
		t.Fatalf("insertInitialLogEntry() error = %v", err)
	}

	chatText := "assistant output"
	update := &UpdateLogData{
		Status: "success",
		ChatOutput: &schemas.ChatMessage{
			Role: schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{
				ContentStr: &chatText,
			},
		},
	}

	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update, true); err != nil {
		t.Fatalf("updateLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if !strings.Contains(logEntry.ContentSummary, chatText) {
		t.Fatalf("expected content summary to include chat output, got %q", logEntry.ContentSummary)
	}
}

func TestUpdateLogEntrySuppressesChatOutputWhenContentLoggingDisabled(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:  store,
		logger: testLogger{},
	}

	requestID := "req-chat-disabled"
	now := time.Now().UTC()
	initial := &InitialLogData{
		Object:   "chat_completion",
		Provider: "openai",
		Model:    "gpt-4o-mini",
	}

	if err := plugin.insertInitialLogEntry(context.Background(), requestID, "", now, 0, nil, initial); err != nil {
		t.Fatalf("insertInitialLogEntry() error = %v", err)
	}

	chatText := "assistant output should not be logged"
	update := &UpdateLogData{
		Status: "success",
		ChatOutput: &schemas.ChatMessage{
			Role: schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{
				ContentStr: &chatText,
			},
		},
	}

	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update, false); err != nil {
		t.Fatalf("updateLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.OutputMessage != "" {
		t.Fatalf("expected output_message to be suppressed, got %q", logEntry.OutputMessage)
	}
	if strings.Contains(logEntry.ContentSummary, chatText) {
		t.Fatalf("expected content summary to suppress chat output, got %q", logEntry.ContentSummary)
	}
}

func TestStoreOrEnqueueRetryPreservesAllEntries(t *testing.T) {
	// Simulate fallback/retry scenario where multiple PostLLMHook calls
	// store entries under the same traceID. All entries must be preserved.
	plugin := &LoggerPlugin{
		logger:     testLogger{},
		writeQueue: make(chan *writeQueueEntry, 10),
	}

	traceID := "trace-retry-test"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, traceID)

	// Simulate 3 retry attempts storing entries under the same traceID
	entry1 := &logstore.Log{ID: "req-attempt-1", Model: "gpt-4o"}
	entry2 := &logstore.Log{ID: "req-attempt-2", Model: "gpt-4o"}
	entry3 := &logstore.Log{ID: "req-attempt-3", Model: "claude-3-5-sonnet"}

	plugin.storeOrEnqueueEntry(ctx, entry1, nil)
	plugin.storeOrEnqueueEntry(ctx, entry2, nil)
	plugin.storeOrEnqueueEntry(ctx, entry3, nil)

	// Verify all 3 entries are stored
	val, ok := plugin.pendingLogsToInject.Load(traceID)
	if !ok {
		t.Fatal("expected pending entries for traceID, got none")
	}
	pending, ok := val.(*pendingInjectEntries)
	if !ok {
		t.Fatal("expected *pendingInjectEntries type")
	}
	if len(pending.entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(pending.entries))
	}
	if pending.entries[0].ID != "req-attempt-1" || pending.entries[1].ID != "req-attempt-2" || pending.entries[2].ID != "req-attempt-3" {
		t.Fatalf("entries not in expected order: %v, %v, %v", pending.entries[0].ID, pending.entries[1].ID, pending.entries[2].ID)
	}

	// Now test Inject flushes all entries with plugin logs attached
	trace := &schemas.Trace{
		TraceID: traceID,
		PluginLogs: []schemas.PluginLogEntry{
			{PluginName: "hello-world", Level: schemas.LogLevelInfo, Message: "test log"},
		},
	}

	if err := plugin.Inject(context.Background(), trace); err != nil {
		t.Fatalf("Inject() error = %v", err)
	}

	// Verify all 3 entries were enqueued to writeQueue
	if len(plugin.writeQueue) != 3 {
		t.Fatalf("expected 3 entries in writeQueue, got %d", len(plugin.writeQueue))
	}

	// Verify plugin logs were attached to each entry
	for i := 0; i < 3; i++ {
		qe := <-plugin.writeQueue
		if qe.log.PluginLogs == "" {
			t.Fatalf("entry %d: expected PluginLogs to be set", i)
		}
	}

	// Verify pendingLogsToInject was cleaned up
	if _, ok := plugin.pendingLogsToInject.Load(traceID); ok {
		t.Fatal("expected pendingLogsToInject to be cleaned up after Inject")
	}
}

// injectAndCollect runs Inject and returns the enqueued entries keyed by ID.
func injectAndCollect(t *testing.T, entries []*logstore.Log, upstreamMs, overheadMs float64) map[string]*logstore.Log {
	t.Helper()
	plugin := &LoggerPlugin{
		logger:     testLogger{},
		writeQueue: make(chan *writeQueueEntry, len(entries)),
	}
	traceID := "trace-overhead-order"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, traceID)
	for _, e := range entries {
		plugin.storeOrEnqueueEntry(ctx, e, nil)
	}
	trace := &schemas.Trace{
		TraceID: traceID,
		RootSpan: &schemas.Span{
			Attributes: map[string]any{
				schemas.AttrBifrostUpstreamDurationMs: upstreamMs,
				schemas.AttrBifrostOverheadDurationMs: overheadMs,
			},
		},
	}
	if err := plugin.Inject(context.Background(), trace); err != nil {
		t.Fatalf("Inject() error = %v", err)
	}
	out := make(map[string]*logstore.Log, len(entries))
	for len(plugin.writeQueue) > 0 {
		qe := <-plugin.writeQueue
		out[qe.log.ID] = qe.log
	}
	if len(out) != len(entries) {
		t.Fatalf("expected %d enqueued entries, got %d", len(entries), len(out))
	}
	return out
}

// TestInject_StampsOverheadOnLatestTimestampRow guards against positional selection:
// a concurrent list_models fan-out appends entries under one trace in nondeterministic
// completion order, so request-level upstream/overhead must land on the latest-Timestamp
// row, not "whichever slice position happened to be last". Here the latest row is the
// middle one, so a positional "last" would stamp the wrong entry.
func TestInject_StampsOverheadOnLatestTimestampRow(t *testing.T) {
	base := time.Now().UTC()
	got := injectAndCollect(t, []*logstore.Log{
		{ID: "a", Provider: "openai", Timestamp: base},
		{ID: "b", Provider: "anthropic", Timestamp: base.Add(2 * time.Second)}, // latest, but middle
		{ID: "c", Provider: "gemini", Timestamp: base.Add(1 * time.Second)},    // positional last
	}, 10, 5)

	b := got["b"]
	if b.UpstreamLatency == nil || *b.UpstreamLatency != 10 {
		t.Fatalf("entry b UpstreamLatency = %v, want 10", b.UpstreamLatency)
	}
	if b.OverheadLatency == nil || *b.OverheadLatency != 5 {
		t.Fatalf("entry b OverheadLatency = %v, want 5", b.OverheadLatency)
	}
	if b.Latency == nil || *b.Latency != 15 {
		t.Fatalf("entry b Latency = %v, want 15 (upstream+overhead)", b.Latency)
	}
	for _, id := range []string{"a", "c"} {
		if e := got[id]; e.UpstreamLatency != nil || e.OverheadLatency != nil {
			t.Fatalf("entry %s must not carry request-level latency: up=%v ov=%v", id, e.UpstreamLatency, e.OverheadLatency)
		}
	}
}

// TestInject_OverheadTieBrokenByID pins determinism when timestamps collide (the
// realistic fan-out case): the highest ID wins, so the target row is stable across runs.
func TestInject_OverheadTieBrokenByID(t *testing.T) {
	ts := time.Now().UTC()
	got := injectAndCollect(t, []*logstore.Log{
		{ID: "id-c", Provider: "openai", Timestamp: ts},
		{ID: "id-a", Provider: "anthropic", Timestamp: ts},
		{ID: "id-b", Provider: "gemini", Timestamp: ts},
	}, 8, 2)

	if e := got["id-c"]; e.OverheadLatency == nil || *e.OverheadLatency != 2 {
		t.Fatalf("highest ID id-c should carry overhead, got %v", e.OverheadLatency)
	}
	for _, id := range []string{"id-a", "id-b"} {
		if e := got[id]; e.UpstreamLatency != nil || e.OverheadLatency != nil {
			t.Fatalf("entry %s must not carry request-level latency", id)
		}
	}
}

func TestConvertToProcessedStreamResponseUsesResponsesStreamTypeForWebSocketResponses(t *testing.T) {
	result := &schemas.StreamAccumulatorResult{
		RequestID:      "req-ws-3000",
		RequestedModel: "gpt-4o-mini",
		ResolvedModel:  "gpt-4o-mini",
		Provider:       schemas.OpenAI,
		Status:         "success",
	}

	processed := convertToProcessedStreamResponse(result, schemas.WebSocketResponsesRequest)
	if processed == nil {
		t.Fatal("expected processed stream response, got nil")
	}
	if processed.StreamType != "responses" {
		t.Fatalf("expected stream type responses, got %s", processed.StreamType)
	}
}

func TestApplyRealtimeOutputToEntryBackfillsUserTranscriptFromRawRequest(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "Hello!"
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest:  `{"type":"conversation.item.input_audio_transcription.completed","transcript":"Hello."}`,
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 1 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 1", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Role != schemas.ChatMessageRoleUser {
		t.Fatalf("InputHistoryParsed[0].Role = %q, want user", entry.InputHistoryParsed[0].Role)
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != "Hello." {
		t.Fatalf("InputHistoryParsed[0] = %+v, want transcript", entry.InputHistoryParsed[0])
	}
	if entry.OutputMessageParsed == nil || entry.OutputMessageParsed.Content == nil || entry.OutputMessageParsed.Content.ContentStr == nil || *entry.OutputMessageParsed.Content.ContentStr != assistantText {
		t.Fatalf("OutputMessageParsed = %+v, want assistant text", entry.OutputMessageParsed)
	}
	if !strings.Contains(entry.ContentSummary, "Hello.") {
		t.Fatalf("ContentSummary = %q, want user transcript", entry.ContentSummary)
	}
	if !strings.Contains(entry.ContentSummary, "Hello!") {
		t.Fatalf("ContentSummary = %q, want assistant text", entry.ContentSummary)
	}
}

func TestApplyRealtimeOutputToEntryBackfillsMissingTranscriptPlaceholder(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "Hi there!"
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest:  `{"type":"conversation.item.input_audio_transcription.completed","transcript":""}`,
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 1 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 1", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != realtimeMissingTranscriptText {
		t.Fatalf("InputHistoryParsed[0] = %+v, want missing transcript placeholder", entry.InputHistoryParsed[0])
	}
	if !strings.Contains(entry.ContentSummary, realtimeMissingTranscriptText) {
		t.Fatalf("ContentSummary = %q, want missing transcript placeholder", entry.ContentSummary)
	}
}

func TestApplyRealtimeOutputToEntryBackfillsDoneMissingTranscriptPlaceholder(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "Hi there!"
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest:  `{"type":"conversation.item.done","item":{"id":"item_user","type":"message","role":"user","status":"completed","content":[{"type":"input_audio","transcript":null}]}}`,
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 1 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 1", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != realtimeMissingTranscriptText {
		t.Fatalf("InputHistoryParsed[0] = %+v, want missing transcript placeholder", entry.InputHistoryParsed[0])
	}
}

func TestApplyRealtimeOutputToEntryBackfillsRetrievedUserAndToolHistory(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "I checked that for you."
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest: strings.Join([]string{
					`{"type":"conversation.item.retrieved","item":{"id":"item_user","type":"message","role":"user","status":"completed","content":[{"type":"input_text","text":"Where is my order?"}]}}`,
					`{"type":"conversation.item.retrieved","item":{"id":"item_tool","type":"function_call_output","call_id":"call_123","status":"completed","output":"{\"status\":\"delivered\"}"}}`,
				}, "\n\n"),
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 2 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 2", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Role != schemas.ChatMessageRoleUser {
		t.Fatalf("InputHistoryParsed[0].Role = %q, want user", entry.InputHistoryParsed[0].Role)
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != "Where is my order?" {
		t.Fatalf("InputHistoryParsed[0] = %+v, want user content", entry.InputHistoryParsed[0])
	}
	if entry.InputHistoryParsed[1].Role != schemas.ChatMessageRoleTool {
		t.Fatalf("InputHistoryParsed[1].Role = %q, want tool", entry.InputHistoryParsed[1].Role)
	}
	if entry.InputHistoryParsed[1].Content == nil || entry.InputHistoryParsed[1].Content.ContentStr == nil || *entry.InputHistoryParsed[1].Content.ContentStr != `{"status":"delivered"}` {
		t.Fatalf("InputHistoryParsed[1] = %+v, want tool content", entry.InputHistoryParsed[1])
	}
	if entry.InputHistoryParsed[1].ChatToolMessage == nil || entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID == nil || *entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID != "call_123" {
		t.Fatalf("InputHistoryParsed[1].ChatToolMessage = %+v, want tool call id", entry.InputHistoryParsed[1].ChatToolMessage)
	}
}

func TestApplyRealtimeOutputToEntryBackfillsCreatedUserAndToolHistory(t *testing.T) {
	t.Parallel()

	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RawRequest: strings.Join([]string{
					`{"type":"conversation.item.created","item":{"id":"item_user","type":"message","role":"user","status":"completed","content":[{"type":"input_text","text":"I need help"}]}}`,
					`{"type":"conversation.item.created","item":{"id":"item_tool","type":"function_call_output","call_id":"call_456","status":"completed","output":"{\"status\":\"ok\"}"}}`,
				}, "\n\n"),
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)

	if len(entry.InputHistoryParsed) != 2 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 2", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Role != schemas.ChatMessageRoleUser {
		t.Fatalf("InputHistoryParsed[0].Role = %q, want user", entry.InputHistoryParsed[0].Role)
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != "I need help" {
		t.Fatalf("InputHistoryParsed[0] = %+v, want user content", entry.InputHistoryParsed[0])
	}
	if entry.InputHistoryParsed[1].Role != schemas.ChatMessageRoleTool {
		t.Fatalf("InputHistoryParsed[1].Role = %q, want tool", entry.InputHistoryParsed[1].Role)
	}
	if entry.InputHistoryParsed[1].Content == nil || entry.InputHistoryParsed[1].Content.ContentStr == nil || *entry.InputHistoryParsed[1].Content.ContentStr != `{"status":"ok"}` {
		t.Fatalf("InputHistoryParsed[1] = %+v, want tool content", entry.InputHistoryParsed[1])
	}
	if entry.InputHistoryParsed[1].ChatToolMessage == nil || entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID == nil || *entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID != "call_456" {
		t.Fatalf("InputHistoryParsed[1].ChatToolMessage = %+v, want tool call id", entry.InputHistoryParsed[1].ChatToolMessage)
	}
}

func TestApplyRealtimeOutputToEntryBackfillsAddedUserAndToolHistory(t *testing.T) {
	t.Parallel()

	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "Done."
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest: strings.Join([]string{
					`{"type":"conversation.item.added","item":{"id":"item_user","type":"message","role":"user","status":"completed","content":[{"type":"input_text","text":"hello from added item"}]}}`,
					`{"type":"conversation.item.added","item":{"id":"item_tool","type":"function_call_output","call_id":"call_added","status":"completed","output":"{\"status\":\"ok\"}"}}`,
				}, "\n\n"),
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 2 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 2", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != "hello from added item" {
		t.Fatalf("InputHistoryParsed[0] = %+v, want added user content", entry.InputHistoryParsed[0])
	}
	if entry.InputHistoryParsed[1].ChatToolMessage == nil || entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID == nil || *entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID != "call_added" {
		t.Fatalf("InputHistoryParsed[1].ChatToolMessage = %+v, want added tool call id", entry.InputHistoryParsed[1].ChatToolMessage)
	}
}

func TestApplyRealtimeOutputToEntryMergesRawTranscriptIntoStructuredRealtimeHistory(t *testing.T) {
	t.Parallel()

	plugin := &LoggerPlugin{}
	entry := &logstore.Log{
		InputHistoryParsed: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Can you help with my ticket?"),
				},
			},
			{
				Role: schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr(`{"status":"open"}`),
				},
				ChatToolMessage: &schemas.ChatToolMessage{
					ToolCallID: schemas.Ptr("call_789"),
				},
			},
		},
	}

	assistantText := "Let me check."
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest: strings.Join([]string{
					`{"type":"conversation.item.input_audio_transcription.completed","transcript":"Hello."}`,
					`{"type":"conversation.item.retrieved","item":{"id":"item_tool","type":"function_call_output","call_id":"call_789","status":"completed","output":"{\"status\":\"open\"}"}}`,
				}, "\n\n"),
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 3 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 3", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != "Can you help with my ticket?" {
		t.Fatalf("InputHistoryParsed[0] = %+v, want structured user content", entry.InputHistoryParsed[0])
	}
	if entry.InputHistoryParsed[1].Role != schemas.ChatMessageRoleUser {
		t.Fatalf("InputHistoryParsed[1].Role = %q, want user", entry.InputHistoryParsed[1].Role)
	}
	if entry.InputHistoryParsed[1].Content == nil || entry.InputHistoryParsed[1].Content.ContentStr == nil || *entry.InputHistoryParsed[1].Content.ContentStr != "Hello." {
		t.Fatalf("InputHistoryParsed[1] = %+v, want raw transcript merge", entry.InputHistoryParsed[1])
	}
	if entry.InputHistoryParsed[2].Role != schemas.ChatMessageRoleTool {
		t.Fatalf("InputHistoryParsed[2].Role = %q, want tool", entry.InputHistoryParsed[2].Role)
	}
	if entry.InputHistoryParsed[2].ChatToolMessage == nil || entry.InputHistoryParsed[2].ChatToolMessage.ToolCallID == nil || *entry.InputHistoryParsed[2].ChatToolMessage.ToolCallID != "call_789" {
		t.Fatalf("InputHistoryParsed[2].ChatToolMessage = %+v, want original tool call id", entry.InputHistoryParsed[2].ChatToolMessage)
	}
	if strings.Count(entry.ContentSummary, "Hello.") != 1 {
		t.Fatalf("ContentSummary = %q, want one merged transcript", entry.ContentSummary)
	}
}

func TestApplyRealtimeOutputToEntryDoesNotPersistRawWhenShouldStoreRawFalse(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "Hello!"
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest:  `{"type":"conversation.item.input_audio_transcription.completed","transcript":"Hello."}`,
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, false, true)

	if entry.RawRequest != "" {
		t.Fatalf("expected RawRequest to remain empty when shouldStoreRaw=false, got %q", entry.RawRequest)
	}
	if entry.RawResponse != "" {
		t.Fatalf("expected RawResponse to remain empty when shouldStoreRaw=false, got %q", entry.RawResponse)
	}
	if len(entry.InputHistoryParsed) == 0 {
		t.Fatal("expected InputHistoryParsed to still be backfilled when shouldStoreRaw=false")
	}
	if entry.InputHistoryParsed[0].Role != schemas.ChatMessageRoleUser {
		t.Fatalf("InputHistoryParsed[0].Role = %q, want user", entry.InputHistoryParsed[0].Role)
	}
}

// TestContentLoggingEnabledHelper verifies precedence: ctx override > global config > default-enabled.
func TestContentLoggingEnabledHelper(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name          string
		globalDisable *bool
		ctxOverride   *bool // nil = don't set the key
		want          bool
	}{
		{"no config no override → enabled", nil, nil, true},
		{"global disable=false no override → enabled", boolPtr(false), nil, true},
		{"global disable=true no override → disabled", boolPtr(true), nil, false},
		{"ctx override=false global disable=true → enabled", boolPtr(true), boolPtr(false), true},
		{"ctx override=true global disable=false → disabled", boolPtr(false), boolPtr(true), false},
		{"ctx override=true nil global → disabled", nil, boolPtr(true), false},
		{"ctx override=false nil global → enabled", nil, boolPtr(false), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &LoggerPlugin{disableContentLogging: tc.globalDisable}

			var ctx *schemas.BifrostContext
			if tc.ctxOverride != nil {
				ctx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				ctx.SetValue(schemas.BifrostContextKeyAllowPerRequestStorageOverride, true)
				ctx.SetValue(schemas.BifrostContextKeyDisableContentLogging, *tc.ctxOverride)
			}

			got := p.contentLoggingEnabled(ctx)
			if got != tc.want {
				t.Errorf("contentLoggingEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestContentLoggingEnabledHelperNilCtx verifies nil context falls back to global config.
func TestContentLoggingEnabledHelperNilCtx(t *testing.T) {
	disabled := true
	p := &LoggerPlugin{disableContentLogging: &disabled}
	if p.contentLoggingEnabled(nil) {
		t.Error("expected false with nil ctx and global disable=true")
	}
}

// TestUpdateLogEntryPerRequestOverrideEnablesContent verifies that passing contentLoggingEnabled=true
// to updateLogEntry stores output even when the plugin's global toggle is disabled.
func TestUpdateLogEntryPerRequestOverrideEnablesContent(t *testing.T) {
	store := newTestStore(t)
	disabled := true
	plugin := &LoggerPlugin{
		store:                 store,
		logger:                testLogger{},
		disableContentLogging: &disabled, // global: off
	}

	requestID := "req-per-request-enable"
	now := time.Now().UTC()
	if err := plugin.insertInitialLogEntry(context.Background(), requestID, "", now, 0, nil, &InitialLogData{
		Object:   "chat_completion",
		Provider: "openai",
		Model:    "gpt-4o-mini",
	}); err != nil {
		t.Fatalf("insertInitialLogEntry() error = %v", err)
	}

	chatText := "should be stored via per-request override"
	update := &UpdateLogData{
		Status: "success",
		ChatOutput: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: &chatText},
		},
	}

	// Explicitly pass true — simulates the per-request ctx override enabling content logging
	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update, true); err != nil {
		t.Fatalf("updateLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.OutputMessage == "" {
		t.Error("expected output_message to be stored when contentLoggingEnabled=true override is used")
	}
}

// TestUpdateLogEntryPerRequestOverrideDisablesContent verifies that passing contentLoggingEnabled=false
// suppresses output even when the plugin's global toggle is enabled.
func TestUpdateLogEntryPerRequestOverrideDisablesContent(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:  store,
		logger: testLogger{},
		// global: nil → content logging on by default
	}

	requestID := "req-per-request-disable"
	now := time.Now().UTC()
	if err := plugin.insertInitialLogEntry(context.Background(), requestID, "", now, 0, nil, &InitialLogData{
		Object:   "chat_completion",
		Provider: "openai",
		Model:    "gpt-4o-mini",
	}); err != nil {
		t.Fatalf("insertInitialLogEntry() error = %v", err)
	}

	chatText := "should NOT be stored"
	update := &UpdateLogData{
		Status: "success",
		ChatOutput: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: &chatText},
		},
	}

	// Explicitly pass false — simulates x-bf-disable-content-logging: true on this request
	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update, false); err != nil {
		t.Fatalf("updateLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.OutputMessage != "" {
		t.Errorf("expected output_message to be suppressed, got %q", logEntry.OutputMessage)
	}
}

// TestApplyNonStreamingOutputToEntryContentLoggingDisabled verifies that output fields are
// suppressed when contentLoggingEnabled=false.
func TestApplyNonStreamingOutputToEntryContentLoggingDisabled(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	chatText := "should not appear"
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role:    schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{ContentStr: &chatText},
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
			},
		},
	}

	plugin.applyNonStreamingOutputToEntry(entry, result, false, false)

	if entry.OutputMessageParsed != nil {
		t.Error("expected OutputMessageParsed to be nil when contentLoggingEnabled=false")
	}
}

// TestApplyNonStreamingOutputToEntryContentLoggingEnabled verifies that output fields are
// stored when contentLoggingEnabled=true regardless of the global plugin config.
func TestApplyNonStreamingOutputToEntryContentLoggingEnabled(t *testing.T) {
	disabled := true
	plugin := &LoggerPlugin{disableContentLogging: &disabled} // global off, but explicit true passed
	entry := &logstore.Log{}

	chatText := "should appear"
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role:    schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{ContentStr: &chatText},
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
			},
		},
	}

	plugin.applyNonStreamingOutputToEntry(entry, result, false, true)

	if entry.OutputMessageParsed == nil {
		t.Error("expected OutputMessageParsed to be set when contentLoggingEnabled=true")
	}
}

// TestApplyNonStreamingOutputToEntryVideoOutputs verifies that each video response lands in
// its own log column. Generation, remix and retrieve all return BifrostVideoGenerationResponse,
// so the request type is the only thing separating generation from retrieve.
func TestApplyNonStreamingOutputToEntryVideoOutputs(t *testing.T) {
	videoResponse := func(requestType schemas.RequestType) *schemas.BifrostResponse {
		return &schemas.BifrostResponse{
			VideoGenerationResponse: &schemas.BifrostVideoGenerationResponse{
				ID:          "vid_123:openai",
				Status:      schemas.VideoStatusQueued,
				ExtraFields: schemas.BifrostResponseExtraFields{RequestType: requestType},
			},
		}
	}

	tests := []struct {
		name   string
		result *schemas.BifrostResponse
		verify func(t *testing.T, entry *logstore.Log)
	}{
		{
			name:   "generation",
			result: videoResponse(schemas.VideoGenerationRequest),
			verify: func(t *testing.T, entry *logstore.Log) {
				if entry.VideoGenerationOutputParsed == nil {
					t.Fatal("expected VideoGenerationOutputParsed to be set")
				}
				if entry.VideoRetrieveOutputParsed != nil {
					t.Error("expected VideoRetrieveOutputParsed to stay nil")
				}
				if entry.VideoGenerationOutputParsed.ID != "vid_123:openai" {
					t.Errorf("expected provider-scoped ID, got %q", entry.VideoGenerationOutputParsed.ID)
				}
			},
		},
		{
			name:   "remix routes to generation",
			result: videoResponse(schemas.VideoRemixRequest),
			verify: func(t *testing.T, entry *logstore.Log) {
				if entry.VideoGenerationOutputParsed == nil {
					t.Error("expected VideoGenerationOutputParsed to be set")
				}
			},
		},
		{
			name:   "retrieve",
			result: videoResponse(schemas.VideoRetrieveRequest),
			verify: func(t *testing.T, entry *logstore.Log) {
				if entry.VideoRetrieveOutputParsed == nil {
					t.Fatal("expected VideoRetrieveOutputParsed to be set")
				}
				if entry.VideoGenerationOutputParsed != nil {
					t.Error("expected VideoGenerationOutputParsed to stay nil")
				}
			},
		},
		{
			name: "download",
			result: &schemas.BifrostResponse{
				VideoDownloadResponse: &schemas.BifrostVideoDownloadResponse{
					VideoID:     "vid_123:openai",
					ContentType: "video/mp4",
					ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.VideoDownloadRequest},
				},
			},
			verify: func(t *testing.T, entry *logstore.Log) {
				if entry.VideoDownloadOutputParsed == nil {
					t.Error("expected VideoDownloadOutputParsed to be set")
				}
			},
		},
		{
			name: "list",
			result: &schemas.BifrostResponse{
				VideoListResponse: &schemas.BifrostVideoListResponse{
					Object:      "list",
					Data:        []schemas.VideoObject{{ID: "vid_123:openai"}},
					ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.VideoListRequest},
				},
			},
			verify: func(t *testing.T, entry *logstore.Log) {
				if entry.VideoListOutputParsed == nil {
					t.Error("expected VideoListOutputParsed to be set")
				}
			},
		},
		{
			name: "delete",
			result: &schemas.BifrostResponse{
				VideoDeleteResponse: &schemas.BifrostVideoDeleteResponse{
					ID:          "vid_123:openai",
					Deleted:     true,
					ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.VideoDeleteRequest},
				},
			},
			verify: func(t *testing.T, entry *logstore.Log) {
				if entry.VideoDeleteOutputParsed == nil {
					t.Error("expected VideoDeleteOutputParsed to be set")
				}
			},
		},
	}

	plugin := &LoggerPlugin{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &logstore.Log{}
			plugin.applyNonStreamingOutputToEntry(entry, tt.result, false, true)
			tt.verify(t, entry)
		})
	}

	t.Run("content logging disabled", func(t *testing.T) {
		entry := &logstore.Log{}
		plugin.applyNonStreamingOutputToEntry(entry, videoResponse(schemas.VideoGenerationRequest), false, false)
		if entry.VideoGenerationOutputParsed != nil {
			t.Error("expected VideoGenerationOutputParsed to be nil when contentLoggingEnabled=false")
		}
	})
}

// TestGuardrailDebugForLogReadsContextWithoutResponse verifies input blocks remain observable.
func TestGuardrailDebugForLogReadsContextWithoutResponse(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	requireCall := schemas.BifrostGuardrailJudgeCall{
		JudgeProvider: schemas.OpenAI,
		JudgeModel:    "gpt-test",
		TotalTokens:   18,
	}
	if !schemas.AppendGuardrailJudgeCallOnContext(ctx, requireCall) {
		t.Fatal("failed to append guardrail judge call")
	}

	debug := guardrailDebugForLog(ctx, nil)
	if debug == nil || len(debug.JudgeCalls) != 1 {
		t.Fatalf("guardrail debug = %#v; want one context judge call", debug)
	}
	if debug.JudgeCalls[0] != requireCall {
		t.Fatalf("guardrail call = %#v; want %#v", debug.JudgeCalls[0], requireCall)
	}
}

// Batch claims are fenced on the runner id, so it must differ between workers
// that could contend for the same job. In OSS SetClusterNodeID is never called,
// so the per-process fallback is what keeps replicas sharing a database apart.
func TestBatchRunnerID(t *testing.T) {
	plugin := &LoggerPlugin{}

	t.Run("falls back to a per-process id when no cluster node is set", func(t *testing.T) {
		id := plugin.batchRunnerID("logging")
		if id == "logging" || id == "logging:" {
			t.Fatalf("runner id must not collapse to a shared constant, got %q", id)
		}
		if !strings.HasPrefix(id, "logging:") {
			t.Fatalf("expected a logging: prefix, got %q", id)
		}
		// Distinct roles in the same process must not collide either.
		if other := plugin.batchRunnerID("batch-sweeper"); other == id {
			t.Fatalf("distinct prefixes produced the same runner id: %q", id)
		}
	})

	t.Run("prefers the cluster node id when present", func(t *testing.T) {
		clustered := &LoggerPlugin{}
		clustered.clusterNodeID.Store("node-7")
		if got := clustered.batchRunnerID("logging"); got != "logging:node-7" {
			t.Fatalf("expected logging:node-7, got %q", got)
		}
	})
}

func TestRecordBatchJobLifecycle_CompletedSetsNextCheckAt(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin := &LoggerPlugin{
		ctx:        context.Background(),
		store:      store,
		batchStore: bs,
		logger:     testLogger{},
	}

	tests := []struct {
		name       string
		status     string
		wantNow    bool
		wantMinute bool
		wantNil    bool
	}{
		{name: "in_progress", status: string(schemas.BatchStatusInProgress), wantMinute: true},
		{name: "validating", status: string(schemas.BatchStatusValidating), wantMinute: true},
		{name: "finalizing", status: string(schemas.BatchStatusFinalizing), wantMinute: true},
		{name: "completed", status: string(schemas.BatchStatusCompleted), wantNow: true},
		// Anthropic reports "ended"; it takes the same immediate-accounting branch.
		{name: "ended", status: string(schemas.BatchStatusEnded), wantNow: true},
		{name: "failed", status: string(schemas.BatchStatusFailed), wantNil: true},
		{name: "cancelled", status: string(schemas.BatchStatusCancelled), wantNil: true},
		{name: "expired", status: string(schemas.BatchStatusExpired), wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batchID := "batch-" + tt.name
			entry := &logstore.Log{
				Provider: string(schemas.OpenAI),
				Model:    "gpt-4o-mini",
			}
			retrieveResp := &schemas.BifrostBatchRetrieveResponse{
				ID:       batchID,
				Endpoint: string(schemas.BatchEndpointChatCompletions),
				Status:   schemas.BatchStatus(tt.status),
			}
			result := &schemas.BifrostResponse{
				BatchRetrieveResponse: retrieveResp,
			}
			plugin.recordBatchJobLifecycle(entry, result)

			job, err := bs.GetProviderJob(context.Background(), cstables.ProviderJobID(cstables.ProviderJobKindBatch, "openai", batchID))
			if err != nil {
				t.Fatalf("GetProviderJob() error = %v", err)
			}
			if job.JobID != batchID {
				t.Fatalf("expected batch_id %s, got %s", batchID, job.JobID)
			}
			if job.ProviderStatus != tt.status {
				t.Fatalf("expected status %s, got %s", tt.status, job.ProviderStatus)
			}

			switch {
			case tt.wantNow:
				if job.NextCheckAt == nil {
					t.Fatal("expected NextCheckAt to be set")
				}
				if job.NextCheckAt.After(time.Now().UTC().Add(2 * time.Second)) {
					t.Fatalf("expected NextCheckAt to be now, got %v", job.NextCheckAt)
				}
			case tt.wantMinute:
				if job.NextCheckAt == nil {
					t.Fatal("expected NextCheckAt to be set")
				}
				if job.NextCheckAt.Before(time.Now().UTC().Add(50*time.Second)) ||
					job.NextCheckAt.After(time.Now().UTC().Add(70*time.Second)) {
					t.Fatalf("expected NextCheckAt ~1min from now, got %v (now=%v)", job.NextCheckAt, time.Now().UTC())
				}
			case tt.wantNil:
				if job.NextCheckAt != nil {
					t.Fatalf("expected NextCheckAt to be nil, got %v", *job.NextCheckAt)
				}
			}
		})
	}
}

func TestRecordBatchJobLifecycle_CreatePersistsModel(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin := &LoggerPlugin{
		ctx:        context.Background(),
		store:      store,
		batchStore: bs,
		logger:     testLogger{},
	}

	entry := &logstore.Log{
		ID:       "req-create-batch",
		Provider: string(schemas.OpenAI),
		Model:    "gpt-4o",
	}
	createResp := &schemas.BifrostBatchCreateResponse{
		ID:          "batch-create-123",
		Status:      schemas.BatchStatusValidating,
		InputFileID: "file-abc",
	}
	result := &schemas.BifrostResponse{
		BatchCreateResponse: createResp,
	}
	plugin.recordBatchJobLifecycle(entry, result)

	job, err := bs.GetProviderJob(context.Background(), cstables.ProviderJobID(cstables.ProviderJobKindBatch, "openai", "batch-create-123"))
	if err != nil {
		t.Fatalf("GetProviderJob() error = %v", err)
	}
	if job.Model != "gpt-4o" {
		t.Fatalf("expected model %s, got %s", "gpt-4o", job.Model)
	}
	if job.InputFileID != "file-abc" {
		t.Fatalf("expected input_file_id %s, got %s", "file-abc", job.InputFileID)
	}
	if job.AccountingStatus != cstables.ProviderJobAccountingStatusPending {
		t.Fatalf("expected accounting_status %s, got %s", cstables.ProviderJobAccountingStatusPending, job.AccountingStatus)
	}
}

func TestAccountBatchResults_NonResultsResponseIsNoop(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin := &LoggerPlugin{
		ctx:        context.Background(),
		store:      store,
		batchStore: bs,
		logger:     testLogger{},
		// Pricing must be set or accountBatchResults returns on the nil-pricing
		// guard and this test passes without ever reaching the check it covers.
		pricingManager: newTestPricingManager(t),
	}

	entry := &logstore.Log{
		ID:       "req-no-results",
		Provider: string(schemas.OpenAI),
		Model:    "gpt-4o-mini",
	}
	nonResultsResult := &schemas.BifrostResponse{
		ListModelsResponse: &schemas.BifrostListModelsResponse{},
	}
	// accountBatchResults with no BatchResultsResponse should be a no-op
	plugin.accountBatchResults(entry, nonResultsResult, nil)

	// Verify no batch_jobs rows were created
	jobs, err := bs.ListDueProviderJobs(context.Background(), cstables.ProviderJobKindBatch, "openai", time.Now().UTC().Add(time.Hour), 100)
	if err != nil {
		t.Fatalf("ListDueProviderJobs() error = %v", err)
	}
	if len(jobs) > 0 {
		t.Fatalf("expected no batch jobs from non-batch-response, got %d", len(jobs))
	}
}

func TestAccountBatchResults_EmptyResultsMarksUnpriceable(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin := &LoggerPlugin{
		ctx:            context.Background(),
		store:          store,
		batchStore:     bs,
		logger:         testLogger{},
		pricingManager: newTestPricingManager(t),
	}

	entry := &logstore.Log{
		ID:       "req-empty-batch-results",
		Provider: string(schemas.OpenAI),
		Model:    "gpt-4o-mini",
	}
	result := &schemas.BifrostResponse{
		BatchResultsResponse: &schemas.BifrostBatchResultsResponse{
			BatchID: "batch-empty-results",
			Results: nil,
		},
	}

	plugin.accountBatchResults(entry, result, nil)

	job, err := bs.GetProviderJob(context.Background(), cstables.ProviderJobID(cstables.ProviderJobKindBatch, "openai", "batch-empty-results"))
	if err != nil {
		t.Fatalf("GetProviderJob() error = %v", err)
	}
	if job.AccountingStatus != cstables.ProviderJobAccountingStatusUnpriceable {
		t.Fatalf("expected accounting_status %s, got %s", cstables.ProviderJobAccountingStatusUnpriceable, job.AccountingStatus)
	}
	if job.UnpriceableReason == nil || *job.UnpriceableReason != jobaccounting.UnpriceableReasonNoResults {
		t.Fatalf("expected unpriceable_reason %s, got %#v", jobaccounting.UnpriceableReasonNoResults, job.UnpriceableReason)
	}
}

func TestAccountBatchResults_RepeatedFetchDisplaysPriceWithoutBilling(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, bs, newTestPricingManager(t), nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	newResult := func() *schemas.BifrostResponse {
		return &schemas.BifrostResponse{
			BatchResultsResponse: &schemas.BifrostBatchResultsResponse{
				BatchID: "batch-repeat-fetch",
				Results: []schemas.BatchResultItem{{
					CustomID: "custom-1",
					Response: &schemas.BatchResultResponse{
						StatusCode: 200,
						Body: map[string]interface{}{
							"model": "gpt-4o",
							"usage": map[string]interface{}{
								"prompt_tokens":     100,
								"completion_tokens": 50,
								"total_tokens":      150,
							},
						},
					},
				}},
			},
		}
	}

	first := &logstore.Log{ID: "req-fetch-1", Provider: string(schemas.OpenAI), Model: "gpt-4o"}
	plugin.accountBatchResults(first, newResult(), nil)

	if first.BatchDebugParsed == nil || first.BatchDebugParsed.Accounting == nil || first.BatchDebugParsed.Accounting.Cost == nil {
		t.Fatalf("expected first call's own row to show the settled price, got %#v", first.BatchDebugParsed)
	}
	wantCost := *first.BatchDebugParsed.Accounting.Cost
	if wantCost <= 0 {
		t.Fatalf("expected a positive settled cost, got %v", wantCost)
	}
	if first.Cost != nil {
		t.Fatalf("entry.Cost must stay nil on a batch_results row, got %v", *first.Cost)
	}
	if first.BatchDebugParsed.Status != string(schemas.BatchStatusCompleted) {
		t.Fatalf("expected status %q, got %q", schemas.BatchStatusCompleted, first.BatchDebugParsed.Status)
	}

	second := &logstore.Log{ID: "req-fetch-2", Provider: string(schemas.OpenAI), Model: "gpt-4o"}
	plugin.accountBatchResults(second, newResult(), nil)

	if second.BatchDebugParsed == nil || second.BatchDebugParsed.Accounting == nil || second.BatchDebugParsed.Accounting.Cost == nil {
		t.Fatalf("expected the repeat fetch's own row to also show the settled price, got %#v", second.BatchDebugParsed)
	}
	if diff := *second.BatchDebugParsed.Accounting.Cost - wantCost; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("repeat fetch price %v does not match originally settled price %v", *second.BatchDebugParsed.Accounting.Cost, wantCost)
	}
	if second.BatchDebugParsed.Accounting.Incomplete {
		t.Fatal("repeat fetch must not show incomplete once the batch fully priced")
	}
	if second.Cost != nil {
		t.Fatalf("entry.Cost must stay nil on the repeat fetch's row too, got %v", *second.Cost)
	}
	if second.BatchDebugParsed.Status != string(schemas.BatchStatusCompleted) {
		t.Fatalf("expected mirrored status %q, got %q", schemas.BatchStatusCompleted, second.BatchDebugParsed.Status)
	}

	logs, err := store.SearchLogs(context.Background(), logstore.SearchFilters{}, logstore.PaginationOptions{Limit: 10})
	if err != nil {
		t.Fatalf("SearchLogs() error = %v", err)
	}
	aggregateRows := 0
	for _, l := range logs.Logs {
		if l.ID == jobaccounting.AccountingLogID(jobaccounting.ProviderJobKindBatch, schemas.OpenAI, "batch-repeat-fetch") {
			aggregateRows++
		}
	}
	if aggregateRows != 1 {
		t.Fatalf("expected exactly 1 aggregate cost row, got %d", aggregateRows)
	}
}

// noopBatchFetcher satisfies jobaccounting.BatchResultFetcher without touching a
// provider: the point of the test below is that it is never reached.
type noopBatchFetcher struct{}

func (noopBatchFetcher) RetrieveBatch(context.Context, *cstables.TableProviderJob) (*schemas.BifrostBatchRetrieveResponse, error) {
	return nil, nil
}

func (noopBatchFetcher) FetchBatchResults(context.Context, *cstables.TableProviderJob) (*schemas.BifrostBatchResultsResponse, error) {
	return nil, nil
}

// A plugin reload can call the wiring against an instance that is already shutting
// down. Cleanup sets the shutdown flag and cancels the sweeper under p.mu before it
// reaches p.wg.Wait(), so a start arriving afterwards must decline rather than run
// wg.Add concurrently with that Wait — which is a WaitGroup contract violation and
// would leave a sweeper writing through a closed plugin.
func TestStartBatchAccountingSweeperAfterCleanupIsRefused(t *testing.T) {
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, newTestStore(t), newFakeBatchStore(), newTestPricingManager(t), nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	cancel := plugin.StartBatchAccountingSweeper(noopBatchFetcher{}, time.Minute, nil)
	if cancel == nil {
		t.Fatal("StartBatchAccountingSweeper must always return a cancel func")
	}
	cancel() // must be safe to call on the refused path

	plugin.mu.Lock()
	registered := plugin.batchSweeperCancel
	plugin.mu.Unlock()
	if registered != nil {
		t.Fatal("no sweeper should be registered after Cleanup")
	}
}

// The batch job row is the only durable record of who to bill: settlement can run
// hours later from a sweeper with no request context. A virtual key alone is not
// enough — an access profile shares one across users — so the user, team, customer
// and the creating log row must all be captured at create time.
func TestRecordBatchJobLifecycle_CreatePersistsRequesterIdentity(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin := &LoggerPlugin{
		ctx:        context.Background(),
		store:      store,
		batchStore: bs,
		logger:     testLogger{},
	}

	vkID := "vk-access-profile"
	userID := "user-alice"
	teamID := "team-1"
	customerID := "customer-1"
	entry := &logstore.Log{
		ID:                 "req-create-batch-identity",
		Provider:           string(schemas.OpenAI),
		Model:              "gpt-4o",
		SelectedKeyID:      "key-1",
		VirtualKeyID:       &vkID,
		UserID:             &userID,
		TeamID:             &teamID,
		CustomerID:         &customerID,
		BudgetIDsParsed:    []string{"budget-1"},
		RateLimitIDsParsed: []string{"rl-1"},
	}
	plugin.recordBatchJobLifecycle(entry, &schemas.BifrostResponse{
		BatchCreateResponse: &schemas.BifrostBatchCreateResponse{
			ID:     "batch-identity-123",
			Status: schemas.BatchStatusValidating,
		},
	})

	job, err := bs.GetProviderJob(context.Background(), cstables.ProviderJobID(cstables.ProviderJobKindBatch, "openai", "batch-identity-123"))
	if err != nil {
		t.Fatalf("GetProviderJob() error = %v", err)
	}
	if job.UserID == nil || *job.UserID != userID {
		t.Fatalf("expected user_id %s, got %v", userID, job.UserID)
	}
	if job.TeamID == nil || *job.TeamID != teamID {
		t.Fatalf("expected team_id %s, got %v", teamID, job.TeamID)
	}
	if job.CustomerID == nil || *job.CustomerID != customerID {
		t.Fatalf("expected customer_id %s, got %v", customerID, job.CustomerID)
	}
	if job.SourceLogID == nil || *job.SourceLogID != entry.ID {
		t.Fatalf("expected source_log_id %s, got %v", entry.ID, job.SourceLogID)
	}
}
