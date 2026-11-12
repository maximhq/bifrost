package heimdall

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

const (
	// maxSessionRows bounds the rows read for one session. A session that fills it is too long anyway.
	maxSessionRows = 1000
	// listMaxPages bounds how many pages ListSessions reads to fill one page of sessions.
	listMaxPages = 5
	listPageSize = 50
)

// LogReader is the part of logstore.LogStore the reconstructor uses; any logstore.LogStore satisfies it.
// GetSessionLogs is left out on purpose: it filters on parent_request_id and finds nothing for a session.
type LogReader interface {
	SearchLogs(ctx context.Context, filters logstore.SearchFilters, pagination logstore.PaginationOptions) (*logstore.SearchResult, error)
	FindByID(ctx context.Context, id string) (*logstore.Log, error)
	SearchMCPToolLogs(ctx context.Context, filters logstore.MCPToolLogSearchFilters, pagination logstore.PaginationOptions) (*logstore.MCPToolLogSearchResult, error)
}

// ReconOptions tune what counts as a replayable session.
type ReconOptions struct {
	MaxSteps int // most steps a session may have; 0 takes the default
	// Baseline, when set, rejects sessions that did not use this route.
	Baseline *Route
	// InjectedTools reports whether a provider has injected tools configured now.
	InjectedTools func(schemas.ModelProvider) bool
}

// Reconstructor turns logged rows into session traces.
type Reconstructor struct {
	logs LogReader
	opts ReconOptions
}

// NewReconstructor reads sessions from a log store.
func NewReconstructor(logs LogReader, opts ReconOptions) *Reconstructor {
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = defaultMaxSteps
	}
	return &Reconstructor{logs: logs, opts: opts}
}

// SessionQuery selects sessions to list.
type SessionQuery struct {
	StartTime *time.Time
	EndTime   *time.Time
	Provider  string
	Model     string
	Limit     int
	Offset    int
}

