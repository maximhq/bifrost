package datasets

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRowVariables(t *testing.T) {
	columns := []Column{
		{ID: "c1", Name: "Question", Type: ColumnInput},
		{ID: "c2", Name: "Answer", Type: ColumnExpectedOutput},
		{ID: "c3", Name: "calls", Type: ColumnExpectedToolCalls},
		{ID: "c4", Name: "steps", Type: ColumnExpectedSteps},
		{ID: "c5", Name: "earlier", Type: ColumnConversationHistory},
		{ID: "c6", Name: "scenario", Type: ColumnScenario},
		{ID: "c7", Name: "reply", Type: ColumnOutput},
		{ID: "c8", Name: "context", Type: ColumnVariable},
		{ID: "c9", Name: "db_schema", Type: ColumnVariable},
		{ID: "c10", Name: "labels", Type: ColumnTags},
	}
	cells := map[string]string{
		"c1": "what is 2+2", "c2": "4", "c3": `[{"name":"add"}]`, "c4": "add the numbers",
		"c5": `[{"role":"user","content":"hi"}]`, "c6": "math", "c7": "four",
		"c8": "arithmetic", "c9": "", "c10": `["easy"]`, "gone": "cell of a deleted column",
	}
	assert.Equal(t, map[string]string{
		"input":               "what is 2+2",
		"expected_output":     "4",
		"expected_tool_calls": `[{"name":"add"}]`,
		"expected_steps":      "add the numbers",
		"history":             `[{"role":"user","content":"hi"}]`,
		"scenario":            "math",
		"output":              "four",
		"context":             "arithmetic",
	}, RowVariables(columns, cells), "typed columns map by type, variables by name, tags and empty cells map to nothing")
}

func TestValidateColumns(t *testing.T) {
	ok := []Column{{Name: "input", Type: ColumnInput}, {Name: "context", Type: ColumnVariable}, {Name: "docs", Type: ColumnVariable}}
	require.NoError(t, ValidateColumns(ok))

	cases := map[string][]Column{
		"unknown type":            {{Name: "x", Type: "image"}},
		"empty name":              {{Name: " ", Type: ColumnInput}},
		"duplicate name":          {{Name: "a", Type: ColumnVariable}, {Name: "a", Type: ColumnVariable}},
		"two inputs":              {{Name: "a", Type: ColumnInput}, {Name: "b", Type: ColumnInput}},
		"variable named input":    {{Name: "input", Type: ColumnVariable}},
		"variable named history":  {{Name: "history", Type: ColumnVariable}},
		"more than the limit":     make([]Column, MaxColumns+1),
		"variable named scenario": {{Name: "scenario", Type: ColumnVariable}},
	}
	for name, columns := range cases {
		err := ValidateColumns(columns)
		require.Error(t, err, name)
		assert.True(t, evals.IsInvalid(err), name)
	}
	for name := range Templates {
		require.NoError(t, ValidateColumns(Templates[name]), name)
	}
}

func TestValidateCell(t *testing.T) {
	calls := Column{Name: "calls", Type: ColumnExpectedToolCalls}
	require.NoError(t, ValidateCell(calls, ""))
	require.NoError(t, ValidateCell(calls, `[{"name":"x"}]`))
	require.Error(t, ValidateCell(calls, `[{"name":`))
	require.NoError(t, ValidateCell(Column{Name: "input", Type: ColumnInput}, `[{"name":`), "text columns take any string")
}

func TestFileColumns(t *testing.T) {
	files := Column{ID: "c-files", Name: "screenshots", Type: ColumnFile}

	// A dataset can have many file columns, and each fills a variable with its own name.
	require.NoError(t, ValidateColumns([]Column{{Name: "input", Type: ColumnInput}, files, {Name: "docs", Type: ColumnFile}}))
	require.Error(t, ValidateColumns([]Column{{Name: "output", Type: ColumnFile}}), "a file column cannot shadow a typed variable")

	// A cell is a JSON list of files: uploaded ones by ID, others by URL.
	stored, err := NormalizeCell(files, `[{"id":"f1","name":"a.png","content_type":"image/png","size":10,"url":"/api/evals/datasets/d/files/f1"},{"url":"https://example.com/b.pdf"}]`)
	require.NoError(t, err)
	refs, err := ParseFileCell(stored)
	require.NoError(t, err)
	require.Len(t, refs, 2)
	assert.Equal(t, "f1", refs[0].ID)
	assert.Equal(t, "b.pdf", refs[1].Name, "a file given by URL is named after the URL's last segment")

	// A plain list of URLs, as a CSV carries it, becomes the same shape.
	stored, err = NormalizeCell(files, "https://example.com/a.png, https://example.com/b.png")
	require.NoError(t, err)
	refs, err = ParseFileCell(stored)
	require.NoError(t, err)
	require.Len(t, refs, 2)
	assert.Equal(t, "https://example.com/b.png", refs[1].URL)

	for _, bad := range []string{`[{"name":"no id or url"}]`, `{"url":"https://example.com/a.png"}`, "not a url", `[{"url":"ftp://example.com/a"}]`} {
		_, err := NormalizeCell(files, bad)
		require.Error(t, err, bad)
	}
	stored, err = NormalizeCell(files, "")
	require.NoError(t, err)
	assert.Empty(t, stored)
	stored, err = NormalizeCell(files, "[]")
	require.NoError(t, err)
	assert.Empty(t, stored, "an empty list is an empty cell")

	// Other columns are stored as given.
	stored, err = NormalizeCell(Column{Name: "input", Type: ColumnInput}, "a, b")
	require.NoError(t, err)
	assert.Equal(t, "a, b", stored)

	variables := RowVariables([]Column{files}, map[string]string{"c-files": `[{"url":"https://example.com/a.png"}]`})
	assert.Equal(t, map[string]string{"screenshots": `[{"url":"https://example.com/a.png"}]`}, variables)
}

