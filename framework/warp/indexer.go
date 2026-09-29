package warp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

const (
	warpIndexQueueSize    = 256
	warpIndexWorkers      = 2
	warpMaxEmbeddingBytes = 16 * 1024
)

// EmbeddingExecutor is the narrow part of the gateway client Warp needs.
type EmbeddingExecutor func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError)

// IndexOutcome distinguishes a privacy/shape skip from a successful upsert.
type IndexOutcome string

const (
	IndexOutcomeIndexed IndexOutcome = "indexed"
	IndexOutcomeSkipped IndexOutcome = "skipped"
)

type logIndexItem struct {
	id       string
	text     string
	metadata map[string]interface{}
}

// LogIndexer owns all stream-sized and queued indexing state outside request
// contexts. Live notifications only enqueue small, owned snapshots.
type LogIndexer struct {
	store   configstore.WarpStore
	vectors vectorstore.VectorStore
	embed   EmbeddingExecutor
	logger  schemas.Logger

	queue chan logIndexItem
	done  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup
	// closeMu serializes Enqueue against Close, and closed is what Enqueue
	// checks. The done channel alone cannot do this: select picks at random
	// among ready cases, so with a buffered queue it could take the send even
	// with done already closed - handing the item to workers that have exited.
	closeMu sync.RWMutex
	closed  bool
	// ctx scopes every background index to the indexer's own lifetime, and
	// cancel interrupts whatever is in flight when Close runs. Workers used to
	// pass context.Background(), which generateWarpEmbedding turns into a
	// NoDeadline BifrostContext - so a provider that stopped answering held
	// shutdown open with nothing able to interrupt it.
	ctx    context.Context
	cancel context.CancelFunc
	// ensureMu guards the provisioning cache below. Provisioning is per
	// configuration, not per log: ListNamespaces plus CreateNamespace on every
	// indexed log is vector-store overhead on the hot path, and under load it
	// turns into queue-full drops. A successful ensure records its pair here;
	// a failed write clears it, because the failure may mean the namespace
	// vanished underneath the cache.
	ensureMu         sync.Mutex
	ensuredNamespace string
	ensuredDimension int
	// drainBudget is how long Close lets accepted work finish before cancelling
	// it. A field rather than a constant so a test can pick a short one without
	// waiting out the production value.
	drainBudget time.Duration
}

// warpIndexItemTimeout bounds one background index. Long enough for a slow
// embedding call, short enough that a wedged provider cannot pin a worker for
// the life of the process.
const warpIndexItemTimeout = 2 * time.Minute

func NewLogIndexer(store configstore.WarpStore, vectors vectorstore.VectorStore, embed EmbeddingExecutor, logger schemas.Logger) *LogIndexer {
	ctx, cancel := context.WithCancel(context.Background())
	indexer := &LogIndexer{
		store: store, vectors: vectors, embed: embed, logger: logger,
		queue: make(chan logIndexItem, warpIndexQueueSize), done: make(chan struct{}),
		ctx: ctx, cancel: cancel, drainBudget: warpIndexDrainBudget,
	}
	for range warpIndexWorkers {
		indexer.wg.Add(1)
		go indexer.worker()
	}
	return indexer
}

// Enqueue never blocks the logging writer or the original inference request.
//
// The read lock is held across the send, so an entry is either accepted while
// workers are still running or refused outright. notifyLogCallbacks invokes its
// subscribers after releasing its own mutex, so a callback genuinely can arrive
// after Shutdown unsubscribed it - and an item accepted then would sit in a
// queue nobody reads, unindexed and unrecorded.
func (i *LogIndexer) Enqueue(_ context.Context, entry *logstore.Log) {
	item, ok := buildLogIndexItem(entry)
	if !ok {
		return
	}
	i.closeMu.RLock()
	defer i.closeMu.RUnlock()
	if i.closed {
		i.warnf("warp log indexing has shut down; skipped log %s (manual backfill can repair it)", item.id)
		return
	}
	select {
	case i.queue <- item:
	default:
		i.warnf("warp log embedding queue is full; skipped log %s (manual backfill can repair it)", item.id)
	}
}

// Index performs the same idempotent operation synchronously for Sidekiq.
func (i *LogIndexer) Index(ctx context.Context, entry *logstore.Log) (IndexOutcome, error) {
	outcome, _, err := i.IndexWithConfig(ctx, nil, entry)
	return outcome, err
}

