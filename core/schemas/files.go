// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

// FilePurpose represents the purpose of an uploaded file.
type FilePurpose string

const (
	FilePurposeBatch       FilePurpose = "batch"
	FilePurposeAssistants  FilePurpose = "assistants"
	FilePurposeFineTune    FilePurpose = "fine-tune"
	FilePurposeVision      FilePurpose = "vision"
	FilePurposeBatchOutput FilePurpose = "batch_output"
	FilePurposeUserData    FilePurpose = "user_data"
	FilePurposeResponses   FilePurpose = "responses"
	FilePurposeEvals       FilePurpose = "evals"
)

// FileStatus represents the status of a file.
type FileStatus string

const (
	FileStatusUploaded   FileStatus = "uploaded"
	FileStatusProcessed  FileStatus = "processed"
	FileStatusProcessing FileStatus = "processing"
	FileStatusError      FileStatus = "error"
	FileStatusDeleted    FileStatus = "deleted"
)

// FileStorageBackend represents the storage backend type.
type FileStorageBackend string

const (
	FileStorageAPI    FileStorageBackend = "api"    // OpenAI/Azure REST API
	FileStorageS3     FileStorageBackend = "s3"     // AWS S3
	FileStorageGCS    FileStorageBackend = "gcs"    // Google Cloud Storage
	FileStorageMemory FileStorageBackend = "memory" // In-memory (for Anthropic virtual files)
)

// FileObject represents a file object returned by the API.
type FileObject struct {
	ID            string      `json:"id"`
	Object        string      `json:"object,omitempty"` // "file"
	Bytes         int64       `json:"bytes"`
	CreatedAt     int64       `json:"created_at"`
	UpdatedAt     int64       `json:"updated_at,omitempty"`
	Filename      string      `json:"filename"`
	Purpose       FilePurpose `json:"purpose"`
	Status        FileStatus  `json:"status,omitempty"`
	StatusDetails *string     `json:"status_details,omitempty"`
	ExpiresAt     *int64      `json:"expires_at,omitempty"`
}

// BifrostFileUploadRequest represents a request to upload a file.
type BifrostFileUploadRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`

	// File content
	File        []byte      `json:"-"`                      // Raw file content (not serialized)
	Filename    string      `json:"filename"`               // Original filename
	Purpose     FilePurpose `json:"purpose"`                // Purpose of the file (e.g., "batch")
	ContentType *string     `json:"content_type,omitempty"` // MIME type of the file

	// Storage configuration (for S3/GCS backends)
	StorageConfig *FileStorageConfig `json:"storage_config,omitempty"`

	// Expiration configuration (OpenAI only)
	ExpiresAfter *FileExpiresAfter `json:"expires_after,omitempty"`

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// S3StorageConfig represents AWS S3 storage configuration.
type S3StorageConfig struct {
	Bucket string `json:"bucket,omitempty"`
	Region string `json:"region,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

// GCSStorageConfig represents Google Cloud Storage configuration.
type GCSStorageConfig struct {
	Bucket  string `json:"bucket,omitempty"`
	Project string `json:"project,omitempty"`
	Prefix  string `json:"prefix,omitempty"`
}

// FileExpiresAfter represents an expiration configuration for uploaded files.
type FileExpiresAfter struct {
	Anchor  string `json:"anchor"`  // e.g., "created_at"
	Seconds int    `json:"seconds"` // 3600-2592000 (1 hour to 30 days)
}

// FileStorageConfig represents storage configuration for cloud storage backends.
type FileStorageConfig struct {
	S3  *S3StorageConfig  `json:"s3,omitempty"`
	GCS *GCSStorageConfig `json:"gcs,omitempty"`
}

