// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

// BatchStatus represents the status of a batch job.
type BatchStatus string

const (
	BatchStatusValidating BatchStatus = "validating"
	BatchStatusFailed     BatchStatus = "failed"
	BatchStatusInProgress BatchStatus = "in_progress"
	BatchStatusFinalizing BatchStatus = "finalizing"
	BatchStatusCompleted  BatchStatus = "completed"
	BatchStatusExpired    BatchStatus = "expired"
	BatchStatusCancelling BatchStatus = "cancelling"
	BatchStatusCancelled  BatchStatus = "cancelled"
	BatchStatusEnded      BatchStatus = "ended"   // Anthropic-specific
	BatchStatusDeleted    BatchStatus = "deleted" // Gemini-specific
)

// BatchEndpoint represents supported batch API endpoints.
type BatchEndpoint string

const (
	BatchEndpointChatCompletions BatchEndpoint = "/v1/chat/completions"
	BatchEndpointEmbeddings      BatchEndpoint = "/v1/embeddings"
	BatchEndpointCompletions     BatchEndpoint = "/v1/completions"
	BatchEndpointResponses       BatchEndpoint = "/v1/responses"
	BatchEndpointMessages        BatchEndpoint = "/v1/messages" // Anthropic
)

// BatchRequestItem represents a single request in a batch (for inline requests).
type BatchRequestItem struct {
	CustomID string         `json:"custom_id"`        // User-provided unique ID for this request
	Method   string         `json:"method,omitempty"` // HTTP method (typically "POST")
	URL      string         `json:"url,omitempty"`    // Endpoint URL (e.g., "/v1/chat/completions")
	Body     map[string]any `json:"body,omitempty"`   // Request body parameters
	Params   map[string]any `json:"params,omitempty"` // Alternative to Body for Anthropic
}

// BatchRequestCounts tracks the counts of requests in different states.
type BatchRequestCounts struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Succeeded int `json:"succeeded,omitempty"` // Anthropic-specific
	Expired   int `json:"expired,omitempty"`   // Anthropic-specific
	Canceled  int `json:"canceled,omitempty"`  // Anthropic-specific
	Pending   int `json:"pending,omitempty"`   // Anthropic-specific
}

// IsZero reports whether no provider request counts are present.
func (c BatchRequestCounts) IsZero() bool {
	return c.Total == 0 &&
		c.Completed == 0 &&
		c.Failed == 0 &&
		c.Succeeded == 0 &&
		c.Expired == 0 &&
		c.Canceled == 0 &&
		c.Pending == 0
}

// BifrostBatchDebug is the batch detail attached to a log row: which batch the
// request addressed, how the provider reported its progress, and — on the
// aggregate cost row only — how settlement priced it.
type BifrostBatchDebug struct {
	BatchID string `json:"batch_id,omitempty"`
	// Status is the provider's batch lifecycle status (BatchStatus, e.g.
	// "in_progress" or "completed") as last known when this row was written. On a
	// batch_create/batch_retrieve row it is that call's own response status. On the
	// aggregate cost row it is the status settlement observed (always a terminal
	// one, since settlement only runs once the batch completed) — a later /results
	// fetch of the same batch mirrors this value rather than re-deriving it, so it
	// can go stale relative to the provider if queried long after settlement.
	Status string `json:"status,omitempty"`
	// Endpoint is the batch's provider endpoint (/v1/embeddings, /v1/chat/completions,
	// …). The row's Object column is always "batch_results", which carries no modality,
	// so without this a later repricing pass cannot tell an embeddings batch from a
	// chat one and would look the wrong catalog rates up. Empty on rows written before
	// this field existed; callers must keep a fallback for them.
	Endpoint      string                `json:"endpoint,omitempty"`
	RequestCounts *BatchRequestCounts   `json:"request_counts,omitempty"`
	Accounting    *BatchAccountingDebug `json:"accounting,omitempty"` // Set only on the aggregate cost row
}

// IsZero reports whether no batch detail is present, so callers can skip
// attaching an empty record to a log.
func (d *BifrostBatchDebug) IsZero() bool {
	if d == nil {
		return true
	}
	return d.BatchID == "" && d.Status == "" && d.Endpoint == "" && d.RequestCounts == nil && d.Accounting == nil
}

