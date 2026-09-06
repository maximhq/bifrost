package warp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
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
}

func NewLogIndexer(store configstore.WarpStore, vectors vectorstore.VectorStore, embed EmbeddingExecutor, logger schemas.Logger) *LogIndexer {
	indexer := &LogIndexer{store: store, vectors: vectors, embed: embed, logger: logger, queue: make(chan logIndexItem, warpIndexQueueSize), done: make(chan struct{})}
	for range warpIndexWorkers {
		indexer.wg.Add(1)
		go indexer.worker()
	}
	return indexer
}

// Enqueue never blocks the logging writer or the original inference request.
func (i *LogIndexer) Enqueue(_ context.Context, entry *logstore.Log) {
	item, ok := buildLogIndexItem(entry)
	if !ok {
		return
	}
	select {
	case <-i.done:
		return
	case i.queue <- item:
	default:
		i.warnf("warp log embedding queue is full; skipped log %s (manual backfill can repair it)", item.id)
	}
}

// Index performs the same idempotent operation synchronously for Sidekiq.
func (i *LogIndexer) Index(ctx context.Context, entry *logstore.Log) (IndexOutcome, error) {
	item, ok := buildLogIndexItem(entry)
	if !ok {
		return IndexOutcomeSkipped, nil
	}
	if err := i.indexItem(ctx, item); err != nil {
		return "", err
	}
	return IndexOutcomeIndexed, nil
}

func (i *LogIndexer) worker() {
	defer i.wg.Done()
	for {
		select {
		case <-i.done:
			return
		case item := <-i.queue:
			if err := i.indexItem(context.Background(), item); err != nil {
				i.warnf("failed to index Warp log %s: %v", item.id, err)
			}
		}
	}
}

func (i *LogIndexer) indexItem(ctx context.Context, item logIndexItem) error {
	row, err := i.store.GetWarpConfig(ctx)
	if err != nil {
		return fmt.Errorf("read Warp configuration: %w", err)
	}
	config := configFromRow(row)
	if !config.IsConfigured() {
		return nil
	}
	namespace := config.EffectiveLogVectorStoreNamespace()
	if err := ensureWarpNamespace(ctx, i.vectors, namespace, config.EmbeddingDimension); err != nil {
		return err
	}
	embedding, err := generateWarpEmbedding(ctx, i.embed, config, item.text)
	if err != nil {
		return err
	}
	return i.vectors.Add(ctx, namespace, item.id, embedding, item.metadata)
}

func (i *LogIndexer) Close() {
	i.once.Do(func() { close(i.done) })
	i.wg.Wait()
}

func (i *LogIndexer) warnf(format string, args ...any) {
	if i.logger != nil {
		i.logger.Warn(format, args...)
	}
}

func ensureWarpNamespace(ctx context.Context, store vectorstore.VectorStore, namespace string, dimension int) error {
	if store == nil {
		return ErrNoVectorStore
	}
	return store.CreateNamespace(ctx, namespace, dimension, map[string]vectorstore.VectorStoreProperties{
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

func generateWarpEmbedding(ctx context.Context, executor EmbeddingExecutor, config *schemas.WarpConfig, text string) ([]float32, error) {
	if executor == nil {
		return nil, fmt.Errorf("embedding executor is not configured")
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
		return nil, fmt.Errorf("%s", message)
	}
	if response == nil || len(response.Data) == 0 {
		return nil, fmt.Errorf("embedding provider returned no vectors")
	}
	vector, err := embeddingToFloat32(response.Data[0].Embedding)
	if err != nil {
		return nil, err
	}
	if len(vector) != config.EmbeddingDimension {
		return nil, fmt.Errorf("embedding dimension mismatch: got %d, want %d", len(vector), config.EmbeddingDimension)
	}
	return vector, nil
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
	if entry.App != nil && strings.EqualFold(strings.TrimSpace(*entry.App), "Warp") {
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
	metadata := map[string]interface{}{
		"log_id": entry.ID, "timestamp": entry.Timestamp.Unix(), "object": entry.Object,
		"provider": entry.Provider, "model": entry.Model, "status": entry.Status, "warp_log": true,
		"latency_ms": roundedMetric(entry.Latency, 1), "cost_micro_usd": roundedMetric(entry.Cost, 1_000_000),
		"prompt_tokens": entry.PromptTokens, "completion_tokens": entry.CompletionTokens, "total_tokens": entry.TotalTokens,
		"parent_request_id": stringValue(entry.ParentRequestID), "app": stringValue(entry.App),
		"virtual_key_id": stringValue(entry.VirtualKeyID), "user_id": stringValue(entry.UserID),
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
