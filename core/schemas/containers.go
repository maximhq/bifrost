// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

// ContainerStatus represents the status of a container.
type ContainerStatus string

const (
	ContainerStatusRunning ContainerStatus = "running"
)

// ContainerExpiresAfter represents the expiration configuration for a container.
type ContainerExpiresAfter struct {
	Anchor  string `json:"anchor"`  // The anchor point for expiration (e.g., "last_active_at")
	Minutes int    `json:"minutes"` // Number of minutes after anchor point
}

// ContainerObject represents a container object returned by the API.
type ContainerObject struct {
	ID           string                 `json:"id"`
	Object       string                 `json:"object,omitempty"` // "container"
	Name         string                 `json:"name"`
	CreatedAt    int64                  `json:"created_at"`
	Status       ContainerStatus        `json:"status,omitempty"`
	ExpiresAfter *ContainerExpiresAfter `json:"expires_after,omitempty"`
	LastActiveAt *int64                 `json:"last_active_at,omitempty"`
	MemoryLimit  string                 `json:"memory_limit,omitempty"` // e.g., "1g", "4g"
	Metadata     map[string]string      `json:"metadata,omitempty"`
}

// BifrostContainerCreateRequest represents a request to create a container.
type BifrostContainerCreateRequest struct {
	Provider ModelProvider `json:"provider"`

	// Required fields
	Name string `json:"name"` // Name of the container

	// Optional fields
	ExpiresAfter *ContainerExpiresAfter `json:"expires_after,omitempty"` // Expiration configuration
	FileIDs      []string               `json:"file_ids,omitempty"`      // IDs of existing files to copy into this container
	MemoryLimit  string                 `json:"memory_limit,omitempty"`  // Memory limit (e.g., "1g", "4g")
	Metadata     map[string]string      `json:"metadata,omitempty"`      // User-provided metadata

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// BifrostContainerCreateResponse represents the response from creating a container.
type BifrostContainerCreateResponse struct {
	ID           string                 `json:"id"`
	Object       string                 `json:"object,omitempty"` // "container"
	Name         string                 `json:"name"`
	CreatedAt    int64                  `json:"created_at"`
	Status       ContainerStatus        `json:"status,omitempty"`
	ExpiresAfter *ContainerExpiresAfter `json:"expires_after,omitempty"`
	LastActiveAt *int64                 `json:"last_active_at,omitempty"`
	MemoryLimit  string                 `json:"memory_limit,omitempty"`
	Metadata     map[string]string      `json:"metadata,omitempty"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostContainerListRequest represents a request to list containers.
type BifrostContainerListRequest struct {
	Provider ModelProvider `json:"provider"`

	// Pagination
	Limit int     `json:"limit,omitempty"` // Max results to return (1-100, default 20)
	After *string `json:"after,omitempty"` // Cursor for pagination
	Order *string `json:"order,omitempty"` // Sort order (asc/desc), default desc

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// BifrostContainerListResponse represents the response from listing containers.
type BifrostContainerListResponse struct {
	Object  string            `json:"object,omitempty"` // "list"
	Data    []ContainerObject `json:"data"`
	FirstID *string           `json:"first_id,omitempty"`
	LastID  *string           `json:"last_id,omitempty"`
	HasMore bool              `json:"has_more,omitempty"`
	After   *string           `json:"after,omitempty"` // Encoded cursor for next page (includes key index for multi-key pagination)

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostContainerRetrieveRequest represents a request to retrieve a container.
type BifrostContainerRetrieveRequest struct {
	Provider    ModelProvider `json:"provider"`
	ContainerID string        `json:"container_id"` // ID of the container to retrieve

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// BifrostContainerRetrieveResponse represents the response from retrieving a container.
type BifrostContainerRetrieveResponse struct {
	ID           string                 `json:"id"`
	Object       string                 `json:"object,omitempty"` // "container"
	Name         string                 `json:"name"`
	CreatedAt    int64                  `json:"created_at"`
	Status       ContainerStatus        `json:"status,omitempty"`
	ExpiresAfter *ContainerExpiresAfter `json:"expires_after,omitempty"`
	LastActiveAt *int64                 `json:"last_active_at,omitempty"`
	MemoryLimit  string                 `json:"memory_limit,omitempty"`
	Metadata     map[string]string      `json:"metadata,omitempty"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostContainerDeleteRequest represents a request to delete a container.
type BifrostContainerDeleteRequest struct {
	Provider    ModelProvider `json:"provider"`
	ContainerID string        `json:"container_id"` // ID of the container to delete

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// BifrostContainerDeleteResponse represents the response from deleting a container.
type BifrostContainerDeleteResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"` // "container.deleted"
	Deleted bool   `json:"deleted"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// =============================================================================
// CONTAINER FILES API
// =============================================================================

// ContainerFileObject represents a file within a container.
type ContainerFileObject struct {
	ID          string `json:"id"`
	Object      string `json:"object,omitempty"` // "container.file"
	Bytes       int64  `json:"bytes"`
	CreatedAt   int64  `json:"created_at"`
	ContainerID string `json:"container_id"`
	Path        string `json:"path"`
	Source      string `json:"source"` // "user" typically
}

// BifrostContainerFileCreateRequest represents a request to create a file in a container.
type BifrostContainerFileCreateRequest struct {
	Provider    ModelProvider `json:"provider"`
	ContainerID string        `json:"container_id"` // ID of the container

	// One of these must be provided
	File   []byte  `json:"-"`                   // File content (for multipart upload)
	FileID *string `json:"file_id,omitempty"`   // Reference to existing file
	Path   *string `json:"file_path,omitempty"` // Path for the file in the container

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// BifrostContainerFileCreateResponse represents the response from creating a container file.
type BifrostContainerFileCreateResponse struct {
	ID          string `json:"id"`
	Object      string `json:"object,omitempty"` // "container.file"
	Bytes       int64  `json:"bytes"`
	CreatedAt   int64  `json:"created_at"`
	ContainerID string `json:"container_id"`
	Path        string `json:"path"`
	Source      string `json:"source"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostContainerFileListRequest represents a request to list files in a container.
type BifrostContainerFileListRequest struct {
	Provider    ModelProvider `json:"provider"`
	ContainerID string        `json:"container_id"` // ID of the container

	// Pagination
	Limit int     `json:"limit,omitempty"` // Max results to return (1-100, default 20)
	After *string `json:"after,omitempty"` // Cursor for pagination
	Order *string `json:"order,omitempty"` // Sort order (asc/desc), default desc

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// BifrostContainerFileListResponse represents the response from listing container files.
type BifrostContainerFileListResponse struct {
	Object  string                `json:"object,omitempty"` // "list"
	Data    []ContainerFileObject `json:"data"`
	FirstID *string               `json:"first_id,omitempty"`
	LastID  *string               `json:"last_id,omitempty"`
	HasMore bool                  `json:"has_more,omitempty"`
	After   *string               `json:"after,omitempty"` // Encoded cursor for next page (includes key index for multi-key pagination)

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostContainerFileRetrieveRequest represents a request to retrieve a container file.
type BifrostContainerFileRetrieveRequest struct {
	Provider    ModelProvider `json:"provider"`
	ContainerID string        `json:"container_id"` // ID of the container
	FileID      string        `json:"file_id"`      // ID of the file to retrieve

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// BifrostContainerFileRetrieveResponse represents the response from retrieving a container file.
type BifrostContainerFileRetrieveResponse struct {
	ID          string `json:"id"`
	Object      string `json:"object,omitempty"` // "container.file"
	Bytes       int64  `json:"bytes"`
	CreatedAt   int64  `json:"created_at"`
	ContainerID string `json:"container_id"`
	Path        string `json:"path"`
	Source      string `json:"source"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostContainerFileContentRequest represents a request to retrieve the content of a container file.
type BifrostContainerFileContentRequest struct {
	Provider    ModelProvider `json:"provider"`
	ContainerID string        `json:"container_id"` // ID of the container
	FileID      string        `json:"file_id"`      // ID of the file

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// BifrostContainerFileContentResponse represents the response from retrieving container file content.
type BifrostContainerFileContentResponse struct {
	Content     []byte `json:"content"`      // Raw file content
	ContentType string `json:"content_type"` // MIME type of the content

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostContainerFileDeleteRequest represents a request to delete a container file.
type BifrostContainerFileDeleteRequest struct {
	Provider    ModelProvider `json:"provider"`
	ContainerID string        `json:"container_id"` // ID of the container
	FileID      string        `json:"file_id"`      // ID of the file to delete

	// Extra parameters for provider-specific features
	ExtraParams map[string]interface{} `json:"-"`
}

// BifrostContainerFileDeleteResponse represents the response from deleting a container file.
type BifrostContainerFileDeleteResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"` // "container.file.deleted"
	Deleted bool   `json:"deleted"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}
