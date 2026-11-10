package datasets

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

// Limits of the dataset API.
const (
	// AutoSaveThreshold is how many small edits the draft collects before a version is
	// saved on its own.
	AutoSaveThreshold = 40
	// MaxRowsPerRequest bounds a row add or delete call.
	MaxRowsPerRequest  = 1000
	DefaultRowPageSize = 100
	MaxRowPageSize     = 200
	// MaxLogsPerRequest and MaxSessionsPerRequest bound a curation call.
	MaxLogsPerRequest     = 500
	MaxSessionsPerRequest = 100
	// maxSessionLogs bounds how many requests of one session are read into a row.
	maxSessionLogs = 2000
	// writeBatch is how many rows import and copy hand to the store at a time, so
	// memory stays flat whatever the size of the source.
	writeBatch = 500
	// previewRows is how many data rows an import preview returns.
	previewRows = 5
)

// ErrNoLogStore is returned by the curation calls when the gateway has no log store.
var ErrNoLogStore = invalid("adding rows from logs needs a log store, and this gateway has none")

// Store is the part of the config store the dataset service uses.
type Store interface {
	ListEvalDatasets(ctx context.Context, params configstore.EvalDatasetListParams) ([]tables.TableEvalDataset, int64, error)
	GetEvalDatasetCounts(ctx context.Context, datasetIDs []string) (map[string]configstore.EvalDatasetCounts, error)
	GetEvalDataset(ctx context.Context, id string) (*tables.TableEvalDataset, error)
	CreateEvalDataset(ctx context.Context, dataset *tables.TableEvalDataset, columns []tables.TableEvalDatasetColumn) error
	UpdateEvalDataset(ctx context.Context, dataset *tables.TableEvalDataset) error
	DeleteEvalDataset(ctx context.Context, id string) error
	ListEvalDatasetColumns(ctx context.Context, datasetID string) ([]tables.TableEvalDatasetColumn, error)
	ListEvalDatasetRows(ctx context.Context, params configstore.EvalDatasetRowListParams) ([]tables.TableEvalDatasetRow, error)
	CountEvalDatasetRows(ctx context.Context, datasetID string, version int, splitID, search string) (int64, error)
	GetEvalDatasetRow(ctx context.Context, datasetID, rowID string, version int) (*tables.TableEvalDatasetRow, error)
	ListEvalDatasetVersions(ctx context.Context, datasetID string) ([]tables.TableEvalDatasetVersion, error)
	GetEvalDatasetVersion(ctx context.Context, datasetID string, number int) (*tables.TableEvalDatasetVersion, error)
	UpdateEvalDatasetVersion(ctx context.Context, datasetID string, number int, name, description string) error
	DiffEvalDatasetVersions(ctx context.Context, datasetID string, from, to int, afterSeq int64, limit int) ([]configstore.EvalDatasetRowChange, error)
	ListEvalDatasetSplits(ctx context.Context, datasetID string) ([]tables.TableEvalDatasetSplit, error)
	CountEvalDatasetSplitRows(ctx context.Context, datasetID string) (map[string]int64, error)
	CreateEvalDatasetSplit(ctx context.Context, split *tables.TableEvalDatasetSplit) error
	UpdateEvalDatasetSplit(ctx context.Context, split *tables.TableEvalDatasetSplit) error
	DeleteEvalDatasetSplit(ctx context.Context, datasetID, splitID string) error
	AddEvalDatasetSplitRows(ctx context.Context, datasetID, splitID string, rowIDs []string) (int64, error)
	RemoveEvalDatasetSplitRows(ctx context.Context, datasetID, splitID string, rowIDs []string) (int64, error)
	CreateEvalDatasetFile(ctx context.Context, file *tables.TableEvalDatasetFile) error
	GetEvalDatasetFile(ctx context.Context, datasetID, fileID string) (*tables.TableEvalDatasetFile, error)
	ListEvalDatasetFileKeys(ctx context.Context, datasetID string) ([]string, error)
	ListEvalDatasetFolders(ctx context.Context) ([]tables.TableEvalDatasetFolder, error)
	CreateEvalDatasetFolder(ctx context.Context, folder *tables.TableEvalDatasetFolder) error
	UpdateEvalDatasetFolder(ctx context.Context, folder *tables.TableEvalDatasetFolder) error
	DeleteEvalDatasetFolder(ctx context.Context, id string) error
	SetEvalDatasetFolderParent(ctx context.Context, id string, parentID *string) error
	SetEvalDatasetFolder(ctx context.Context, datasetID string, folderID *string) error
	WithEvalDataset(ctx context.Context, datasetID string, fn func(tx configstore.EvalDatasetTx) error) error
}

// LogReader is the part of the log store the curation calls read.
type LogReader interface {
	FindByID(ctx context.Context, id string) (*logstore.Log, error)
	SearchLogs(ctx context.Context, filters logstore.SearchFilters, pagination logstore.PaginationOptions) (*logstore.SearchResult, error)
}

// FileStore keeps the bytes of uploaded files. The gateway's object store implements it.
type FileStore interface {
	Put(ctx context.Context, key string, data []byte, tags map[string]string) error
	Get(ctx context.Context, key string) ([]byte, error)
	DeleteBatch(ctx context.Context, keys []string) error
}

// MaxInlineFileBytes is the largest file a dataset takes when the gateway has no
// object store, where the file is kept in the config database. With an object store
// the only limit is the gateway's request size limit.
const MaxInlineFileBytes = 5 << 20

// Service is the dataset API. The HTTP handlers are a thin layer over it.
type Service struct {
	store Store
	logs  LogReader
	files FileStore
}

// SetFileStore gives the service a place to keep uploaded files. Without one, files
// are kept in the config database, up to MaxInlineFileBytes each.
func (s *Service) SetFileStore(files FileStore) { s.files = files }

// NewService returns a dataset service. logs may be nil, in which case rows cannot be
// added from logs or sessions.
func NewService(store Store, logs LogReader) *Service {
	return &Service{store: store, logs: logs}
}

// ============================================================================
// Datasets
// ============================================================================

// DatasetSummary is a dataset as a listing shows it.
type DatasetSummary struct {
	tables.TableEvalDataset
	ColumnCount int64 `json:"column_count"`
	RowCount    int64 `json:"row_count"`
	SplitCount  int64 `json:"split_count"`
}

// SplitView is a split with its current row count.
type SplitView struct {
	tables.TableEvalDatasetSplit
	RowCount int64 `json:"row_count"`
}

// DatasetView is one dataset with its draft columns and splits.
type DatasetView struct {
	tables.TableEvalDataset
	Columns  []tables.TableEvalDatasetColumn `json:"columns"`
	Splits   []SplitView                     `json:"splits"`
	RowCount int64                           `json:"row_count"`
}

// ColumnSpec describes a column to add.
type ColumnSpec struct {
	Name string     `json:"name"`
	Type ColumnType `json:"type"`
}

// DatasetInput creates or updates a dataset. Template, Columns and FolderID apply on
// create only; Move changes the folder later.
type DatasetInput struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Tags        []string     `json:"tags"`
	MaxVersions *int         `json:"max_versions"`
	Template    string       `json:"template"`
	Columns     []ColumnSpec `json:"columns"`
	FolderID    *string      `json:"folder_id"`
	CreatedBy   string       `json:"-"`
}

// FolderInput creates or updates a dataset folder. ParentID applies on create only;
// MoveFolder changes it later.
type FolderInput struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	ParentID    *string `json:"parent_id"`
}

func toColumns(stored []tables.TableEvalDatasetColumn) []Column {
	columns := make([]Column, len(stored))
	for i, column := range stored {
		columns[i] = Column{ID: column.ID, Name: column.Name, Type: ColumnType(column.Type)}
	}
	return columns
}

