package registry

import (
	"context"
	"fmt"
	"strconv"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// Prompt is one saved prompt as the prompts plugin reads it: its ID and its name. Its versions are
// PromptVersion records.
type Prompt struct {
	ID   string
	Name string
}

// PromptID is a prompt record's primary key.
func PromptID(p *Prompt) string { return p.ID }

// PromptsPage reads up to limit prompts with IDs after the cursor, in ID order.
func PromptsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*Prompt, string, error) {
	rows, err := readPrompts(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("id > ?", after).Order("id").Limit(limit) })
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID, nil
}

// PromptsByID reads the prompts with these IDs; one with no row is left out.
func PromptsByID(ctx context.Context, db *gorm.DB, ids []string) ([]*Prompt, error) {
	return readPrompts(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "id", ids)) })
}

// readPrompts reads the prompts where selects.
func readPrompts(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*Prompt, error) {
	var rows []tables.TablePrompt
	if err := where(db.WithContext(ctx).Select("id", "name")).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*Prompt, 0, len(rows))
	for i := range rows {
		out = append(out, &Prompt{ID: rows[i].ID, Name: rows[i].Name})
	}
	return out, nil
}

// PromptVersion is the record of one saved prompt version: the row's columns, embedded, with its
// messages, as the prompts plugin injects it. Records are shared: readers never change them. A
// version stored in a form this version cannot read fails every read of it with
// configstore.ErrConfigUnreadable naming it: the boot load and a refresh of the whole kind fail,
// and a refresh of it or of its prompt by ID fails and keeps the records there.
type PromptVersion struct {
	tables.TablePromptVersion
}

// Indexes of the prompt version collection: PromptVersionByNumber finds a version by
// PromptVersionKey, and PromptVersionLatest finds a prompt's latest version by the prompt's ID. The
// database keeps a prompt's version numbers unique but not its latest flag, so PromptVersionLatest
// is a multi index whose GetBy answers with the lowest ID, as a lookup of the one latest row did.
const (
	PromptVersionByNumber = "number"
	PromptVersionLatest   = "latest"
)

// PromptVersionID is a prompt version record's primary key.
func PromptVersionID(v *PromptVersion) string { return NumericID(v.ID) }

// PromptVersionPrompt is the prompt a version belongs to.
func PromptVersionPrompt(v *PromptVersion) string { return v.PromptID }

// PromptVersionKey is the key of a prompt's version number in PromptVersionByNumber.
func PromptVersionKey(promptID string, number int) string {
	return promptID + ":" + strconv.Itoa(number)
}

// PromptVersionNumberKey is a version's key in PromptVersionByNumber.
func PromptVersionNumberKey(v *PromptVersion) string {
	return PromptVersionKey(v.PromptID, v.VersionNumber)
}

// PromptVersionLatestKey is a version's key in PromptVersionLatest: its prompt's ID when it is the
// latest version, and none otherwise.
func PromptVersionLatestKey(v *PromptVersion) []string {
	if !v.IsLatest {
		return nil
	}
	return []string{v.PromptID}
}

// PromptVersionsPage reads up to limit versions with IDs after the cursor, in ID order.
func PromptVersionsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*PromptVersion, string, error) {
	var from uint64
	if after != "" {
		var err error
		if from, err = strconv.ParseUint(after, 10, 64); err != nil {
			return nil, "", err
		}
	}
	rows, err := readPromptVersions(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("id > ?", from).Order("id").Limit(limit) })
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, PromptVersionID(rows[len(rows)-1]), nil
}

// PromptVersionsByID reads the versions with these IDs; one with no row is left out, and one this
// version cannot read fails the read.
func PromptVersionsByID(ctx context.Context, db *gorm.DB, ids []string) ([]*PromptVersion, error) {
	rowIDs := make([]uint, 0, len(ids))
	for _, id := range ids {
		if n, err := strconv.ParseUint(id, 10, strconv.IntSize); err == nil {
			rowIDs = append(rowIDs, uint(n))
		}
	}
	if len(rowIDs) == 0 {
		return nil, nil
	}
	return readPromptVersions(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InUints(q, "id", rowIDs)) })
}

// PromptVersionsByPrompts reads every version of these prompts, as a refresh of a prompt does. A
// version this version cannot read fails the read.
func PromptVersionsByPrompts(ctx context.Context, db *gorm.DB, promptIDs []string) ([]*PromptVersion, error) {
	return readPromptVersions(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "prompt_id", promptIDs)) })
}

// readPromptVersions reads the versions where selects, each with its messages in order. A version
// whose row or messages do not decode fails the read, naming it. The messages are read by the
// versions' row IDs, bound as one argument, so a prompt with any number of versions stays within the
// statement parameter limit.
func readPromptVersions(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*PromptVersion, error) {
	raw := db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true})
	var rows []tables.TablePromptVersion
	if err := where(raw).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uint, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	var messages []tables.TablePromptVersionMessage
	if err := raw.Where(queryscope.InUints(raw, "version_id", ids)).Order("version_id, order_index ASC").Find(&messages).Error; err != nil {
		return nil, err
	}
	byVersion := make(map[uint][]tables.TablePromptVersionMessage, len(rows))
	for _, m := range messages {
		byVersion[m.VersionID] = append(byVersion[m.VersionID], m)
	}
	out := make([]*PromptVersion, 0, len(rows))
	for i := range rows {
		row := rows[i]
		row.Messages = byVersion[row.ID]
		if err := decodePromptVersion(&row); err != nil {
			return nil, fmt.Errorf("prompt version %d: %w: %w", row.ID, configstore.ErrConfigUnreadable, err)
		}
		out = append(out, &PromptVersion{TablePromptVersion: row})
	}
	return out, nil
}

// decodePromptVersion decodes a version's stored JSON and its messages', as loading them with hooks
// would.
func decodePromptVersion(row *tables.TablePromptVersion) error {
	if err := row.AfterFind(nil); err != nil {
		return err
	}
	for i := range row.Messages {
		if err := row.Messages[i].AfterFind(nil); err != nil {
			return err
		}
	}
	return nil
}
