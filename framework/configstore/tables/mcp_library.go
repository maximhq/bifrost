package tables

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TableMCPLibrary represents a single discoverable MCP server in the MCP
// library catalog. Most rows are synced from the external MCP library datasheet
// (see modelcatalog.DefaultMCPLibraryURL) on a configurable interval, mirroring
// the governance_model_pricing / governance_model_parameters tables. Orgs may
// also publish their own internal servers as "custom" rows (see Source), which
// are protected from being overwritten or resurrected by the remote sync.
//
// A row is a *template* for an schemas.MCPClientConfig: it carries the
// connection details a user needs to install the server, shaped the same way
// the live config is. The connection fields are mutually exclusive by
// ConnectionType — ConnectionURL for http/sse, StdioConfig for stdio — matching
// MCPClientConfig.ConnectionString / MCPClientConfig.StdioConfig.
//
// Each row is keyed by a stable slug derived from the display name so the sync
// upsert is idempotent.
type TableMCPLibrary struct {
	ID          uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	Slug        string `gorm:"type:varchar(255);not null;uniqueIndex:idx_mcp_library_slug" json:"slug"`
	Name        string `gorm:"type:varchar(255);not null" json:"name"`
	Description string `gorm:"type:text" json:"description,omitempty"`
	Category    string `gorm:"type:varchar(100);index:idx_mcp_library_category" json:"category,omitempty"`

	// ConnectionType is one of schemas.MCPConnectionType ("http" | "stdio" |
	// "sse") and selects which connection field below is populated.
	ConnectionType schemas.MCPConnectionType `gorm:"type:varchar(20);not null" json:"connection_type"`

	// ConnectionURL is the server endpoint for http/sse entries (parallel to
	// MCPClientConfig.ConnectionString). Empty for stdio entries. Stored as a
	// plain template string — the catalog publishes no secrets, so callers
	// supply auth at install time.
	ConnectionURL string `gorm:"type:text" json:"connection_url,omitempty"`

	// StdioConfig holds the command/args/env names for stdio entries (parallel
	// to MCPClientConfig.StdioConfig). Nil for http/sse entries. Envs lists the
	// environment variable *names* the user must provide locally; no values are
	// ever published in the catalog.
	StdioConfig *schemas.MCPStdioConfig `gorm:"type:text;serializer:json;default:null" json:"stdio_config,omitempty"`

	// AuthType declares what authentication the server expects (none, headers,
	// oauth, ...) so the install UI can prompt accordingly. RequiredHeaderKeys
	// lists the header names a headers/per-user-headers server needs — values
	// are supplied by the user at install time, never stored in the catalog.
	AuthType           schemas.MCPAuthType `gorm:"type:varchar(20);default:'none'" json:"auth_type,omitempty"`
	RequiredHeaderKeys []string            `gorm:"type:text;serializer:json;default:null" json:"required_header_keys,omitempty"`

	// Presentation / discovery metadata.
	IconURL   string         `gorm:"type:text" json:"icon_url,omitempty"`
	DocsURL   string         `gorm:"type:text" json:"docs_url,omitempty"`
	Publisher string         `gorm:"type:varchar(255)" json:"publisher,omitempty"`
	Tags      []string       `gorm:"type:text;serializer:json;default:null" json:"tags,omitempty"`
	Metadata  map[string]any `gorm:"type:text;serializer:json;default:null" json:"metadata,omitempty"`

	// Source distinguishes remote-synced rows ("remote") from org-internal rows
	// a user published through the API ("custom"). Custom rows are protected from
	// the remote sync: a slug clash in the remote payload is skipped, never
	// overwritten. Defaults to "remote" so existing rows and the sync upsert keep
	// their old behavior.
	Source string `gorm:"type:varchar(20);not null;default:'remote';index:idx_mcp_library_source" json:"source"`

	// DeletedAt is a soft-delete tombstone (nil = visible). A user may hide any
	// entry — including a remote-seeded one — and the tombstone must survive the
	// next sync so the row is never resurrected. This is a plain nullable
	// timestamp rather than gorm.DeletedAt on purpose: the sync upsert keys off
	// slug and must still see tombstoned rows by slug to skip them; gorm's
	// soft-delete would hide them from that lookup and let duplicates reinsert.
	DeletedAt *time.Time `gorm:"index:idx_mcp_library_deleted_at;default:null" json:"-"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for the MCP library catalog.
func (TableMCPLibrary) TableName() string { return "mcp_library" }
