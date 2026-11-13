package datasets

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/evals"
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
	// maxImportFields bounds a mapping's CSV field position, so a bad position cannot size a
	// huge slice.
	maxImportFields = 10000
)

// maxCurateBytes bounds the text of all the logs one AddFromLogs call reads, or of all the
// sessions one AddFromSessions call reads (requests, replies and tool calls), so a call
// holds at most this much before its write.
// A var so tests can lower it.
var maxCurateBytes = 64 << 20

// ErrEvalDatasetChangedDuringCopy reports a dataset whose draft changed while a copy read it.
var ErrEvalDatasetChangedDuringCopy = errors.New("the dataset changed while it was being copied; copy it again")

// ErrEvalDatasetChangedDuringExport reports a draft whose columns, or a split whose rows,
// changed while an export read them.
var ErrEvalDatasetChangedDuringExport = errors.New("the dataset changed while it was being exported; export it again")

// ErrNoLogStore is returned by the curation calls when the gateway has no log store.
var ErrNoLogStore = invalid("adding rows from logs needs a log store, and this gateway has none")

// Store is the part of the config store the dataset service uses.
type Store interface {
	ListEvalDatasets(ctx context.Context, params configstore.EvalDatasetListParams) ([]tables.TableEvalDataset, int64, error)
	GetEvalDatasetCounts(ctx context.Context, datasetIDs []string) (map[string]configstore.EvalDatasetCounts, error)
	GetEvalDataset(ctx context.Context, id string) (*tables.TableEvalDataset, error)
	CreateEvalDataset(ctx context.Context, dataset *tables.TableEvalDataset, columns []tables.TableEvalDatasetColumn) error
	UpdateEvalDataset(ctx context.Context, dataset *tables.TableEvalDataset) error
	DeleteEvalDataset(ctx context.Context, id string) ([]string, error)
	ListEvalDatasetColumns(ctx context.Context, datasetID string) ([]tables.TableEvalDatasetColumn, error)
	ListEvalDatasetRows(ctx context.Context, params configstore.EvalDatasetRowListParams) ([]tables.TableEvalDatasetRow, error)
	CountEvalDatasetRows(ctx context.Context, params configstore.EvalDatasetRowListParams) (int64, error)
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
	SetEvalDatasetSplitRows(ctx context.Context, datasetID, splitID string, add, remove []string) (int64, int64, error)
	CreateEvalDatasetFile(ctx context.Context, file *tables.TableEvalDatasetFile) error
	GetEvalDatasetFile(ctx context.Context, datasetID, fileID string) (*tables.TableEvalDatasetFile, error)
	ListEvalDatasetFileKeys(ctx context.Context, datasetID string) ([]string, error)
	ListEvalDatasetFiles(ctx context.Context, datasetID string) ([]tables.TableEvalDatasetFile, error)
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

// strictLogReader is a log store that can fail a read whose content could not be loaded,
// instead of returning the row without it. The hybrid log store implements it: its
// FindByID degrades to the metadata-only row when the object read fails, which a
// curation call would take for a log stored without content.
type strictLogReader interface {
	FindByIDStrict(ctx context.Context, id string) (*logstore.Log, error)
}

// readLog reads one log with its content, strictly when the log store can.
func (s *Service) readLog(ctx context.Context, id string) (*logstore.Log, error) {
	if strict, ok := s.logs.(strictLogReader); ok {
		return strict.FindByIDStrict(ctx, id)
	}
	return s.logs.FindByID(ctx, id)
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
	store  Store
	logs   LogReader
	files  FileStore
	spans  SpanReader
	logger schemas.Logger
}

// cleanupTimeout bounds the compensating writes that run after a request failed or was
// cancelled.
const cleanupTimeout = 10 * time.Second

// SetLogger gives the service a logger for failures it cannot return, such as an object that
// could not be removed after the request already finished.
func (s *Service) SetLogger(logger schemas.Logger) { s.logger = logger }

// cleanupContext is a context for compensating work: it keeps ctx's values but not its
// cancellation, so a client that goes away does not leave half-made state behind.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

func (s *Service) warn(format string, args ...any) {
	if s.logger != nil {
		s.logger.Warn(format, args...)
	}
}

// SetFileStore gives the service a place to keep uploaded files. Without one, files
// are kept in the config database, up to MaxInlineFileBytes each.
func (s *Service) SetFileStore(files FileStore) { s.files = files }

// NewService returns a dataset service. logs may be nil, in which case rows cannot be
// added from logs or sessions.
// rowDatasetFinder is the store lookup of the dataset a row belongs to. A store without it cannot
// answer RowDataset.
type rowDatasetFinder interface {
	FindEvalDatasetIDForRow(ctx context.Context, rowID string) (string, error)
}

// RowDataset returns the id of the dataset whose draft holds a row with this id, or "" when there is
// none or the store cannot look a row up on its own.
func (s *Service) RowDataset(ctx context.Context, rowID string) (string, error) {
	finder, ok := s.store.(rowDatasetFinder)
	if !ok || rowID == "" {
		return "", nil
	}
	id, err := finder.FindEvalDatasetIDForRow(ctx, rowID)
	if errors.Is(err, configstore.ErrNotFound) {
		return "", nil
	}
	return id, err
}

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
	if err := evals.ValidateTags(in.Tags); err != nil {
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
	rows, err := s.store.CountEvalDatasetRows(ctx, configstore.EvalDatasetRowListParams{DatasetID: id})
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
	if err := evals.ValidateTags(in.Tags); err != nil {
		return nil, err
	}
	if err := validateMaxVersions(in.MaxVersions); err != nil {
		return nil, err
	}
	// One transaction: a prune the new cap needs that fails rolls the cap back with it.
	err := s.store.WithEvalDataset(ctx, id, func(tx configstore.EvalDatasetTx) error {
		if err := tx.UpdateMetadata(strings.TrimSpace(in.Name), in.Description, in.Tags, in.MaxVersions); err != nil {
			return err
		}
		if in.MaxVersions == nil {
			return nil
		}
		return tx.PruneVersions()
	})
	if err != nil {
		return nil, err
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
	// The store checks for a cycle in the move's own transaction, under the hierarchy lock.
	if err := s.store.SetEvalDatasetFolderParent(ctx, id, parentID); err != nil {
		if errors.Is(err, configstore.ErrEvalDatasetFolderCycle) {
			return nil, invalid("%s", err.Error())
		}
		return nil, err
	}
	folders, err := s.store.ListEvalDatasetFolders(ctx)
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
	// The keys are read by the delete itself, so a file uploaded just before it is included.
	keys, err := s.store.DeleteEvalDataset(ctx, id)
	if err != nil {
		return err
	}
	// The dataset is gone either way. Objects that fail to delete are only orphaned; the log
	// names them so an operator can remove them.
	// The keys are gone with the records, so the cleanup runs on a context a cancelled
	// request does not reach.
	if s.files != nil && len(keys) > 0 {
		cleanupCtx, cancel := cleanupContext(ctx)
		defer cancel()
		if err := s.files.DeleteBatch(cleanupCtx, keys); err != nil {
			s.warn("evals datasets: failed to delete the files of deleted dataset %s, objects left behind: %v: %v", id, keys, err)
		}
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
			// On a context the request's cancellation does not reach: a client that went away is
			// the common reason the record failed, and the object would be orphaned otherwise.
			cleanupCtx, cancel := cleanupContext(ctx)
			if cleanupErr := s.files.DeleteBatch(cleanupCtx, []string{file.ObjectKey}); cleanupErr != nil {
				s.warn("evals datasets: failed to remove object %s after its file record failed: %v", file.ObjectKey, cleanupErr)
			}
			cancel()
		}
		return nil, err
	}
	return &FileRef{ID: file.ID, Name: file.Name, ContentType: file.ContentType, Size: file.Size, URL: fileURL(datasetID, file.ID)}, nil
}

// File returns an uploaded file's record and its bytes.
func (s *Service) File(ctx context.Context, datasetID, fileID string) (*tables.TableEvalDatasetFile, []byte, error) {
	// The file record has no read scope of its own; the dataset's applies.
	if _, err := s.store.GetEvalDataset(ctx, datasetID); err != nil {
		return nil, nil, err
	}
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
// copy starts at version 1 and carries no history. A failure after the copy is created
// removes it.
func (s *Service) Duplicate(ctx context.Context, id, name, actor string) (view *DatasetView, err error) {
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
	defer func() {
		if err == nil {
			return
		}
		// On a context the request's cancellation does not reach, so a cancelled copy is removed too.
		cleanupCtx, cancel := cleanupContext(ctx)
		defer cancel()
		keys, cleanupErr := s.store.DeleteEvalDataset(cleanupCtx, dataset.ID)
		if cleanupErr != nil {
			s.warn("evals datasets: failed to remove the partial copy %s of dataset %s: %v", dataset.ID, id, cleanupErr)
			return
		}
		if s.files != nil && len(keys) > 0 {
			if cleanupErr := s.files.DeleteBatch(cleanupCtx, keys); cleanupErr != nil {
				s.warn("evals datasets: failed to delete the files of the partial copy %s, objects left behind: %v: %v", dataset.ID, keys, cleanupErr)
			}
		}
	}()
	// A copy owns its files, so deleting the source leaves the copy whole: each source file
	// gets a record of its own, and the copy's file cells are rewritten to name it.
	fileRefs, err := s.copyFiles(ctx, id, dataset.ID, actor)
	if err != nil {
		return nil, err
	}
	// Cells are keyed by column ID, and the copy's columns have new IDs.
	sourceIDs := make([]string, len(source.Columns))
	copyIDs := make([]string, len(source.Columns))
	fileColumn := make([]bool, len(source.Columns))
	for i := range source.Columns {
		sourceIDs[i] = source.Columns[i].ID
		copyIDs[i] = stored[i].ID
		fileColumn[i] = ColumnType(source.Columns[i].Type) == ColumnFile
	}
	if fileRefs == nil {
		fileRefs = map[string]FileRef{}
	}
	// A file uploaded and referenced after the listing above is copied when a cell names
	// it. A file the source no longer has is already broken in the source, and its cell is
	// kept as stored.
	gone := map[string]struct{}{}
	copyMissing := func(fileID string) (FileRef, bool, error) {
		if _, known := gone[fileID]; known {
			return FileRef{}, false, nil
		}
		record, err := s.store.GetEvalDatasetFile(ctx, id, fileID)
		if errors.Is(err, configstore.ErrNotFound) {
			gone[fileID] = struct{}{}
			return FileRef{}, false, nil
		}
		if err != nil {
			return FileRef{}, false, err
		}
		ref, err := s.copyFile(ctx, id, dataset.ID, actor, record)
		if err != nil {
			return FileRef{}, false, err
		}
		fileRefs[fileID] = ref
		return ref, true, nil
	}
	// The schema was read before the rows, which are read a page per call, so a column
	// edit in between would put cells of a new shape under the old schema. The source's
	// (HeadVersion, DraftChanges) pair fences the copy: every draft write bumps
	// DraftChanges, every save bumps HeadVersion, and the head cannot be deleted, so the
	// pair never repeats. It is checked once the rows and splits are copied, outside any
	// transaction of the copy, so no pool connection is taken while one is held; a change
	// fails the copy and the deferred cleanup removes it, version 1 included. Split membership is not
	// versioned and does not move the pair; each membership write moves its split's
	// UpdatedAt instead, which is checked once the splits are copied.
	unchanged := func() error {
		current, err := s.store.GetEvalDataset(ctx, id)
		if err != nil {
			return err
		}
		if current.HeadVersion != source.HeadVersion || current.DraftChanges != source.DraftChanges {
			return ErrEvalDatasetChangedDuringCopy
		}
		return nil
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
					if fileColumn[j] && values[j] != "" {
						if values[j], err = rewriteFileCell(values[j], fileRefs, copyMissing); err != nil {
							return err
						}
					}
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
	// A member replaced between two pages of a split would give the copy a membership the
	// source never had, so a split edited during the copy fails it, as a draft edit does.
	if len(source.Splits) > 0 {
		current, err := s.store.ListEvalDatasetSplits(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, split := range source.Splits {
			i := slices.IndexFunc(current, func(c tables.TableEvalDatasetSplit) bool { return c.ID == split.ID })
			if i < 0 || !current[i].UpdatedAt.Equal(split.UpdatedAt) {
				return nil, ErrEvalDatasetChangedDuringCopy
			}
		}
	}
	if err := unchanged(); err != nil {
		return nil, err
	}
	return s.Get(ctx, dataset.ID)
}

// copyFiles gives the dataset copyID a record of its own for each file of sourceID, with
// its own object when the source file is in the object store, and returns the copy's
// reference for each source file ID. Objects are stored before their records, as an upload
// does; a record that fails removes its object, and the caller's cleanup removes the rest.
func (s *Service) copyFiles(ctx context.Context, sourceID, copyID, actor string) (map[string]FileRef, error) {
	records, err := s.store.ListEvalDatasetFiles(ctx, sourceID)
	if err != nil || len(records) == 0 {
		return nil, err
	}
	refs := make(map[string]FileRef, len(records))
	for i := range records {
		ref, err := s.copyFile(ctx, sourceID, copyID, actor, &records[i])
		if err != nil {
			return nil, err
		}
		refs[records[i].ID] = ref
	}
	return refs, nil
}

// copyFile gives the dataset copyID a record of its own for one file of sourceID, and
// returns the copy's reference. See copyFiles.
func (s *Service) copyFile(ctx context.Context, sourceID, copyID, actor string, source *tables.TableEvalDatasetFile) (FileRef, error) {
	file := &tables.TableEvalDatasetFile{
		ID: uuid.NewString(), DatasetID: copyID, Name: source.Name, ContentType: source.ContentType, Size: source.Size, CreatedBy: actor,
	}
	if source.ObjectKey == "" {
		file.Data = source.Data
		if file.Data == nil {
			full, err := s.store.GetEvalDatasetFile(ctx, sourceID, source.ID)
			if err != nil {
				return FileRef{}, err
			}
			file.Data = full.Data
		}
	} else {
		if s.files == nil {
			return FileRef{}, errors.New("the dataset has files in the object store, and this gateway has none configured")
		}
		data, err := s.files.Get(ctx, source.ObjectKey)
		if err != nil {
			return FileRef{}, fmt.Errorf("failed to read file %s: %w", source.ID, err)
		}
		file.ObjectKey = "evals/datasets/" + copyID + "/files/" + file.ID
		if err := s.files.Put(ctx, file.ObjectKey, data, map[string]string{"dataset_id": copyID}); err != nil {
			return FileRef{}, fmt.Errorf("failed to store the file: %w", err)
		}
	}
	if err := s.store.CreateEvalDatasetFile(ctx, file); err != nil {
		if file.ObjectKey != "" {
			cleanupCtx, cancel := cleanupContext(ctx)
			if cleanupErr := s.files.DeleteBatch(cleanupCtx, []string{file.ObjectKey}); cleanupErr != nil {
				s.warn("evals datasets: failed to remove object %s after its file record failed: %v", file.ObjectKey, cleanupErr)
			}
			cancel()
		}
		return FileRef{}, err
	}
	return FileRef{ID: file.ID, URL: fileURL(copyID, file.ID)}, nil
}

// ============================================================================
// Writes and the save rules
// ============================================================================

// write runs one small edit to a dataset and applies the save rule around it, in one
// transaction: a version is saved once the draft holds AutoSaveThreshold changes.
//
// It returns the version the change was saved in, or nil when it stayed in the draft.
func (s *Service) write(ctx context.Context, id, actor string, fn func(tx configstore.EvalDatasetTx) error) (*tables.TableEvalDatasetVersion, error) {
	var saved *tables.TableEvalDatasetVersion
	err := s.store.WithEvalDataset(ctx, id, func(tx configstore.EvalDatasetTx) error {
		if err := fn(tx); err != nil {
			return err
		}
		changes := tx.Dataset().DraftChanges
		if changes < AutoSaveThreshold {
			return nil
		}
		version, err := tx.SaveVersion(configstore.EvalDatasetVersionSave{CreatedBy: actor, Description: fmt.Sprintf("Saved automatically after %d edits", changes)})
		saved = version
		return err
	})
	return saved, err
}

// writeBulk runs one bulk change (import, curation) in one transaction, so it stands alone in
// the history: the small edits waiting in the draft are saved first as their own version, the
// change is applied, and it is saved as a version noted with note.
//
// plan reads what it needs in the transaction and returns the writes to make. A nil apply means
// the change has nothing to write, and then nothing is saved: the waiting edits stay in the
// draft. It returns the version the change was saved in, or nil when nothing was written.
func (s *Service) writeBulk(ctx context.Context, id, actor, note string, plan func(tx configstore.EvalDatasetTx) (apply func() error, err error)) (*tables.TableEvalDatasetVersion, error) {
	var saved *tables.TableEvalDatasetVersion
	err := s.store.WithEvalDataset(ctx, id, func(tx configstore.EvalDatasetTx) error {
		apply, err := plan(tx)
		if err != nil || apply == nil {
			return err
		}
		if err := savePending(tx, actor); err != nil {
			return err
		}
		if err := apply(); err != nil {
			return err
		}
		if tx.Dataset().DraftChanges == 0 {
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
	return savePendingWith(tx, actor, false)
}

// savePendingWith is savePending with control over the version cap. A restore skips it
// so the version it restores is still there; its own save prunes.
func savePendingWith(tx configstore.EvalDatasetTx, actor string, skipPrune bool) error {
	if tx.Dataset().DraftChanges == 0 {
		return nil
	}
	_, err := tx.SaveVersion(configstore.EvalDatasetVersionSave{
		CreatedBy: actor, Description: "Saved automatically before a bulk change", SkipPrune: skipPrune,
	})
	return err
}

// ============================================================================
// Columns
// ============================================================================

// AddColumn adds a column to the draft.
func (s *Service) AddColumn(ctx context.Context, id string, in ColumnSpec, actor string) (*tables.TableEvalDatasetColumn, error) {
	column := &tables.TableEvalDatasetColumn{Name: strings.TrimSpace(in.Name), Type: string(in.Type)}
	_, err := s.write(ctx, id, actor, func(tx configstore.EvalDatasetTx) error {
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
	_, err := s.write(ctx, id, actor, func(tx configstore.EvalDatasetTx) error {
		existing, err := tx.Columns()
		if err != nil {
			return err
		}
		columns := toColumns(existing)
		at := slices.IndexFunc(columns, func(c Column) bool { return c.ID == columnID })
		if at < 0 {
			return configstore.ErrNotFound
		}
		typeChanged := columns[at].Type != in.Type
		columns[at].Name, columns[at].Type = column.Name, in.Type
		if err := ValidateColumns(columns); err != nil {
			return err
		}
		// A structured type takes JSON of its own shape: every cell already in the column has
		// to fit it, or the change is refused and the column keeps its type. A cell the new
		// type stores differently (a URL list in a file column) is rewritten in the draft as the
		// scan reaches it, so the request holds one page of rows; saved versions keep the old
		// value. The scan reads each page before calling back, and a rewrite keeps the row's
		// sequence, so a write neither disturbs the page nor brings the row back. A later cell
		// that does not fit rolls the earlier rewrites back with the transaction.
		if typeChanged && in.Type.Structured() {
			if err := tx.ScanDraftCells(func(rowID string, cells map[string]string) error {
				value := cells[columnID]
				if value == "" {
					return nil
				}
				stored, err := NormalizeCell(columns[at], value)
				if err != nil {
					return invalid("column %q has a value in row %s that is not valid for type %s: %v", column.Name, rowID, in.Type, err)
				}
				if stored == value {
					return nil
				}
				if stored == "" {
					delete(cells, columnID)
				} else {
					cells[columnID] = stored
				}
				cellsJSON, err := providerUtils.MarshalSorted(cells)
				if err != nil {
					return err
				}
				_, err = tx.SetRowCells(rowID, string(cellsJSON))
				return err
			}); err != nil {
				return err
			}
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
	_, err := s.write(ctx, id, actor, func(tx configstore.EvalDatasetTx) error { return tx.DeleteColumn(columnID) })
	return err
}

// ReorderColumns sets the display order. columnIDs must name every column once.
func (s *Service) ReorderColumns(ctx context.Context, id string, columnIDs []string, actor string) error {
	_, err := s.write(ctx, id, actor, func(tx configstore.EvalDatasetTx) error {
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
	Offset  int // skips rows by position; not with After
	Limit   int
	// SkipTotal leaves Total at 0 and reads no count, for a reader that only walks the rows.
	SkipTotal bool
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
	// A saved version has no read scope of its own; the dataset's applies to both branches.
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return nil, err
	}
	if version > 0 {
		saved, err := s.store.GetEvalDatasetVersion(ctx, id, version)
		if err != nil {
			return nil, err
		}
		return versionColumns(saved), nil
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

// Rows returns a page of a dataset's rows, in order. query.Offset skips that many rows,
// to read a page by row position; it cannot be combined with query.After.
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
	searchColumns := make([]string, len(columns))
	for i, column := range columns {
		searchColumns[i] = column.ID
	}
	rows, err := s.store.ListEvalDatasetRows(ctx, configstore.EvalDatasetRowListParams{
		DatasetID: id, Version: query.Version, SplitID: query.SplitID, Search: query.Search, SearchColumns: searchColumns,
		AfterSeq: query.After, Offset: query.Offset, Limit: limit + 1,
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
	if query.SkipTotal {
		return page, nil
	}
	if page.Total, err = s.store.CountEvalDatasetRows(ctx, configstore.EvalDatasetRowListParams{
		DatasetID: id, Version: query.Version, SplitID: query.SplitID, Search: query.Search, SearchColumns: searchColumns,
	}); err != nil {
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
		// A row may name one column by ID and by name; two different values for it is ambiguous.
		if previous, set := cells[columns[at].ID]; set && previous != stored {
			return nil, invalid("column %q is given two different values", columns[at].Name)
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
	version, err := s.write(ctx, id, actor, func(tx configstore.EvalDatasetTx) error {
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
		// An empty cell is not stored, so the answer leaves it out too.
		maps.DeleteFunc(cells, func(_, v string) bool { return v == "" })
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
	version, err := s.write(ctx, id, actor, func(tx configstore.EvalDatasetTx) error {
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
	version, err := s.write(ctx, id, actor, func(tx configstore.EvalDatasetTx) error {
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
	version, err := s.write(ctx, id, actor, func(tx configstore.EvalDatasetTx) error {
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
	HasHeader     bool
	Mapping       []ImportColumn // where each CSV field goes; empty matches the header by name
	CreateColumns bool           // header fields with no column become variable columns; only without Mapping
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
// when the UI sends only the first bytes of a file, so a broken last row is ignored:
// one the parser rejects, and one that ends the input inside an open quoted field.
func (s *Service) PreviewImport(ctx context.Context, id string, r io.Reader) (*ImportPreview, error) {
	columns, err := s.columnsAt(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	reader, quotes := newTrackedCSVReader(r, true)
	header, err := reader.Read()
	if err != nil {
		return nil, invalid("the file has no readable CSV header")
	}
	preview := &ImportPreview{Header: slices.Clone(header), Rows: make([][]string, 0, previewRows)}
	atEOF := false
	for len(preview.Rows) <= previewRows {
		record, err := reader.Read()
		if err != nil {
			atEOF = errors.Is(err, io.EOF)
			break
		}
		if len(preview.Rows) == previewRows {
			break // a row follows the last one shown, so that one is whole
		}
		preview.Rows = append(preview.Rows, slices.Clone(record))
	}
	// With lazy quotes the parser returns a field cut inside its quotes as if it were closed,
	// so an input that ends inside one has its last row cut.
	if atEOF && quotes.insideQuotes() && len(preview.Rows) > 0 {
		preview.Rows = preview.Rows[:len(preview.Rows)-1]
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
		seenIndex := make(map[int]struct{}, len(opts.Mapping))
		for _, entry := range opts.Mapping {
			if entry.Index < 0 {
				return nil, nil, nil, invalid("a mapping index cannot be negative")
			}
			if entry.Index >= maxImportFields || (header != nil && entry.Index >= len(header)) {
				return nil, nil, nil, invalid("CSV field %d is past the last field of the file", entry.Index)
			}
			if _, dup := seenIndex[entry.Index]; dup {
				return nil, nil, nil, invalid("CSV field %d is mapped twice", entry.Index)
			}
			seenIndex[entry.Index] = struct{}{}
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
				// A header a CSV export escaped names the column without the quote.
				name := strings.TrimSpace(unescapeCSVCell(strings.TrimSpace(header[index])))
				create = append(create, newColumn{index, Column{Name: name, Type: ColumnVariable}})
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
// it and nothing is added. opts.Mapping says where each CSV field goes; when it is empty,
// the header is matched to the dataset's columns by name, and opts.CreateColumns turns
// header fields that match no column into variable columns.
func (s *Service) Import(ctx context.Context, id string, r io.Reader, opts ImportOptions) (*ImportResult, error) {
	result := &ImportResult{}
	version, err := s.writeBulk(ctx, id, opts.Actor, "Imported from CSV", func(tx configstore.EvalDatasetTx) (func() error, error) {
		return func() error { return importRows(tx, r, opts, result) }, nil
	})
	if err != nil {
		return nil, err
	}
	if version != nil {
		result.Version = new(version.Number)
	}
	return result, nil
}

// importRows reads a CSV into the dataset inside an import's transaction.
func importRows(tx configstore.EvalDatasetTx, r io.Reader, opts ImportOptions, result *ImportResult) error {
	reader, quotes := newTrackedCSVReader(r, true)
	// openLine is the line the last record's last field starts on: a file that ends inside
	// quotes ends in that field, so it is the line the error names.
	openLine := 0
	var header []string
	if opts.HasHeader {
		record, err := reader.Read()
		if err != nil {
			return invalid("the file has no readable CSV header")
		}
		openLine, _ = reader.FieldPos(len(record) - 1)
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
			// With lazy quotes the parser closes a field cut inside its quotes, so the last
			// row would be imported cut. The transaction rolls back the batches already written.
			if quotes.insideQuotes() {
				return invalid("line %d: the CSV ends inside a quoted field", openLine)
			}
			break
		}
		if err != nil {
			var parseErr *csv.ParseError
			if errors.As(err, &parseErr) {
				return invalid("the CSV is not valid at line %d: %v", parseErr.Line, parseErr.Err)
			}
			return err
		}
		openLine, _ = reader.FieldPos(len(record) - 1)
		// A record counts only through its mapped fields: one whose values all sit in
		// unmapped fields, or normalize to empty cells (an empty file list), would be an
		// empty row, so it is skipped like a blank line.
		blank := true
		for i, value := range record {
			if value == "" || i >= len(targets) || targets[i] == "" {
				continue
			}
			stored, err := NormalizeCell(byID[targets[i]], value)
			if err != nil {
				line, _ := reader.FieldPos(i)
				return invalid("line %d: %v", line, err)
			}
			record[i] = stored
			if stored != "" {
				blank = false
			}
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
}

// Export streams a dataset, or one split of it, as CSV or JSONL. Rows are read page
// by page and written straight to out.
func (s *Service) Export(ctx context.Context, id string, out io.Writer, format string, version int, splitID string) error {
	columns, err := s.columnsAt(ctx, id, version)
	if err != nil {
		return err
	}
	// Split membership is not versioned, and each split membership write (SetSplitRows,
	// and the store's add and remove membership writes) moves its split's UpdatedAt, which
	// the end-of-stream check compares, as Duplicate does. Rows added to the split by
	// AddRows, or deleted by DeleteRows, are draft row writes: a draft export shows them as
	// each page read them, with or without a split.
	var splitUpdatedAt time.Time
	if splitID != "" {
		if splitUpdatedAt, err = s.splitUpdatedAt(ctx, id, splitID); err != nil {
			return err
		}
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
	// A dataset deleted, or a version pruned, after the first page reads as an empty page,
	// so the source is read again before the file is passed off as complete. A version
	// number is never reused, so a pruned version stays gone.
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return err
	}
	if version > 0 {
		if _, err := s.store.GetEvalDatasetVersion(ctx, id, version); err != nil {
			return err
		}
	} else {
		// The draft's columns were read once, before the rows, so a column change between
		// pages would leave cells of a new shape under the old type in the file. Row edits
		// do not fail the export; they show as the page read saw them.
		stored, err := s.store.ListEvalDatasetColumns(ctx, id)
		if err != nil {
			return err
		}
		if !slices.Equal(toColumns(stored), columns) {
			return ErrEvalDatasetChangedDuringExport
		}
	}
	if splitID != "" {
		current, err := s.splitUpdatedAt(ctx, id, splitID)
		if err != nil {
			return err
		}
		if !current.Equal(splitUpdatedAt) {
			return ErrEvalDatasetChangedDuringExport
		}
	}
	return writer.Flush()
}

// splitUpdatedAt returns when the split's membership or settings last changed, or
// ErrNotFound when the dataset has no such split.
func (s *Service) splitUpdatedAt(ctx context.Context, id, splitID string) (time.Time, error) {
	splits, err := s.store.ListEvalDatasetSplits(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	i := slices.IndexFunc(splits, func(split tables.TableEvalDatasetSplit) bool { return split.ID == splitID })
	if i < 0 {
		return time.Time{}, configstore.ErrNotFound
	}
	return splits[i].UpdatedAt, nil
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
// missing, have no logged content, or whose content could not be read are skipped and
// reported.
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
	// Each full log is turned into its exchange as it is read and then dropped, so one log
	// is held at a time. The exchanges' text counts against maxCurateBytes, as sessions do;
	// past it the call fails as invalid, before anything is written.
	type loggedExchange struct {
		id string
		ex exchange
	}
	exchanges := make([]loggedExchange, 0, len(logIDs))
	used := 0
	for _, logID := range logIDs {
		log, err := s.readLog(ctx, logID)
		if errors.Is(err, logstore.ErrPayloadUnreadable) {
			s.warn("evals datasets: skipped log %s: %v", logID, err)
			result.Skipped = append(result.Skipped, Skipped{ID: logID, Reason: "the log's content could not be read"})
			continue
		}
		if err != nil && !errors.Is(err, logstore.ErrNotFound) {
			// A store that fails is not a missing log; nothing is added.
			return nil, fmt.Errorf("failed to read log %s: %w", logID, err)
		}
		if log == nil {
			result.Skipped = append(result.Skipped, Skipped{ID: logID, Reason: "log not found"})
			continue
		}
		ex := readExchange(log)
		used += ex.textBytes()
		if used > maxCurateBytes {
			return nil, invalid("the logs hold more than %d MiB of text; add fewer logs per call", maxCurateBytes>>20)
		}
		exchanges = append(exchanges, loggedExchange{id: log.ID, ex: ex})
	}
	version, err := s.writeBulk(ctx, id, actor, "Added from logs", func(tx configstore.EvalDatasetTx) (func() error, error) {
		stored, err := tx.Columns()
		if err != nil {
			return nil, err
		}
		columns := toColumns(stored)
		encoded := make([]string, 0, len(exchanges))
		for _, logged := range exchanges {
			cells, skip := exchangeCells(logged.ex, columns)
			if skip != "" {
				result.Skipped = append(result.Skipped, Skipped{ID: logged.id, Reason: skip})
				continue
			}
			if len(cells) == 0 {
				result.Skipped = append(result.Skipped, Skipped{ID: logged.id, Reason: "the dataset has no input, output, conversation history or expected tool calls column to fill"})
				continue
			}
			encoded = append(encoded, EncodeCellsMap(columns, cells))
		}
		if len(encoded) == 0 {
			return nil, nil
		}
		return func() error {
			if _, err := tx.AddRows(encoded, splitID); err != nil {
				return err
			}
			result.RowsAdded = len(encoded)
			return nil
		}, nil
	})
	if err != nil {
		return nil, err
	}
	if version != nil {
		result.Version = new(version.Number)
	}
	return result, nil
}

// errSessionTooLong reports a session with more than maxSessionLogs requests.
var errSessionTooLong = fmt.Errorf("the session has more than %d requests", maxSessionLogs)

// errSessionUnreadable reports a session one of whose requests failed to read.
var errSessionUnreadable = errors.New("a request of the session could not be read")

// sessionLogs reads the requests of one session, oldest first, with their content, and
// returns its turns and how many requests it read. Only root requests are read, so a
// retry or a fallback is not counted as a turn. A session with more than maxSessionLogs
// requests fails with errSessionTooLong rather than becoming a row cut short.
//
// Each full log is turned into its turn as it is read and then dropped, so one log is
// held at a time. used counts the turn text read so far across the call; past
// maxCurateBytes the read fails as invalid, before anything is written.
//
// The listing pages newest first on the log store's keyset cursor: each page starts
// strictly after the (timestamp, ID) of the last row of the page before. A deletion
// anywhere in the session never shifts a page, and a request that arrives during the read
// sorts above the first page and is not read. So the IDs read are the session as it stood
// at the first page, minus the requests deleted since; no ID is read twice.
func (s *Service) sessionLogs(ctx context.Context, sessionID string, used *int) ([]SessionTurn, int, error) {
	const page = 100
	ids := make([]string, 0, page)
	pagination := logstore.PaginationOptions{Limit: page, SortBy: "timestamp", Order: "desc", SkipCount: true}
	for {
		found, err := s.logs.SearchLogs(ctx, logstore.SearchFilters{SessionID: sessionID, RootsOnly: true}, pagination)
		if err != nil {
			return nil, 0, err
		}
		if found == nil || len(found.Logs) == 0 {
			break
		}
		for i := range found.Logs {
			ids = append(ids, found.Logs[i].ID)
		}
		// One request past the limit tells a session that is too long from one that fits.
		if len(ids) > maxSessionLogs {
			return nil, 0, errSessionTooLong
		}
		if len(found.Logs) < page {
			break
		}
		last := found.Logs[len(found.Logs)-1]
		pagination.AfterTimestamp = new(last.Timestamp)
		pagination.AfterID = last.ID
	}
	slices.Reverse(ids)
	turns := make([]SessionTurn, 0, len(ids))
	read := 0
	for _, logID := range ids {
		// A listing carries no content; the full log does.
		full, err := s.readLog(ctx, logID)
		if err != nil && !errors.Is(err, logstore.ErrNotFound) {
			// A turn that fails to read, its content included, is not a missing turn; the
			// session would be saved short.
			return nil, 0, fmt.Errorf("%w: %w", errSessionUnreadable, err)
		}
		if full == nil {
			continue // removed since the listing
		}
		read++
		var added bool
		if turns, added = appendSessionTurn(turns, full); !added {
			continue
		}
		*used += turns[len(turns)-1].textBytes()
		if *used > maxCurateBytes {
			return nil, 0, invalid("the sessions hold more than %d MiB of text; add fewer sessions per call", maxCurateBytes>>20)
		}
	}
	return turns, read, nil
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
	used := 0
	for _, sessionID := range sessionIDs {
		turns, read, err := s.sessionLogs(ctx, sessionID, &used)
		if errors.Is(err, errSessionTooLong) {
			result.Skipped = append(result.Skipped, Skipped{ID: sessionID, Reason: err.Error()})
			continue
		}
		if errors.Is(err, errSessionUnreadable) {
			s.warn("evals datasets: skipped session %s: %v", sessionID, err)
			result.Skipped = append(result.Skipped, Skipped{ID: sessionID, Reason: errSessionUnreadable.Error()})
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(turns) == 0 {
			reason := "the session has no requests"
			if read > 0 {
				reason = "the session's requests have no content; content logging was off"
			}
			result.Skipped = append(result.Skipped, Skipped{ID: sessionID, Reason: reason})
			continue
		}
		sessions = append(sessions, turns)
	}
	version, err := s.writeBulk(ctx, id, actor, "Added from sessions", func(tx configstore.EvalDatasetTx) (func() error, error) {
		if len(sessions) == 0 {
			return nil, nil
		}
		stored, err := tx.Columns()
		if err != nil {
			return nil, err
		}
		columns := toColumns(stored)
		var create *Column
		if !slices.ContainsFunc(columns, func(c Column) bool { return c.Type == ColumnVariable && c.Name == SessionColumnName }) {
			create = &Column{Name: SessionColumnName, Type: ColumnVariable}
			if err := ValidateColumns(append(slices.Clone(columns), *create)); err != nil {
				return nil, err
			}
		}
		return func() error {
			if create != nil {
				added := &tables.TableEvalDatasetColumn{Name: create.Name, Type: string(create.Type)}
				if err := tx.AddColumn(added); err != nil {
					return err
				}
				create.ID = added.ID
				columns = append(columns, *create)
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
		}, nil
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
// version. Edits waiting in the draft are saved first, as their own version. Under a
// version cap the restore's save prunes the oldest versions, which can remove the ones
// it would take to undo it. Restoring the head with no draft changes saves nothing.
func (s *Service) Restore(ctx context.Context, id string, number int, actor string) (*tables.TableEvalDatasetVersion, error) {
	var saved *tables.TableEvalDatasetVersion
	err := s.store.WithEvalDataset(ctx, id, func(tx configstore.EvalDatasetTx) error {
		// Checked before the waiting edits are saved, which would create the version.
		if number < 1 || number > tx.Dataset().HeadVersion {
			return configstore.ErrNotFound
		}
		if tx.Dataset().DraftChanges == 0 && tx.Dataset().HeadVersion == number {
			return nil // already at this version
		}
		if err := savePendingWith(tx, actor, true); err != nil {
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

// DiffDraft names the unsaved draft in Diff. Version numbers start at 1, so 0 is free.
const DiffDraft = 0

// Diff compares version `to` with an earlier version. to may be HeadVersion+1, which
// is the draft, or DiffDraft, which names the draft as of this call's own read of the
// dataset. against nil means the saved version just before to, or the empty dataset
// when there is none.
func (s *Service) Diff(ctx context.Context, id string, to int, against *int, after int64, limit int) (*Diff, error) {
	dataset, err := s.store.GetEvalDataset(ctx, id)
	if err != nil {
		return nil, err
	}
	draft := dataset.HeadVersion + 1
	if to == DiffDraft {
		to = draft
	}
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
	if to == draft {
		// The draft may have been saved as version to after the head read above, and the
		// new draft's columns changed, while the rows are read as version to. A saved
		// version is immutable, so its own columns are read instead.
		current, err := s.store.GetEvalDataset(ctx, id)
		if err != nil {
			return nil, err
		}
		if current.HeadVersion >= to {
			if afterColumns, err = s.columnsAt(ctx, id, to); err != nil {
				return nil, err
			}
		}
	}
	if limit <= 0 {
		limit = DefaultRowPageSize
	}
	limit = min(limit, MaxRowPageSize)
	diff := &Diff{From: from, To: to, Columns: diffColumns(before, afterColumns), Rows: make([]RowChange, 0, limit)}
	// A row edited and edited back has records in both versions with the same cells, and is no
	// change. Such rows are dropped here, so candidate pages are read until the page holds limit
	// real changes and one more tells another page follows, or the store has no more. A page is
	// never empty while changes follow, and NextAfter is set only when one does.
	cursor := after
	for len(diff.Rows) <= limit {
		changes, err := s.store.DiffEvalDatasetVersions(ctx, id, from, to, cursor, limit+1)
		if err != nil {
			return nil, err
		}
		rows, last, err := diffRows(changes, before, afterColumns, limit+1-len(diff.Rows))
		if err != nil {
			return nil, err
		}
		diff.Rows = append(diff.Rows, rows...)
		if len(changes) <= limit {
			break // the store has no more
		}
		cursor = last
	}
	// The rows were read after the columns, so check, as Export does, that the response
	// still describes the versions it names. A saved version can be pruned while the rows
	// are read, and its records with it, which would show its rows as added; a version
	// number is never reused, so a missing one answers not found. A draft saved during the
	// read makes `to` a saved version, so the diff runs once more against it. That version is
	// immutable and is read through the saved-version branch, so the second pass ends there.
	// An edit on the live draft with no save in between shows on the next read, as Rows does.
	if from > 0 {
		if _, err := s.store.GetEvalDatasetVersion(ctx, id, from); err != nil {
			return nil, err
		}
	}
	if to != draft {
		if _, err := s.store.GetEvalDatasetVersion(ctx, id, to); err != nil {
			return nil, err
		}
	} else {
		current, err := s.store.GetEvalDataset(ctx, id)
		if err != nil {
			return nil, err
		}
		if current.HeadVersion >= to {
			return s.Diff(ctx, id, to, &from, after, limit)
		}
	}
	if len(diff.Rows) > limit {
		diff.Rows = diff.Rows[:limit]
		diff.NextAfter = diff.Rows[limit-1].Seq
	}
	return diff, nil
}

// diffRows turns candidate changes into row changes, dropping rows whose cells are the same in
// both versions, until want rows are collected. last is the sequence number of the last
// candidate consumed.
func diffRows(changes []configstore.EvalDatasetRowChange, before, afterColumns []Column, want int) (rows []RowChange, last int64, err error) {
	for i, change := range changes {
		if len(rows) == want {
			break
		}
		last = change.Seq
		row := RowChange{RowID: change.RowID, Seq: change.Seq}
		if change.Before != nil {
			if err := decodeRow(changes[i].Before, before); err != nil {
				return nil, 0, err
			}
			row.Before = change.Before.Cells
		}
		if change.After != nil {
			if err := decodeRow(changes[i].After, afterColumns); err != nil {
				return nil, 0, err
			}
			row.After = change.After.Cells
		}
		switch {
		case change.Before == nil:
			row.Change = ChangeAdded
		case change.After == nil:
			row.Change = ChangeRemoved
		case maps.Equal(change.Before.Cells, change.After.Cells):
			// Edited and edited back: the two versions read the same cells.
			continue
		default:
			row.Change = ChangeChanged
		}
		rows = append(rows, row)
	}
	return rows, last, nil
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
	// The dataset is read through the caller's scope first, as the other split edits do.
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
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
// and how many left. The add and the remove commit together or not at all.
func (s *Service) SetSplitRows(ctx context.Context, id, splitID string, add, remove []string) (int64, int64, error) {
	if len(add) > MaxRowsPerRequest || len(remove) > MaxRowsPerRequest {
		return 0, 0, invalid("at most %d rows can be added to or removed from a split in one call", MaxRowsPerRequest)
	}
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return 0, 0, err
	}
	// One transaction: the change is all or nothing.
	return s.store.SetEvalDatasetSplitRows(ctx, id, splitID, add, remove)
}