// IndexWithConfig indexes against a caller-pinned configuration.
//
// The backfill verifies the embedding configuration has not changed before each
// batch, but re-reading it per entry reopened the window it just closed: a save
// landing between the check and the write indexed logs under the new embedding
// space while the job still believed it was on the frozen one. Passing the
// verified config down means the check and the write agree by construction,
// with no lock spanning the two.
//
// A nil config restores the live read, which is what the in-process queue wants.
//
// The returned usage is what the embedding call consumed, reported even when
// the upsert after it failed: the provider billed the call either way, and the
// backfill totals it so an operator can see what a window cost to index.
func (i *LogIndexer) IndexWithConfig(ctx context.Context, config *schemas.WarpConfig, entry *logstore.Log) (IndexOutcome, *schemas.BifrostLLMUsage, error) {
	item, ok := buildLogIndexItem(entry)
	if !ok {
		return IndexOutcomeSkipped, nil, nil
	}
	indexed, usage, err := i.indexItemWithConfig(ctx, config, item)
	if err != nil {
		return "", usage, err
	}
	if !indexed {
		// Nothing was written - Warp is not configured to embed. Reporting this
		// as indexed tells the backfill it has covered a log it never touched, so
		// the gap is never repaired.
		return IndexOutcomeSkipped, usage, nil
	}
	return IndexOutcomeIndexed, usage, nil
}

func (i *LogIndexer) worker() {
	defer i.wg.Done()
	for {
		select {
		case <-i.done:
			// Drain what was already accepted. Enqueue told its caller the item
			// was taken, so dropping the rest on an orderly shutdown loses exactly
			// the work the backfill would then have to redo. The item context is
			// still bounded, so a wedged provider cannot stall shutdown forever.
			for {
				select {
				case item := <-i.queue:
					i.runItem(item)
				default:
					return
				}
			}
		case item := <-i.queue:
			i.runItem(item)
		}
	}
}

// runItem indexes one queued item under a bounded context.
//
// Derived from the indexer's own context so Close can interrupt it, and
// deadlined so one unresponsive provider cannot hold a worker - or a shutdown -
// open indefinitely.
func (i *LogIndexer) runItem(item logIndexItem) {
	itemCtx, cancelItem := context.WithTimeout(i.ctx, warpIndexItemTimeout)
	defer cancelItem()
	if _, err := i.indexItem(itemCtx, item); err != nil {
		i.warnf("failed to index Warp log %s: %v", item.id, err)
	}
}

func (i *LogIndexer) indexItem(ctx context.Context, item logIndexItem) (bool, error) {
	indexed, _, err := i.indexItemWithConfig(ctx, nil, item)
	return indexed, err
}

// indexItemWithConfig indexes against a caller-pinned configuration, and
// reports whether anything was actually written so callers can tell "indexed"
// apart from "there was nothing to index", along with the embedding call's
// usage whenever one was made.
func (i *LogIndexer) indexItemWithConfig(ctx context.Context, config *schemas.WarpConfig, item logIndexItem) (bool, *schemas.BifrostLLMUsage, error) {
	if config == nil {
		row, err := i.store.GetWarpConfig(ctx)
		if err != nil {
			return false, nil, fmt.Errorf("read Warp configuration: %w", err)
		}
		config = configFromRow(row)
	}
	if !config.IsConfigured() {
		return false, nil, nil
	}
	namespace := config.EffectiveLogVectorStoreNamespace()
	if err := i.ensureNamespaceOnce(ctx, namespace, config.EmbeddingDimension); err != nil {
		return false, nil, err
	}
	embedding, usage, err := generateWarpEmbedding(ctx, i.embed, config, item.text)
	if err != nil {
		return false, usage, err
	}
	if err := i.vectors.Add(ctx, namespace, item.id, embedding, item.metadata); err != nil {
		// The failure may mean the namespace vanished underneath the cache, so
		// the next item re-provisions instead of writing into the void forever.
		i.ensureMu.Lock()
		i.ensuredNamespace, i.ensuredDimension = "", 0
		i.ensureMu.Unlock()
		return false, usage, err
	}
	return true, usage, nil
}

