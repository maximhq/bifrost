package datasets

import (
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// ToolCall is one tool call a model made, in the shape the tool call accuracy
// evaluator and the expected_tool_calls column use.
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// historyMessage is one earlier message in a conversation_history cell.
type historyMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SessionTurn is one request and reply of a session, as the agent evaluators read it.
type SessionTurn struct {
	Turn      int        `json:"turn"`
	Request   string     `json:"request"`
	Response  string     `json:"response"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// exchange is what one log contributes: the messages sent, the reply and its tool calls.
type exchange struct {
	messages  []historyMessage // every message of the request that has text, in order
	response  string
	toolCalls []ToolCall
	// finalUserTextless is set when the request's last user message has no text (an image
	// only), so no earlier text prompt stands in for it.
	finalUserTextless bool
}

func chatText(content *schemas.ChatMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	var b strings.Builder
	for _, block := range content.ContentBlocks {
		if block.Text == nil || *block.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(*block.Text)
	}
	return b.String()
}

func responsesText(content *schemas.ResponsesMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	var b strings.Builder
	for _, block := range content.ContentBlocks {
		if block.Text == nil || *block.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(*block.Text)
	}
	return b.String()
}

// toolCall builds a ToolCall. Arguments arrive as a JSON string; they are kept as
// JSON when they parse, and as a JSON string when they do not.
func toolCall(name, arguments string) ToolCall {
	call := ToolCall{Name: name}
	arguments = strings.TrimSpace(arguments)
	switch {
	case arguments == "":
		call.Arguments = json.RawMessage(`{}`)
	case json.Valid([]byte(arguments)):
		call.Arguments = json.RawMessage(arguments)
	default:
		quoted, _ := json.Marshal(arguments)
		call.Arguments = quoted
	}
	return call
}

func chatToolCalls(calls []schemas.ChatAssistantMessageToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		if call.Function.Name == nil || *call.Function.Name == "" {
			continue
		}
		out = append(out, toolCall(*call.Function.Name, call.Function.Arguments))
	}
	return out
}

// readExchange pulls the conversation out of a log, whichever API shape it was logged in.
func readExchange(log *logstore.Log) exchange {
	var ex exchange
	switch {
	case len(log.InputHistoryParsed) > 0:
		for _, message := range log.InputHistoryParsed {
			text := chatText(message.Content)
			if message.Role == schemas.ChatMessageRoleUser {
				ex.finalUserTextless = text == ""
			}
			if text != "" {
				ex.messages = append(ex.messages, historyMessage{Role: string(message.Role), Content: text})
			}
		}
	case len(log.ResponsesInputHistoryParsed) > 0:
		for _, message := range log.ResponsesInputHistoryParsed {
			if message.Role == nil {
				continue
			}
			text := responsesText(message.Content)
			if *message.Role == schemas.ResponsesInputMessageRoleUser {
				ex.finalUserTextless = text == ""
			}
			if text != "" {
				ex.messages = append(ex.messages, historyMessage{Role: string(*message.Role), Content: text})
			}
		}
	}

	if out := log.OutputMessageParsed; out != nil {
		ex.response = chatText(out.Content)
		if out.ChatAssistantMessage != nil {
			ex.toolCalls = chatToolCalls(out.ChatAssistantMessage.ToolCalls)
		}
	}
	var responseText strings.Builder
	for _, item := range log.ResponsesOutputParsed {
		if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeFunctionCall {
			if tool := item.ResponsesToolMessage; tool != nil && tool.Name != nil {
				arguments := ""
				if tool.Arguments != nil {
					arguments = *tool.Arguments
				}
				ex.toolCalls = append(ex.toolCalls, toolCall(*tool.Name, arguments))
			}
			continue
		}
		if item.Type != nil && *item.Type != schemas.ResponsesMessageTypeMessage {
			continue
		}
		if text := responsesText(item.Content); text != "" {
			if responseText.Len() > 0 {
				responseText.WriteByte('\n')
			}
			responseText.WriteString(text)
		}
	}
	if ex.response == "" {
		ex.response = responseText.String()
	}
	if len(ex.toolCalls) == 0 {
		ex.toolCalls = chatToolCalls(log.ToolCallsParsed)
	}
	return ex
}

// lastUser returns the index of the last user message, or -1. It is -1 as well when the
// last user message has no text: an earlier prompt is not that turn's request.
func (ex exchange) lastUser() int {
	if ex.finalUserTextless {
		return -1
	}
	for i := len(ex.messages) - 1; i >= 0; i-- {
		if ex.messages[i].Role == string(schemas.ChatMessageRoleUser) {
			return i
		}
	}
	return -1
}

func (ex exchange) empty() bool {
	return len(ex.messages) == 0 && ex.response == "" && len(ex.toolCalls) == 0
}

func marshalString(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

// LogCells builds a row's cells from one request log, by column type. It returns
// false when the log has no content to copy, which is the case when content logging
// was off for the request.
func LogCells(log *logstore.Log, columns []Column) (map[string]string, bool) {
	ex := readExchange(log)
	if ex.empty() {
		return nil, false
	}
	last := ex.lastUser()
	cells := make(map[string]string, 4)
	for _, column := range columns {
		switch column.Type {
		case ColumnInput:
			if last >= 0 {
				cells[column.ID] = ex.messages[last].Content
			}
		case ColumnConversationHistory:
			switch {
			case last > 0:
				cells[column.ID] = marshalString(ex.messages[:last])
			case ex.finalUserTextless && len(ex.messages) > 0:
				// Every kept message comes before the textless last turn.
				cells[column.ID] = marshalString(ex.messages)
			}
		case ColumnOutput:
			cells[column.ID] = ex.response
		case ColumnExpectedToolCalls:
			if len(ex.toolCalls) > 0 {
				cells[column.ID] = marshalString(ex.toolCalls)
			}
		}
	}
	stripNUL(cells)
	return cells, true
}

// stripNUL drops NUL characters from cells built from logs. Log text is not typed by the user,
// so it is cleaned rather than refused; NormalizeCell refuses NUL in what the user sends.
func stripNUL(cells map[string]string) {
	for id, value := range cells {
		if strings.IndexByte(value, 0) >= 0 {
			cells[id] = strings.ReplaceAll(value, "\x00", "")
		}
	}
}

// SessionTurns turns a session's logs, oldest first, into turns. Each log repeats the
// earlier turns in its history, so a turn's request is only that log's last user
// message. Logs without content are left out.
func SessionTurns(logs []*logstore.Log) []SessionTurn {
	turns := make([]SessionTurn, 0, len(logs))
	for _, log := range logs {
		ex := readExchange(log)
		if ex.empty() {
			continue
		}
		turn := SessionTurn{Turn: len(turns), Response: ex.response, ToolCalls: ex.toolCalls}
		if last := ex.lastUser(); last >= 0 {
			turn.Request = ex.messages[last].Content
		}
		turns = append(turns, turn)
	}
	return turns
}

// SessionCells builds a row's cells from a session's turns: the whole session in the
// variable column named "session", the first request as the input and the last reply
// as the output. It returns false when there are no turns.
func SessionCells(turns []SessionTurn, columns []Column) (map[string]string, bool) {
	if len(turns) == 0 {
		return nil, false
	}
	cells := make(map[string]string, 3)
	for _, column := range columns {
		switch {
		case column.Type == ColumnVariable && column.Name == SessionColumnName:
			cells[column.ID] = marshalString(turns)
		case column.Type == ColumnInput:
			cells[column.ID] = turns[0].Request
		case column.Type == ColumnOutput:
			cells[column.ID] = turns[len(turns)-1].Response
		}
	}
	stripNUL(cells)
	return cells, true
}