// BatchAccountingDebug records how a settled batch was priced. On the aggregate
// cost row (the one CreateIfNotExists writes once), the row's own cost and
// token_usage columns hold the row-level totals ModelBreakdowns breaks down.
// A repeated /results fetch after the batch already settled gets its own,
// separate log row for that HTTP call — Cost on THAT row is a read-only echo
// of the aggregate row's price so the caller can display it too; see Cost below.
type BatchAccountingDebug struct {
	ModelBreakdowns map[string]BatchModelBreakdown `json:"model_breakdowns,omitempty"`
	Cost            *float64                       `json:"cost,omitempty"`
	// ParseErrorCount is how many result rows the provider returned that could not
	// be parsed at all. Their usage is unrecoverable — the raw results are not
	// persisted — so the count is kept as the record of how much the row omits.
	ParseErrorCount int `json:"parse_error_count,omitempty"`
	// Incomplete marks a total that is known to under-state the batch: some
	// usage-bearing row failed to price, or rows were lost to parse errors. It is
	// only ever set, never cleared by a repricing pass — usage that never reached
	// ModelBreakdowns (a row with no model at all, or a parse error) cannot be
	// recovered by repricing the breakdowns that did land.
	Incomplete bool `json:"incomplete,omitempty"`
}

// BatchModelBreakdown is the per-model slice of a settled batch's usage and
// cost. Cost is a pointer for the same reason logstore's row-level Cost is:
// nil means "not priced yet" (e.g. the catalog had no rate for this model at
// settlement time), distinct from a real $0. A future cost-recalculation pass
// can price a nil-Cost entry from its Usage and fill it in without touching
// the entries that already priced.
type BatchModelBreakdown struct {
	Model        string          `json:"model"`
	RequestCount int             `json:"request_count"`
	Usage        BifrostLLMUsage `json:"usage"`
	Cost         *float64        `json:"cost,omitempty"`
}

// BatchErrors represents errors encountered during batch processing.
type BatchErrors struct {
	Object string       `json:"object,omitempty"`
	Data   []BatchError `json:"data,omitempty"`
}

// BatchError represents a single error in batch processing.
type BatchError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Param   string `json:"param,omitempty"`
	Line    *int   `json:"line,omitempty"`
}

// BifrostBatchCreateRequest represents a request to create a batch job.
type BifrostBatchCreateRequest struct {
	Provider       ModelProvider `json:"provider"`
	Model          *string       `json:"model,omitempty"` // Model hint for routing (optional for file-based) it may or may not present depending on the provider and usage of integration vs direct API
	RawRequestBody []byte        `json:"-"`               // Raw request body (not serialized)

	// OpenAI-style: file-based batching
	InputFileID string `json:"input_file_id,omitempty"` // ID of uploaded JSONL file

	// Anthropic-style: inline requests
	Requests []BatchRequestItem `json:"requests,omitempty"` // Inline request items

	// Azure-style: Blob storage input and output folder
	InputBlob    *string            `json:"input_blob,omitempty"`
	OutputFolder *BatchOutputFolder `json:"output_folder,omitempty"`

	// Common fields
	DisplayName        *string            `json:"display_name,omitempty"`         // Human-readable job name (e.g. Vertex displayName)
	Endpoint           BatchEndpoint      `json:"endpoint,omitempty"`             // Target endpoint for batch requests
	CompletionWindow   string             `json:"completion_window,omitempty"`    // Time window (e.g., "24h")
	Metadata           map[string]string  `json:"metadata,omitempty"`             // User-provided metadata
	OutputExpiresAfter *BatchExpiresAfter `json:"output_expires_after,omitempty"` // Expiration for batch output (OpenAI only)

	// Extra parameters for provider-specific features
	ExtraParams map[string]any `json:"-"`
}

type BatchOutputFolder struct {
	URL string `json:"url"`
}

// BatchExpiresAfter represents an expiration configuration for batch output.
type BatchExpiresAfter struct {
	Anchor  string `json:"anchor"`  // e.g., "created_at"
	Seconds int    `json:"seconds"` // 3600-2592000 (1 hour to 30 days)
}

// GetRawRequestBody returns the raw request body.
func (request *BifrostBatchCreateRequest) GetRawRequestBody() []byte {
	return request.RawRequestBody
}

