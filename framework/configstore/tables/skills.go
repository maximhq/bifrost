// Package tables provides tables for the configstore
package tables

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	SkillSourceTypeURL     = "url"
	SkillSourceTypeDataURL = "dataurl"
	SkillSourceTypeText    = "text"
	SkillSourceTypeUpload  = "upload"
)

// SkillStringMap is stored as JSON and represents spec metadata string pairs.
type SkillStringMap map[string]string

// SkillJSONMap is stored as JSON and represents arbitrary extra frontmatter.
type SkillJSONMap map[string]any

// Value implements driver.Valuer for SkillStringMap.
func (m SkillStringMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

// Scan implements sql.Scanner for SkillStringMap.
func (m *SkillStringMap) Scan(value any) error {
	return scanSkillJSON(value, m)
}

// Value implements driver.Valuer for SkillJSONMap.
func (m SkillJSONMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

// Scan implements sql.Scanner for SkillJSONMap.
func (m *SkillJSONMap) Scan(value any) error {
	return scanSkillJSON(value, m)
}

func scanSkillJSON(value any, dest any) error {
	if value == nil {
		return nil
	}

	var data []byte
	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("unsupported skill JSON value type %T", value)
	}

	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, dest)
}

// TableSkill represents a skill in the repository. Every save creates a version snapshot.
type TableSkill struct {
	ID               string         `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name             string         `gorm:"type:varchar(64);not null;uniqueIndex" json:"name"`
	Description      string         `gorm:"type:varchar(1024);not null" json:"description"`
	License          *string        `gorm:"type:text" json:"license,omitempty"`
	Compatibility    *string        `gorm:"type:varchar(500)" json:"compatibility,omitempty"`
	Metadata         SkillStringMap `gorm:"type:json" json:"metadata,omitempty"`
	ExtraFrontmatter SkillJSONMap   `gorm:"type:json;column:extra_frontmatter" json:"extra_frontmatter,omitempty"`
	AllowedTools     *string        `gorm:"type:text;column:allowed_tools" json:"allowed_tools,omitempty"`
	SkillMDBody      string         `gorm:"type:text;not null;column:skill_md_body" json:"skill_md_body"`
	LatestVersion    string         `gorm:"type:varchar(100);not null;column:latest_version" json:"latest_version"`
	CreatedBy        *string        `gorm:"type:varchar(255);column:created_by" json:"created_by,omitempty"`
	ConfigHash       string         `gorm:"type:varchar(64)" json:"-"`
	CreatedAt        time.Time      `gorm:"not null" json:"created_at"`
	UpdatedAt        time.Time      `gorm:"not null" json:"updated_at"`

	Versions []TableSkillVersion `gorm:"foreignKey:SkillID;constraint:OnDelete:CASCADE" json:"versions,omitempty"`

	// Transient: populated from the serving version's files for API convenience.
	// Not stored in the skills table; filled by the store layer on read.
	Files []TableSkillFile `gorm:"-" json:"files,omitempty"`

	// Transient: serving version file count for list responses.
	// Not stored in the skills table; filled by the store layer on list reads.
	FileCount int64 `gorm:"-" json:"file_count"`

	// Transient: most recently created version string across all versions of this skill.
	// Filled by the store layer; used by the frontend for version bump validation.
	HighestVersion string `gorm:"-" json:"highest_version,omitempty"`
}

// TableName for TableSkill.
func (TableSkill) TableName() string { return "skills" }

// TableSkillVersion represents an immutable snapshot of a skill save.
// Files belong to versions, not directly to skills.
type TableSkillVersion struct {
	ID                  string       `gorm:"type:varchar(36);primaryKey" json:"id"`
	SkillID             string       `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_skill_version" json:"skill_id"`
	Version             string       `gorm:"type:varchar(100);not null;uniqueIndex:idx_skill_version" json:"version"`
	SkillMDBody         string       `gorm:"type:text;not null;column:skill_md_body" json:"skill_md_body,omitempty"`
	FrontmatterSnapshot SkillJSONMap `gorm:"type:json;column:frontmatter_snapshot" json:"frontmatter_snapshot,omitempty"`
	CreatedBy           *string      `gorm:"type:varchar(255);column:created_by" json:"created_by,omitempty"`
	CreatedAt           time.Time    `gorm:"not null" json:"created_at"`

	Skill *TableSkill `gorm:"foreignKey:SkillID" json:"skill,omitempty"`

	Files []TableSkillFile `gorm:"foreignKey:SkillVersionID;constraint:OnDelete:CASCADE" json:"files,omitempty"`
}

