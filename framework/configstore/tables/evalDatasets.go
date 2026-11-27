package tables

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// TableEvalDataset is a dataset: a typed table of test cases. Its rows are versioned.
// Every edit goes to the draft, which is version HeadVersion+1, and saving a version
// moves HeadVersion up.
type TableEvalDataset struct {
	ID          string   `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name        string   `gorm:"type:varchar(255);not null;uniqueIndex:idx_evals_dataset_name" json:"name"`
	Description string   `gorm:"type:text" json:"description"`
	TagsJSON    *string  `gorm:"type:text;column:tags_json" json:"-"`
	Tags        []string `gorm:"-" json:"tags,omitempty"`
	// FolderID is the folder the dataset is filed under. Nil means no folder.
	FolderID *string `gorm:"type:varchar(36)" json:"folder_id,omitempty"`
	// NextSeq is the sequence number the next added row gets. Rows are listed by it.
	NextSeq int64 `gorm:"not null;default:1" json:"-"`
	// HeadVersion is the last saved version number, 0 before the first save.
	HeadVersion int `gorm:"not null;default:0" json:"head_version"`
	// DraftChanges counts the edits made since the last saved version.
	DraftChanges int `gorm:"not null;default:0" json:"draft_changes"`
	// MaxVersions caps how many saved versions are kept. Nil keeps every version.
	MaxVersions *int      `json:"max_versions,omitempty"`
	CreatedBy   string    `gorm:"type:varchar(255)" json:"created_by,omitempty"`
	DeletedAt   int64     `gorm:"column:deleted_at_unix_nano;not null;default:0;uniqueIndex:idx_evals_dataset_name" json:"-"` // 0 while live
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null" json:"updated_at"`
}

// TableName for TableEvalDataset
func (TableEvalDataset) TableName() string { return "evals_datasets" }

// TableEvalDatasetFile is a file uploaded to a dataset, which file cells refer to by
// ID. The bytes live in the object store when the gateway has one (ObjectKey is set
// and Data is empty), and in Data otherwise. A file is kept until its dataset is
// deleted: saved versions may still read it.
type TableEvalDatasetFile struct {
	ID          string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	DatasetID   string    `gorm:"type:varchar(36);not null;index" json:"dataset_id"`
	Name        string    `gorm:"type:varchar(255);not null" json:"name"`
	ContentType string    `gorm:"type:varchar(255);not null" json:"content_type"`
	Size        int64     `gorm:"not null" json:"size"`
	ObjectKey   string    `gorm:"type:varchar(512)" json:"-"`
	Data        []byte    `json:"-"`
	CreatedBy   string    `gorm:"type:varchar(255)" json:"created_by,omitempty"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
}

// TableName for TableEvalDatasetFile
func (TableEvalDatasetFile) TableName() string { return "evals_dataset_files" }

// TableEvalDatasetFolder groups datasets in the UI. Folders nest, and hold nothing
// but the grouping: deleting one moves what it holds up one level.
type TableEvalDatasetFolder struct {
	ID   string `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name string `gorm:"type:varchar(255);not null;uniqueIndex:idx_evals_dataset_folder_name" json:"name"`
	// ParentID is the folder this one sits in. Nil means the top level.
	ParentID    *string   `gorm:"type:varchar(36)" json:"parent_id,omitempty"`
	Description string    `gorm:"type:text" json:"description"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null" json:"updated_at"`
}

// TableName for TableEvalDatasetFolder
func (TableEvalDatasetFolder) TableName() string { return "evals_dataset_folders" }