func TestCSVReaderHandlesSpreadsheetOutput(t *testing.T) {
	file := "\xEF\xBB\xBFinput,Expected Output\r\n\"line one\nline two\",\"say \"\"hi\"\"\"\r\nshort\r\na,b,extra\r\n"
	reader := NewCSVReader(strings.NewReader(file))
	var records [][]string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		records = append(records, append([]string(nil), record...))
	}
	assert.Equal(t, [][]string{
		{"input", "Expected Output"},
		{"line one\nline two", `say "hi"`},
		{"short"},
		{"a", "b", "extra"},
	}, records, "the byte order mark is dropped; quotes, newlines and ragged rows are read")
}

func TestSuggestMapping(t *testing.T) {
	columns := []Column{
		{ID: "c1", Name: "question", Type: ColumnInput},
		{ID: "c2", Name: "expected_output", Type: ColumnExpectedOutput},
		{ID: "c3", Name: "context", Type: ColumnVariable},
	}
	header := []string{"Input", "Expected Output", "CONTEXT", "notes", "expected-output"}
	assert.Equal(t, []string{"c1", "c2", "c3", "", ""}, SuggestMapping(header, columns),
		"names match ignoring case, spaces, underscores and hyphens; a type name matches too; a column is used once")
}

func TestEncodeCellsRoundTrip(t *testing.T) {
	values := []string{"plain", "quote \" slash \\ newline \n tab \t bell \x07 emoji 🙂 bad \xff", "", "skipped"}
	ids := []string{"c1", "c2", "c3", ""}
	var buf bytes.Buffer
	EncodeCells(&buf, ids, values)

	var decoded map[string]string
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded), buf.String())
	assert.Equal(t, map[string]string{"c1": "plain", "c2": "quote \" slash \\ newline \n tab \t bell \x07 emoji 🙂 bad �"}, decoded,
		"empty values and unmapped fields are left out")

	columns := []Column{{ID: "c2"}, {ID: "c1"}}
	assert.Equal(t, `{"c2":"b","c1":"a"}`, EncodeCellsMap(columns, map[string]string{"c1": "a", "c2": "b", "gone": "x"}),
		"cells are written in column order, and cells of unknown columns are dropped")
}

func TestRowWriter(t *testing.T) {
	columns := []Column{
		{ID: "c1", Name: "input", Type: ColumnInput},
		{ID: "c2", Name: "calls", Type: ColumnExpectedToolCalls},
	}
	rows := []string{`{"c1":"a, \"b\"","c2":"[{\"name\":\"x\"}]"}`, `{"c1":"second"}`}

	var csvOut bytes.Buffer
	writer, err := NewRowWriter(&csvOut, FormatCSV, columns)
	require.NoError(t, err)
	for _, row := range rows {
		require.NoError(t, writer.Write(row))
	}
	require.NoError(t, writer.Flush())
	assert.Equal(t, "input,calls\n\"a, \"\"b\"\"\",\"[{\"\"name\"\":\"\"x\"\"}]\"\nsecond,\n", csvOut.String())

	var jsonl bytes.Buffer
	writer, err = NewRowWriter(&jsonl, FormatJSONL, columns)
	require.NoError(t, err)
	for _, row := range rows {
		require.NoError(t, writer.Write(row))
	}
	require.NoError(t, writer.Flush())
	lines := strings.Split(strings.TrimSpace(jsonl.String()), "\n")
	require.Len(t, lines, 2)
	assert.JSONEq(t, `{"input":"a, \"b\"","calls":[{"name":"x"}]}`, lines[0], "a structured cell is written as JSON, not as a string")
	assert.JSONEq(t, `{"input":"second","calls":""}`, lines[1])

	_, err = NewRowWriter(&jsonl, "xlsx", columns)
	require.Error(t, err)
}

func chatLog(id string, history []schemas.ChatMessage, reply string, calls ...schemas.ChatAssistantMessageToolCall) *logstore.Log {
	out := &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant}
	if reply != "" {
		out.Content = &schemas.ChatMessageContent{ContentStr: new(reply)}
	}
	if len(calls) > 0 {
		out.ChatAssistantMessage = &schemas.ChatAssistantMessage{ToolCalls: calls}
	}
	return &logstore.Log{ID: id, InputHistoryParsed: history, OutputMessageParsed: out}
}