// TableName for TableSkillVersion.
func (TableSkillVersion) TableName() string { return "skill_versions" }

// TableSkillFile represents a file associated with a skill version.
// The file row is a pointer to the underlying blob/storage; blobs are reused
// across versions when the file content hasn't changed.
type TableSkillFile struct {
	ID             string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	SkillVersionID string    `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_skill_file_path;column:skill_version_id" json:"skill_version_id"`
	Path           string    `gorm:"type:varchar(1024);not null;uniqueIndex:idx_skill_file_path" json:"path"`
	SourceType     string    `gorm:"type:varchar(32);not null;column:source_type" json:"source_type"`
	SourceURL      *string   `gorm:"type:text;column:source_url" json:"source_url,omitempty"`
	StorageKey     *string   `gorm:"type:text;column:storage_key" json:"storage_key,omitempty"`
	BlobID         *string   `gorm:"type:varchar(36);index;column:blob_id" json:"blob_id,omitempty"`
	MimeType       string    `gorm:"type:varchar(255);column:mime_type" json:"mime_type"`
	FileSizeBytes  int64     `gorm:"not null;default:0;column:file_size_bytes" json:"file_size_bytes"`
	CreatedAt      time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time `gorm:"not null" json:"updated_at"`

	SkillVersion *TableSkillVersion  `gorm:"foreignKey:SkillVersionID" json:"skill_version,omitempty"`
	Blob         *TableSkillFileBlob `gorm:"foreignKey:BlobID;constraint:OnDelete:SET NULL" json:"blob,omitempty"`

	InlineContent *string `gorm:"-" json:"content,omitempty"`
	DataURL       *string `gorm:"-" json:"dataurl,omitempty"`
	UploadID      *string `gorm:"-" json:"upload_id,omitempty"`
}

// TableName for TableSkillFile.
func (TableSkillFile) TableName() string { return "skill_files" }

// BeforeSave normalizes the path before persisting so the unique index enforces the canonical form.
func (f *TableSkillFile) BeforeSave(tx *gorm.DB) error {
	f.Path = strings.TrimSpace(f.Path)
	return nil
}

// NormalizedPath returns a trimmed relative path so uniqueness logic is stable.
func (f TableSkillFile) NormalizedPath() string {
	return strings.TrimSpace(f.Path)
}

// TableSkillFileBlob stores fallback file bytes when object storage is unavailable.
type TableSkillFileBlob struct {
	ID        string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	Data      []byte    `gorm:"not null" json:"-"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
}

// TableName for TableSkillFileBlob.
func (TableSkillFileBlob) TableName() string { return "skill_file_blobs" }

// BeforeCreate ensures map fields are initialized before insertion.
func (s *TableSkill) BeforeCreate(tx *gorm.DB) error {
	if s.Metadata == nil {
		s.Metadata = SkillStringMap{}
	}
	if s.ExtraFrontmatter == nil {
		s.ExtraFrontmatter = SkillJSONMap{}
	}
	return nil
}

// BeforeSave ensures map fields are initialized before update.
func (s *TableSkill) BeforeSave(tx *gorm.DB) error {
	if s.Metadata == nil {
		s.Metadata = SkillStringMap{}
	}
	if s.ExtraFrontmatter == nil {
		s.ExtraFrontmatter = SkillJSONMap{}
	}
	return nil
}

// BeforeCreate ensures snapshot fields are initialized before insertion.
func (v *TableSkillVersion) BeforeCreate(tx *gorm.DB) error {
	if v.FrontmatterSnapshot == nil {
		v.FrontmatterSnapshot = SkillJSONMap{}
	}
	return nil
}