// BeforeSave GORM hook to serialize JSON fields
func (d *TableEvalDataset) BeforeSave(tx *gorm.DB) error {
	if d.Tags == nil {
		d.TagsJSON = nil
		return nil
	}
	data, err := json.Marshal(d.Tags)
	if err != nil {
		return err
	}
	d.TagsJSON = new(string(data))
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (d *TableEvalDataset) AfterFind(tx *gorm.DB) error {
	if d.TagsJSON != nil && *d.TagsJSON != "" {
		if err := json.Unmarshal([]byte(*d.TagsJSON), &d.Tags); err != nil {
			return err
		}
	}
	return nil
}

// TableEvalDatasetColumn is a column of a dataset's draft. A saved version keeps its
// own copy of the columns, so changing or deleting one here never alters history.
type TableEvalDatasetColumn struct {
	ID        string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	DatasetID string    `gorm:"type:varchar(36);not null;uniqueIndex:idx_evals_dataset_column_name" json:"dataset_id"`
	Name      string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_evals_dataset_column_name" json:"name"`
	Type      string    `gorm:"type:varchar(32);not null" json:"type"`
	Position  int       `gorm:"not null;default:0" json:"position"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

// TableName for TableEvalDatasetColumn
func (TableEvalDatasetColumn) TableName() string { return "evals_dataset_columns" }

// EvalDatasetVersionColumn is a column as a saved version recorded it.
type EvalDatasetVersionColumn struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Position int    `json:"position"`
}

// TableEvalDatasetVersion is a saved version of a dataset. It holds no rows: the rows
// of version N are the row records whose [AddedIn, RemovedIn) range contains N.
type TableEvalDatasetVersion struct {
	ID          string                     `gorm:"type:varchar(36);primaryKey" json:"id"`
	DatasetID   string                     `gorm:"type:varchar(36);not null;uniqueIndex:idx_evals_dataset_version" json:"dataset_id"`
	Number      int                        `gorm:"not null;uniqueIndex:idx_evals_dataset_version" json:"number"`
	Name        string                     `gorm:"type:varchar(255)" json:"name,omitempty"`
	Description string                     `gorm:"type:text" json:"description,omitempty"`
	ColumnsJSON string                     `gorm:"type:text;not null;column:columns_json" json:"-"`
	Columns     []EvalDatasetVersionColumn `gorm:"-" json:"columns"`
	RowCount    int64                      `gorm:"not null;default:0" json:"row_count"`
	RowsAdded   int64                      `gorm:"not null;default:0" json:"rows_added"`
	RowsChanged int64                      `gorm:"not null;default:0" json:"rows_changed"`
	RowsRemoved int64                      `gorm:"not null;default:0" json:"rows_removed"`
	// RestoredFrom is the version this one was restored from, when it was made by a restore.
	RestoredFrom *int      `json:"restored_from,omitempty"`
	CreatedBy    string    `gorm:"type:varchar(255)" json:"created_by,omitempty"`
	CreatedAt    time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time `gorm:"not null" json:"updated_at"`
}

// TableName for TableEvalDatasetVersion
func (TableEvalDatasetVersion) TableName() string { return "evals_dataset_versions" }

// BeforeSave GORM hook to serialize JSON fields
func (v *TableEvalDatasetVersion) BeforeSave(tx *gorm.DB) error {
	if v.Columns == nil {
		v.ColumnsJSON = "[]"
		return nil
	}
	data, err := json.Marshal(v.Columns)
	if err != nil {
		return err
	}
	v.ColumnsJSON = string(data)
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (v *TableEvalDatasetVersion) AfterFind(tx *gorm.DB) error {
	if v.ColumnsJSON != "" {
		if err := json.Unmarshal([]byte(v.ColumnsJSON), &v.Columns); err != nil {
			return err
		}
	}
	return nil
}

// TableEvalDatasetRow is one stored record of a dataset row. A dataset row keeps its
// RowID for life; an edit to a row that a saved version uses retires the old record
// and writes a new one, so the saved version still reads what it read before.
type TableEvalDatasetRow struct {
	ID        string `gorm:"type:varchar(36);primaryKey" json:"-"`
	DatasetID string `gorm:"type:varchar(36);not null;index:idx_evals_dataset_row_seq,priority:1;index:idx_evals_dataset_row_id,priority:1" json:"-"`
	RowID     string `gorm:"type:varchar(36);not null;index:idx_evals_dataset_row_id,priority:2" json:"id"`
	Seq       int64  `gorm:"not null;index:idx_evals_dataset_row_seq,priority:2" json:"seq"`
	// CellsJSON maps column ID to the cell's string value. Cells are keyed by ID, not
	// name, so renaming or deleting a column rewrites no row.
	CellsJSON string `gorm:"type:text;not null;column:cells_json" json:"-"`
	// Cells is the decoded form, filled on request by DecodeCells. It is not decoded on
	// every read because export and import work on the JSON text directly.
	Cells map[string]string `gorm:"-" json:"cells"`
	// AddedIn is the first version that has this record.
	AddedIn int `gorm:"not null" json:"-"`
	// RemovedIn is the first version that no longer has it. Nil while it is current.
	RemovedIn *int      `gorm:"index:idx_evals_dataset_row_seq,priority:3" json:"-"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
}

// TableName for TableEvalDatasetRow
func (TableEvalDatasetRow) TableName() string { return "evals_dataset_rows" }

// DecodeCells fills Cells from CellsJSON.
func (r *TableEvalDatasetRow) DecodeCells() error {
	r.Cells = map[string]string{}
	if r.CellsJSON == "" {
		return nil
	}
	return json.Unmarshal([]byte(r.CellsJSON), &r.Cells)
}

// TableEvalDatasetSplit is a named subset of a dataset's rows.
type TableEvalDatasetSplit struct {
	ID          string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	DatasetID   string    `gorm:"type:varchar(36);not null;uniqueIndex:idx_evals_dataset_split_name" json:"dataset_id"`
	Name        string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_evals_dataset_split_name" json:"name"`
	Description string    `gorm:"type:text" json:"description"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null" json:"updated_at"`
}

// TableName for TableEvalDatasetSplit
func (TableEvalDatasetSplit) TableName() string { return "evals_dataset_splits" }

// TableEvalDatasetSplitRow puts a dataset row in a split. It names the row's stable
// RowID, so membership survives edits to the row.
type TableEvalDatasetSplitRow struct {
	SplitID   string `gorm:"type:varchar(36);primaryKey" json:"split_id"`
	RowID     string `gorm:"type:varchar(36);primaryKey" json:"row_id"`
	DatasetID string `gorm:"type:varchar(36);not null;index" json:"-"`
}

// TableName for TableEvalDatasetSplitRow
func (TableEvalDatasetSplitRow) TableName() string { return "evals_dataset_split_rows" }
