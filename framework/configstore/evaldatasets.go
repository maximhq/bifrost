package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	Search    string // case-insensitive substring of any cell
	AfterSeq  int64  // rows after this sequence number
	Offset    int    // skip this many rows; for reading a page by row position
	Limit     int    // 0 means no limit
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
		quoted, err := json.Marshal(params.Tag)
		if err != nil {
			return nil, 0, err
		}
		query = query.Where(`evals_datasets.tags_json LIKE ? ESCAPE '\'`, "%"+likeEscaper.Replace(string(quoted))+"%")
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
	if err := s.checkEvalDatasetFolder(ctx, dataset.FolderID); err != nil {
		return err
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
	return s.DB().WithContext(ctx).Create(file).Error
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
	if err := s.checkEvalDatasetFolder(ctx, folder.ParentID); err != nil {
		return err
	}
	if err := s.DB().WithContext(ctx).Create(folder).Error; err != nil {
		if isUniqueConstraintError(err) {
			return ErrEvalDatasetFolderNameExists
		}
		return err
	}
	return nil
}

// UpdateEvalDatasetFolder changes a folder's name and description.
func (s *RDBConfigStore) UpdateEvalDatasetFolder(ctx context.Context, folder *tables.TableEvalDatasetFolder) error {
	if folder == nil {
		return errors.New("folder is required")
	}
	result := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetFolder{}).Where("id = ?", folder.ID).
		Updates(map[string]any{"name": folder.Name, "description": folder.Description, "updated_at": time.Now()})
	if result.Error != nil {
		if isUniqueConstraintError(result.Error) {
			return ErrEvalDatasetFolderNameExists
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return s.DB().WithContext(ctx).First(folder, "id = ?", folder.ID).Error
}

// SetEvalDatasetFolderParent moves a folder into another folder, or to the top level
// when parentID is nil. The caller checks that the move makes no cycle.
func (s *RDBConfigStore) SetEvalDatasetFolderParent(ctx context.Context, id string, parentID *string) error {
	if err := s.checkEvalDatasetFolder(ctx, parentID); err != nil {
		return err
	}
	result := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetFolder{}).Where("id = ?", id).
		Updates(map[string]any{"parent_id": parentID, "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteEvalDatasetFolder removes a folder. What it holds is kept: its datasets and
// its folders move up into the folder it was in, in the same transaction.
func (s *RDBConfigStore) DeleteEvalDatasetFolder(ctx context.Context, id string) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var folder tables.TableEvalDatasetFolder
		if err := tx.First(&folder, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
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

// checkEvalDatasetFolder reports ErrNotFound when folderID names a folder that does
// not exist. Nil means no folder, which is always fine.
func (s *RDBConfigStore) checkEvalDatasetFolder(ctx context.Context, folderID *string) error {
	if folderID == nil {
		return nil
	}
	var count int64
	if err := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetFolder{}).Where("id = ?", *folderID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// SetEvalDatasetFolder files a dataset under a folder, or under none when folderID
// is nil. It does not count as a change to the dataset's contents.
func (s *RDBConfigStore) SetEvalDatasetFolder(ctx context.Context, datasetID string, folderID *string) error {
	if err := s.checkEvalDatasetFolder(ctx, folderID); err != nil {
		return err
	}
	result := s.DB().WithContext(ctx).Model(&tables.TableEvalDataset{}).
		Where("id = ? AND deleted_at_unix_nano = 0", datasetID).UpdateColumn("folder_id", folderID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteEvalDataset soft-deletes a dataset, which frees its name, and removes its
// columns, rows, versions and splits.
func (s *RDBConfigStore) DeleteEvalDataset(ctx context.Context, id string) error {
	if _, err := s.GetEvalDataset(ctx, id); err != nil {
		return err
	}
	if runs, err := s.CountEvalRunsOnDataset(ctx, id, 0); err != nil {
		return err
	} else if runs > 0 {
		return ErrEvalDatasetHasRuns
	}
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

// evalDatasetRowSearch narrows a row query to rows with a cell that contains search,
// ignoring case. An empty search changes nothing.
func evalDatasetRowSearch(query *gorm.DB, search string) *gorm.DB {
	if search = strings.TrimSpace(search); search == "" {
		return query
	}
	return query.Where(`LOWER(evals_dataset_rows.cells_json) LIKE ? ESCAPE '\'`, "%"+likeEscaper.Replace(strings.ToLower(search))+"%")
}

// ListEvalDatasetRows returns a page of rows in sequence order. Paging by sequence
// number (AfterSeq) costs the same on a late page as on the first, and suits a reader
// that walks the whole dataset. Paging by Offset reads a page by row position, which
// is what a grid needs to jump anywhere.
func (s *RDBConfigStore) ListEvalDatasetRows(ctx context.Context, params EvalDatasetRowListParams) ([]tables.TableEvalDatasetRow, error) {
	query := evalDatasetRowSearch(s.evalDatasetRowQuery(ctx, params.DatasetID, params.Version, params.SplitID), params.Search)
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

// CountEvalDatasetRows counts the rows of a version (0 is the draft), optionally in
// one split and optionally only those a search matches.
func (s *RDBConfigStore) CountEvalDatasetRows(ctx context.Context, datasetID string, version int, splitID, search string) (int64, error) {
	var count int64
	err := evalDatasetRowSearch(s.evalDatasetRowQuery(ctx, datasetID, version, splitID), search).Count(&count).Error
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

	seqQuery := s.DB().WithContext(ctx).Model(&tables.TableEvalDatasetRow{}).
		Distinct("seq").Where("dataset_id = ? AND seq > ?", datasetID, afterSeq).Where(differs, args...).Order("seq ASC")
	if limit > 0 {
		seqQuery = seqQuery.Limit(limit)
	}
	var seqs []int64
	if err := seqQuery.Pluck("seq", &seqs).Error; err != nil {
		return nil, err
	}
	if len(seqs) == 0 {
		return nil, nil
	}
	var records []tables.TableEvalDatasetRow
	if err := s.DB().WithContext(ctx).Where("dataset_id = ? AND seq IN ?", datasetID, seqs).Where(differs, args...).
		Order("seq ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	changes := make([]EvalDatasetRowChange, 0, len(seqs))
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
	if _, err := s.GetEvalDataset(ctx, split.DatasetID); err != nil {
		return err
	}
	if split.ID == "" {
		split.ID = uuid.NewString()
	}
	if err := s.DB().WithContext(ctx).Create(split).Error; err != nil {
		if isUniqueConstraintError(err) {
			return ErrEvalDatasetSplitNameExists
		}
		return err
	}
	return nil
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
		if err := requireEvalDatasetSplit(tx, datasetID, splitID); err != nil {
			return err
		}
		n, err := addEvalDatasetSplitRows(tx, datasetID, splitID, rowIDs)
		added = n
		return err
	})
	return added, err
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
		if err := requireEvalDatasetSplit(tx, datasetID, splitID); err != nil {
			return err
		}
		for start := 0; start < len(rowIDs); start += evalDatasetBatch {
			chunk := rowIDs[start:min(start+evalDatasetBatch, len(rowIDs))]
			result := tx.Where("split_id = ? AND row_id IN ?", splitID, chunk).Delete(&tables.TableEvalDatasetSplitRow{})
			if result.Error != nil {
				return result.Error
			}
			removed += result.RowsAffected
		}
		return nil
	})
	return removed, err
}

// ============================================================================
// Evals - Dataset writes
// ============================================================================

// WithEvalDataset runs fn with the write side of one dataset, in a transaction that
// holds the dataset's lock. The dataset's counters are stored when fn returns nil;
// any error rolls every write back.
func (s *RDBConfigStore) WithEvalDataset(ctx context.Context, datasetID string, fn func(tx EvalDatasetTx) error) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Writing the dataset row first takes its row lock on Postgres and the write
		// lock on SQLite, so the read below is of a dataset no other writer can change.
		touched := tx.Model(&tables.TableEvalDataset{}).
			Where("id = ? AND deleted_at_unix_nano = 0", datasetID).
			UpdateColumn("updated_at", time.Now())
		if touched.Error != nil {
			return touched.Error
		}
		if touched.RowsAffected == 0 {
			return ErrNotFound
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
	if err := w.PruneVersions(); err != nil {
		return nil, err
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
	// A version that a test run reads is that run's record of what it measured.
	inUse, err := evalRunDatasetVersions(w.tx, w.dataset.ID)
	if err != nil {
		return err
	}
	if slices.Contains(inUse, number) {
		return ErrEvalDatasetVersionInUse
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
	// Versions that test runs read are stepped over: they stay until the runs go.
	inUse, err := evalRunDatasetVersions(w.tx, w.dataset.ID)
	if err != nil {
		return err
	}
	prune := slices.DeleteFunc(slices.Clone(numbers[:excess]), func(number int) bool { return slices.Contains(inUse, number) })
	if len(prune) == 0 {
		return nil
	}
	if err := w.tx.Where("dataset_id = ? AND number IN ?", w.dataset.ID, prune).
		Delete(&tables.TableEvalDatasetVersion{}).Error; err != nil {
		return err
	}
	return w.collectRows()
}

// collectRows removes retired records that no remaining version reads. A record is
// read by the versions from AddedIn up to, but not including, RemovedIn.
func (w *evalDatasetTx) collectRows() error {
	return w.tx.Exec(`DELETE FROM evals_dataset_rows WHERE dataset_id = ? AND removed_in IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM evals_dataset_versions v WHERE v.dataset_id = evals_dataset_rows.dataset_id
			AND v.number >= evals_dataset_rows.added_in AND v.number < evals_dataset_rows.removed_in)`,
		w.dataset.ID).Error
}
