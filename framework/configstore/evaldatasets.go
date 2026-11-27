package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm/clause"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// Dataset errors a caller can act on.
var (
	ErrEvalDatasetNameExists       = errors.New("a dataset with this name already exists")
	ErrEvalDatasetColumnNameExists = errors.New("the dataset already has a column with this name")
	ErrEvalDatasetSplitNameExists  = errors.New("the dataset already has a split with this name")
	ErrEvalDatasetFolderNameExists = errors.New("a dataset folder with this name already exists")
	ErrEvalDatasetNoChanges        = errors.New("the dataset has no changes to save")
	ErrEvalDatasetLatestVersion    = errors.New("the latest version of a dataset cannot be deleted")
	ErrEvalDatasetDraftNotSaved    = errors.New("the dataset has unsaved changes; save them before restoring")
	ErrEvalDatasetFolderCycle      = errors.New("a folder cannot be moved into itself or into a folder inside it")
)

// evalDatasetLive selects datasets that have not been deleted.
const evalDatasetLive = "evals_datasets.deleted_at_unix_nano = 0"

// evalDatasetBatch is how many rows or IDs one statement carries. It keeps bulk
// writes and IN lists well under every supported database's parameter limit.
const evalDatasetBatch = 500

// EvalDatasetListParams filters and pages a dataset listing.
type EvalDatasetListParams struct {
	Search string // case-insensitive substring of name or description
	Tag    string // exact tag
	Limit  int    // 0 means no limit
	Offset int
}

// EvalDatasetCounts are the sizes a dataset listing shows.
type EvalDatasetCounts struct {
	Columns int64 `json:"columns"`
	Rows    int64 `json:"rows"`
	Splits  int64 `json:"splits"`
}

// EvalDatasetRowListParams selects a page of a dataset's rows.
type EvalDatasetRowListParams struct {
	DatasetID string
	Version   int    // a saved version number; 0 reads the draft
	SplitID   string // only rows in this split
	Search    string // case-insensitive substring of a cell's value
	// SearchColumns are the columns Search reads: the version's own. A deleted column's
	// leftover cells and the column IDs used as keys are not searched.
	SearchColumns []string
	AfterSeq      int64 // rows after this sequence number
	Offset        int   // skip this many rows; for reading a page by row position
	Limit         int   // 0 means no limit
}

// EvalDatasetRowChange is one row that differs between two versions. Before is nil
// for an added row and After is nil for a removed one.
type EvalDatasetRowChange struct {
	RowID  string
	Seq    int64
	Before *tables.TableEvalDatasetRow
	After  *tables.TableEvalDatasetRow
}

// EvalDatasetVersionSave describes a version being saved.
type EvalDatasetVersionSave struct {
	Name         string
	Description  string
	CreatedBy    string
	RestoredFrom *int
	// Force saves even when the draft has no changes. A restore uses it.
	Force bool
	// SkipPrune leaves the version cap alone for this save. A restore saves pending edits
	// first, and pruning then could delete the very version being restored; the
	// restore's own save applies the cap afterwards.
	SkipPrune bool
}

// EvalDatasetTx is the write side of one dataset. Every change to a dataset's rows,
// columns and versions goes through it, inside one transaction that holds the
// dataset's lock, so two writers can never interleave edits or share a version number.
type EvalDatasetTx interface {
	// Dataset is the locked dataset. Its counters reflect the writes made so far.
	Dataset() *tables.TableEvalDataset
	Columns() ([]tables.TableEvalDatasetColumn, error)
	AddColumn(column *tables.TableEvalDatasetColumn) error
	UpdateColumn(column *tables.TableEvalDatasetColumn) error
	DeleteColumn(columnID string) error
	ReorderColumns(columnIDs []string) error
	// AddRows appends one row per cells JSON object, optionally into a split.
	AddRows(cellsJSON []string, splitID string) ([]tables.TableEvalDatasetRow, error)
	GetRow(rowID string) (*tables.TableEvalDatasetRow, error)
	SetRowCells(rowID, cellsJSON string) (*tables.TableEvalDatasetRow, error)
	DeleteRows(rowIDs []string) (int64, error)
	SaveVersion(save EvalDatasetVersionSave) (*tables.TableEvalDatasetVersion, error)
	// Restore makes the draft equal to a saved version. The draft must be clean.
	Restore(number int) error
	DeleteVersion(number int) error
	// PruneVersions deletes the oldest versions over the dataset's MaxVersions.
	PruneVersions() error
	// UpdateMetadata writes the dataset's name, description, tags and version cap.
	UpdateMetadata(name, description string, tags []string, maxVersions *int) error
	// ScanDraftCells calls fn with every draft row's cells, in sequence order.
	ScanDraftCells(fn func(rowID string, cells map[string]string) error) error
}

// ============================================================================
// Evals - Datasets
// ============================================================================

// ListEvalDatasets returns live datasets, newest first, with the total before paging.
func (s *RDBConfigStore) ListEvalDatasets(ctx context.Context, params EvalDatasetListParams) ([]tables.TableEvalDataset, int64, error) {
	query := s.ScopedDB(ctx).Model(&tables.TableEvalDataset{}).Where(evalDatasetLive)
	if search := strings.TrimSpace(params.Search); search != "" {
		pattern := "%" + likeEscaper.Replace(strings.ToLower(search)) + "%"
		query = query.Where(`(LOWER(evals_datasets.name) LIKE ? ESCAPE '\' OR LOWER(evals_datasets.description) LIKE ? ESCAPE '\')`, pattern, pattern)
	}
	if params.Tag != "" {
		// Compared as array elements, so a tag that contains the wanted one in its JSON text,
		// such as x"foo for foo, does not match.
		if query.Dialector.Name() == "postgres" {
			query = query.Where(`EXISTS (SELECT 1 FROM jsonb_array_elements_text(evals_datasets.tags_json::jsonb) AS tag(value) WHERE tag.value = ?)`, params.Tag)
		} else {
			query = query.Where(`EXISTS (SELECT 1 FROM json_each(evals_datasets.tags_json) AS tag WHERE tag.value = ?)`, params.Tag)
		}
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	query = query.Order("evals_datasets.created_at DESC, evals_datasets.id DESC")
	if params.Limit > 0 {
		query = query.Limit(params.Limit)
	}
	if params.Offset > 0 {
		query = query.Offset(params.Offset)
	}
	var datasets []tables.TableEvalDataset
	if err := query.Find(&datasets).Error; err != nil {
		return nil, 0, err
	}
	return datasets, total, nil
}

// GetEvalDatasetCounts returns the column, current row and split counts of datasets.
func (s *RDBConfigStore) GetEvalDatasetCounts(ctx context.Context, datasetIDs []string) (map[string]EvalDatasetCounts, error) {
	counts := make(map[string]EvalDatasetCounts, len(datasetIDs))
	if len(datasetIDs) == 0 {
		return counts, nil
	}
	type grouped struct {
		DatasetID string
		N         int64
	}
	load := func(model any, where string, set func(c *EvalDatasetCounts, n int64)) error {
		var rows []grouped
		query := s.DB().WithContext(ctx).Model(model).Select("dataset_id, COUNT(*) AS n").Where("dataset_id IN ?", datasetIDs)
		if where != "" {
			query = query.Where(where)
		}
		if err := query.Group("dataset_id").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			c := counts[row.DatasetID]
			set(&c, row.N)
			counts[row.DatasetID] = c
		}
		return nil
	}
	if err := load(&tables.TableEvalDatasetColumn{}, "", func(c *EvalDatasetCounts, n int64) { c.Columns = n }); err != nil {
		return nil, err
	}
	if err := load(&tables.TableEvalDatasetRow{}, "removed_in IS NULL", func(c *EvalDatasetCounts, n int64) { c.Rows = n }); err != nil {
		return nil, err
	}
	if err := load(&tables.TableEvalDatasetSplit{}, "", func(c *EvalDatasetCounts, n int64) { c.Splits = n }); err != nil {
		return nil, err
	}
	return counts, nil
}