// BifrostBatchCreateResponse represents the response from creating a batch job.
type BifrostBatchCreateResponse struct {
	ID               string             `json:"id"`
	Object           string             `json:"object,omitempty"`       // "batch" for OpenAI
	DisplayName      *string            `json:"display_name,omitempty"` // Human-readable job name (e.g. Vertex displayName)
	Endpoint         string             `json:"endpoint,omitempty"`
	InputFileID      string             `json:"input_file_id,omitempty"`
	CompletionWindow string             `json:"completion_window,omitempty"`
	Status           BatchStatus        `json:"status"`
	RequestCounts    BatchRequestCounts `json:"request_counts"`
	Metadata         map[string]string  `json:"metadata,omitempty"`
	CreatedAt        int64              `json:"created_at,omitempty"`
	ExpiresAt        *int64             `json:"expires_at,omitempty"`

	// Output file references (OpenAI)
	OutputFileID *string `json:"output_file_id,omitempty"`
	ErrorFileID  *string `json:"error_file_id,omitempty"`

	// Anthropic-specific
	ProcessingStatus *string `json:"processing_status,omitempty"`
	ResultsURL       *string `json:"results_url,omitempty"`

	// Gemini-specific (operation response)
	OperationName *string `json:"operation_name,omitempty"`

	// Azure-specific Blob Storage URLs (returned when using blob storage input/output)
	InputBlob  *string `json:"input_blob,omitempty"`
	OutputBlob *string `json:"output_blob,omitempty"`
	ErrorBlob  *string `json:"error_blob,omitempty"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostBatchListRequest represents a request to list batch jobs.
type BifrostBatchListRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`

	// Pagination
	Limit      int     `json:"limit,omitempty"`       // Max results to return
	After      *string `json:"after,omitempty"`       // Cursor for pagination (OpenAI)
	BeforeID   *string `json:"before_id,omitempty"`   // Pagination cursor (Anthropic)
	AfterID    *string `json:"after_id,omitempty"`    // Pagination cursor (Anthropic)
	PageToken  *string `json:"page_token,omitempty"`  // For Gemini pagination
	PageSize   int     `json:"page_size,omitempty"`   // For Gemini pagination
	NextCursor *string `json:"next_cursor,omitempty"` // For Gemini pagination

	// Extra parameters for provider-specific features
	ExtraParams map[string]any `json:"-"`
}