// ensureNamespaceOnce provisions the namespace unless the last successful
// ensure already covered this exact namespace/dimension pair.
func (i *LogIndexer) ensureNamespaceOnce(ctx context.Context, namespace string, dimension int) error {
	i.ensureMu.Lock()
	defer i.ensureMu.Unlock()
	if i.ensuredNamespace == namespace && i.ensuredDimension == dimension {
		return nil
	}
	if _, err := ensureWarpNamespace(ctx, i.vectors, namespace, dimension); err != nil {
		return err
	}
	i.ensuredNamespace, i.ensuredDimension = namespace, dimension
	return nil
}

// warpIndexDrainBudget is how long Close lets workers finish accepted items
// before it stops being polite about it.
const warpIndexDrainBudget = 5 * time.Second

func (i *LogIndexer) Close() {
	// Marked closed under the write lock first, so any Enqueue already inside
	// its read lock finishes before workers are told to stop, and any that
	// arrives afterwards is refused rather than queued.
	i.closeMu.Lock()
	i.closed = true
	i.closeMu.Unlock()

	i.once.Do(func() { close(i.done) })

	// Two requirements that pull against each other. Enqueue told its caller the
	// item was taken, so an orderly shutdown has to drain what it accepted -
	// dropping it loses exactly the work the backfill would then redo. But a
	// provider that has stopped answering must not hold shutdown open forever.
	//
	// So: give the drain a budget, and only if it overruns cancel the context
	// out from under whatever is still running. Closing done alone would do
	// neither - it stops workers taking new items and leaves in-flight ones
	// uninterruptible.
	drained := make(chan struct{})
	go func() {
		i.wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
	case <-time.After(i.drainBudget):
		i.warnf("warp log indexing did not finish within %s; cancelling remaining work", i.drainBudget)
		if i.cancel != nil {
			i.cancel()
		}
		<-drained
	}
	if i.cancel != nil {
		i.cancel()
	}
}

func (i *LogIndexer) warnf(format string, args ...any) {
	if i.logger != nil {
		i.logger.Warn(format, args...)
	}
}

