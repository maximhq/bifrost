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

// grantAttributionTarget points at the attribution fields of one row type, so LLM rows and relay
// rows take the same grant attribution through one implementation.
type grantAttributionTarget struct {
	virtualKeyID, virtualKeyName, teamID, teamName, customerID, customerName           **string
	businessUnitID, businessUnitName, projectID, projectName, userID, userName         **string
	userEmail                                                                          **string
	teamIDs, teamNames, customerIDs, customerNames, businessUnitIDs, businessUnitNames *[]string
	// The serialized set columns: a set already stored on the row is never replaced.
	storedTeamIDs, storedTeamNames, storedCustomerIDs, storedCustomerNames, storedBusinessUnitIDs, storedBusinessUnitNames *string
}

// applyGrantAttribution supplies identity fields absent on a fresh realtime turn context.
// Access is cached on the session's grant, so scope stamping need not run again.
func applyGrantAttribution(ctx *schemas.BifrostContext, entry *logstore.Log) {
	applyGrantAttributionTo(ctx, grantAttributionTarget{
		virtualKeyID: &entry.VirtualKeyID, virtualKeyName: &entry.VirtualKeyName,
		teamID: &entry.TeamID, teamName: &entry.TeamName,
		customerID: &entry.CustomerID, customerName: &entry.CustomerName,
		businessUnitID: &entry.BusinessUnitID, businessUnitName: &entry.BusinessUnitName,
		projectID: &entry.ProjectID, projectName: &entry.ProjectName,
		userID: &entry.UserID, userName: &entry.UserName, userEmail: &entry.UserEmail,
		teamIDs: &entry.TeamIDsParsed, teamNames: &entry.TeamNamesParsed,
		customerIDs: &entry.CustomerIDsParsed, customerNames: &entry.CustomerNamesParsed,
		businessUnitIDs: &entry.BusinessUnitIDsParsed, businessUnitNames: &entry.BusinessUnitNamesParsed,
		storedTeamIDs: entry.TeamIDs, storedTeamNames: entry.TeamNames,
		storedCustomerIDs: entry.CustomerIDs, storedCustomerNames: entry.CustomerNames,
		storedBusinessUnitIDs: entry.BusinessUnitIDs, storedBusinessUnitNames: entry.BusinessUnitNames,
	})
}

// applyRelayGrantAttribution gives a relay row the grant attribution an LLM row gets, so every
// team, customer and business unit the call is attributed to is recorded, not only the first.
func applyRelayGrantAttribution(ctx *schemas.BifrostContext, entry *logstore.RelayLog) {
	applyGrantAttributionTo(ctx, grantAttributionTarget{
		virtualKeyID: &entry.VirtualKeyID, virtualKeyName: &entry.VirtualKeyName,
		teamID: &entry.TeamID, teamName: &entry.TeamName,
		customerID: &entry.CustomerID, customerName: &entry.CustomerName,
		businessUnitID: &entry.BusinessUnitID, businessUnitName: &entry.BusinessUnitName,
		projectID: &entry.ProjectID, projectName: &entry.ProjectName,
		userID: &entry.UserID, userName: &entry.UserName, userEmail: &entry.UserEmail,
		teamIDs: &entry.TeamIDsParsed, teamNames: &entry.TeamNamesParsed,
		customerIDs: &entry.CustomerIDsParsed, customerNames: &entry.CustomerNamesParsed,
		businessUnitIDs: &entry.BusinessUnitIDsParsed, businessUnitNames: &entry.BusinessUnitNamesParsed,
		storedTeamIDs: entry.TeamIDs, storedTeamNames: entry.TeamNames,
		storedCustomerIDs: entry.CustomerIDs, storedCustomerNames: entry.CustomerNames,
		storedBusinessUnitIDs: entry.BusinessUnitIDs, storedBusinessUnitNames: entry.BusinessUnitNames,
	})
}

func applyGrantAttributionTo(ctx *schemas.BifrostContext, t grantAttributionTarget) {
	if ctx.Grant() == nil || ctx.Grant().Identity() == nil {
		return
	}
	identity := ctx.Grant().Identity()
	fill := func(id, name **string, ref *schemas.EntityRef) {
		if ref != nil && *id == nil {
			*id, *name = schemas.Ptr(ref.ID), schemas.Ptr(ref.Name)
		}
	}
	first := func(refs []schemas.EntityRef) *schemas.EntityRef {
		if len(refs) == 0 {
			return nil
		}
		return &refs[0]
	}
	fill(t.virtualKeyID, t.virtualKeyName, identity.VirtualKey())
	fill(t.teamID, t.teamName, first(identity.Teams()))
	fill(t.customerID, t.customerName, first(identity.Customers()))
	fill(t.businessUnitID, t.businessUnitName, first(identity.BusinessUnits()))
	fill(t.projectID, t.projectName, identity.Project())
	if user := identity.User(); user != nil && *t.userID == nil {
		*t.userID, *t.userName = schemas.Ptr(user.ID), schemas.Ptr(user.Name)
		if user.Email != "" {
			*t.userEmail = schemas.Ptr(clampString(user.Email, maxPersistedUserEmailLen))
		}
	}
	set := func(ids, names *[]string, storedIDs, storedNames *string, refs []schemas.EntityRef) {
		if len(refs) == 0 {
			return
		}
		if len(*ids) == 0 && storedIDs == nil {
			*ids = make([]string, len(refs))
			for i, ref := range refs {
				(*ids)[i] = ref.ID
			}
		}
		if len(*names) == 0 && storedNames == nil {
			*names = make([]string, len(refs))
			for i, ref := range refs {
				(*names)[i] = ref.Name
			}
		}
	}
	set(t.teamIDs, t.teamNames, t.storedTeamIDs, t.storedTeamNames, identity.Teams())
	set(t.customerIDs, t.customerNames, t.storedCustomerIDs, t.storedCustomerNames, identity.Customers())
	set(t.businessUnitIDs, t.businessUnitNames, t.storedBusinessUnitIDs, t.storedBusinessUnitNames, identity.BusinessUnits())
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
		MetadataFilters:     map[string][]string{realtimeConnectionMetadata: {update.connectionID}},
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