// VersionColumns returns the columns a saved version had, in order. A test run reads
// a pinned version through them.
func VersionColumns(version *tables.TableEvalDatasetVersion) []Column {
	return versionColumns(version)
}

func versionColumns(version *tables.TableEvalDatasetVersion) []Column {
	columns := make([]Column, len(version.Columns))
	for i, column := range version.Columns {
		columns[i] = Column{ID: column.ID, Name: column.Name, Type: ColumnType(column.Type)}
	}
	return columns
}

func validateMaxVersions(max *int) error {
	if max != nil && *max < 1 {
		return invalid("max_versions must be at least 1, or left empty to keep every version")
	}
	return nil
}

// List returns datasets with their sizes.
func (s *Service) List(ctx context.Context, params configstore.EvalDatasetListParams) ([]DatasetSummary, int64, error) {
	datasets, total, err := s.store.ListEvalDatasets(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, len(datasets))
	for i := range datasets {
		ids[i] = datasets[i].ID
	}
	counts, err := s.store.GetEvalDatasetCounts(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	summaries := make([]DatasetSummary, len(datasets))
	for i, dataset := range datasets {
		c := counts[dataset.ID]
		summaries[i] = DatasetSummary{TableEvalDataset: dataset, ColumnCount: c.Columns, RowCount: c.Rows, SplitCount: c.Splits}
	}
	return summaries, total, nil
}

// Create makes a dataset from explicit columns, a template, or nothing.
func (s *Service) Create(ctx context.Context, in DatasetInput) (*DatasetView, error) {
	if err := ValidateName("dataset", in.Name); err != nil {
		return nil, err
	}
	if err := validateMaxVersions(in.MaxVersions); err != nil {
		return nil, err
	}
	columns := make([]Column, 0, len(in.Columns))
	if in.Template != "" {
		template, ok := Templates[in.Template]
		if !ok {
			return nil, invalid("unknown template %q; use qa, retrieval or agent", in.Template)
		}
		columns = append(columns, template...)
	}
	for _, column := range in.Columns {
		columns = append(columns, Column{Name: strings.TrimSpace(column.Name), Type: column.Type})
	}
	if err := ValidateColumns(columns); err != nil {
		return nil, err
	}
	dataset := &tables.TableEvalDataset{
		Name: strings.TrimSpace(in.Name), Description: in.Description, Tags: in.Tags,
		MaxVersions: in.MaxVersions, CreatedBy: in.CreatedBy, FolderID: in.FolderID,
	}
	stored := make([]tables.TableEvalDatasetColumn, len(columns))
	for i, column := range columns {
		stored[i] = tables.TableEvalDatasetColumn{Name: column.Name, Type: string(column.Type)}
	}
	if err := s.store.CreateEvalDataset(ctx, dataset, stored); err != nil {
		return nil, err
	}
	return s.Get(ctx, dataset.ID)
}

// Get returns one dataset with its columns, splits and current row count.
func (s *Service) Get(ctx context.Context, id string) (*DatasetView, error) {
	dataset, err := s.store.GetEvalDataset(ctx, id)
	if err != nil {
		return nil, err
	}
	columns, err := s.store.ListEvalDatasetColumns(ctx, id)
	if err != nil {
		return nil, err
	}
	splits, err := s.Splits(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.CountEvalDatasetRows(ctx, id, 0, "", "")
	if err != nil {
		return nil, err
	}
	return &DatasetView{TableEvalDataset: *dataset, Columns: columns, Splits: splits, RowCount: rows}, nil
}

// Update changes a dataset's name, description, tags and version cap. Lowering the
// cap removes the oldest versions over it right away.
func (s *Service) Update(ctx context.Context, id string, in DatasetInput) (*DatasetView, error) {
	if err := ValidateName("dataset", in.Name); err != nil {
		return nil, err
	}
	if err := validateMaxVersions(in.MaxVersions); err != nil {
		return nil, err
	}
	dataset := &tables.TableEvalDataset{
		ID: id, Name: strings.TrimSpace(in.Name), Description: in.Description, Tags: in.Tags, MaxVersions: in.MaxVersions,
	}
	if err := s.store.UpdateEvalDataset(ctx, dataset); err != nil {
		return nil, err
	}
	if in.MaxVersions != nil {
		if err := s.store.WithEvalDataset(ctx, id, func(tx configstore.EvalDatasetTx) error { return tx.PruneVersions() }); err != nil {
			return nil, err
		}
	}
	return s.Get(ctx, id)
}

// Move files a dataset under a folder, or under none when folderID is nil.
func (s *Service) Move(ctx context.Context, id string, folderID *string) (*DatasetView, error) {
	if err := s.store.SetEvalDatasetFolder(ctx, id, folderID); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Folders lists the dataset folders by name.
func (s *Service) Folders(ctx context.Context) ([]tables.TableEvalDatasetFolder, error) {
	folders, err := s.store.ListEvalDatasetFolders(ctx)
	if err != nil {
		return nil, err
	}
	if folders == nil {
		folders = []tables.TableEvalDatasetFolder{}
	}
	return folders, nil
}

// CreateFolder makes a dataset folder.
func (s *Service) CreateFolder(ctx context.Context, in FolderInput) (*tables.TableEvalDatasetFolder, error) {
	if err := ValidateName("folder", in.Name); err != nil {
		return nil, err
	}
	folder := &tables.TableEvalDatasetFolder{Name: strings.TrimSpace(in.Name), Description: in.Description, ParentID: in.ParentID}
	if err := s.store.CreateEvalDatasetFolder(ctx, folder); err != nil {
		return nil, err
	}
	return folder, nil
}

// UpdateFolder changes a folder's name and description.
func (s *Service) UpdateFolder(ctx context.Context, id string, in FolderInput) (*tables.TableEvalDatasetFolder, error) {
	if err := ValidateName("folder", in.Name); err != nil {
		return nil, err
	}
	folder := &tables.TableEvalDatasetFolder{ID: id, Name: strings.TrimSpace(in.Name), Description: in.Description}
	if err := s.store.UpdateEvalDatasetFolder(ctx, folder); err != nil {
		return nil, err
	}
	return folder, nil
}

// MoveFolder puts a folder inside another one, or at the top level when parentID is
// nil. A folder cannot go into itself or into a folder below it.
func (s *Service) MoveFolder(ctx context.Context, id string, parentID *string) (*tables.TableEvalDatasetFolder, error) {
	folders, err := s.store.ListEvalDatasetFolders(ctx)
	if err != nil {
		return nil, err
	}
	parents := make(map[string]*string, len(folders))
	for i := range folders {
		parents[folders[i].ID] = folders[i].ParentID
	}
	// Walk up from the new parent. Meeting the folder itself means the move would
	// close a loop. The step count bounds the walk even if stored data has a loop.
	at := parentID
	for steps := 0; at != nil && steps <= len(folders); steps++ {
		if *at == id {
			return nil, invalid("a folder cannot be moved into itself or into a folder inside it")
		}
		at = parents[*at]
	}
	if err := s.store.SetEvalDatasetFolderParent(ctx, id, parentID); err != nil {
		return nil, err
	}
	folders, err = s.store.ListEvalDatasetFolders(ctx)
	if err != nil {
		return nil, err
	}
	for i := range folders {
		if folders[i].ID == id {
			return &folders[i], nil
		}
	}
	return nil, configstore.ErrNotFound
}

// DeleteFolder removes a folder. What it holds is kept and moves up one level.
func (s *Service) DeleteFolder(ctx context.Context, id string) error {
	return s.store.DeleteEvalDatasetFolder(ctx, id)
}

// Delete removes a dataset.
func (s *Service) Delete(ctx context.Context, id string) error {
	keys, err := s.store.ListEvalDatasetFileKeys(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteEvalDataset(ctx, id); err != nil {
		return err
	}
	// The dataset is gone either way. Objects that fail to delete are only orphaned.
	if s.files != nil && len(keys) > 0 {
		_ = s.files.DeleteBatch(ctx, keys)
	}
	return nil
}

// fileURL is where a dataset's uploaded file is served.
func fileURL(datasetID, fileID string) string {
	return "/api/evals/datasets/" + datasetID + "/files/" + fileID
}

// UploadFile stores a file for a dataset's file cells and returns the reference a
// cell holds. The bytes go to the object store when the gateway has one, and to the
// config database otherwise, where a file may be MaxInlineFileBytes at most.
func (s *Service) UploadFile(ctx context.Context, datasetID, name, contentType string, data []byte, actor string) (*FileRef, error) {
	name = strings.TrimSpace(name)
	if err := ValidateName("file", name); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, invalid("the file is empty")
	}
	if _, err := s.store.GetEvalDataset(ctx, datasetID); err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	file := &tables.TableEvalDatasetFile{
		ID: uuid.NewString(), DatasetID: datasetID, Name: name, ContentType: contentType, Size: int64(len(data)), CreatedBy: actor,
	}
	if s.files == nil {
		if len(data) > MaxInlineFileBytes {
			return nil, invalid("the file is larger than %d MB, the most a dataset takes without an object store; configure an object store to upload larger files", MaxInlineFileBytes>>20)
		}
		file.Data = data
	} else {
		file.ObjectKey = "evals/datasets/" + datasetID + "/files/" + file.ID
		if err := s.files.Put(ctx, file.ObjectKey, data, map[string]string{"dataset_id": datasetID}); err != nil {
			return nil, fmt.Errorf("failed to store the file: %w", err)
		}
	}
	if err := s.store.CreateEvalDatasetFile(ctx, file); err != nil {
		if file.ObjectKey != "" {
			_ = s.files.DeleteBatch(ctx, []string{file.ObjectKey})
		}
		return nil, err
	}
	return &FileRef{ID: file.ID, Name: file.Name, ContentType: file.ContentType, Size: file.Size, URL: fileURL(datasetID, file.ID)}, nil
}

// File returns an uploaded file's record and its bytes.
func (s *Service) File(ctx context.Context, datasetID, fileID string) (*tables.TableEvalDatasetFile, []byte, error) {
	file, err := s.store.GetEvalDatasetFile(ctx, datasetID, fileID)
	if err != nil {
		return nil, nil, err
	}
	if file.ObjectKey == "" {
		return file, file.Data, nil
	}
	if s.files == nil {
		return nil, nil, errors.New("the file is in the object store, and this gateway has none configured")
	}
	data, err := s.files.Get(ctx, file.ObjectKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read the file: %w", err)
	}
	return file, data, nil
}

// Duplicate copies a dataset's columns, current rows and splits under a new name. The
// copy starts at version 1 and carries no history.
func (s *Service) Duplicate(ctx context.Context, id, name, actor string) (*DatasetView, error) {
	source, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = source.Name + " (copy)"
	}
	if err := ValidateName("dataset", name); err != nil {
		return nil, err
	}
	dataset := &tables.TableEvalDataset{
		Name: strings.TrimSpace(name), Description: source.Description, Tags: source.Tags,
		MaxVersions: source.MaxVersions, CreatedBy: actor,
	}
	stored := make([]tables.TableEvalDatasetColumn, len(source.Columns))
	for i, column := range source.Columns {
		stored[i] = tables.TableEvalDatasetColumn{Name: column.Name, Type: column.Type}
	}
	if err := s.store.CreateEvalDataset(ctx, dataset, stored); err != nil {
		return nil, err
	}
	// Cells are keyed by column ID, and the copy's columns have new IDs.
	sourceIDs := make([]string, len(source.Columns))
	copyIDs := make([]string, len(source.Columns))
	for i := range source.Columns {
		sourceIDs[i] = source.Columns[i].ID
		copyIDs[i] = stored[i].ID
	}
	// Rows are copied a page at a time, each page in its own short transaction, and
	// the last one saves version 1. A failure removes the half-made copy.
	rowIDs := make(map[string]string)
	copyRows := func() error {
		buf := getBuffer()
		defer putBuffer(buf)
		values := make([]string, len(sourceIDs))
		batch := make([]string, 0, writeBatch)
		var after int64
		for {
			rows, err := s.store.ListEvalDatasetRows(ctx, configstore.EvalDatasetRowListParams{DatasetID: id, AfterSeq: after, Limit: writeBatch})
			if err != nil {
				return err
			}
			batch = batch[:0]
			for i := range rows {
				if err := rows[i].DecodeCells(); err != nil {
					return err
				}
				for j, sourceID := range sourceIDs {
					values[j] = rows[i].Cells[sourceID]
				}
				buf.Reset()
				EncodeCells(buf, copyIDs, values)
				batch = append(batch, buf.String())
			}
			last := len(rows) < writeBatch
			err = s.store.WithEvalDataset(ctx, dataset.ID, func(tx configstore.EvalDatasetTx) error {
				added, err := tx.AddRows(batch, "")
				if err != nil {
					return err
				}
				for i := range added {
					rowIDs[rows[i].RowID] = added[i].RowID
				}
				if !last {
					return nil
				}
				_, err = tx.SaveVersion(configstore.EvalDatasetVersionSave{
					CreatedBy: actor, Description: fmt.Sprintf("Copied from %s", source.Name), Force: true,
				})
				return err
			})
			if err != nil {
				return err
			}
			if last {
				return nil
			}
			after = rows[len(rows)-1].Seq
		}
	}
	if err := copyRows(); err != nil {
		_ = s.store.DeleteEvalDataset(ctx, dataset.ID)
		return nil, err
	}
	for _, split := range source.Splits {
		copied := &tables.TableEvalDatasetSplit{DatasetID: dataset.ID, Name: split.Name, Description: split.Description}
		if err := s.store.CreateEvalDatasetSplit(ctx, copied); err != nil {
			return nil, err
		}
		var after int64
		for {
			rows, err := s.store.ListEvalDatasetRows(ctx, configstore.EvalDatasetRowListParams{DatasetID: id, SplitID: split.ID, AfterSeq: after, Limit: writeBatch})
			if err != nil {
				return nil, err
			}
			if len(rows) == 0 {
				break
			}
			members := make([]string, 0, len(rows))
			for i := range rows {
				if copyID, ok := rowIDs[rows[i].RowID]; ok {
					members = append(members, copyID)
				}
			}
			if _, err := s.store.AddEvalDatasetSplitRows(ctx, dataset.ID, copied.ID, members); err != nil {
				return nil, err
			}
			after = rows[len(rows)-1].Seq
		}
	}
	return s.Get(ctx, dataset.ID)
}

// ============================================================================
// Writes and the save rules
// ============================================================================

// write runs one change to a dataset and applies the save rules around it, all in
// one transaction:
//
//   - A bulk change (import, curation) first saves any small edits waiting in the
//     draft as their own version, then saves itself as a version, so it stands alone
//     in the history.
//   - A small edit saves a version once the draft holds AutoSaveThreshold changes.
//
// It returns the version the change was saved in, or nil when it stayed in the draft.
func (s *Service) write(ctx context.Context, id, actor, bulkNote string, fn func(tx configstore.EvalDatasetTx) error) (*tables.TableEvalDatasetVersion, error) {
	var saved *tables.TableEvalDatasetVersion
	err := s.store.WithEvalDataset(ctx, id, func(tx configstore.EvalDatasetTx) error {
		bulk := bulkNote != ""
		if bulk {
			if err := savePending(tx, actor); err != nil {
				return err
			}
		}
		if err := fn(tx); err != nil {
			return err
		}
		changes := tx.Dataset().DraftChanges
		note := bulkNote
		switch {
		case bulk && changes > 0:
		case !bulk && changes >= AutoSaveThreshold:
			note = fmt.Sprintf("Saved automatically after %d edits", changes)
		default:
			return nil
		}
		version, err := tx.SaveVersion(configstore.EvalDatasetVersionSave{CreatedBy: actor, Description: note})
		saved = version
		return err
	})
	return saved, err
}

// savePending saves the draft's waiting edits as a version, if it has any.
func savePending(tx configstore.EvalDatasetTx, actor string) error {
	if tx.Dataset().DraftChanges == 0 {
		return nil
	}
	_, err := tx.SaveVersion(configstore.EvalDatasetVersionSave{
		CreatedBy: actor, Description: "Saved automatically before a bulk change",
	})
	return err
}

// ============================================================================
// Columns
// ============================================================================

// AddColumn adds a column to the draft.
func (s *Service) AddColumn(ctx context.Context, id string, in ColumnSpec, actor string) (*tables.TableEvalDatasetColumn, error) {
	column := &tables.TableEvalDatasetColumn{Name: strings.TrimSpace(in.Name), Type: string(in.Type)}
	_, err := s.write(ctx, id, actor, "", func(tx configstore.EvalDatasetTx) error {
		existing, err := tx.Columns()
		if err != nil {
			return err
		}
		if err := ValidateColumns(append(toColumns(existing), Column{Name: column.Name, Type: in.Type})); err != nil {
			return err
		}
		return tx.AddColumn(column)
	})
	if err != nil {
		return nil, err
	}
	return column, nil
}

// UpdateColumn renames or retypes a column.
func (s *Service) UpdateColumn(ctx context.Context, id, columnID string, in ColumnSpec, actor string) (*tables.TableEvalDatasetColumn, error) {
	column := &tables.TableEvalDatasetColumn{ID: columnID, DatasetID: id, Name: strings.TrimSpace(in.Name), Type: string(in.Type)}
	_, err := s.write(ctx, id, actor, "", func(tx configstore.EvalDatasetTx) error {
		existing, err := tx.Columns()
		if err != nil {
			return err
		}
		columns := toColumns(existing)
		at := slices.IndexFunc(columns, func(c Column) bool { return c.ID == columnID })
		if at < 0 {
			return configstore.ErrNotFound
		}
		columns[at].Name, columns[at].Type = column.Name, in.Type
		if err := ValidateColumns(columns); err != nil {
			return err
		}
		column.Position = existing[at].Position
		return tx.UpdateColumn(column)
	})
	if err != nil {
		return nil, err
	}
	return column, nil
}

// DeleteColumn removes a column from the draft. Saved versions still show it.
func (s *Service) DeleteColumn(ctx context.Context, id, columnID, actor string) error {
	_, err := s.write(ctx, id, actor, "", func(tx configstore.EvalDatasetTx) error { return tx.DeleteColumn(columnID) })
	return err
}

// ReorderColumns sets the display order. columnIDs must name every column once.
func (s *Service) ReorderColumns(ctx context.Context, id string, columnIDs []string, actor string) error {
	_, err := s.write(ctx, id, actor, "", func(tx configstore.EvalDatasetTx) error {
		existing, err := tx.Columns()
		if err != nil {
			return err
		}
		if len(columnIDs) != len(existing) {
			return invalid("the order must name each of the %d columns once", len(existing))
		}
		seen := make(map[string]struct{}, len(columnIDs))
		for _, columnID := range columnIDs {
			if _, dup := seen[columnID]; dup {
				return invalid("column %q is named twice in the order", columnID)
			}
			seen[columnID] = struct{}{}
			if !slices.ContainsFunc(existing, func(c tables.TableEvalDatasetColumn) bool { return c.ID == columnID }) {
				return invalid("column %q is not in this dataset", columnID)
			}
		}
		return tx.ReorderColumns(columnIDs)
	})
	return err
}

// ============================================================================
// Rows
// ============================================================================

// RowQuery selects a page of rows.
type RowQuery struct {
	Version int // a saved version; 0 reads the draft
	SplitID string
	Search  string
	After   int64
	// Offset skips this many rows, to read a page by row position. It cannot be
	// combined with After.
	Offset int
	Limit  int
}

// RowsPage is a page of rows. NextAfter is the cursor for the next page, 0 at the
// end. Total counts the rows the query matches, across all pages.
type RowsPage struct {
	Rows      []tables.TableEvalDatasetRow `json:"rows"`
	NextAfter int64                        `json:"next_after"`
	Total     int64                        `json:"total"`
	Version   int                          `json:"version"`
}

// columnsAt returns the columns of a version, or of the draft for version 0.
func (s *Service) columnsAt(ctx context.Context, id string, version int) ([]Column, error) {
	if version > 0 {
		saved, err := s.store.GetEvalDatasetVersion(ctx, id, version)
		if err != nil {
			return nil, err
		}
		return versionColumns(saved), nil
	}
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return nil, err
	}
	stored, err := s.store.ListEvalDatasetColumns(ctx, id)
	if err != nil {
		return nil, err
	}
	return toColumns(stored), nil
}

// decodeRow fills a row's cells and drops those of columns the version does not have.
func decodeRow(row *tables.TableEvalDatasetRow, columns []Column) error {
	if err := row.DecodeCells(); err != nil {
		return err
	}
	if len(row.Cells) == 0 {
		return nil
	}
	kept := make(map[string]string, len(row.Cells))
	for _, column := range columns {
		if value, ok := row.Cells[column.ID]; ok {
			kept[column.ID] = value
		}
	}
	row.Cells = kept
	return nil
}

// Rows returns a page of a dataset's rows, in order.
func (s *Service) Rows(ctx context.Context, id string, query RowQuery) (*RowsPage, error) {
	columns, err := s.columnsAt(ctx, id, query.Version)
	if err != nil {
		return nil, err
	}
	if query.Offset < 0 || (query.Offset > 0 && query.After > 0) {
		return nil, invalid("offset must not be negative, and cannot be combined with after")
	}
	limit := query.Limit
	if limit <= 0 {
		limit = DefaultRowPageSize
	}
	limit = min(limit, MaxRowPageSize)
	// One extra row tells whether another page follows.
	rows, err := s.store.ListEvalDatasetRows(ctx, configstore.EvalDatasetRowListParams{
		DatasetID: id, Version: query.Version, SplitID: query.SplitID, Search: query.Search, AfterSeq: query.After, Offset: query.Offset, Limit: limit + 1,
	})
	if err != nil {
		return nil, err
	}
	page := &RowsPage{Version: query.Version}
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextAfter = rows[limit-1].Seq
	}
	for i := range rows {
		if err := decodeRow(&rows[i], columns); err != nil {
			return nil, err
		}
	}
	page.Rows = rows
	if page.Rows == nil {
		page.Rows = []tables.TableEvalDatasetRow{}
	}
	if page.Total, err = s.store.CountEvalDatasetRows(ctx, id, query.Version, query.SplitID, query.Search); err != nil {
		return nil, err
	}
	return page, nil
}

