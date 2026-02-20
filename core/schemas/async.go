package schemas

import "time"

// AsyncJobStatus represents the status of an async job
type AsyncJobStatus string

const (
	AsyncJobStatusPending    AsyncJobStatus = "pending"
	AsyncJobStatusProcessing AsyncJobStatus = "processing"
	AsyncJobStatusCompleted  AsyncJobStatus = "completed"
	AsyncJobStatusFailed     AsyncJobStatus = "failed"
)

const (
	// AsyncHeaderResultTTL is the header containing the result TTL for async job retrieval.
	AsyncHeaderResultTTL = "x-bf-async-job-result-ttl"
	// AsyncHeaderCreate is the header that triggers async job creation on integration routes.
	AsyncHeaderCreate = "x-bf-async"
	// AsyncHeaderGetID is the header containing the job ID for async job retrieval on integration routes.
	AsyncHeaderGetID = "x-bf-async-id"
)

// AsyncJobResponse is the JSON response returned when creating or polling an async job
type AsyncJobResponse struct {
	ID          string         `json:"id"`
	Status      AsyncJobStatus `json:"status"`
	ExpiresAt   *time.Time     `json:"expires_at,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	StatusCode  int            `json:"status_code,omitempty"`
	Result      interface{}    `json:"result,omitempty"`
	Error       *BifrostError  `json:"error,omitempty"`
}
