package logging

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// applyGrantAttribution supplies identity fields absent on a fresh realtime turn context.
// Access is cached on the session's grant, so scope stamping need not run again.
func applyGrantAttribution(ctx *schemas.BifrostContext, entry *logstore.Log) {
	if ctx.Grant() == nil || ctx.Grant().Identity() == nil {
		return
	}
	identity := ctx.Grant().Identity()
	if vk := identity.VirtualKey(); vk != nil && entry.VirtualKeyID == nil {
		entry.VirtualKeyID, entry.VirtualKeyName = schemas.Ptr(vk.ID), schemas.Ptr(vk.Name)
	}
	if teams := identity.Teams(); len(teams) > 0 && entry.TeamID == nil {
		entry.TeamID, entry.TeamName = schemas.Ptr(teams[0].ID), schemas.Ptr(teams[0].Name)
	}
	if customers := identity.Customers(); len(customers) > 0 && entry.CustomerID == nil {
		entry.CustomerID, entry.CustomerName = schemas.Ptr(customers[0].ID), schemas.Ptr(customers[0].Name)
	}
	if units := identity.BusinessUnits(); len(units) > 0 && entry.BusinessUnitID == nil {
		entry.BusinessUnitID, entry.BusinessUnitName = schemas.Ptr(units[0].ID), schemas.Ptr(units[0].Name)
	}
	if project := identity.Project(); project != nil && entry.ProjectID == nil {
		entry.ProjectID, entry.ProjectName = schemas.Ptr(project.ID), schemas.Ptr(project.Name)
	}
	if user := identity.User(); user != nil && entry.UserID == nil {
		entry.UserID, entry.UserName = schemas.Ptr(user.ID), schemas.Ptr(user.Name)
	}
	teams := identity.Teams()
	if len(entry.TeamIDsParsed) == 0 && entry.TeamIDs == nil && len(teams) > 0 {
		entry.TeamIDsParsed = make([]string, len(teams))
		for i, team := range teams {
			entry.TeamIDsParsed[i] = team.ID
		}
	}
	if len(entry.TeamNamesParsed) == 0 && entry.TeamNames == nil && len(teams) > 0 {
		entry.TeamNamesParsed = make([]string, len(teams))
		for i, team := range teams {
			entry.TeamNamesParsed[i] = team.Name
		}
	}
	customers := identity.Customers()
	if len(entry.CustomerIDsParsed) == 0 && entry.CustomerIDs == nil && len(customers) > 0 {
		entry.CustomerIDsParsed = make([]string, len(customers))
		for i, customer := range customers {
			entry.CustomerIDsParsed[i] = customer.ID
		}
	}
	if len(entry.CustomerNamesParsed) == 0 && entry.CustomerNames == nil && len(customers) > 0 {
		entry.CustomerNamesParsed = make([]string, len(customers))
		for i, customer := range customers {
			entry.CustomerNamesParsed[i] = customer.Name
		}
	}
	units := identity.BusinessUnits()
	if len(entry.BusinessUnitIDsParsed) == 0 && entry.BusinessUnitIDs == nil && len(units) > 0 {
		entry.BusinessUnitIDsParsed = make([]string, len(units))
		for i, unit := range units {
			entry.BusinessUnitIDsParsed[i] = unit.ID
		}
	}
	if len(entry.BusinessUnitNamesParsed) == 0 && entry.BusinessUnitNames == nil && len(units) > 0 {
		entry.BusinessUnitNamesParsed = make([]string, len(units))
		for i, unit := range units {
			entry.BusinessUnitNamesParsed[i] = unit.Name
		}
	}
}

const realtimeInputItemsMetadata = "realtime_input_items"
const realtimeConnectionMetadata = "realtime_connection_id"

// realtimeInputItem records the persisted input position a provider item belongs to.
type realtimeInputItem struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
}