// BifrostFileUploadResponse represents the response from uploading a file.
type BifrostFileUploadResponse struct {
	ID            string      `json:"id"`
	Object        string      `json:"object,omitempty"` // "file"
	Bytes         int64       `json:"bytes"`
	CreatedAt     int64       `json:"created_at"`
	Filename      string      `json:"filename"`
	Purpose       FilePurpose `json:"purpose"`
	Status        FileStatus  `json:"status,omitempty"`
	StatusDetails *string     `json:"status_details,omitempty"`
	ExpiresAt     *int64      `json:"expires_at,omitempty"`

	// Storage backend info
	StorageBackend FileStorageBackend `json:"storage_backend,omitempty"`
	StorageURI     string             `json:"storage_uri,omitempty"` // S3/GCS URI if applicable

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostFileListRequest represents a request to list files.
type BifrostFileListRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`

	RawRequestBody []byte `json:"-"` // Raw request body (not serialized)

	// Filters
	Purpose FilePurpose `json:"purpose,omitempty"` // Filter by purpose

	// Pagination
	Limit int     `json:"limit,omitempty"` // Max results to return
	After *string `json:"after,omitempty"` // Cursor for pagination
	Order *string `json:"order,omitempty"` // Sort order (asc/desc)

	// Storage configuration (for S3/GCS backends)
	StorageConfig *FileStorageConfig `json:"storage_config,omitempty"`

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// GetRawRequestBody returns the raw request body.
func (request *BifrostFileListRequest) GetRawRequestBody() []byte {
	return request.RawRequestBody
}

// BifrostFileListResponse represents the response from listing files.
type BifrostFileListResponse struct {
	Object  string       `json:"object,omitempty"` // "list"
	Data    []FileObject `json:"data"`
	HasMore bool         `json:"has_more,omitempty"`
	After   *string      `json:"after,omitempty"` // Continuation token for pagination

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostFileRetrieveRequest represents a request to retrieve file metadata.
type BifrostFileRetrieveRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`

	RawRequestBody []byte `json:"-"` // Raw request body (not serialized)

	FileID string `json:"file_id"` // ID of the file to retrieve

	// Storage configuration (for S3/GCS backends)
	StorageConfig *FileStorageConfig `json:"storage_config,omitempty"`

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// GetRawRequestBody returns the raw request body.
func (request *BifrostFileRetrieveRequest) GetRawRequestBody() []byte {
	return request.RawRequestBody
}

// BifrostFileRetrieveResponse represents the response from retrieving file metadata.
type BifrostFileRetrieveResponse struct {
	ID            string      `json:"id"`
	Object        string      `json:"object,omitempty"` // "file"
	Bytes         int64       `json:"bytes"`
	CreatedAt     int64       `json:"created_at"`
	UpdatedAt     int64       `json:"updated_at,omitempty"`
	Filename      string      `json:"filename"`
	Purpose       FilePurpose `json:"purpose"`
	Status        FileStatus  `json:"status,omitempty"`
	StatusDetails *string     `json:"status_details,omitempty"`
	ExpiresAt     *int64      `json:"expires_at,omitempty"`

	// Storage backend info
	StorageBackend FileStorageBackend `json:"storage_backend,omitempty"`
	StorageURI     string             `json:"storage_uri,omitempty"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostFileDeleteRequest represents a request to delete a file.
type BifrostFileDeleteRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`
	FileID   string        `json:"file_id"` // ID of the file to delete

	RawRequestBody []byte `json:"-"` // Raw request body (not serialized)

	// Storage configuration (for S3/GCS backends)
	StorageConfig *FileStorageConfig `json:"storage_config,omitempty"`

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// GetRawRequestBody returns the raw request body.
func (request *BifrostFileDeleteRequest) GetRawRequestBody() []byte {
	return request.RawRequestBody
}

// BifrostFileDeleteResponse represents the response from deleting a file.
type BifrostFileDeleteResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"` // "file"
	Deleted bool   `json:"deleted"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostFileContentRequest represents a request to download file content.
type BifrostFileContentRequest struct {
	Provider ModelProvider `json:"provider"`
	Model    *string       `json:"model"`
	FileID   string        `json:"file_id"` // ID of the file to download

	RawRequestBody []byte `json:"-"` // Raw request body (not serialized)

	// Storage configuration (for S3/GCS backends)
	StorageConfig *FileStorageConfig `json:"storage_config,omitempty"`

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// GetRawRequestBody returns the raw request body.
func (request *BifrostFileContentRequest) GetRawRequestBody() []byte {
	return request.RawRequestBody
}

// BifrostFileContentResponse represents the response from downloading file content.
type BifrostFileContentResponse struct {
	FileID      string `json:"file_id"`
	Content     []byte `json:"-"`                      // Raw file content (not serialized)
	ContentType string `json:"content_type,omitempty"` // MIME type

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}