// ensureWarpNamespace creates the namespace if it is not already there, and
// reports whether this call is the one that created it.
//
// The caller needs that answer to clean up after a failed save: a namespace it
// created is empty and referenced by nothing, so removing it is safe, while one
// that already existed may hold vectors a previous configuration still indexes
// and must be left alone.
func ensureWarpNamespace(ctx context.Context, store vectorstore.VectorStore, namespace string, dimension int) (bool, error) {
	if store == nil {
		return false, ErrNoVectorStore
	}
	// Asked before creating, because CreateNamespace is idempotent and cannot
	// tell the caller which of the two things it did.
	existing, err := store.ListNamespaces(ctx, namespace)
	if err != nil {
		// Fatal, deliberately. Creating anyway would have to report
		// created=false - ownership is unknowable - so a save that then failed
		// would skip its compensation and orphan a namespace it made. Callers
		// already propagate this error, and a store whose ListNamespaces fails
		// is rarely one whose CreateNamespace was about to succeed.
		return false, fmt.Errorf("could not check for existing vector namespace: %w", err)
	}
	created := !slices.Contains(existing, namespace)
	return created, store.CreateNamespace(ctx, namespace, dimension, map[string]vectorstore.VectorStoreProperties{
		"log_id":            {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Bifrost log ID"},
		"timestamp":         {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Log timestamp in Unix seconds"},
		"object":            {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Bifrost request type"},
		"provider":          {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Serving provider"},
		"model":             {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Serving model"},
		"status":            {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Terminal log status"},
		"latency_ms":        {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "End-to-end latency rounded to milliseconds"},
		"cost_micro_usd":    {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Request cost in millionths of a US dollar"},
		"prompt_tokens":     {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Input token count"},
		"completion_tokens": {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Output token count"},
		"total_tokens":      {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Total token count"},
		"parent_request_id": {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Fallback or session parent"},
		"app":               {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Detected client app"},
		"virtual_key_id":    {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Virtual key ID"},
		"user_id":           {DataType: vectorstore.VectorStorePropertyTypeString, Description: "User ID"},
		"team_ids":          {DataType: vectorstore.VectorStorePropertyTypeStringArray, Description: "Team IDs"},
		"customer_ids":      {DataType: vectorstore.VectorStorePropertyTypeStringArray, Description: "Customer IDs"},
		"business_unit_ids": {DataType: vectorstore.VectorStorePropertyTypeStringArray, Description: "Business unit IDs"},
		"warp_log":          {DataType: vectorstore.VectorStorePropertyTypeBoolean, Description: "Owned by Warp log search"},
	})
}

func generateWarpEmbedding(ctx context.Context, executor EmbeddingExecutor, config *schemas.WarpConfig, text string) ([]float32, *schemas.BifrostLLMUsage, error) {
	if executor == nil {
		return nil, nil, fmt.Errorf("embedding executor is not configured")
	}
	embeddingCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	defer embeddingCtx.Cancel()
	embeddingCtx.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true)
	bifrost.ClearContextForInternalRequest(embeddingCtx)
	if config.EmbeddingAPIKeyID != "" {
		embeddingCtx.SetValue(schemas.BifrostContextKeyAPIKeyID, config.EmbeddingAPIKeyID)
	}
	dimension := config.EmbeddingDimension
	request := &schemas.BifrostEmbeddingRequest{
		Provider: config.EmbeddingProvider,
		Model:    config.EmbeddingModel,
		Input:    &schemas.EmbeddingInput{Text: &text},
		Params:   &schemas.EmbeddingParameters{Dimensions: &dimension},
	}
	response, bifrostErr := executor(embeddingCtx, request)
	if bifrostErr != nil {
		message := "embedding request failed"
		if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
			message = bifrostErr.Error.Message
		}
		return nil, nil, fmt.Errorf("%s", message)
	}
	if response == nil {
		return nil, nil, fmt.Errorf("embedding provider returned no vectors")
	}
	// Usage rides along on every failure after this point: the provider
	// answered, so the call was billed whether or not its vector is usable.
	if len(response.Data) == 0 {
		return nil, response.Usage, fmt.Errorf("embedding provider returned no vectors")
	}
	vector, err := embeddingToFloat32(response.Data[0].Embedding)
	if err != nil {
		return nil, response.Usage, err
	}
	if len(vector) != config.EmbeddingDimension {
		return nil, response.Usage, fmt.Errorf("embedding dimension mismatch: got %d, want %d", len(vector), config.EmbeddingDimension)
	}
	return vector, response.Usage, nil
}

func embeddingToFloat32(value schemas.EmbeddingStruct) ([]float32, error) {
	switch {
	case value.EmbeddingStr != nil:
		var vector []float32
		if err := json.Unmarshal([]byte(*value.EmbeddingStr), &vector); err != nil {
			return nil, fmt.Errorf("parse string embedding: %w", err)
		}
		return vector, nil
	case value.EmbeddingArray != nil:
		vector := make([]float32, len(value.EmbeddingArray))
		for index, item := range value.EmbeddingArray {
			vector[index] = float32(item)
		}
		return vector, nil
	case value.Embedding2DArray != nil:
		var vector []float32
		for _, row := range value.Embedding2DArray {
			for _, item := range row {
				vector = append(vector, float32(item))
			}
		}
		return vector, nil
	case value.EmbeddingInt8Array != nil:
		vector := make([]float32, len(value.EmbeddingInt8Array))
		for index, item := range value.EmbeddingInt8Array {
			vector[index] = float32(item)
		}
		return vector, nil
	case value.EmbeddingInt32Array != nil:
		vector := make([]float32, len(value.EmbeddingInt32Array))
		for index, item := range value.EmbeddingInt32Array {
			vector[index] = float32(item)
		}
		return vector, nil
	default:
		return nil, fmt.Errorf("embedding data is empty")
	}
}

func buildLogIndexItem(entry *logstore.Log) (logIndexItem, bool) {
	if entry == nil || entry.ID == "" || entry.ContentHidden || !terminalWarpLogStatus(entry.Status) || !conversationalWarpObject(entry.Object) {
		return logIndexItem{}, false
	}
	if entry.App != nil && strings.EqualFold(strings.TrimSpace(*entry.App), warpAppName) {
		return logIndexItem{}, false
	}
	if entry.UserAgent != nil && strings.Contains(strings.ToLower(*entry.UserAgent), "bifrost-warp") {
		return logIndexItem{}, false
	}
	user := strings.Join(strings.Fields(entry.BuildInputContentSummary()), " ")
	if user == "" {
		user = strings.Join(strings.Fields(entry.ContentSummary), " ")
	}
	assistant := strings.Join(strings.Fields(logAssistantText(entry)), " ")
	text := boundedConversationText(user, assistant, warpMaxEmbeddingBytes)
	if text == "" {
		return logIndexItem{}, false
	}
	// The scalar filter fields are stored lowered, because the store-side
	// prefilter compares exactly while the post-filter compares with EqualFold
	// - appendScalarQuery lowers the filter values to meet these. Hydration
	// reads the full row from the logstore, so nothing rendered loses casing.
	metadata := map[string]interface{}{
		"log_id": entry.ID, "timestamp": entry.Timestamp.Unix(), "object": entry.Object,
		"provider": strings.ToLower(entry.Provider), "model": strings.ToLower(entry.Model), "status": strings.ToLower(entry.Status), "warp_log": true,
		"latency_ms": roundedMetric(entry.Latency, 1), "cost_micro_usd": roundedMetric(entry.Cost, 1_000_000),
		"prompt_tokens": entry.PromptTokens, "completion_tokens": entry.CompletionTokens, "total_tokens": entry.TotalTokens,
		"parent_request_id": stringValue(entry.ParentRequestID), "app": strings.ToLower(stringValue(entry.App)),
		"virtual_key_id": strings.ToLower(stringValue(entry.VirtualKeyID)), "user_id": strings.ToLower(stringValue(entry.UserID)),
		"team_ids": mergedIDs(entry.TeamID, entry.TeamIDs), "customer_ids": mergedIDs(entry.CustomerID, entry.CustomerIDs),
		"business_unit_ids": mergedIDs(entry.BusinessUnitID, entry.BusinessUnitIDs),
	}
	return logIndexItem{id: entry.ID, text: text, metadata: metadata}, true
}

func roundedMetric(value *float64, multiplier float64) int64 {
	if value == nil {
		return 0
	}
	return int64(math.Round(*value * multiplier))
}

func terminalWarpLogStatus(status string) bool {
	switch status {
	case "success", "error", "cancelled":
		return true
	default:
		return false
	}
}

func conversationalWarpObject(object string) bool {
	switch schemas.RequestType(object) {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest, schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		return true
	default:
		return object == "chat.completion" || object == "chat.completion.chunk" || object == "response"
	}
}

func logAssistantText(entry *logstore.Log) string {
	if entry.OutputMessageParsed != nil {
		return chatContentText(entry.OutputMessageParsed.Content)
	}
	for index := len(entry.ResponsesOutputParsed) - 1; index >= 0; index-- {
		message := entry.ResponsesOutputParsed[index]
		if message.Role == nil || *message.Role == schemas.ResponsesInputMessageRoleAssistant {
			if text := responsesContentText(message.Content); text != "" {
				return text
			}
		}
	}
	return ""
}

func chatContentText(content *schemas.ChatMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	parts := make([]string, 0, len(content.ContentBlocks))
	for _, block := range content.ContentBlocks {
		if block.Text != nil {
			parts = append(parts, *block.Text)
		}
	}
	return strings.Join(parts, " ")
}

func responsesContentText(content *schemas.ResponsesMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	parts := make([]string, 0, len(content.ContentBlocks))
	for _, block := range content.ContentBlocks {
		if block.Text != nil {
			parts = append(parts, *block.Text)
		}
	}
	return strings.Join(parts, " ")
}

func boundedConversationText(user, assistant string, limit int) string {
	var builder strings.Builder
	appendBounded := func(label, value string) {
		if value == "" || builder.Len() >= limit {
			return
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		prefix := label + ": "
		if builder.Len()+len(prefix) >= limit {
			return
		}
		builder.WriteString(prefix)
		remaining := limit - builder.Len()
		for len(value) > 0 && remaining > 0 {
			r, size := utf8.DecodeRuneInString(value)
			if size > remaining {
				break
			}
			builder.WriteRune(r)
			value = value[size:]
			remaining -= size
		}
	}
	appendBounded("user", user)
	appendBounded("assistant", assistant)
	return strings.TrimSpace(builder.String())
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func mergedIDs(single, encoded *string) []string {
	values := make([]string, 0, 2)
	if encoded != nil && *encoded != "" {
		_ = json.Unmarshal([]byte(*encoded), &values)
	}
	if single != nil && *single != "" {
		found := false
		for _, value := range values {
			if value == *single {
				found = true
				break
			}
		}
		if !found {
			values = append(values, *single)
		}
	}
	return values
}