// SessionSummary is one line of the session list. It carries no eligibility, which needs full rows.
type SessionSummary struct {
	SessionID string    `json:"session_id"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	StartedAt time.Time `json:"started_at"`
	Turns     int       `json:"turns"`
	TotalCost float64   `json:"total_cost"`
}

// SessionPage is one page of sessions.
type SessionPage struct {
	Sessions []SessionSummary `json:"sessions"`
	Count    int              `json:"count"`
	Limit    int              `json:"limit"`
	Offset   int              `json:"offset"`
}

// ListSessions lists sessions newest first, one line each. Rows without a session ID are skipped, and
// pages are read until the requested one is full, up to a cap.
func (r *Reconstructor) ListSessions(ctx context.Context, q SessionQuery) (*SessionPage, error) {
	if q.Limit <= 0 {
		q.Limit = listPageSize
	}
	filters := logstore.SearchFilters{
		StartTime:     q.StartTime,
		EndTime:       q.EndTime,
		RootsOnly:     true,
		GroupSessions: true,
	}
	if q.Provider != "" {
		filters.Providers = []string{q.Provider}
	}
	if q.Model != "" {
		filters.Models = []string{q.Model}
	}
	want := q.Offset + q.Limit
	var found []SessionSummary
	for page := 0; page < listMaxPages && len(found) < want; page++ {
		res, err := r.logs.SearchLogs(ctx, filters, logstore.PaginationOptions{
			Limit:     listPageSize,
			Offset:    page * listPageSize,
			SortBy:    string(logstore.SortByTimestamp),
			Order:     string(logstore.SortDesc),
			SkipCount: true,
		})
		if err != nil {
			return nil, fmt.Errorf("heimdall: list sessions: %w", err)
		}
		for i := range res.Logs {
			row := &res.Logs[i]
			if row.SessionID == nil || *row.SessionID == "" {
				continue
			}
			found = append(found, SessionSummary{
				SessionID: *row.SessionID,
				Provider:  row.Provider,
				Model:     row.Model,
				StartedAt: row.Timestamp,
				Turns:     1 + int(row.SessionChildCount),
				TotalCost: row.SessionTotalCost,
			})
		}
		if len(res.Logs) < listPageSize {
			break
		}
	}
	out := &SessionPage{Sessions: []SessionSummary{}, Limit: q.Limit, Offset: q.Offset}
	if q.Offset < len(found) {
		out.Sessions = found[q.Offset:min(want, len(found))]
	}
	out.Count = len(out.Sessions)
	return out, nil
}

// Summary describes one session without loading its content, from the list columns alone. found is false
// when no row carries that session ID.
func (r *Reconstructor) Summary(ctx context.Context, sessionID string) (summary SessionSummary, found bool, err error) {
	res, err := r.logs.SearchLogs(ctx, logstore.SearchFilters{SessionID: sessionID}, logstore.PaginationOptions{
		Limit:     maxSessionRows,
		SortBy:    string(logstore.SortByTimestamp),
		Order:     string(logstore.SortAsc),
		SkipCount: true,
	})
	if err != nil {
		return SessionSummary{}, false, fmt.Errorf("heimdall: search session %q: %w", sessionID, err)
	}
	for i := range res.Logs {
		row := &res.Logs[i]
		if row.ParentRequestID != nil && *row.ParentRequestID != "" {
			continue
		}
		if !found {
			summary = SessionSummary{SessionID: sessionID, Provider: row.Provider, Model: row.Model, StartedAt: row.Timestamp}
			found = true
		}
		summary.Turns++
		if row.Cost != nil {
			summary.TotalCost += *row.Cost
		}
	}
	return summary, found, nil
}

// Build rebuilds one session. The trace is nil unless the session is eligible. The error is for
// store failures; an ineligible session is an answer, not an error.
func (r *Reconstructor) Build(ctx context.Context, sessionID string) (*SessionTrace, Eligibility, error) {
	res, err := r.logs.SearchLogs(ctx, logstore.SearchFilters{SessionID: sessionID}, logstore.PaginationOptions{
		Limit:     maxSessionRows,
		SortBy:    string(logstore.SortByTimestamp),
		Order:     string(logstore.SortAsc),
		SkipCount: true,
	})
	if err != nil {
		return nil, Eligibility{}, fmt.Errorf("heimdall: search session %q: %w", sessionID, err)
	}

	// Fallback attempts are child rows. A failed turn the client retried is replaced by the retry.
	var top, kept []*logstore.Log
	for i := range res.Logs {
		row := &res.Logs[i]
		if row.ParentRequestID != nil && *row.ParentRequestID != "" {
			continue
		}
		top = append(top, row)
		if row.Status == statusSuccess {
			kept = append(kept, row)
		}
	}
	if len(top) == 0 {
		return nil, ineligible(CodeUnknownSession, "no rows for session %q", sessionID), nil
	}
	last := top[len(top)-1]

	// Everything decided from list columns comes before any payload is read, so a hidden row's
	// content is never loaded.
	for _, row := range append(slices.Clone(kept), last) {
		if row.ContentHidden {
			return nil, ineligible(CodeContentHidden, "row %s has hidden content", row.ID), nil
		}
	}
	if last.Status != statusSuccess {
		return nil, ineligible(CodeIncomplete, "last row %s has status %q", last.ID, last.Status), nil
	}
	for _, row := range kept {
		if code := checkRow(row); code != "" {
			return nil, ineligible(code, "row %s", row.ID), nil
		}
	}
	route := Route{Provider: schemas.ModelProvider(kept[0].Provider), Model: kept[0].Model}
	for _, row := range kept[1:] {
		if row.Provider != string(route.Provider) || row.Model != route.Model {
			return nil, ineligible(CodeMixedRoute, "rows used %s/%s and %s/%s", route.Provider, route.Model, row.Provider, row.Model), nil
		}
	}
	if b := r.opts.Baseline; b != nil && *b != route {
		return nil, ineligible(CodeRouteMismatch, "session used %s/%s, the baseline is %s/%s", route.Provider, route.Model, b.Provider, b.Model), nil
	}
	if len(res.Logs) >= maxSessionRows || len(kept) > r.opts.MaxSteps+1 {
		return nil, ineligible(CodeTooLong, "%d rows for a limit of %d steps", len(kept), r.opts.MaxSteps), nil
	}

	rows := make([]*logstore.Log, len(kept))
	for i, listRow := range kept {
		full, err := r.logs.FindByID(ctx, listRow.ID)
		if err != nil {
			return nil, Eligibility{}, fmt.Errorf("heimdall: load row %s: %w", listRow.ID, err)
		}
		if full == nil {
			return nil, Eligibility{}, fmt.Errorf("heimdall: row %s vanished", listRow.ID)
		}
		if code := checkRow(full); code != "" {
			return nil, ineligible(code, "row %s", full.ID), nil
		}
		if full.OutputMessageParsed == nil {
			return nil, ineligible(CodeIncomplete, "row %s has no output message", full.ID), nil
		}
		rows[i] = full
	}

	// Rows that share a timestamp are ordered by how much history they carry: each turn's history
	// extends the one before it, so message order decides, not the clock.
	slices.SortStableFunc(rows, func(a, b *logstore.Log) int {
		if c := a.Timestamp.Compare(b.Timestamp); c != 0 {
			return c
		}
		return len(a.InputHistoryParsed) - len(b.InputHistoryParsed)
	})

	if code, msg := checkTools(rows); code != "" {
		return nil, ineligible(code, "%s", msg), nil
	}
	if code, msg := checkPrefixes(rows); code != "" {
		return nil, ineligible(code, "%s", msg), nil
	}

	lastRow := rows[len(rows)-1]
	transcript := append(slices.Clone(lastRow.InputHistoryParsed), *lastRow.OutputMessageParsed)
	split, elig := splitTranscript(transcript)
	if !elig.Eligible() {
		return nil, elig, nil
	}
	if len(split.recorded) == 0 {
		return nil, ineligible(CodeNoToolCalls, "the session called no tools"), nil
	}
	if len(split.actions) > r.opts.MaxSteps {
		return nil, ineligible(CodeTooLong, "%d steps for a limit of %d", len(split.actions), r.opts.MaxSteps), nil
	}
	if r.opts.InjectedTools != nil && r.opts.InjectedTools(route.Provider) {
		return nil, ineligible(CodeInjectedTool, "provider %s has injected tools configured", route.Provider), nil
	}
	runs, err := r.logs.SearchMCPToolLogs(ctx, logstore.MCPToolLogSearchFilters{SessionID: sessionID}, logstore.PaginationOptions{Limit: 1})
	if err != nil {
		return nil, Eligibility{}, fmt.Errorf("heimdall: count tool runs for %q: %w", sessionID, err)
	}
	if int(runs.Pagination.TotalCount) > len(split.recorded) {
		return nil, ineligible(CodeInjectedTool, "%d tool runs logged, but the messages hold %d", runs.Pagination.TotalCount, len(split.recorded)), nil
	}

	params, _ := decodeParams(lastRow)
	trace := &SessionTrace{
		SessionID: sessionID,
		Route:     route,
		Seed:      split.seed,
		Tools:     declaredTools(lastRow, params),
		Params:    replayParams(params),
		Actions:   split.actions,
		RowIDs:    make([]string, len(rows)),
		recorded:  split.recorded,
	}
	for i, row := range rows {
		trace.RowIDs[i] = row.ID
	}
	trace.Cassette = trace.NewCassette(ToolRules{})
	return trace, Eligibility{Code: CodeEligible}, nil
}

// checkTools fails when the declared tools differ between rows.
func checkTools(rows []*logstore.Log) (code, msg string) {
	var first [32]byte
	for i, row := range rows {
		params, _ := decodeParams(row)
		h := toolsHash(declaredTools(row, params))
		if i == 0 {
			first = h
		} else if h != first {
			return CodeToolsChanged, fmt.Sprintf("row %s declares different tools from row %s", row.ID, rows[0].ID)
		}
	}
	return "", ""
}

// checkPrefixes fails when a row's history does not start with the previous row's history and reply.
func checkPrefixes(rows []*logstore.Log) (code, msg string) {
	for i := 1; i < len(rows); i++ {
		prev := append(slices.Clone(rows[i-1].InputHistoryParsed), *rows[i-1].OutputMessageParsed)
		cur := rows[i].InputHistoryParsed
		if len(cur) < len(prev) {
			return CodeHistoryRewritten, fmt.Sprintf("row %s has %d messages, fewer than the %d before it", rows[i].ID, len(cur), len(prev))
		}
		for j := range prev {
			if messageHash(prev[j]) != messageHash(cur[j]) {
				return CodeHistoryRewritten, fmt.Sprintf("row %s differs from row %s at message %d", rows[i].ID, rows[i-1].ID, j)
			}
		}
	}
	return "", ""
}

// split is a transcript broken into its parts.
type split struct {
	seed     []schemas.ChatMessage
	actions  []Action
	recorded []recordedCall
}

// splitTranscript breaks a transcript into the seed (everything before the first assistant reply),
// the actions that follow, and the tool calls with their results. It reports why it cannot when the
// transcript is not a single-user-turn conversation with every call answered.
func splitTranscript(messages []schemas.ChatMessage) (split, Eligibility) {
	var out split
	i := 0
	for ; i < len(messages) && messages[i].Role != schemas.ChatMessageRoleAssistant; i++ {
		m := messages[i]
		if m.Role == schemas.ChatMessageRoleTool {
			return split{}, ineligible(CodeUnpairedToolCall, "a tool result at message %d comes before any assistant reply", i)
		}
		text, nonText := messageText(m.Content)
		if nonText {
			return split{}, ineligible(CodeNonTextContent, "message %d holds more than text", i)
		}
		seed := schemas.ChatMessage{Role: m.Role, Name: m.Name, Content: &schemas.ChatMessageContent{ContentStr: &text}}
		out.seed = append(out.seed, seed)
	}
	if i == len(messages) {
		return split{}, ineligible(CodeIncomplete, "no assistant reply in the transcript")
	}

	for i < len(messages) {
		m := messages[i]
		switch m.Role {
		case schemas.ChatMessageRoleAssistant:
			var calls []schemas.ChatAssistantMessageToolCall
			if m.ChatAssistantMessage != nil {
				calls = m.ToolCalls
			}
			i++
			if len(calls) == 0 {
				text, _ := messageText(m.Content)
				out.actions = append(out.actions, Action{Kind: ActionFinal, Text: text})
				continue
			}
			end := i
			for end < len(messages) && messages[end].Role == schemas.ChatMessageRoleTool {
				end++
			}
			recorded, elig := pairCalls(calls, messages[i:end])
			if !elig.Eligible() {
				return split{}, elig
			}
			out.recorded = append(out.recorded, recorded...)
			out.actions = append(out.actions, toolsAction(recorded))
			i = end
		case schemas.ChatMessageRoleTool:
			return split{}, ineligible(CodeUnpairedToolCall, "a tool result at message %d answers no call", i)
		default:
			return split{}, ineligible(CodeMultiUserTurn, "a %s message at %d follows an assistant reply", m.Role, i)
		}
	}
	return out, Eligibility{Code: CodeEligible}
}

// pairCalls matches each call of one assistant turn to its result by tool call ID. Results may come
// in any order. Every call needs exactly one result and every result a call.
func pairCalls(calls []schemas.ChatAssistantMessageToolCall, results []schemas.ChatMessage) ([]recordedCall, Eligibility) {
	byID := make(map[string]schemas.ChatMessage, len(results))
	for _, res := range results {
		if res.ChatToolMessage == nil || res.ToolCallID == nil || *res.ToolCallID == "" {
			return nil, ineligible(CodeUnpairedToolCall, "a tool result has no tool call id")
		}
		if _, dup := byID[*res.ToolCallID]; dup {
			return nil, ineligible(CodeUnpairedToolCall, "two results for tool call %q", *res.ToolCallID)
		}
		byID[*res.ToolCallID] = res
	}
	recorded := make([]recordedCall, 0, len(calls))
	for _, call := range calls {
		if call.ID == nil || *call.ID == "" || call.Function.Name == nil {
			return nil, ineligible(CodeUnpairedToolCall, "a tool call has no id or name")
		}
		res, ok := byID[*call.ID]
		if !ok {
			return nil, ineligible(CodeUnpairedToolCall, "tool call %q has no result", *call.ID)
		}
		delete(byID, *call.ID)
		text, nonText := messageText(res.Content)
		if nonText {
			return nil, ineligible(CodeNonTextContent, "the result of tool call %q holds more than text", *call.ID)
		}
		rc := recordedCall{Name: *call.Function.Name, Args: call.Function.Arguments, Result: text}
		if res.IsError != nil {
			rc.IsError = *res.IsError
		}
		recorded = append(recorded, rc)
	}
	if len(byID) > 0 {
		return nil, ineligible(CodeUnpairedToolCall, "%d tool results answer no call", len(byID))
	}
	return recorded, Eligibility{Code: CodeEligible}
}

// toolsAction is a parallel group of calls, sorted so order within the group does not matter.
func toolsAction(recorded []recordedCall) Action {
	calls := make([]ToolCall, len(recorded))
	for i, rc := range recorded {
		calls[i] = ToolCall{Name: rc.Name, Args: CanonicalArgs(rc.Args, nil)}
	}
	return sortedGroup(calls)
}

// toolsHash digests a tool list, so two rows that declared the same tools compare equal.
func toolsHash(tools []schemas.ChatTool) [32]byte {
	b, _ := json.Marshal(tools)
	return sha256.Sum256(b)
}