// realtimeInputUpdate is a queue-owned event, not session-retained association state.
type realtimeInputUpdate struct {
	sessionStartedAt time.Time
	connectionID     string
	itemID           string
	transcript       string
	raw              string
	attempts         int
}

// recordRealtimeInputItems persists missing input item IDs beside their exact message positions.
// Associations are omitted if raw positions do not match the existing merged history.
func recordRealtimeInputItems(entry *logstore.Log, raw string) {
	delete(entry.MetadataParsed, realtimeInputItemsMetadata)
	delete(entry.MetadataParsed, "realtime_input_count")
	delete(entry.MetadataParsed, realtimeConnectionMetadata)
	var messages []schemas.ChatMessage
	var items []realtimeInputItem
	for _, part := range strings.Split(raw, "\n\n") {
		input := extractRealtimeInputHistoryFromRawRequest(part)
		if len(input) == 0 {
			continue
		}
		event, err := schemas.ParseRealtimeEvent([]byte(part))
		if err != nil || event == nil {
			continue
		}
		if len(input) == 1 && realtimeInputHistoryMessageContent(input[0]) == realtimeMissingTranscriptText && event.Item != nil && event.Item.ID != "" {
			items = append(items, realtimeInputItem{ID: event.Item.ID, Index: len(messages)})
		}
		messages = append(messages, input...)
	}
	if len(items) == 0 {
		return
	}
	// Correction indexes are valid only if raw events align with the merged history.
	// Keep the history intact and skip best-effort correction when it is ambiguous.
	if len(entry.InputHistoryParsed) != len(messages) {
		return
	}
	for i, message := range messages {
		existing := entry.InputHistoryParsed[i]
		if existing.Role != message.Role ||
			strings.TrimSpace(realtimeInputHistoryMessageContent(existing)) != strings.TrimSpace(realtimeInputHistoryMessageContent(message)) ||
			strings.TrimSpace(realtimeInputHistoryToolCallID(existing)) != strings.TrimSpace(realtimeInputHistoryToolCallID(message)) {
			return
		}
	}
	if entry.MetadataParsed == nil {
		entry.MetadataParsed = make(map[string]interface{})
	}
	entry.MetadataParsed[realtimeInputItemsMetadata] = items
	entry.MetadataParsed["realtime_input_count"] = len(messages)
}

// prepareRealtimeTranscriptLog records the transport connection independently of caller labels.
// Content-disabled and redacted rows never expose an input correction target.
func (p *LoggerPlugin) prepareRealtimeTranscriptLog(ctx *schemas.BifrostContext, entry *logstore.Log) {
	if entry.Object != "realtime.turn" || entry.MetadataParsed == nil {
		return
	}
	connectionID, _ := ctx.Value(schemas.BifrostContextKeyRealtimeConnectionID).(string)
	_, redacted := schemas.RedactionDataFromContext(ctx)
	if connectionID == "" || redacted || !p.resolveContentPolicy(ctx).visible() {
		delete(entry.MetadataParsed, realtimeInputItemsMetadata)
		delete(entry.MetadataParsed, "realtime_input_count")
		delete(entry.MetadataParsed, realtimeConnectionMetadata)
		return
	}
	if _, ok := entry.MetadataParsed[realtimeInputItemsMetadata]; ok {
		entry.MetadataParsed[realtimeConnectionMetadata] = connectionID
	}
}

// EnqueueRealtimeTranscript hands a late transcript to the existing bounded log writer.
// It does no database work and starts no goroutine on the transport path.
// The session start bounds the lookup without assuming a maximum session duration.
func (p *LoggerPlugin) EnqueueRealtimeTranscript(connectionID string, sessionStartedAt time.Time, itemID, transcript, raw string) {
	if connectionID == "" || sessionStartedAt.IsZero() || itemID == "" || strings.TrimSpace(transcript) == "" {
		return
	}
	p.enqueueRealtimeInputUpdate(&realtimeInputUpdate{connectionID: connectionID, sessionStartedAt: sessionStartedAt, itemID: itemID, transcript: transcript, raw: raw})
}

