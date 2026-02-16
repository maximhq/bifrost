// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import "sync"

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
	BatchStatusEnded      BatchStatus = "ended" // Anthropic-specific
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
	CustomID string                 `json:"custom_id"`        // User-provided unique ID for this request
	Method   string                 `json:"method,omitempty"` // HTTP method (typically "POST")
	URL      string                 `json:"url,omitempty"`    // Endpoint URL (e.g., "/v1/chat/completions")
	Body     map[string]interface{} `json:"body,omitempty"`   // Request body parameters
	Params   map[string]interface{} `json:"params,omitempty"` // Alternative to Body for Anthropic
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

	// Common fields
	Endpoint            BatchEndpoint      `json:"endpoint,omitempty"`              // Target endpoint for batch requests
	CompletionWindow    string             `json:"completion_window,omitempty"`     // Time window (e.g., "24h")
	Metadata            map[string]string  `json:"metadata,omitempty"`              // User-provided metadata
	OutputExpiresAfter  *BatchExpiresAfter `json:"output_expires_after,omitempty"` // Expiration for batch output (OpenAI only)

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
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
	Object           string             `json:"object,omitempty"` // "batch" for OpenAI
	Endpoint         string             `json:"endpoint,omitempty"`
	InputFileID      string             `json:"input_file_id,omitempty"`
	CompletionWindow string             `json:"completion_window,omitempty"`
	Status           BatchStatus        `json:"status"`
	RequestCounts    BatchRequestCounts `json:"request_counts,omitempty"`
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

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// bifrostBatchCreateResponsePool provides a pool for BifrostBatchCreateResponse objects.
var bifrostBatchCreateResponsePool = sync.Pool{
	New: func() interface{} {
		return &BifrostBatchCreateResponse{}
	},
}

// AcquireBifrostBatchCreateResponse gets a BifrostBatchCreateResponse from the pool and resets it.
func AcquireBifrostBatchCreateResponse() *BifrostBatchCreateResponse {
	r := bifrostBatchCreateResponsePool.Get().(*BifrostBatchCreateResponse)
	*r = BifrostBatchCreateResponse{}
	return r
}