func chatMessage(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: new(text)}}
}

func TestLogCells(t *testing.T) {
	columns := []Column{
		{ID: "in", Name: "input", Type: ColumnInput},
		{ID: "hist", Name: "history", Type: ColumnConversationHistory},
		{ID: "out", Name: "output", Type: ColumnOutput},
		{ID: "calls", Name: "calls", Type: ColumnExpectedToolCalls},
		{ID: "exp", Name: "expected", Type: ColumnExpectedOutput},
	}
	log := chatLog("log-1",
		[]schemas.ChatMessage{
			chatMessage(schemas.ChatMessageRoleSystem, "be brief"),
			chatMessage(schemas.ChatMessageRoleUser, "hi"),
			chatMessage(schemas.ChatMessageRoleAssistant, "hello"),
			chatMessage(schemas.ChatMessageRoleUser, "weather in Paris?"),
		},
		"Let me check.",
		schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: new("get_weather"), Arguments: `{"city":"Paris"}`}},
		schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: new("broken"), Arguments: `{"city":`}},
	)
	cells, ok := LogCells(log, columns)
	require.True(t, ok)
	assert.Equal(t, "weather in Paris?", cells["in"], "the input is the last user message")
	assert.JSONEq(t, `[{"role":"system","content":"be brief"},{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]`, cells["hist"])
	assert.Equal(t, "Let me check.", cells["out"])
	assert.JSONEq(t, `[{"name":"get_weather","arguments":{"city":"Paris"}},{"name":"broken","arguments":"{\"city\":"}]`, cells["calls"],
		"arguments are kept as JSON when they parse, and as a string when they do not")
	_, filled := cells["exp"]
	assert.False(t, filled, "expected output is left for the user")

	_, ok = LogCells(&logstore.Log{ID: "no-content"}, columns)
	assert.False(t, ok, "a log without content has nothing to copy")
}

func TestLogCellsFromResponsesAPI(t *testing.T) {
	columns := []Column{
		{ID: "in", Name: "input", Type: ColumnInput},
		{ID: "out", Name: "output", Type: ColumnOutput},
		{ID: "calls", Name: "calls", Type: ColumnExpectedToolCalls},
	}
	user := schemas.ResponsesInputMessageRoleUser
	message := schemas.ResponsesMessageTypeMessage
	call := schemas.ResponsesMessageTypeFunctionCall
	log := &logstore.Log{
		ID: "resp-1",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			{Role: &user, Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Text: new("book a table")}}}},
		},
		ResponsesOutputParsed: []schemas.ResponsesMessage{
			{Type: &call, ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: new("book"), Arguments: new(`{"seats":2}`)}},
			{Type: &message, Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Text: new("Booked.")}}}},
		},
	}
	cells, ok := LogCells(log, columns)
	require.True(t, ok)
	assert.Equal(t, "book a table", cells["in"])
	assert.Equal(t, "Booked.", cells["out"])
	assert.JSONEq(t, `[{"name":"book","arguments":{"seats":2}}]`, cells["calls"])
}

func TestSessionTurnsAndCells(t *testing.T) {
	first := chatLog("t0", []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "deploy the site")}, "Starting.",
		schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: new("deploy"), Arguments: `{"env":"prod"}`}})
	// The second request repeats the first turn in its history.
	second := chatLog("t1", []schemas.ChatMessage{
		chatMessage(schemas.ChatMessageRoleUser, "deploy the site"),
		chatMessage(schemas.ChatMessageRoleAssistant, "Starting."),
		chatMessage(schemas.ChatMessageRoleUser, "is it healthy?"),
	}, "All checks pass.")
	turns := SessionTurns([]*logstore.Log{first, {ID: "no-content"}, second})
	require.Len(t, turns, 2, "a log without content is not a turn")
	assert.Equal(t, 1, turns[1].Turn)
	assert.Equal(t, "is it healthy?", turns[1].Request, "a turn's request is only that log's last user message")

	columns := []Column{
		{ID: "in", Name: "input", Type: ColumnInput},
		{ID: "out", Name: "output", Type: ColumnOutput},
		{ID: "sess", Name: SessionColumnName, Type: ColumnVariable},
		{ID: "ctx", Name: "context", Type: ColumnVariable},
	}
	cells, ok := SessionCells(turns, columns)
	require.True(t, ok)
	assert.Equal(t, "deploy the site", cells["in"])
	assert.Equal(t, "All checks pass.", cells["out"])
	assert.JSONEq(t, `[
		{"turn":0,"request":"deploy the site","response":"Starting.","tool_calls":[{"name":"deploy","arguments":{"env":"prod"}}]},
		{"turn":1,"request":"is it healthy?","response":"All checks pass."}
	]`, cells["sess"])
	assert.Equal(t, map[string]string{"input": "deploy the site", "output": "All checks pass.", "session": cells["sess"]}, RowVariables(columns, cells))

	_, ok = SessionCells(nil, columns)
	assert.False(t, ok)
}