// GetEvalDataset returns a live dataset by ID. A dataset outside the caller's scope
// reads as ErrNotFound, the same as one that does not exist.
func (s *RDBConfigStore) GetEvalDataset(ctx context.Context, id string) (*tables.TableEvalDataset, error) {
	return s.getEvalDataset(ctx, nil, id)
}

func (s *RDBConfigStore) getEvalDataset(ctx context.Context, tx *gorm.DB, id string) (*tables.TableEvalDataset, error) {
	var dataset tables.TableEvalDataset
	if err := s.ScopedDB(ctx, tx).Where(evalDatasetLive).Where("evals_datasets.id = ?", id).First(&dataset).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &dataset, nil
}

// CreateEvalDataset stores a new dataset with its starting columns, in the order given.
func (s *RDBConfigStore) CreateEvalDataset(ctx context.Context, dataset *tables.TableEvalDataset, columns []tables.TableEvalDatasetColumn) error {
	if dataset == nil {
		return errors.New("dataset is required")
	}
	if dataset.ID == "" {
		dataset.ID = uuid.NewString()
	}
	dataset.NextSeq = 1
	dataset.HeadVersion = 0
	dataset.DraftChanges = 0
	dataset.DeletedAt = 0
	for i := range columns {
		if columns[i].ID == "" {
			columns[i].ID = uuid.NewString()
		}
		columns[i].DatasetID = dataset.ID
		columns[i].Position = i
	}
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if dataset.FolderID != nil {
			if err := lockEvalDatasetFolders(tx); err != nil {
				return err
			}
		}
		if err := s.checkEvalDatasetFolder(ctx, tx, dataset.FolderID); err != nil {
			return err
		}
		if err := tx.Create(dataset).Error; err != nil {
			if isUniqueConstraintError(err) {
				return ErrEvalDatasetNameExists
			}
			return err
		}
		if len(columns) == 0 {
			return nil
		}
		if err := tx.Create(&columns).Error; err != nil {
			if isUniqueConstraintError(err) {
				return ErrEvalDatasetColumnNameExists
			}
			return err
		}
		return nil
	})
}

// UpdateEvalDataset changes a dataset's name, description, tags and version cap.
func (s *RDBConfigStore) UpdateEvalDataset(ctx context.Context, dataset *tables.TableEvalDataset) error {
	if dataset == nil {
		return errors.New("dataset is required")
	}
	if _, err := s.GetEvalDataset(ctx, dataset.ID); err != nil {
		return err
	}
	var tagsJSON *string
	if dataset.Tags != nil {
		data, err := json.Marshal(dataset.Tags)
		if err != nil {
			return err
		}
		tagsJSON = new(string(data))
	}
	result := s.DB().WithContext(ctx).Model(&tables.TableEvalDataset{}).
		Where("id = ? AND deleted_at_unix_nano = 0", dataset.ID).
		Updates(map[string]any{
			"name":         dataset.Name,
			"description":  dataset.Description,
			"tags_json":    tagsJSON,
			"max_versions": dataset.MaxVersions,
			"updated_at":   time.Now(),
		})
	if result.Error != nil {
		if isUniqueConstraintError(result.Error) {
			return ErrEvalDatasetNameExists
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateEvalDatasetFile stores an uploaded file's record, and its bytes when it
// carries them.
func (s *RDBConfigStore) CreateEvalDatasetFile(ctx context.Context, file *tables.TableEvalDatasetFile) error {
	if file == nil {
		return errors.New("file is required")
	}
	if file.ID == "" {
		file.ID = uuid.NewString()
	}
	// Under the dataset's lock: a file for a dataset deleted meanwhile is refused, so the caller
	// removes its object instead of leaving a row no delete will clean up.
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEvalDataset(tx, file.DatasetID); err != nil {
			return err
		}
		return tx.Create(file).Error
	})
}

// GetEvalDatasetFile returns a dataset's file with its bytes, or ErrNotFound.
func (s *RDBConfigStore) GetEvalDatasetFile(ctx context.Context, datasetID, fileID string) (*tables.TableEvalDatasetFile, error) {
	var file tables.TableEvalDatasetFile
	if err := s.DB().WithContext(ctx).First(&file, "id = ? AND dataset_id = ?", fileID, datasetID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &file, nil
}

// ListEvalDatasetFiles returns a dataset's file records without their inline bytes,
// oldest first. GetEvalDatasetFile reads one with its bytes.
func (s *RDBConfigStore) ListEvalDatasetFiles(ctx context.Context, datasetID string) ([]tables.TableEvalDatasetFile, error) {
	var files []tables.TableEvalDatasetFile
	err := s.DB().WithContext(ctx).Omit("data").Where("dataset_id = ?", datasetID).
		Order("created_at ASC, id ASC").Find(&files).Error
	return files, err
}

// ListEvalDatasetFileKeys returns the object store keys of a dataset's files. A file
// kept in the database has none.
func (s *RDBConfigStore) ListEvalDatasetFileKeys(ctx context.Context, datasetID string) ([]string, error) {
	var keys []string
	err := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetFile{}).
		Where("dataset_id = ? AND object_key <> ''", datasetID).Pluck("object_key", &keys).Error
	return keys, err
}

// ListEvalDatasetFolders returns every dataset folder, by name.
func (s *RDBConfigStore) ListEvalDatasetFolders(ctx context.Context) ([]tables.TableEvalDatasetFolder, error) {
	var folders []tables.TableEvalDatasetFolder
	if err := s.ScopedDB(ctx).Order("name ASC, id ASC").Find(&folders).Error; err != nil {
		return nil, err
	}
	return folders, nil
}

// CreateEvalDatasetFolder stores a new dataset folder.
func (s *RDBConfigStore) CreateEvalDatasetFolder(ctx context.Context, folder *tables.TableEvalDatasetFolder) error {
	if folder == nil {
		return errors.New("folder is required")
	}
	if folder.ID == "" {
		folder.ID = uuid.NewString()
	}
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEvalDatasetFolders(tx); err != nil {
			return err
		}
		if err := s.checkEvalDatasetFolder(ctx, tx, folder.ParentID); err != nil {
			return err
		}
		return tx.Create(folder).Error
	})
	if isUniqueConstraintError(err) {
		return ErrEvalDatasetFolderNameExists
	}
	return err
}