// ReleaseBifrostBatchCreateResponse returns a BifrostBatchCreateResponse to the pool.
// The caller must ensure no other goroutine holds a reference to this response.
func ReleaseBifrostBatchCreateResponse(r *BifrostBatchCreateResponse) {
	if r == nil {
		return
	}
	r.ID = ""
	r.Object = ""
	r.Endpoint = ""
	r.InputFileID = ""
	r.CompletionWindow = ""
	r.Status = ""
	r.RequestCounts = BatchRequestCounts{}
	r.Metadata = nil
	r.CreatedAt = 0
	r.ExpiresAt = nil
	r.OutputFileID = nil
	r.ErrorFileID = nil
	r.ProcessingStatus = nil
	r.ResultsURL = nil
	r.OperationName = nil
	r.ExtraFields = BifrostResponseExtraFields{}
	bifrostBatchCreateResponsePool.Put(r)
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
	ExtraParams map[string]interface{} `json:"-"`
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

// bifrostBatchListResponsePool provides a pool for BifrostBatchListResponse objects.
var bifrostBatchListResponsePool = sync.Pool{
	New: func() interface{} {
		return &BifrostBatchListResponse{}
	},
}

// AcquireBifrostBatchListResponse gets a BifrostBatchListResponse from the pool and resets it.
func AcquireBifrostBatchListResponse() *BifrostBatchListResponse {
	r := bifrostBatchListResponsePool.Get().(*BifrostBatchListResponse)
	*r = BifrostBatchListResponse{}
	return r
}

// ReleaseBifrostBatchListResponse returns a BifrostBatchListResponse to the pool.
// The caller must ensure no other goroutine holds a reference to this response.
func ReleaseBifrostBatchListResponse(r *BifrostBatchListResponse) {
	if r == nil {
		return
	}
	r.Object = ""
	r.Data = nil
	r.FirstID = nil
	r.LastID = nil
	r.HasMore = false
	r.NextCursor = nil
	r.ExtraFields = BifrostResponseExtraFields{}
	bifrostBatchListResponsePool.Put(r)
}

// BifrostBatchRetrieveRequest represents a request to retrieve a batch job.
type BifrostBatchRetrieveRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`
	BatchID  string        `json:"batch_id"` // ID of the batch to retrieve

	RawRequestBody []byte `json:"-"` // Raw request body (not serialized)

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// GetRawRequestBody returns the raw request body.
func (request *BifrostBatchRetrieveRequest) GetRawRequestBody() []byte {
	return request.RawRequestBody
}

// BifrostBatchRetrieveResponse represents the response from retrieving a batch job.
type BifrostBatchRetrieveResponse struct {
	ID               string             `json:"id"`
	Object           string             `json:"object,omitempty"`
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

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// bifrostBatchRetrieveResponsePool provides a pool for BifrostBatchRetrieveResponse objects.
var bifrostBatchRetrieveResponsePool = sync.Pool{
	New: func() interface{} {
		return &BifrostBatchRetrieveResponse{}
	},
}

// AcquireBifrostBatchRetrieveResponse gets a BifrostBatchRetrieveResponse from the pool and resets it.
func AcquireBifrostBatchRetrieveResponse() *BifrostBatchRetrieveResponse {
	r := bifrostBatchRetrieveResponsePool.Get().(*BifrostBatchRetrieveResponse)
	*r = BifrostBatchRetrieveResponse{}
	return r
}

// ReleaseBifrostBatchRetrieveResponse returns a BifrostBatchRetrieveResponse to the pool.
// The caller must ensure no other goroutine holds a reference to this response.
func ReleaseBifrostBatchRetrieveResponse(r *BifrostBatchRetrieveResponse) {
	if r == nil {
		return
	}
	r.ID = ""
	r.Object = ""
	r.Endpoint = ""
	r.InputFileID = ""
	r.CompletionWindow = ""
	r.Status = ""
	r.RequestCounts = BatchRequestCounts{}
	r.Metadata = nil
	r.CreatedAt = 0
	r.ExpiresAt = nil
	r.InProgressAt = nil
	r.FinalizingAt = nil
	r.CompletedAt = nil
	r.FailedAt = nil
	r.ExpiredAt = nil
	r.CancellingAt = nil
	r.CancelledAt = nil
	r.OutputFileID = nil
	r.ErrorFileID = nil
	r.Errors = nil
	r.ProcessingStatus = nil
	r.ResultsURL = nil
	r.ArchivedAt = nil
	r.OperationName = nil
	r.Done = nil
	r.Progress = nil
	r.ExtraFields = BifrostResponseExtraFields{}
	bifrostBatchRetrieveResponsePool.Put(r)
}

// BifrostBatchCancelRequest represents a request to cancel a batch job.
type BifrostBatchCancelRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`
	BatchID  string        `json:"batch_id"` // ID of the batch to cancel

	RawRequestBody []byte `json:"-"` // Raw request body (not serialized)

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
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

// bifrostBatchCancelResponsePool provides a pool for BifrostBatchCancelResponse objects.
var bifrostBatchCancelResponsePool = sync.Pool{
	New: func() interface{} {
		return &BifrostBatchCancelResponse{}
	},
}

// AcquireBifrostBatchCancelResponse gets a BifrostBatchCancelResponse from the pool and resets it.
func AcquireBifrostBatchCancelResponse() *BifrostBatchCancelResponse {
	r := bifrostBatchCancelResponsePool.Get().(*BifrostBatchCancelResponse)
	*r = BifrostBatchCancelResponse{}
	return r
}

// ReleaseBifrostBatchCancelResponse returns a BifrostBatchCancelResponse to the pool.
// The caller must ensure no other goroutine holds a reference to this response.
func ReleaseBifrostBatchCancelResponse(r *BifrostBatchCancelResponse) {
	if r == nil {
		return
	}
	r.ID = ""
	r.Object = ""
	r.Status = ""
	r.RequestCounts = BatchRequestCounts{}
	r.CancellingAt = nil
	r.CancelledAt = nil
	r.ExtraFields = BifrostResponseExtraFields{}
	bifrostBatchCancelResponsePool.Put(r)
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
	ExtraParams map[string]interface{} `json:"-"`
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

// BatchResultResponse represents OpenAI-style result response.
type BatchResultResponse struct {
	StatusCode int                    `json:"status_code"`
	RequestID  string                 `json:"request_id,omitempty"`
	Body       map[string]interface{} `json:"body,omitempty"`
}

// BatchResultData represents Anthropic-style result data.
type BatchResultData struct {
	Type    string                 `json:"type"` // "succeeded", "errored", "expired", "canceled"
	Message map[string]interface{} `json:"message,omitempty"`
}

// BatchResultError represents an error for a single batch request.
type BatchResultError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// BifrostBatchResultsResponse represents the response from retrieving batch results.
type BifrostBatchResultsResponse struct {
	BatchID string            `json:"batch_id"`
	Results []BatchResultItem `json:"results"`

	// For streaming results (Anthropic)
	HasMore    bool    `json:"has_more,omitempty"`
	NextCursor *string `json:"next_cursor,omitempty"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// bifrostBatchResultsResponsePool provides a pool for BifrostBatchResultsResponse objects.
var bifrostBatchResultsResponsePool = sync.Pool{
	New: func() interface{} {
		return &BifrostBatchResultsResponse{}
	},
}

// AcquireBifrostBatchResultsResponse gets a BifrostBatchResultsResponse from the pool and resets it.
func AcquireBifrostBatchResultsResponse() *BifrostBatchResultsResponse {
	r := bifrostBatchResultsResponsePool.Get().(*BifrostBatchResultsResponse)
	*r = BifrostBatchResultsResponse{}
	return r
}

// ReleaseBifrostBatchResultsResponse returns a BifrostBatchResultsResponse to the pool.
// The caller must ensure no other goroutine holds a reference to this response.
func ReleaseBifrostBatchResultsResponse(r *BifrostBatchResultsResponse) {
	if r == nil {
		return
	}
	r.BatchID = ""
	r.Results = nil
	r.HasMore = false
	r.NextCursor = nil
	r.ExtraFields = BifrostResponseExtraFields{}
	bifrostBatchResultsResponsePool.Put(r)
}