// enqueueRealtimeInputUpdate shares normal logging backpressure and shutdown behavior.
func (p *LoggerPlugin) enqueueRealtimeInputUpdate(update *realtimeInputUpdate) {
	if p.closed.Load() {
		return
	}
	defer func() {
		if recover() != nil {
			p.countDrop()
		}
	}()
	select {
	case p.writeQueue <- &writeQueueEntry{realtimeInput: update}:
	default:
		p.countDrop()
	}
}

// writeRealtimeInputUpdate finds the original persisted turn using connection and item IDs.
// It replaces one input position without changing other input, accounting or output fields.
func (p *LoggerPlugin) writeRealtimeInputUpdate(ctx context.Context, update *realtimeInputUpdate) error {
	if update.sessionStartedAt.IsZero() {
		return errors.New("realtime transcript correction requires a session start time")
	}
	found, err := p.store.SearchLogs(ctx, logstore.SearchFilters{
		StartTime:           &update.sessionStartedAt,
		MetadataFilters:     map[string]string{realtimeConnectionMetadata: update.connectionID},
		RealtimeInputItemID: update.itemID,
	}, logstore.PaginationOptions{Limit: 2, SkipCount: true})
	if err != nil {
		return err
	}
	if len(found.Logs) == 0 {
		return logstore.ErrNotFound
	}
	if len(found.Logs) != 1 {
		return errors.New("realtime input belongs to multiple saved turns")
	}
	entry, err := p.store.FindByID(ctx, found.Logs[0].ID)
	if err != nil {
		return err
	}
	if entry.ContentHidden {
		return nil
	}
	encoded, err := sonic.Marshal(entry.MetadataParsed[realtimeInputItemsMetadata])
	if err != nil {
		return err
	}
	var items []realtimeInputItem
	if err := sonic.Unmarshal(encoded, &items); err != nil {
		return err
	}
	var count int
	countJSON, _ := sonic.Marshal(entry.MetadataParsed["realtime_input_count"])
	if err := sonic.Unmarshal(countJSON, &count); err != nil {
		return err
	}
	if len(entry.InputHistoryParsed) != count {
		return logstore.ErrNotFound
	}
	index := -1
	for _, item := range items {
		if item.ID == update.itemID {
			index = item.Index
			break
		}
	}
	if index < 0 || index >= len(entry.InputHistoryParsed) {
		return errors.New("realtime input position missing from saved payload")
	}
	message := entry.InputHistoryParsed[index]
	if message.Role != schemas.ChatMessageRoleUser {
		return errors.New("realtime input position is not a user message")
	}
	if realtimeInputHistoryMessageContent(message) == update.transcript {
		return nil
	}
	if realtimeInputHistoryMessageContent(message) != realtimeMissingTranscriptText {
		return nil
	}
	message.Content = &schemas.ChatMessageContent{ContentStr: schemas.Ptr(update.transcript)}
	entry.InputHistoryParsed[index] = message
	correction := &logstore.Log{ID: entry.ID, InputHistoryParsed: entry.InputHistoryParsed, ContentHidden: entry.ContentHidden, RealtimeInputCorrection: &logstore.RealtimeInputCorrection{Index: index, ItemID: update.itemID, Transcript: update.transcript, Summary: entry.BuildContentSummary()}}
	if entry.RawRequest != "" {
		correction.RealtimeInputCorrection.Raw = update.raw
		parts := strings.Split(entry.RawRequest, "\n\n")
		for i, part := range parts {
			event, err := schemas.ParseRealtimeEvent([]byte(part))
			if err == nil && event != nil && event.Item != nil && event.Item.ID == update.itemID {
				parts[i] = update.raw
			}
		}
		correction.RawRequest = strings.Join(parts, "\n\n")
	}
	return p.store.BatchUpdate(ctx, []*logstore.Log{correction})
}