// resolveCells turns cells keyed by column ID or column name into cells keyed by ID,
// and checks each value against its column.
func resolveCells(columns []Column, in map[string]string) (map[string]string, error) {
	cells := make(map[string]string, len(in))
	for key, value := range in {
		at := slices.IndexFunc(columns, func(c Column) bool { return c.ID == key })
		if at < 0 {
			at = slices.IndexFunc(columns, func(c Column) bool { return c.Name == key })
		}
		if at < 0 {
			return nil, invalid("%q is not a column of this dataset", key)
		}
		stored, err := NormalizeCell(columns[at], value)
		if err != nil {
			return nil, err
		}
		cells[columns[at].ID] = stored
	}
	return cells, nil
}

// WriteResult reports a write: the rows it touched and the version it was saved in,
// when a save rule fired.
type WriteResult struct {
	Rows    []tables.TableEvalDatasetRow `json:"rows,omitempty"`
	Count   int64                        `json:"count"`
	Version *int                         `json:"version,omitempty"`
}

func (r *WriteResult) saved(version *tables.TableEvalDatasetVersion) *WriteResult {
	if version != nil {
		r.Version = new(version.Number)
	}
	return r
}

// AddRows appends rows. Each row's cells are keyed by column ID or column name.
func (s *Service) AddRows(ctx context.Context, id string, rows []map[string]string, splitID, actor string) (*WriteResult, error) {
	if len(rows) == 0 {
		return nil, invalid("at least one row is required")
	}
	if len(rows) > MaxRowsPerRequest {
		return nil, invalid("at most %d rows can be added in one call", MaxRowsPerRequest)
	}
	result := &WriteResult{}
	version, err := s.write(ctx, id, actor, "", func(tx configstore.EvalDatasetTx) error {
		stored, err := tx.Columns()
		if err != nil {
			return err
		}
		added, err := appendRows(tx, toColumns(stored), rows, splitID)
		if err != nil {
			return err
		}
		result.Rows, result.Count = added, int64(len(added))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result.saved(version), nil
}

// appendRows validates rows against the columns and appends them to the draft.
func appendRows(tx configstore.EvalDatasetTx, columns []Column, rows []map[string]string, splitID string) ([]tables.TableEvalDatasetRow, error) {
	encoded := make([]string, len(rows))
	resolved := make([]map[string]string, len(rows))
	for i, row := range rows {
		cells, err := resolveCells(columns, row)
		if err != nil {
			return nil, err
		}
		resolved[i] = cells
		encoded[i] = EncodeCellsMap(columns, cells)
	}
	added, err := tx.AddRows(encoded, splitID)
	if err != nil {
		return nil, err
	}
	for i := range added {
		added[i].Cells = resolved[i]
	}
	return added, nil
}

// patchRow changes some cells of one draft row and returns the row as stored.
func patchRow(tx configstore.EvalDatasetTx, columns []Column, rowID string, patch map[string]string) (*tables.TableEvalDatasetRow, error) {
	changes, err := resolveCells(columns, patch)
	if err != nil {
		return nil, err
	}
	row, err := tx.GetRow(rowID)
	if err != nil {
		return nil, err
	}
	// Decoding against the current columns also drops cells of deleted columns.
	if err := decodeRow(row, columns); err != nil {
		return nil, err
	}
	if row.Cells == nil {
		row.Cells = make(map[string]string, len(changes))
	}
	for columnID, value := range changes {
		row.Cells[columnID] = value
	}
	updated, err := tx.SetRowCells(rowID, EncodeCellsMap(columns, row.Cells))
	if err != nil {
		return nil, err
	}
	if err := decodeRow(updated, columns); err != nil {
		return nil, err
	}
	return updated, nil
}

// UpdateRow changes some cells of one row. A cell set to "" is cleared.
func (s *Service) UpdateRow(ctx context.Context, id, rowID string, patch map[string]string, actor string) (*WriteResult, error) {
	result := &WriteResult{}
	version, err := s.write(ctx, id, actor, "", func(tx configstore.EvalDatasetTx) error {
		stored, err := tx.Columns()
		if err != nil {
			return err
		}
		updated, err := patchRow(tx, toColumns(stored), rowID, patch)
		if err != nil {
			return err
		}
		result.Rows, result.Count = []tables.TableEvalDatasetRow{*updated}, 1
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result.saved(version), nil
}

// RowPatch changes some cells of one row. Cells are keyed by column ID or name.
type RowPatch struct {
	RowID string            `json:"id"`
	Cells map[string]string `json:"cells"`
}

// CellEdits is one spreadsheet edit that touches many cells: a paste or a range
// delete. Updates change existing rows; Rows are appended after them.
type CellEdits struct {
	Updates []RowPatch          `json:"updates"`
	Rows    []map[string]string `json:"rows"`
	SplitID string              `json:"split_id"`
}

// CellEditResult reports the rows a CellEdits call changed and added.
type CellEditResult struct {
	Updated []tables.TableEvalDatasetRow `json:"updated"`
	Added   []tables.TableEvalDatasetRow `json:"added"`
	Version *int                         `json:"version,omitempty"`
}

// EditCells applies a paste or a range delete in one transaction, so a bad cell
// leaves nothing half applied. A cell set to "" is cleared.
func (s *Service) EditCells(ctx context.Context, id string, edits CellEdits, actor string) (*CellEditResult, error) {
	if len(edits.Updates) == 0 && len(edits.Rows) == 0 {
		return nil, invalid("at least one row to update or add is required")
	}
	if len(edits.Updates) > MaxRowsPerRequest || len(edits.Rows) > MaxRowsPerRequest {
		return nil, invalid("at most %d rows can be updated or added in one call", MaxRowsPerRequest)
	}
	result := &CellEditResult{
		Updated: make([]tables.TableEvalDatasetRow, 0, len(edits.Updates)),
		Added:   []tables.TableEvalDatasetRow{},
	}
	version, err := s.write(ctx, id, actor, "", func(tx configstore.EvalDatasetTx) error {
		stored, err := tx.Columns()
		if err != nil {
			return err
		}
		columns := toColumns(stored)
		for _, patch := range edits.Updates {
			updated, err := patchRow(tx, columns, patch.RowID, patch.Cells)
			if err != nil {
				return err
			}
			result.Updated = append(result.Updated, *updated)
		}
		if len(edits.Rows) == 0 {
			return nil
		}
		added, err := appendRows(tx, columns, edits.Rows, edits.SplitID)
		result.Added = added
		return err
	})
	if err != nil {
		return nil, err
	}
	if version != nil {
		result.Version = new(version.Number)
	}
	return result, nil
}

// DeleteRows removes rows from the draft.
func (s *Service) DeleteRows(ctx context.Context, id string, rowIDs []string, actor string) (*WriteResult, error) {
	if len(rowIDs) == 0 {
		return nil, invalid("at least one row ID is required")
	}
	if len(rowIDs) > MaxRowsPerRequest {
		return nil, invalid("at most %d rows can be deleted in one call", MaxRowsPerRequest)
	}
	result := &WriteResult{}
	version, err := s.write(ctx, id, actor, "", func(tx configstore.EvalDatasetTx) error {
		deleted, err := tx.DeleteRows(rowIDs)
		result.Count = deleted
		return err
	})
	if err != nil {
		return nil, err
	}
	return result.saved(version), nil
}

// RowVariables returns the evaluator variables one row produces.
func (s *Service) RowVariables(ctx context.Context, id, rowID string, version int) (map[string]string, error) {
	columns, err := s.columnsAt(ctx, id, version)
	if err != nil {
		return nil, err
	}
	row, err := s.store.GetEvalDatasetRow(ctx, id, rowID, version)
	if err != nil {
		return nil, err
	}
	if err := row.DecodeCells(); err != nil {
		return nil, err
	}
	return RowVariables(columns, row.Cells), nil
}

// ============================================================================
// Import and export
// ============================================================================

// ImportColumn maps one CSV field, by position, to a dataset column. ColumnID names
// an existing column; otherwise Name and Type describe a column to create.
type ImportColumn struct {
	Index    int        `json:"index"`
	ColumnID string     `json:"column_id,omitempty"`
	Name     string     `json:"name,omitempty"`
	Type     ColumnType `json:"type,omitempty"`
}

// ImportOptions controls a CSV import.
type ImportOptions struct {
	HasHeader bool
	// Mapping says where each CSV field goes. When empty, the header is matched to the
	// dataset's columns by name.
	Mapping []ImportColumn
	// CreateColumns turns header fields that match no column into variable columns.
	// It applies only when Mapping is empty.
	CreateColumns bool
	SplitID       string
	Actor         string
}

// ImportResult reports a finished import.
type ImportResult struct {
	RowsAdded      int      `json:"rows_added"`
	ColumnsCreated []string `json:"columns_created,omitempty"`
	Version        *int     `json:"version,omitempty"`
}

// ImportPreview is the head of a CSV with a suggested column mapping.
type ImportPreview struct {
	Header []string   `json:"header"`
	Rows   [][]string `json:"rows"`
	// Mapping has one entry per header field: the matching column ID, or "".
	Mapping []string `json:"mapping"`
}

// PreviewImport reads the head of a CSV. The input may be cut off mid-row, as it is
// when the UI sends only the first bytes of a file, so a broken last row is ignored.
func (s *Service) PreviewImport(ctx context.Context, id string, r io.Reader) (*ImportPreview, error) {
	columns, err := s.columnsAt(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	reader := NewCSVReader(r)
	header, err := reader.Read()
	if err != nil {
		return nil, invalid("the file has no readable CSV header")
	}
	preview := &ImportPreview{Header: slices.Clone(header), Rows: make([][]string, 0, previewRows)}
	for len(preview.Rows) < previewRows {
		record, err := reader.Read()
		if err != nil {
			break
		}
		preview.Rows = append(preview.Rows, slices.Clone(record))
	}
	preview.Mapping = SuggestMapping(preview.Header, columns)
	return preview, nil
}

// importTargets works out, for each CSV field position, the column ID its values go
// to, creating the columns the import asks for.
func importTargets(tx configstore.EvalDatasetTx, opts ImportOptions, header []string) ([]string, []Column, []string, error) {
	stored, err := tx.Columns()
	if err != nil {
		return nil, nil, nil, err
	}
	columns := toColumns(stored)
	type newColumn struct {
		index  int
		column Column
	}
	var create []newColumn
	var targets []string
	place := func(index int, columnID string) {
		for len(targets) <= index {
			targets = append(targets, "")
		}
		targets[index] = columnID
	}

	if len(opts.Mapping) > 0 {
		used := make(map[string]struct{}, len(opts.Mapping))
		for _, entry := range opts.Mapping {
			if entry.Index < 0 {
				return nil, nil, nil, invalid("a mapping index cannot be negative")
			}
			if entry.ColumnID == "" {
				create = append(create, newColumn{entry.Index, Column{Name: strings.TrimSpace(entry.Name), Type: entry.Type}})
				continue
			}
			if !slices.ContainsFunc(columns, func(c Column) bool { return c.ID == entry.ColumnID }) {
				return nil, nil, nil, invalid("column %q is not in this dataset", entry.ColumnID)
			}
			if _, dup := used[entry.ColumnID]; dup {
				return nil, nil, nil, invalid("two CSV fields are mapped to the same column")
			}
			used[entry.ColumnID] = struct{}{}
			place(entry.Index, entry.ColumnID)
		}
	} else {
		if header == nil {
			return nil, nil, nil, invalid("a CSV without a header needs a column mapping")
		}
		for index, columnID := range SuggestMapping(header, columns) {
			switch {
			case columnID != "":
				place(index, columnID)
			case opts.CreateColumns && strings.TrimSpace(header[index]) != "":
				create = append(create, newColumn{index, Column{Name: strings.TrimSpace(header[index]), Type: ColumnVariable}})
			}
		}
	}

	var created []string
	if len(create) > 0 {
		all := slices.Clone(columns)
		for _, c := range create {
			all = append(all, c.column)
		}
		if err := ValidateColumns(all); err != nil {
			return nil, nil, nil, err
		}
		for _, c := range create {
			column := &tables.TableEvalDatasetColumn{Name: c.column.Name, Type: string(c.column.Type)}
			if err := tx.AddColumn(column); err != nil {
				return nil, nil, nil, err
			}
			place(c.index, column.ID)
			columns = append(columns, Column{ID: column.ID, Name: column.Name, Type: c.column.Type})
			created = append(created, column.Name)
		}
	}
	if !slices.ContainsFunc(targets, func(id string) bool { return id != "" }) {
		return nil, nil, nil, invalid("no CSV field is mapped to a column")
	}
	return targets, columns, created, nil
}

// Import reads a CSV stream into the dataset. Rows are written in batches, so memory
// stays flat whatever the file size. The import is one transaction: a bad row fails
// it and nothing is added.
func (s *Service) Import(ctx context.Context, id string, r io.Reader, opts ImportOptions) (*ImportResult, error) {
	result := &ImportResult{}
	version, err := s.write(ctx, id, opts.Actor, "Imported from CSV", func(tx configstore.EvalDatasetTx) error {
		reader := NewCSVReader(r)
		var header []string
		if opts.HasHeader {
			record, err := reader.Read()
			if err != nil {
				return invalid("the file has no readable CSV header")
			}
			header = slices.Clone(record)
		}
		targets, columns, created, err := importTargets(tx, opts, header)
		if err != nil {
			return err
		}
		result.ColumnsCreated = created
		byID := make(map[string]Column, len(columns))
		for _, column := range columns {
			byID[column.ID] = column
		}

		buf := getBuffer()
		defer putBuffer(buf)
		batch := make([]string, 0, writeBatch)
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			if _, err := tx.AddRows(batch, opts.SplitID); err != nil {
				return err
			}
			result.RowsAdded += len(batch)
			batch = batch[:0]
			return nil
		}
		for {
			record, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				var parseErr *csv.ParseError
				if errors.As(err, &parseErr) {
					return invalid("the CSV is not valid at line %d: %v", parseErr.Line, parseErr.Err)
				}
				return err
			}
			blank := true
			for i, value := range record {
				if value == "" {
					continue
				}
				blank = false
				if i >= len(targets) || targets[i] == "" {
					continue
				}
				stored, err := NormalizeCell(byID[targets[i]], value)
				if err != nil {
					line, _ := reader.FieldPos(i)
					return invalid("line %d: %v", line, err)
				}
				record[i] = stored
			}
			if blank {
				continue
			}
			buf.Reset()
			EncodeCells(buf, targets, record)
			batch = append(batch, buf.String())
			if len(batch) == writeBatch {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if err := flush(); err != nil {
			return err
		}
		if result.RowsAdded == 0 {
			return invalid("the CSV has no rows to import")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if version != nil {
		result.Version = new(version.Number)
	}
	return result, nil
}

// Export streams a dataset, or one split of it, as CSV or JSONL. Rows are read page
// by page and written straight to out.
func (s *Service) Export(ctx context.Context, id string, out io.Writer, format string, version int, splitID string) error {
	columns, err := s.columnsAt(ctx, id, version)
	if err != nil {
		return err
	}
	writer, err := NewRowWriter(out, format, columns)
	if err != nil {
		return err
	}
	var after int64
	for {
		rows, err := s.store.ListEvalDatasetRows(ctx, configstore.EvalDatasetRowListParams{
			DatasetID: id, Version: version, SplitID: splitID, AfterSeq: after, Limit: writeBatch,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for i := range rows {
			if err := writer.Write(rows[i].CellsJSON); err != nil {
				return err
			}
		}
		after = rows[len(rows)-1].Seq
	}
	return writer.Flush()
}

// ============================================================================
// Rows from logs and sessions
// ============================================================================

// Skipped is a log or session a curation call could not turn into a row.
type Skipped struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// CurateResult reports a curation call.
type CurateResult struct {
	RowsAdded int       `json:"rows_added"`
	Skipped   []Skipped `json:"skipped"`
	Version   *int      `json:"version,omitempty"`
}

func uniqueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// AddFromLogs adds one row per request log, mapped by column type. Logs that are
// missing or have no logged content are skipped and reported.
func (s *Service) AddFromLogs(ctx context.Context, id string, logIDs []string, splitID, actor string) (*CurateResult, error) {
	if s.logs == nil {
		return nil, ErrNoLogStore
	}
	logIDs = uniqueIDs(logIDs)
	if len(logIDs) == 0 {
		return nil, invalid("at least one log ID is required")
	}
	if len(logIDs) > MaxLogsPerRequest {
		return nil, invalid("at most %d logs can be added in one call", MaxLogsPerRequest)
	}
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return nil, err
	}
	result := &CurateResult{Skipped: []Skipped{}}
	logs := make([]*logstore.Log, 0, len(logIDs))
	for _, logID := range logIDs {
		log, err := s.logs.FindByID(ctx, logID)
		if err != nil || log == nil {
			result.Skipped = append(result.Skipped, Skipped{ID: logID, Reason: "log not found"})
			continue
		}
		logs = append(logs, log)
	}
	version, err := s.write(ctx, id, actor, "Added from logs", func(tx configstore.EvalDatasetTx) error {
		stored, err := tx.Columns()
		if err != nil {
			return err
		}
		columns := toColumns(stored)
		encoded := make([]string, 0, len(logs))
		for _, log := range logs {
			cells, ok := LogCells(log, columns)
			if !ok {
				result.Skipped = append(result.Skipped, Skipped{ID: log.ID, Reason: "the log has no content; content logging was off for this request"})
				continue
			}
			if len(cells) == 0 {
				result.Skipped = append(result.Skipped, Skipped{ID: log.ID, Reason: "the dataset has no input, output, conversation history or expected tool calls column to fill"})
				continue
			}
			encoded = append(encoded, EncodeCellsMap(columns, cells))
		}
		if _, err := tx.AddRows(encoded, splitID); err != nil {
			return err
		}
		result.RowsAdded = len(encoded)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if version != nil {
		result.Version = new(version.Number)
	}
	return result, nil
}

// sessionLogs reads the requests of one session, oldest first, with their content.
// Only root requests are read, so a retry or a fallback is not counted as a turn.
func (s *Service) sessionLogs(ctx context.Context, sessionID string) ([]*logstore.Log, error) {
	const page = 100
	var logs []*logstore.Log
	for offset := 0; offset < maxSessionLogs; offset += page {
		found, err := s.logs.SearchLogs(ctx,
			logstore.SearchFilters{SessionID: sessionID, RootsOnly: true},
			logstore.PaginationOptions{Limit: page, Offset: offset, SortBy: "timestamp", Order: "asc", SkipCount: true})
		if err != nil {
			return nil, err
		}
		if found == nil || len(found.Logs) == 0 {
			break
		}
		for i := range found.Logs {
			// A listing carries no content; the full log does.
			full, err := s.logs.FindByID(ctx, found.Logs[i].ID)
			if err != nil || full == nil {
				continue
			}
			logs = append(logs, full)
		}
		if len(found.Logs) < page {
			break
		}
	}
	return logs, nil
}

// AddFromSessions adds one row per session. The whole session goes into a variable
// column named "session", which is created when the dataset does not have one.
func (s *Service) AddFromSessions(ctx context.Context, id string, sessionIDs []string, splitID, actor string) (*CurateResult, error) {
	if s.logs == nil {
		return nil, ErrNoLogStore
	}
	sessionIDs = uniqueIDs(sessionIDs)
	if len(sessionIDs) == 0 {
		return nil, invalid("at least one session ID is required")
	}
	if len(sessionIDs) > MaxSessionsPerRequest {
		return nil, invalid("at most %d sessions can be added in one call", MaxSessionsPerRequest)
	}
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return nil, err
	}
	result := &CurateResult{Skipped: []Skipped{}}
	sessions := make([][]SessionTurn, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		logs, err := s.sessionLogs(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		turns := SessionTurns(logs)
		if len(turns) == 0 {
			reason := "the session has no requests"
			if len(logs) > 0 {
				reason = "the session's requests have no content; content logging was off"
			}
			result.Skipped = append(result.Skipped, Skipped{ID: sessionID, Reason: reason})
			continue
		}
		sessions = append(sessions, turns)
	}
	version, err := s.write(ctx, id, actor, "Added from sessions", func(tx configstore.EvalDatasetTx) error {
		if len(sessions) == 0 {
			return nil
		}
		stored, err := tx.Columns()
		if err != nil {
			return err
		}
		columns := toColumns(stored)
		if !slices.ContainsFunc(columns, func(c Column) bool { return c.Type == ColumnVariable && c.Name == SessionColumnName }) {
			column := Column{Name: SessionColumnName, Type: ColumnVariable}
			if err := ValidateColumns(append(slices.Clone(columns), column)); err != nil {
				return err
			}
			added := &tables.TableEvalDatasetColumn{Name: column.Name, Type: string(column.Type)}
			if err := tx.AddColumn(added); err != nil {
				return err
			}
			column.ID = added.ID
			columns = append(columns, column)
		}
		encoded := make([]string, 0, len(sessions))
		for _, turns := range sessions {
			cells, _ := SessionCells(turns, columns)
			encoded = append(encoded, EncodeCellsMap(columns, cells))
		}
		if _, err := tx.AddRows(encoded, splitID); err != nil {
			return err
		}
		result.RowsAdded = len(encoded)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if version != nil {
		result.Version = new(version.Number)
	}
	return result, nil
}

// ============================================================================
// Versions
// ============================================================================

// Versions lists a dataset's saved versions, newest first.
func (s *Service) Versions(ctx context.Context, id string) ([]tables.TableEvalDatasetVersion, error) {
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return nil, err
	}
	return s.store.ListEvalDatasetVersions(ctx, id)
}

// Version returns one saved version with its columns.
func (s *Service) Version(ctx context.Context, id string, number int) (*tables.TableEvalDatasetVersion, error) {
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return nil, err
	}
	return s.store.GetEvalDatasetVersion(ctx, id, number)
}

// SaveVersion saves the draft as a version. It fails with
// configstore.ErrEvalDatasetNoChanges when the draft has no changes.
func (s *Service) SaveVersion(ctx context.Context, id, name, description, actor string) (*tables.TableEvalDatasetVersion, error) {
	if name != "" {
		if err := ValidateName("version", name); err != nil {
			return nil, err
		}
	}
	var saved *tables.TableEvalDatasetVersion
	err := s.store.WithEvalDataset(ctx, id, func(tx configstore.EvalDatasetTx) error {
		version, err := tx.SaveVersion(configstore.EvalDatasetVersionSave{Name: strings.TrimSpace(name), Description: description, CreatedBy: actor})
		saved = version
		return err
	})
	return saved, err
}

// UpdateVersion changes a version's name and note.
func (s *Service) UpdateVersion(ctx context.Context, id string, number int, name, description string) (*tables.TableEvalDatasetVersion, error) {
	if name != "" {
		if err := ValidateName("version", name); err != nil {
			return nil, err
		}
	}
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return nil, err
	}
	if err := s.store.UpdateEvalDatasetVersion(ctx, id, number, strings.TrimSpace(name), description); err != nil {
		return nil, err
	}
	return s.store.GetEvalDatasetVersion(ctx, id, number)
}

// DeleteVersion deletes a saved version and the row records only it needed.
func (s *Service) DeleteVersion(ctx context.Context, id string, number int) error {
	return s.store.WithEvalDataset(ctx, id, func(tx configstore.EvalDatasetTx) error { return tx.DeleteVersion(number) })
}

// Restore makes the dataset equal to a saved version and saves the result as a new
// version. Edits waiting in the draft are saved first, so nothing is lost and the
// restore can itself be undone.
func (s *Service) Restore(ctx context.Context, id string, number int, actor string) (*tables.TableEvalDatasetVersion, error) {
	var saved *tables.TableEvalDatasetVersion
	err := s.store.WithEvalDataset(ctx, id, func(tx configstore.EvalDatasetTx) error {
		if tx.Dataset().DraftChanges == 0 && tx.Dataset().HeadVersion == number {
			return nil // already at this version
		}
		if err := savePending(tx, actor); err != nil {
			return err
		}
		if err := tx.Restore(number); err != nil {
			return err
		}
		version, err := tx.SaveVersion(configstore.EvalDatasetVersionSave{
			CreatedBy: actor, Description: fmt.Sprintf("Restored from version %d", number), RestoredFrom: new(number), Force: true,
		})
		saved = version
		return err
	})
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return s.store.GetEvalDatasetVersion(ctx, id, number)
	}
	return saved, nil
}

// Kinds of row change in a diff.
const (
	ChangeAdded   = "added"
	ChangeRemoved = "removed"
	ChangeChanged = "changed"
)

// RowChange is one row that differs between two versions.
type RowChange struct {
	RowID  string            `json:"row_id"`
	Seq    int64             `json:"seq"`
	Change string            `json:"change"`
	Before map[string]string `json:"before,omitempty"`
	After  map[string]string `json:"after,omitempty"`
}

// ColumnChange is one column that differs between two versions.
type ColumnChange struct {
	Change string  `json:"change"`
	Before *Column `json:"before,omitempty"`
	After  *Column `json:"after,omitempty"`
}

// Diff is what changed from one version to a later one.
type Diff struct {
	From      int            `json:"from"`
	To        int            `json:"to"`
	Columns   []ColumnChange `json:"columns"`
	Rows      []RowChange    `json:"rows"`
	NextAfter int64          `json:"next_after"`
}

func diffColumns(before, after []Column) []ColumnChange {
	changes := []ColumnChange{}
	for i := range after {
		at := slices.IndexFunc(before, func(c Column) bool { return c.ID == after[i].ID })
		switch {
		case at < 0:
			changes = append(changes, ColumnChange{Change: ChangeAdded, After: &after[i]})
		case before[at] != after[i]:
			changes = append(changes, ColumnChange{Change: ChangeChanged, Before: &before[at], After: &after[i]})
		}
	}
	for i := range before {
		if !slices.ContainsFunc(after, func(c Column) bool { return c.ID == before[i].ID }) {
			changes = append(changes, ColumnChange{Change: ChangeRemoved, Before: &before[i]})
		}
	}
	return changes
}

// Diff compares version `to` with an earlier version. to may be HeadVersion+1, which
// is the draft. against nil means the saved version just before to, or the empty
// dataset when there is none.
func (s *Service) Diff(ctx context.Context, id string, to int, against *int, after int64, limit int) (*Diff, error) {
	dataset, err := s.store.GetEvalDataset(ctx, id)
	if err != nil {
		return nil, err
	}
	draft := dataset.HeadVersion + 1
	if to < 1 || to > draft {
		return nil, configstore.ErrNotFound
	}
	from := 0
	if against != nil {
		from = *against
	} else {
		versions, err := s.store.ListEvalDatasetVersions(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, version := range versions { // newest first
			if version.Number < to {
				from = version.Number
				break
			}
		}
	}
	if from < 0 || from >= to {
		return nil, invalid("a version can only be compared with an earlier one")
	}
	columnsOf := func(number int) ([]Column, error) {
		switch number {
		case 0:
			return nil, nil
		case draft:
			return s.columnsAt(ctx, id, 0)
		default:
			return s.columnsAt(ctx, id, number)
		}
	}
	before, err := columnsOf(from)
	if err != nil {
		return nil, err
	}
	afterColumns, err := columnsOf(to)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultRowPageSize
	}
	limit = min(limit, MaxRowPageSize)
	changes, err := s.store.DiffEvalDatasetVersions(ctx, id, from, to, after, limit+1)
	if err != nil {
		return nil, err
	}
	diff := &Diff{From: from, To: to, Columns: diffColumns(before, afterColumns), Rows: make([]RowChange, 0, len(changes))}
	if len(changes) > limit {
		changes = changes[:limit]
		diff.NextAfter = changes[limit-1].Seq
	}
	for _, change := range changes {
		row := RowChange{RowID: change.RowID, Seq: change.Seq}
		if change.Before != nil {
			if err := decodeRow(change.Before, before); err != nil {
				return nil, err
			}
			row.Before = change.Before.Cells
		}
		if change.After != nil {
			if err := decodeRow(change.After, afterColumns); err != nil {
				return nil, err
			}
			row.After = change.After.Cells
		}
		switch {
		case change.Before == nil:
			row.Change = ChangeAdded
		case change.After == nil:
			row.Change = ChangeRemoved
		case change.Before.CellsJSON == change.After.CellsJSON:
			// Edited and edited back: the two versions read the same cells.
			continue
		default:
			row.Change = ChangeChanged
		}
		diff.Rows = append(diff.Rows, row)
	}
	return diff, nil
}

// ============================================================================
// Splits
// ============================================================================

// SplitInput creates or updates a split.
type SplitInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Splits lists a dataset's splits with their row counts.
func (s *Service) Splits(ctx context.Context, id string) ([]SplitView, error) {
	splits, err := s.store.ListEvalDatasetSplits(ctx, id)
	if err != nil {
		return nil, err
	}
	counts, err := s.store.CountEvalDatasetSplitRows(ctx, id)
	if err != nil {
		return nil, err
	}
	views := make([]SplitView, len(splits))
	for i, split := range splits {
		views[i] = SplitView{TableEvalDatasetSplit: split, RowCount: counts[split.ID]}
	}
	return views, nil
}

// CreateSplit adds a split to a dataset.
func (s *Service) CreateSplit(ctx context.Context, id string, in SplitInput) (*tables.TableEvalDatasetSplit, error) {
	if err := ValidateName("split", in.Name); err != nil {
		return nil, err
	}
	split := &tables.TableEvalDatasetSplit{DatasetID: id, Name: strings.TrimSpace(in.Name), Description: in.Description}
	if err := s.store.CreateEvalDatasetSplit(ctx, split); err != nil {
		return nil, err
	}
	return split, nil
}

// UpdateSplit renames or describes a split.
func (s *Service) UpdateSplit(ctx context.Context, id, splitID string, in SplitInput) (*tables.TableEvalDatasetSplit, error) {
	if err := ValidateName("split", in.Name); err != nil {
		return nil, err
	}
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return nil, err
	}
	split := &tables.TableEvalDatasetSplit{ID: splitID, DatasetID: id, Name: strings.TrimSpace(in.Name), Description: in.Description}
	if err := s.store.UpdateEvalDatasetSplit(ctx, split); err != nil {
		return nil, err
	}
	return split, nil
}

// DeleteSplit removes a split. Its rows stay in the dataset.
func (s *Service) DeleteSplit(ctx context.Context, id, splitID string) error {
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return err
	}
	return s.store.DeleteEvalDatasetSplit(ctx, id, splitID)
}

// SetSplitRows adds rows to a split and removes others, and returns how many joined
// and how many left.
func (s *Service) SetSplitRows(ctx context.Context, id, splitID string, add, remove []string) (int64, int64, error) {
	if len(add) > MaxRowsPerRequest || len(remove) > MaxRowsPerRequest {
		return 0, 0, invalid("at most %d rows can be added to or removed from a split in one call", MaxRowsPerRequest)
	}
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return 0, 0, err
	}
	added, err := s.store.AddEvalDatasetSplitRows(ctx, id, splitID, add)
	if err != nil {
		return 0, 0, err
	}
	removed, err := s.store.RemoveEvalDatasetSplitRows(ctx, id, splitID, remove)
	if err != nil {
		return added, 0, err
	}
	return added, removed, nil
}