// BifrostBatchListResponse represents the response from listing batch jobs.
type BifrostBatchListResponse struct {
	Object  string                         `json:"object,omitempty"` // "list"
	Data    []BifrostBatchRetrieveResponse `json:"data"`
	FirstID *string                        `json:"first_id,omitempty"`
	LastID  *string                        `json:"last_id,omitempty"`
	HasMore bool                           `json:"has_more,omitempty"`

	// Anthropic pagination
	NextCursor *string `json:"next_cursor,omitempty"` // For cursor-based pagination

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostBatchRetrieveRequest represents a request to retrieve a batch job.
type BifrostBatchRetrieveRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`
	BatchID  string        `json:"batch_id"` // ID of the batch to retrieve

	RawRequestBody []byte `json:"-"` // Raw request body (not serialized)

	// Extra parameters for provider-specific features
	ExtraParams map[string]any `json:"-"`
}

// GetRawRequestBody returns the raw request body.
func (request *BifrostBatchRetrieveRequest) GetRawRequestBody() []byte {
	return request.RawRequestBody
}

// BifrostBatchRetrieveResponse represents the response from retrieving a batch job.
type BifrostBatchRetrieveResponse struct {
	ID               string             `json:"id"`
	Object           string             `json:"object,omitempty"`
	DisplayName      *string            `json:"display_name,omitempty"` // Human-readable job name (e.g. Vertex displayName)
	Endpoint         string             `json:"endpoint,omitempty"`
	InputFileID      string             `json:"input_file_id,omitempty"`
	CompletionWindow string             `json:"completion_window,omitempty"`
	Status           BatchStatus        `json:"status"`
	RequestCounts    BatchRequestCounts `json:"request_counts,omitempty"`
	Metadata         map[string]string  `json:"metadata,omitempty"`
	CreatedAt        int64              `json:"created_at,omitempty"`
	ExpiresAt        *int64             `json:"expires_at,omitempty"`
	InProgressAt     *int64             `json:"in_progress_at,omitempty"`
	FinalizingAt     *int64             `json:"finalizing_at,omitempty"`
	CompletedAt      *int64             `json:"completed_at,omitempty"`
	FailedAt         *int64             `json:"failed_at,omitempty"`
	ExpiredAt        *int64             `json:"expired_at,omitempty"`
	CancellingAt     *int64             `json:"cancelling_at,omitempty"`
	CancelledAt      *int64             `json:"cancelled_at,omitempty"`

	// Output references
	OutputFileID *string      `json:"output_file_id,omitempty"`
	ErrorFileID  *string      `json:"error_file_id,omitempty"`
	Errors       *BatchErrors `json:"errors,omitempty"`

	// Anthropic-specific
	ProcessingStatus *string `json:"processing_status,omitempty"`
	ResultsURL       *string `json:"results_url,omitempty"`
	ArchivedAt       *int64  `json:"archived_at,omitempty"`

	// Gemini-specific
	OperationName *string `json:"operation_name,omitempty"`
	Done          *bool   `json:"done,omitempty"`
	Progress      *int    `json:"progress,omitempty"` // Percentage progress

	// Azure-specific Blob Storage URLs (returned when using blob storage input/output)
	InputBlob  *string `json:"input_blob,omitempty"`
	OutputBlob *string `json:"output_blob,omitempty"`
	ErrorBlob  *string `json:"error_blob,omitempty"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostBatchCancelRequest represents a request to cancel a batch job.
type BifrostBatchCancelRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`
	BatchID  string        `json:"batch_id"` // ID of the batch to cancel

	RawRequestBody []byte `json:"-"` // Raw request body (not serialized)

	// Extra parameters for provider-specific features
	ExtraParams map[string]any `json:"-"`
}

// GetRawRequestBody returns the raw request body.
func (request *BifrostBatchCancelRequest) GetRawRequestBody() []byte {
	return request.RawRequestBody
}

// BifrostBatchCancelResponse represents the response from cancelling a batch job.
type BifrostBatchCancelResponse struct {
	ID            string             `json:"id"`
	Object        string             `json:"object,omitempty"`
	Status        BatchStatus        `json:"status"`
	RequestCounts BatchRequestCounts `json:"request_counts,omitempty"`
	CancellingAt  *int64             `json:"cancelling_at,omitempty"`
	CancelledAt   *int64             `json:"cancelled_at,omitempty"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostBatchDeleteRequest represents a request to delete a batch job.
type BifrostBatchDeleteRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`
	BatchID  string        `json:"batch_id"` // ID of the batch to delete

	RawRequestBody []byte `json:"-"` // Raw request body (not serialized)

	// Extra parameters for provider-specific features
	ExtraParams map[string]any `json:"-"`
}

// GetRawRequestBody returns the raw request body.
func (request *BifrostBatchDeleteRequest) GetRawRequestBody() []byte {
	return request.RawRequestBody
}

// BifrostBatchDeleteResponse represents the response from deleting a batch job.
type BifrostBatchDeleteResponse struct {
	ID            string             `json:"id"`
	Object        string             `json:"object,omitempty"`
	Status        BatchStatus        `json:"status"`
	RequestCounts BatchRequestCounts `json:"request_counts,omitempty"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostBatchResultsRequest represents a request to retrieve batch results.
type BifrostBatchResultsRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`
	BatchID  string        `json:"batch_id"` // ID of the batch to get results for

	RawRequestBody []byte `json:"-"` // Raw request body (not serialized)

	// For OpenAI, results are retrieved via output_file_id (file download)
	// For Anthropic, results are streamed from a dedicated endpoint

	// Extra parameters for provider-specific features
	ExtraParams map[string]any `json:"-"`
}

// GetRawRequestBody returns the raw request body.
func (request *BifrostBatchResultsRequest) GetRawRequestBody() []byte {
	return request.RawRequestBody
}

// BatchResultItem represents a single result from a batch request.
type BatchResultItem struct {
	CustomID string `json:"custom_id"`

	// Result data (varies by request type)
	Response *BatchResultResponse `json:"response,omitempty"` // OpenAI format
	Result   *BatchResultData     `json:"result,omitempty"`   // Anthropic format

	// Error if the individual request failed
	Error *BatchResultError `json:"error,omitempty"`
}

// Failed reports whether the result item represents a failed per-request batch result.
func (i BatchResultItem) Failed() bool {
	if i.Error != nil {
		return true
	}
	if i.Response != nil && (i.Response.StatusCode < 200 || i.Response.StatusCode >= 300) {
		return true
	}
	if i.Result != nil && i.Result.Type != "" && i.Result.Type != "succeeded" {
		return true
	}
	// An item carrying neither a response nor a result is not a success: the
	// provider returned no outcome for that request. Gemini's inline path and
	// Bedrock both emit this shape when a record has no output and no error.
	return i.Response == nil && i.Result == nil
}

// BatchRequestCountsFromResults derives aggregate request counts from batch result rows.
func BatchRequestCountsFromResults(results []BatchResultItem) BatchRequestCounts {
	counts := BatchRequestCounts{Total: len(results)}
	for _, item := range results {
		if item.Failed() {
			counts.Failed++
			continue
		}
		counts.Completed++
	}
	return counts
}

// BatchResultResponse represents OpenAI-style result response.
type BatchResultResponse struct {
	StatusCode int            `json:"status_code"`
	RequestID  string         `json:"request_id,omitempty"`
	Body       map[string]any `json:"body,omitempty"`
}

// BatchResultData represents Anthropic-style result data.
type BatchResultData struct {
	Type    string         `json:"type"` // "succeeded", "errored", "expired", "canceled"
	Message map[string]any `json:"message,omitempty"`
}

// BatchResultError represents an error for a single batch request.
type BatchResultError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// BifrostBatchResultsResponse represents the response from retrieving batch results.
type BifrostBatchResultsResponse struct {
	BatchID  string            `json:"batch_id"`
	Endpoint BatchEndpoint     `json:"-"` // Internal accounting hint; provider-facing result payloads stay unchanged.
	Results  []BatchResultItem `json:"results"`

	// For streaming results (Anthropic)
	HasMore    bool    `json:"has_more,omitempty"`
	NextCursor *string `json:"next_cursor,omitempty"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}