// UpdateEvalDatasetFolder changes a folder's name and description.
func (s *RDBConfigStore) UpdateEvalDatasetFolder(ctx context.Context, folder *tables.TableEvalDatasetFolder) error {
	if folder == nil {
		return errors.New("folder is required")
	}
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockEvalDatasetFolder(ctx, tx, folder.ID); err != nil {
			return err
		}
		result := tx.Model(&tables.TableEvalDatasetFolder{}).Where("id = ?", folder.ID).
			Updates(map[string]any{"name": folder.Name, "description": folder.Description, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return tx.First(folder, "id = ?", folder.ID).Error
	})
	if isUniqueConstraintError(err) {
		return ErrEvalDatasetFolderNameExists
	}
	return err
}

// SetEvalDatasetFolderParent moves a folder into another folder, or to the top level
// when parentID is nil. A move into the folder itself or into a folder below it fails with
// ErrEvalDatasetFolderCycle. The check reads the hierarchy under the hierarchy lock, in the
// move's own transaction, so two moves at once cannot build a cycle between them.
func (s *RDBConfigStore) SetEvalDatasetFolderParent(ctx context.Context, id string, parentID *string) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEvalDatasetFolders(tx); err != nil {
			return err
		}
		if _, err := s.lockEvalDatasetFolder(ctx, tx, id); err != nil {
			return err
		}
		if err := s.checkEvalDatasetFolder(ctx, tx, parentID); err != nil {
			return err
		}
		if parentID != nil {
			var folders []tables.TableEvalDatasetFolder
			if err := tx.Select("id", "parent_id").Find(&folders).Error; err != nil {
				return err
			}
			parents := make(map[string]*string, len(folders))
			for i := range folders {
				parents[folders[i].ID] = folders[i].ParentID
			}
			// Walk up from the new parent. Meeting the folder itself means the move would close a
			// loop. The step count bounds the walk even if stored data has a loop.
			at := parentID
			for steps := 0; at != nil && steps <= len(folders); steps++ {
				if *at == id {
					return ErrEvalDatasetFolderCycle
				}
				at = parents[*at]
			}
		}
		result := tx.Model(&tables.TableEvalDatasetFolder{}).Where("id = ?", id).
			Updates(map[string]any{"parent_id": parentID, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DeleteEvalDatasetFolder removes a folder. What it holds is kept: its datasets and
// its folders move up into the folder it was in, in the same transaction.
func (s *RDBConfigStore) DeleteEvalDatasetFolder(ctx context.Context, id string) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The hierarchy lock first, so a move, create or another delete that files something here
		// either finishes before this sweep (and is moved up by it) or finds the folder gone. The
		// folder and its parent are read after it, so they are current.
		if err := lockEvalDatasetFolders(tx); err != nil {
			return err
		}
		folder, err := s.lockEvalDatasetFolder(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := tx.Model(&tables.TableEvalDataset{}).Where("folder_id = ?", id).UpdateColumn("folder_id", folder.ParentID).Error; err != nil {
			return err
		}
		if err := tx.Model(&tables.TableEvalDatasetFolder{}).Where("parent_id = ?", id).UpdateColumn("parent_id", folder.ParentID).Error; err != nil {
			return err
		}
		result := tx.Where("id = ?", id).Delete(&tables.TableEvalDatasetFolder{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// lockEvalDatasetFolder reads the folder a write changes, through the caller's scope, with an
// update lock on Postgres. A folder the scope hides reads as ErrNotFound, like a missing one.
func (s *RDBConfigStore) lockEvalDatasetFolder(ctx context.Context, tx *gorm.DB, id string) (*tables.TableEvalDatasetFolder, error) {
	var folder tables.TableEvalDatasetFolder
	if err := withRowLock(s.ScopedDB(ctx, tx), "UPDATE").First(&folder, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &folder, nil
}

// checkEvalDatasetFolder reports ErrNotFound when folderID names a folder that does
// not exist or that the caller's scope hides. Nil means no folder, which is always fine. It
// reads the folder with a share lock on Postgres, so a delete of the folder waits for tx and
// then moves what tx filed there.
func (s *RDBConfigStore) checkEvalDatasetFolder(ctx context.Context, tx *gorm.DB, folderID *string) error {
	if folderID == nil {
		return nil
	}
	var folders []tables.TableEvalDatasetFolder
	if err := withRowLock(s.ScopedDB(ctx, tx), "SHARE").Select("id").Where("id = ?", *folderID).Limit(1).Find(&folders).Error; err != nil {
		return err
	}
	if len(folders) == 0 {
		return ErrNotFound
	}
	return nil
}

// lockEvalDatasetFolders takes the one lock every folder hierarchy write holds, for the rest of
// tx. Row locks are not enough: a delete locks its folder and then moves what it holds, while a
// delete of a folder inside it can move a dataset into the first folder after that sweep read it.
// On Postgres it is a transaction advisory lock; SQLite already lets one writer in at a time.
// Folder writes are rare, so the lock costs nothing on the request path.
func lockEvalDatasetFolders(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "evals-dataset-folders").Error
}

// withRowLock adds a row lock of the given strength on Postgres. SQLite takes the database
// write lock for the whole transaction, so it needs none, and has no syntax for one.
func withRowLock(tx *gorm.DB, strength string) *gorm.DB {
	if tx.Dialector.Name() != "postgres" {
		return tx
	}
	return tx.Clauses(clause.Locking{Strength: strength})
}

// SetEvalDatasetFolder files a dataset under a folder, or under none when folderID
// is nil. It does not count as a change to the dataset's contents. The dataset and the folder
// are read through the caller's scope first: one the scope hides is ErrNotFound and is not moved.
func (s *RDBConfigStore) SetEvalDatasetFolder(ctx context.Context, datasetID string, folderID *string) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEvalDatasetFolders(tx); err != nil {
			return err
		}
		if err := s.checkEvalDatasetFolder(ctx, tx, folderID); err != nil {
			return err
		}
		if _, err := s.getEvalDataset(ctx, tx, datasetID); err != nil {
			return err
		}
		result := tx.Model(&tables.TableEvalDataset{}).
			Where("id = ? AND deleted_at_unix_nano = 0", datasetID).UpdateColumn("folder_id", folderID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DeleteEvalDataset soft-deletes a dataset, which frees its name, and removes its
// columns, rows, versions, splits and file records. It returns the object store keys of the
// files it removed, read under the delete, so an upload that finished first is included.
func (s *RDBConfigStore) DeleteEvalDataset(ctx context.Context, id string) ([]string, error) {
	if _, err := s.GetEvalDataset(ctx, id); err != nil {
		return nil, err
	}
	var keys []string
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		result := tx.Model(&tables.TableEvalDataset{}).
			Where("id = ? AND deleted_at_unix_nano = 0", id).
			Updates(map[string]any{"deleted_at_unix_nano": now.UnixNano(), "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Model(&tables.TableEvalDatasetFile{}).Where("dataset_id = ? AND object_key <> ''", id).Pluck("object_key", &keys).Error; err != nil {
			return err
		}
		for _, child := range []any{
			&tables.TableEvalDatasetSplitRow{}, &tables.TableEvalDatasetSplit{}, &tables.TableEvalDatasetRow{},
			&tables.TableEvalDatasetVersion{}, &tables.TableEvalDatasetColumn{}, &tables.TableEvalDatasetFile{},
		} {
			if err := tx.Where("dataset_id = ?", id).Delete(child).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// ListEvalDatasetColumns returns the draft's columns in display order.
func (s *RDBConfigStore) ListEvalDatasetColumns(ctx context.Context, datasetID string) ([]tables.TableEvalDatasetColumn, error) {
	return listEvalDatasetColumns(s.DB().WithContext(ctx), datasetID)
}

func listEvalDatasetColumns(db *gorm.DB, datasetID string) ([]tables.TableEvalDatasetColumn, error) {
	var columns []tables.TableEvalDatasetColumn
	if err := db.Where("dataset_id = ?", datasetID).Order("position ASC, created_at ASC, id ASC").Find(&columns).Error; err != nil {
		return nil, err
	}
	return columns, nil
}

// ============================================================================
// Evals - Dataset rows
// ============================================================================

// evalDatasetRowsAt narrows a row query to the records one version has. Version 0 is
// the draft, which is every record that has not been retired.
func evalDatasetRowsAt(query *gorm.DB, version int) *gorm.DB {
	if version <= 0 {
		return query.Where("evals_dataset_rows.removed_in IS NULL")
	}
	return query.Where("evals_dataset_rows.added_in <= ? AND (evals_dataset_rows.removed_in IS NULL OR evals_dataset_rows.removed_in > ?)", version, version)
}

func (s *RDBConfigStore) evalDatasetRowQuery(ctx context.Context, datasetID string, version int, splitID string) *gorm.DB {
	query := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetRow{}).Where("evals_dataset_rows.dataset_id = ?", datasetID)
	query = evalDatasetRowsAt(query, version)
	if splitID != "" {
		members := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetSplitRow{}).Select("row_id").Where("split_id = ?", splitID)
		query = query.Where("evals_dataset_rows.row_id IN (?)", members)
	}
	return query
}

// evalDatasetRowSearch narrows a row query to rows where one of columns has a value that
// contains search, ignoring case. The values are read out of the cells JSON, so the match is on
// what was typed (quotes and backslashes included), never on the JSON text or its keys. With no
// columns nothing matches. An empty search changes nothing.
func evalDatasetRowSearch(query *gorm.DB, search string, columns []string) *gorm.DB {
	if search = strings.TrimSpace(search); search == "" {
		return query
	}
	if len(columns) == 0 {
		return query.Where("1 = 0")
	}
	pattern := "%" + likeEscaper.Replace(strings.ToLower(search)) + "%"
	if query.Dialector.Name() == "postgres" {
		return query.Where(`EXISTS (SELECT 1 FROM jsonb_each_text(evals_dataset_rows.cells_json::jsonb) AS cell(key, value) WHERE cell.key IN ? AND LOWER(cell.value) LIKE ? ESCAPE '\')`, columns, pattern)
	}
	return query.Where(`EXISTS (SELECT 1 FROM json_each(evals_dataset_rows.cells_json) AS cell WHERE cell.key IN ? AND LOWER(cell.value) LIKE ? ESCAPE '\')`, columns, pattern)
}

// ListEvalDatasetRows returns a page of rows in sequence order. Paging by sequence
// number (AfterSeq) costs the same on a late page as on the first, and suits a reader
// that walks the whole dataset. Paging by Offset reads a page by row position, which
// is what a grid needs to jump anywhere.
func (s *RDBConfigStore) ListEvalDatasetRows(ctx context.Context, params EvalDatasetRowListParams) ([]tables.TableEvalDatasetRow, error) {
	query := evalDatasetRowSearch(s.evalDatasetRowQuery(ctx, params.DatasetID, params.Version, params.SplitID), params.Search, params.SearchColumns)
	if params.AfterSeq > 0 {
		query = query.Where("evals_dataset_rows.seq > ?", params.AfterSeq)
	}
	query = query.Order("evals_dataset_rows.seq ASC")
	if params.Limit > 0 {
		query = query.Limit(params.Limit)
	}
	if params.Offset > 0 {
		query = query.Offset(params.Offset)
	}
	var rows []tables.TableEvalDatasetRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountEvalDatasetRows counts the rows a listing with the same dataset, version, split and
// search returns, without paging.
func (s *RDBConfigStore) CountEvalDatasetRows(ctx context.Context, params EvalDatasetRowListParams) (int64, error) {
	var count int64
	err := evalDatasetRowSearch(s.evalDatasetRowQuery(ctx, params.DatasetID, params.Version, params.SplitID), params.Search, params.SearchColumns).Count(&count).Error
	return count, err
}

// GetEvalDatasetRow returns one row as a version has it (0 is the draft).
func (s *RDBConfigStore) GetEvalDatasetRow(ctx context.Context, datasetID, rowID string, version int) (*tables.TableEvalDatasetRow, error) {
	var row tables.TableEvalDatasetRow
	err := s.evalDatasetRowQuery(ctx, datasetID, version, "").Where("evals_dataset_rows.row_id = ?", rowID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ============================================================================
// Evals - Dataset versions
// ============================================================================

// ListEvalDatasetVersions returns a dataset's saved versions, newest first.
func (s *RDBConfigStore) ListEvalDatasetVersions(ctx context.Context, datasetID string) ([]tables.TableEvalDatasetVersion, error) {
	var versions []tables.TableEvalDatasetVersion
	if err := s.DB().WithContext(ctx).Where("dataset_id = ?", datasetID).Order("number DESC").Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

// GetEvalDatasetVersion returns one saved version.
func (s *RDBConfigStore) GetEvalDatasetVersion(ctx context.Context, datasetID string, number int) (*tables.TableEvalDatasetVersion, error) {
	return getEvalDatasetVersion(s.DB().WithContext(ctx), datasetID, number)
}

func getEvalDatasetVersion(db *gorm.DB, datasetID string, number int) (*tables.TableEvalDatasetVersion, error) {
	var version tables.TableEvalDatasetVersion
	err := db.Where("dataset_id = ? AND number = ?", datasetID, number).First(&version).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &version, nil
}

// UpdateEvalDatasetVersion changes a version's name and note. Its rows never change.
func (s *RDBConfigStore) UpdateEvalDatasetVersion(ctx context.Context, datasetID string, number int, name, description string) error {
	result := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetVersion{}).
		Where("dataset_id = ? AND number = ?", datasetID, number).
		Updates(map[string]any{"name": name, "description": description, "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DiffEvalDatasetVersions returns the rows that differ between version from and the
// later version to, in sequence order, paged by sequence number. from may be 0, which
// is the empty dataset. to may be the draft's number, HeadVersion+1.
//
// It compares what each version reads, not the edits in between, so it stays correct
// when a version between the two has been deleted.
func (s *RDBConfigStore) DiffEvalDatasetVersions(ctx context.Context, datasetID string, from, to int, afterSeq int64, limit int) ([]EvalDatasetRowChange, error) {
	if from >= to {
		return nil, errors.New("diff needs an earlier version and a later one")
	}
	// Records only the earlier version reads, or only the later one.
	const differs = `((added_in <= ? AND removed_in IS NOT NULL AND removed_in > ? AND removed_in <= ?)
		OR (added_in > ? AND added_in <= ? AND (removed_in IS NULL OR removed_in > ?)))`
	args := []any{from, from, to, from, to, to}

	// One statement, with the page's sequence numbers as a subquery: rows removed between two
	// separate reads could otherwise leave an empty page while later changes exist.
	seqQuery := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetRow{}).
		Distinct("seq").Where("dataset_id = ? AND seq > ?", datasetID, afterSeq).Where(differs, args...).Order("seq ASC")
	if limit > 0 {
		seqQuery = seqQuery.Limit(limit)
	}
	var records []tables.TableEvalDatasetRow
	if err := s.DB().WithContext(ctx).Where("dataset_id = ? AND seq IN (?)", datasetID, seqQuery).Where(differs, args...).
		Order("seq ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	changes := make([]EvalDatasetRowChange, 0, len(records))
	for i := range records {
		record := &records[i]
		if len(changes) == 0 || changes[len(changes)-1].Seq != record.Seq {
			changes = append(changes, EvalDatasetRowChange{RowID: record.RowID, Seq: record.Seq})
		}
		change := &changes[len(changes)-1]
		if record.AddedIn > from {
			change.After = record
		} else {
			change.Before = record
		}
	}
	return changes, nil
}

// ============================================================================
// Evals - Dataset splits
// ============================================================================

// ListEvalDatasetSplits returns a dataset's splits, oldest first.
func (s *RDBConfigStore) ListEvalDatasetSplits(ctx context.Context, datasetID string) ([]tables.TableEvalDatasetSplit, error) {
	var splits []tables.TableEvalDatasetSplit
	if err := s.DB().WithContext(ctx).Where("dataset_id = ?", datasetID).Order("created_at ASC, id ASC").Find(&splits).Error; err != nil {
		return nil, err
	}
	return splits, nil
}

// CountEvalDatasetSplitRows returns how many current rows each split of a dataset has.
func (s *RDBConfigStore) CountEvalDatasetSplitRows(ctx context.Context, datasetID string) (map[string]int64, error) {
	var grouped []struct {
		SplitID string
		N       int64
	}
	current := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetRow{}).Select("row_id").
		Where("dataset_id = ? AND removed_in IS NULL", datasetID)
	if err := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetSplitRow{}).
		Select("split_id, COUNT(*) AS n").
		Where("dataset_id = ? AND row_id IN (?)", datasetID, current).
		Group("split_id").Scan(&grouped).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(grouped))
	for _, g := range grouped {
		counts[g.SplitID] = g.N
	}
	return counts, nil
}

// CreateEvalDatasetSplit adds a split to a dataset.
func (s *RDBConfigStore) CreateEvalDatasetSplit(ctx context.Context, split *tables.TableEvalDatasetSplit) error {
	if split == nil {
		return errors.New("split is required")
	}
	if split.ID == "" {
		split.ID = uuid.NewString()
	}
	// Under the dataset's lock: a split for a dataset deleted meanwhile is refused instead of
	// outliving it.
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEvalDataset(tx, split.DatasetID); err != nil {
			return err
		}
		if err := tx.Create(split).Error; err != nil {
			if isUniqueConstraintError(err) {
				return ErrEvalDatasetSplitNameExists
			}
			return err
		}
		return nil
	})
}

// UpdateEvalDatasetSplit changes a split's name and description.
func (s *RDBConfigStore) UpdateEvalDatasetSplit(ctx context.Context, split *tables.TableEvalDatasetSplit) error {
	if split == nil {
		return errors.New("split is required")
	}
	result := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetSplit{}).
		Where("id = ? AND dataset_id = ?", split.ID, split.DatasetID).
		Updates(map[string]any{"name": split.Name, "description": split.Description, "updated_at": time.Now()})
	if result.Error != nil {
		if isUniqueConstraintError(result.Error) {
			return ErrEvalDatasetSplitNameExists
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteEvalDatasetSplit removes a split. Its rows stay in the dataset.
func (s *RDBConfigStore) DeleteEvalDatasetSplit(ctx context.Context, datasetID, splitID string) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Under the dataset's lock, as membership writes are, so a row cannot join the split
		// after it is gone.
		if err := lockEvalDataset(tx, datasetID); err != nil {
			return err
		}
		result := tx.Where("id = ? AND dataset_id = ?", splitID, datasetID).Delete(&tables.TableEvalDatasetSplit{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return tx.Where("split_id = ?", splitID).Delete(&tables.TableEvalDatasetSplitRow{}).Error
	})
}

// AddEvalDatasetSplitRows puts rows in a split and returns how many joined. Row IDs
// that are not current rows of the dataset, or are already members, are ignored.
func (s *RDBConfigStore) AddEvalDatasetSplitRows(ctx context.Context, datasetID, splitID string, rowIDs []string) (int64, error) {
	var added int64
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEvalDataset(tx, datasetID); err != nil {
			return err
		}
		if err := requireEvalDatasetSplit(tx, datasetID, splitID); err != nil {
			return err
		}
		n, err := addEvalDatasetSplitRows(tx, datasetID, splitID, rowIDs)
		added = n
		if err != nil {
			return err
		}
		return touchEvalDatasetSplit(tx, splitID, n)
	})
	return added, err
}

// touchEvalDatasetSplit moves a split's updated_at when its membership changed. Membership
// is not versioned, so this is how a dataset copy, which reads a split a page at a time,
// sees that the split changed while it read.
func touchEvalDatasetSplit(tx *gorm.DB, splitID string, changed int64) error {
	if changed == 0 {
		return nil
	}
	return tx.Model(&tables.TableEvalDatasetSplit{}).Where("id = ?", splitID).Update("updated_at", time.Now()).Error
}

func requireEvalDatasetSplit(tx *gorm.DB, datasetID, splitID string) error {
	var count int64
	if err := tx.Model(&tables.TableEvalDatasetSplit{}).Where("id = ? AND dataset_id = ?", splitID, datasetID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func addEvalDatasetSplitRows(tx *gorm.DB, datasetID, splitID string, rowIDs []string) (int64, error) {
	var added int64
	for start := 0; start < len(rowIDs); start += evalDatasetBatch {
		chunk := rowIDs[start:min(start+evalDatasetBatch, len(rowIDs))]
		var current []string
		if err := tx.Model(&tables.TableEvalDatasetRow{}).Distinct("row_id").
			Where("dataset_id = ? AND removed_in IS NULL AND row_id IN ?", datasetID, chunk).Pluck("row_id", &current).Error; err != nil {
			return added, err
		}
		var members []string
		if err := tx.Model(&tables.TableEvalDatasetSplitRow{}).
			Where("split_id = ? AND row_id IN ?", splitID, chunk).Pluck("row_id", &members).Error; err != nil {
			return added, err
		}
		isMember := make(map[string]struct{}, len(members))
		for _, id := range members {
			isMember[id] = struct{}{}
		}
		links := make([]tables.TableEvalDatasetSplitRow, 0, len(current))
		for _, id := range current {
			if _, ok := isMember[id]; ok {
				continue
			}
			links = append(links, tables.TableEvalDatasetSplitRow{SplitID: splitID, RowID: id, DatasetID: datasetID})
		}
		if len(links) == 0 {
			continue
		}
		if err := tx.Create(&links).Error; err != nil {
			return added, err
		}
		added += int64(len(links))
	}
	return added, nil
}

// RemoveEvalDatasetSplitRows takes rows out of a split and returns how many left.
func (s *RDBConfigStore) RemoveEvalDatasetSplitRows(ctx context.Context, datasetID, splitID string, rowIDs []string) (int64, error) {
	var removed int64
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Under the dataset's lock, as every membership write is.
		if err := lockEvalDataset(tx, datasetID); err != nil {
			return err
		}
		if err := requireEvalDatasetSplit(tx, datasetID, splitID); err != nil {
			return err
		}
		n, err := removeEvalDatasetSplitRows(tx, splitID, rowIDs)
		removed = n
		if err != nil {
			return err
		}
		return touchEvalDatasetSplit(tx, splitID, n)
	})
	return removed, err
}

// SetEvalDatasetSplitRows adds rows to a split and takes others out, in one transaction under
// the dataset's lock, so the change commits whole or not at all. It returns how many joined and
// how many left.
func (s *RDBConfigStore) SetEvalDatasetSplitRows(ctx context.Context, datasetID, splitID string, add, remove []string) (int64, int64, error) {
	var added, removed int64
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEvalDataset(tx, datasetID); err != nil {
			return err
		}
		if err := requireEvalDatasetSplit(tx, datasetID, splitID); err != nil {
			return err
		}
		var err error
		if added, err = addEvalDatasetSplitRows(tx, datasetID, splitID, add); err != nil {
			return err
		}
		if removed, err = removeEvalDatasetSplitRows(tx, splitID, remove); err != nil {
			return err
		}
		return touchEvalDatasetSplit(tx, splitID, added+removed)
	})
	if err != nil {
		return 0, 0, err
	}
	return added, removed, nil
}

func removeEvalDatasetSplitRows(tx *gorm.DB, splitID string, rowIDs []string) (int64, error) {
	var removed int64
	for start := 0; start < len(rowIDs); start += evalDatasetBatch {
		chunk := rowIDs[start:min(start+evalDatasetBatch, len(rowIDs))]
		result := tx.Where("split_id = ? AND row_id IN ?", splitID, chunk).Delete(&tables.TableEvalDatasetSplitRow{})
		if result.Error != nil {
			return removed, result.Error
		}
		removed += result.RowsAffected
	}
	return removed, nil
}

// ============================================================================
// Evals - Dataset writes
// ============================================================================

// WithEvalDataset runs fn with the write side of one dataset, in a transaction that
// holds the dataset's lock. The dataset's counters are stored when fn returns nil;
// any error rolls every write back.
// lockEvalDataset takes a live dataset's lock in tx: writing the dataset row takes its row
// lock on Postgres and the write lock on SQLite, so no other writer can change the dataset, or
// delete it, until tx ends. ErrNotFound when the dataset is gone.
func lockEvalDataset(tx *gorm.DB, datasetID string) error {
	touched := tx.Model(&tables.TableEvalDataset{}).
		Where("id = ? AND deleted_at_unix_nano = 0", datasetID).
		UpdateColumn("updated_at", time.Now())
	if touched.Error != nil {
		return touched.Error
	}
	if touched.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *RDBConfigStore) WithEvalDataset(ctx context.Context, datasetID string, fn func(tx EvalDatasetTx) error) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEvalDataset(tx, datasetID); err != nil {
			return err
		}
		dataset, err := s.getEvalDataset(ctx, tx, datasetID)
		if err != nil {
			return err
		}
		write := &evalDatasetTx{tx: tx, dataset: dataset}
		if err := fn(write); err != nil {
			return err
		}
		return tx.Model(&tables.TableEvalDataset{}).Where("id = ?", datasetID).Updates(map[string]any{
			"next_seq":      dataset.NextSeq,
			"head_version":  dataset.HeadVersion,
			"draft_changes": dataset.DraftChanges,
		}).Error
	})
}

type evalDatasetTx struct {
	tx      *gorm.DB
	dataset *tables.TableEvalDataset
}

// draft is the version number edits are written under.
func (w *evalDatasetTx) draft() int { return w.dataset.HeadVersion + 1 }

func (w *evalDatasetTx) Dataset() *tables.TableEvalDataset { return w.dataset }

func (w *evalDatasetTx) Columns() ([]tables.TableEvalDatasetColumn, error) {
	return listEvalDatasetColumns(w.tx, w.dataset.ID)
}

func (w *evalDatasetTx) AddColumn(column *tables.TableEvalDatasetColumn) error {
	if column == nil {
		return errors.New("column is required")
	}
	if column.ID == "" {
		column.ID = uuid.NewString()
	}
	column.DatasetID = w.dataset.ID
	var last *int
	if err := w.tx.Model(&tables.TableEvalDatasetColumn{}).Where("dataset_id = ?", w.dataset.ID).
		Select("MAX(position)").Scan(&last).Error; err != nil {
		return err
	}
	column.Position = 0
	if last != nil {
		column.Position = *last + 1
	}
	if err := w.tx.Create(column).Error; err != nil {
		if isUniqueConstraintError(err) {
			return ErrEvalDatasetColumnNameExists
		}
		return err
	}
	w.dataset.DraftChanges++
	return nil
}

func (w *evalDatasetTx) UpdateColumn(column *tables.TableEvalDatasetColumn) error {
	if column == nil {
		return errors.New("column is required")
	}
	result := w.tx.Model(&tables.TableEvalDatasetColumn{}).
		Where("id = ? AND dataset_id = ?", column.ID, w.dataset.ID).
		Updates(map[string]any{"name": column.Name, "type": column.Type, "updated_at": time.Now()})
	if result.Error != nil {
		if isUniqueConstraintError(result.Error) {
			return ErrEvalDatasetColumnNameExists
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	w.dataset.DraftChanges++
	return nil
}

// DeleteColumn removes a column from the draft. No row is rewritten: cells are keyed
// by column ID, so the draft simply stops reading them and saved versions still can.
func (w *evalDatasetTx) DeleteColumn(columnID string) error {
	result := w.tx.Where("id = ? AND dataset_id = ?", columnID, w.dataset.ID).Delete(&tables.TableEvalDatasetColumn{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	w.dataset.DraftChanges++
	return nil
}

func (w *evalDatasetTx) ReorderColumns(columnIDs []string) error {
	for position, id := range columnIDs {
		result := w.tx.Model(&tables.TableEvalDatasetColumn{}).
			Where("id = ? AND dataset_id = ?", id, w.dataset.ID).
			UpdateColumn("position", position)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
	}
	w.dataset.DraftChanges++
	return nil
}

func (w *evalDatasetTx) AddRows(cellsJSON []string, splitID string) ([]tables.TableEvalDatasetRow, error) {
	if len(cellsJSON) == 0 {
		return nil, nil
	}
	if splitID != "" {
		if err := requireEvalDatasetSplit(w.tx, w.dataset.ID, splitID); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	rows := make([]tables.TableEvalDatasetRow, len(cellsJSON))
	for i, cells := range cellsJSON {
		if cells == "" {
			cells = "{}"
		}
		rows[i] = tables.TableEvalDatasetRow{
			ID:        uuid.NewString(),
			DatasetID: w.dataset.ID,
			RowID:     uuid.NewString(),
			Seq:       w.dataset.NextSeq,
			CellsJSON: cells,
			AddedIn:   w.draft(),
			CreatedAt: now,
		}
		w.dataset.NextSeq++
	}
	if err := w.tx.CreateInBatches(&rows, evalDatasetBatch).Error; err != nil {
		return nil, err
	}
	if splitID != "" {
		links := make([]tables.TableEvalDatasetSplitRow, len(rows))
		for i := range rows {
			links[i] = tables.TableEvalDatasetSplitRow{SplitID: splitID, RowID: rows[i].RowID, DatasetID: w.dataset.ID}
		}
		if err := w.tx.CreateInBatches(&links, evalDatasetBatch).Error; err != nil {
			return nil, err
		}
	}
	w.dataset.DraftChanges += len(rows)
	return rows, nil
}

func (w *evalDatasetTx) GetRow(rowID string) (*tables.TableEvalDatasetRow, error) {
	var row tables.TableEvalDatasetRow
	err := w.tx.Where("dataset_id = ? AND row_id = ? AND removed_in IS NULL", w.dataset.ID, rowID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// SetRowCells replaces a row's cells. A record first written in this draft is updated
// in place. A record a saved version reads is retired instead and a new one written,
// so that version keeps reading the old cells.
func (w *evalDatasetTx) SetRowCells(rowID, cellsJSON string) (*tables.TableEvalDatasetRow, error) {
	row, err := w.GetRow(rowID)
	if err != nil {
		return nil, err
	}
	if cellsJSON == "" {
		cellsJSON = "{}"
	}
	draft := w.draft()
	if row.AddedIn == draft {
		if err := w.tx.Model(&tables.TableEvalDatasetRow{}).Where("id = ?", row.ID).UpdateColumn("cells_json", cellsJSON).Error; err != nil {
			return nil, err
		}
		row.CellsJSON = cellsJSON
	} else {
		if err := w.tx.Model(&tables.TableEvalDatasetRow{}).Where("id = ?", row.ID).UpdateColumn("removed_in", draft).Error; err != nil {
			return nil, err
		}
		row = &tables.TableEvalDatasetRow{
			ID:        uuid.NewString(),
			DatasetID: w.dataset.ID,
			RowID:     row.RowID,
			Seq:       row.Seq,
			CellsJSON: cellsJSON,
			AddedIn:   draft,
			CreatedAt: row.CreatedAt,
		}
		if err := w.tx.Create(row).Error; err != nil {
			return nil, err
		}
	}
	w.dataset.DraftChanges++
	return row, nil
}

func (w *evalDatasetTx) DeleteRows(rowIDs []string) (int64, error) {
	draft := w.draft()
	var deleted int64
	for start := 0; start < len(rowIDs); start += evalDatasetBatch {
		chunk := rowIDs[start:min(start+evalDatasetBatch, len(rowIDs))]
		// A record written in this draft is in no saved version, so it can go for good.
		hard := w.tx.Where("dataset_id = ? AND row_id IN ? AND removed_in IS NULL AND added_in = ?", w.dataset.ID, chunk, draft).
			Delete(&tables.TableEvalDatasetRow{})
		if hard.Error != nil {
			return deleted, hard.Error
		}
		retired := w.tx.Model(&tables.TableEvalDatasetRow{}).
			Where("dataset_id = ? AND row_id IN ? AND removed_in IS NULL", w.dataset.ID, chunk).
			UpdateColumn("removed_in", draft)
		if retired.Error != nil {
			return deleted, retired.Error
		}
		deleted += hard.RowsAffected + retired.RowsAffected
		if hard.RowsAffected > 0 {
			// Drop split membership of rows that no version has any more.
			if err := w.tx.Exec(`DELETE FROM evals_dataset_split_rows WHERE dataset_id = ? AND row_id IN ?
				AND NOT EXISTS (SELECT 1 FROM evals_dataset_rows r WHERE r.dataset_id = evals_dataset_split_rows.dataset_id AND r.row_id = evals_dataset_split_rows.row_id)`,
				w.dataset.ID, chunk).Error; err != nil {
				return deleted, err
			}
		}
	}
	w.dataset.DraftChanges += int(deleted)
	return deleted, nil
}

// SaveVersion turns the draft into a saved version. No row is copied: the version is
// one row that records the columns and what changed, and the draft number moves up.
func (w *evalDatasetTx) SaveVersion(save EvalDatasetVersionSave) (*tables.TableEvalDatasetVersion, error) {
	if w.dataset.DraftChanges == 0 && !save.Force {
		return nil, ErrEvalDatasetNoChanges
	}
	number := w.draft()
	rows := func() *gorm.DB {
		return w.tx.Model(&tables.TableEvalDatasetRow{}).Where("dataset_id = ?", w.dataset.ID)
	}
	var total, written, retired, changed int64
	if err := rows().Where("removed_in IS NULL").Count(&total).Error; err != nil {
		return nil, err
	}
	if err := rows().Where("added_in = ?", number).Count(&written).Error; err != nil {
		return nil, err
	}
	if err := rows().Where("removed_in = ?", number).Count(&retired).Error; err != nil {
		return nil, err
	}
	// A changed row has both a record written in this version and one retired by it.
	if err := w.tx.Raw(`SELECT COUNT(*) FROM evals_dataset_rows a WHERE a.dataset_id = ? AND a.added_in = ?
		AND EXISTS (SELECT 1 FROM evals_dataset_rows b WHERE b.dataset_id = a.dataset_id AND b.row_id = a.row_id AND b.removed_in = ?)`,
		w.dataset.ID, number, number).Scan(&changed).Error; err != nil {
		return nil, err
	}
	columns, err := w.Columns()
	if err != nil {
		return nil, err
	}
	version := &tables.TableEvalDatasetVersion{
		ID:           uuid.NewString(),
		DatasetID:    w.dataset.ID,
		Number:       number,
		Name:         save.Name,
		Description:  save.Description,
		Columns:      make([]tables.EvalDatasetVersionColumn, len(columns)),
		RowCount:     total,
		RowsAdded:    written - changed,
		RowsChanged:  changed,
		RowsRemoved:  retired - changed,
		RestoredFrom: save.RestoredFrom,
		CreatedBy:    save.CreatedBy,
	}
	for i, column := range columns {
		version.Columns[i] = tables.EvalDatasetVersionColumn{ID: column.ID, Name: column.Name, Type: column.Type, Position: i}
	}
	if err := w.tx.Create(version).Error; err != nil {
		return nil, err
	}
	w.dataset.HeadVersion = number
	w.dataset.DraftChanges = 0
	if !save.SkipPrune {
		if err := w.PruneVersions(); err != nil {
			return nil, err
		}
	}
	return version, nil
}

func (w *evalDatasetTx) Restore(number int) error {
	if w.dataset.DraftChanges != 0 {
		return ErrEvalDatasetDraftNotSaved
	}
	version, err := getEvalDatasetVersion(w.tx, w.dataset.ID, number)
	if err != nil {
		return err
	}
	draft := w.draft()

	// Current records the version does not have are the ones written after it.
	retired := w.tx.Model(&tables.TableEvalDatasetRow{}).
		Where("dataset_id = ? AND removed_in IS NULL AND added_in > ?", w.dataset.ID, number).
		UpdateColumn("removed_in", draft)
	if retired.Error != nil {
		return retired.Error
	}

	// Records the version has that were retired since come back as new records with
	// the same row ID. The originals stay, since older versions still read them.
	var readded int
	lastID := ""
	for {
		var batch []tables.TableEvalDatasetRow
		if err := w.tx.Where("dataset_id = ? AND added_in <= ? AND removed_in IS NOT NULL AND removed_in > ? AND removed_in <= ? AND id > ?",
			w.dataset.ID, number, number, draft, lastID).
			Order("id ASC").Limit(evalDatasetBatch).Find(&batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		lastID = batch[len(batch)-1].ID
		copies := make([]tables.TableEvalDatasetRow, len(batch))
		for i, record := range batch {
			copies[i] = tables.TableEvalDatasetRow{
				ID:        uuid.NewString(),
				DatasetID: record.DatasetID,
				RowID:     record.RowID,
				Seq:       record.Seq,
				CellsJSON: record.CellsJSON,
				AddedIn:   draft,
				CreatedAt: record.CreatedAt,
			}
		}
		if err := w.tx.Create(&copies).Error; err != nil {
			return err
		}
		readded += len(copies)
	}

	if err := w.tx.Where("dataset_id = ?", w.dataset.ID).Delete(&tables.TableEvalDatasetColumn{}).Error; err != nil {
		return err
	}
	if len(version.Columns) > 0 {
		now := time.Now()
		columns := make([]tables.TableEvalDatasetColumn, len(version.Columns))
		for i, column := range version.Columns {
			columns[i] = tables.TableEvalDatasetColumn{
				ID: column.ID, DatasetID: w.dataset.ID, Name: column.Name, Type: column.Type, Position: i,
				CreatedAt: now, UpdatedAt: now,
			}
		}
		if err := w.tx.Create(&columns).Error; err != nil {
			return err
		}
	}
	w.dataset.DraftChanges += int(retired.RowsAffected) + readded + 1
	return nil
}

func (w *evalDatasetTx) DeleteVersion(number int) error {
	if number == w.dataset.HeadVersion {
		return ErrEvalDatasetLatestVersion
	}
	result := w.tx.Where("dataset_id = ? AND number = ?", w.dataset.ID, number).Delete(&tables.TableEvalDatasetVersion{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return w.collectRows()
}

// UpdateMetadata writes the dataset's name, description, tags and version cap in the
// dataset's transaction, so a prune the new cap needs commits or rolls back with it.
func (w *evalDatasetTx) UpdateMetadata(name, description string, tags []string, maxVersions *int) error {
	var tagsJSON *string
	if tags != nil {
		data, err := json.Marshal(tags)
		if err != nil {
			return err
		}
		tagsJSON = new(string(data))
	}
	result := w.tx.Model(&tables.TableEvalDataset{}).Where("id = ?", w.dataset.ID).Updates(map[string]any{
		"name": name, "description": description, "tags_json": tagsJSON, "max_versions": maxVersions, "updated_at": time.Now(),
	})
	if result.Error != nil {
		if isUniqueConstraintError(result.Error) {
			return ErrEvalDatasetNameExists
		}
		return result.Error
	}
	w.dataset.Name, w.dataset.Description, w.dataset.Tags, w.dataset.MaxVersions = name, description, tags, maxVersions
	return nil
}

// ScanDraftCells reads the draft's rows a batch at a time, in sequence order.
func (w *evalDatasetTx) ScanDraftCells(fn func(rowID string, cells map[string]string) error) error {
	var after int64
	for {
		var rows []tables.TableEvalDatasetRow
		query := evalDatasetRowsAt(w.tx.Model(&tables.TableEvalDatasetRow{}).Where("evals_dataset_rows.dataset_id = ?", w.dataset.ID), 0)
		if err := query.Where("evals_dataset_rows.seq > ?", after).Order("evals_dataset_rows.seq ASC").Limit(500).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			if err := rows[i].DecodeCells(); err != nil {
				return err
			}
			if err := fn(rows[i].RowID, rows[i].Cells); err != nil {
				return err
			}
			after = rows[i].Seq
		}
		if len(rows) < 500 {
			return nil
		}
	}
}

func (w *evalDatasetTx) PruneVersions() error {
	if w.dataset.MaxVersions == nil || *w.dataset.MaxVersions <= 0 {
		return nil
	}
	var numbers []int
	if err := w.tx.Model(&tables.TableEvalDatasetVersion{}).Where("dataset_id = ?", w.dataset.ID).
		Order("number ASC").Pluck("number", &numbers).Error; err != nil {
		return err
	}
	excess := len(numbers) - *w.dataset.MaxVersions
	if excess <= 0 {
		return nil
	}
	// Numbers ascend, so the oldest excess versions are the ones up to the last of them.
	if err := w.tx.Where("dataset_id = ? AND number <= ?", w.dataset.ID, numbers[excess-1]).
		Delete(&tables.TableEvalDatasetVersion{}).Error; err != nil {
		return err
	}
	return w.collectRows()
}

// collectRows removes retired records that no remaining version reads. A record is
// read by the versions from AddedIn up to, but not including, RemovedIn. Split links of
// rows left with no record go with them.
func (w *evalDatasetTx) collectRows() error {
	collected := w.tx.Exec(`DELETE FROM evals_dataset_rows WHERE dataset_id = ? AND removed_in IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM evals_dataset_versions v WHERE v.dataset_id = evals_dataset_rows.dataset_id
			AND v.number >= evals_dataset_rows.added_in AND v.number < evals_dataset_rows.removed_in)`,
		w.dataset.ID)
	if collected.Error != nil || collected.RowsAffected == 0 {
		return collected.Error
	}
	return w.tx.Exec(`DELETE FROM evals_dataset_split_rows WHERE dataset_id = ?
		AND NOT EXISTS (SELECT 1 FROM evals_dataset_rows r WHERE r.dataset_id = evals_dataset_split_rows.dataset_id AND r.row_id = evals_dataset_split_rows.row_id)`,
		w.dataset.ID).Error
}
