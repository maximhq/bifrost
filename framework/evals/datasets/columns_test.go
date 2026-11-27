package datasets

import (
	"bytes"
	"encoding/json"
	"fmt"
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

	// Valid, distinct columns, so only the column limit can refuse the larger set.
	atLimit := make([]Column, 0, MaxColumns+1)
	for i := range MaxColumns {
		atLimit = append(atLimit, Column{Name: fmt.Sprintf("v%d", i), Type: ColumnVariable})
	}
	require.NoError(t, ValidateColumns(atLimit), "MaxColumns columns are allowed")
	overLimit := append(atLimit, Column{Name: "one too many", Type: ColumnVariable})
	err := ValidateColumns(overLimit)
	require.Error(t, err, "MaxColumns+1 columns are refused")
	assert.True(t, evals.IsInvalid(err))
	assert.Contains(t, err.Error(), fmt.Sprintf("at most %d columns", MaxColumns))
}

func TestValidateCell(t *testing.T) {
	calls := Column{Name: "calls", Type: ColumnExpectedToolCalls}
	require.NoError(t, ValidateCell(calls, ""))
	require.NoError(t, ValidateCell(calls, `[{"name":"x"}]`))
	require.Error(t, ValidateCell(calls, `[{"name":`))
	require.NoError(t, ValidateCell(Column{Name: "input", Type: ColumnInput}, `[{"name":`), "text columns take any string")

	// Expected tool calls must have the shape Tool Call Accuracy reads, not only be JSON.
	for _, bad := range []string{`42`, `"x"`, `[1]`, `{"inAnyOrder":[1]}`, `{"inAnyOrder":{"name":"a"}}`, `{"anyOne":[[{"name":"a"}],{"name":"b"}]}`} {
		_, err := NormalizeCell(calls, bad)
		require.Error(t, err, bad)
		assert.True(t, evals.IsInvalid(err), bad)
	}
	// Arguments given as a string may be a template that becomes JSON only once variables are filled in.
	for _, good := range []string{`{"name":"a"}`, `[{"name":"a"}]`, `[]`, `{}`, `null`, `{"inAnyOrder":[{"name":"a"}]}`, `{"anyOne":[[{"name":"a"}],[{"name":"b"}]]}`, `{"name":"a","arguments":"{{args}}"}`} {
		stored, err := NormalizeCell(calls, good)
		require.NoError(t, err, good)
		assert.Equal(t, good, stored)
	}

	// A plain call is read the way scoring reads it: a call with no name, or with
	// arguments that are neither an object nor a string, is refused when it is stored.
	for _, bad := range []string{`[{"name":"book","arguments":7}]`, `[{"arguments":{}}]`, `[{"name":""}]`, `[{"function":{"name":"book","arguments":[1]}}]`, `[{"toolUse":{"input":{}}}]`} {
		_, err := NormalizeCell(calls, bad)
		require.Error(t, err, bad)
		assert.True(t, evals.IsInvalid(err), bad)
	}
	for _, good := range []string{
		`[{"name":"book","arguments":"{{args}}"}]`,
		`[{"name":"book","arguments":null}]`,
		`[{"id":"1","type":"function","function":{"name":"book","arguments":"{\"seats\":2}"}}]`,
		`[{"type":"tool_use","id":"1","name":"book","input":{"seats":2}}]`,
		`[{"toolUse":{"toolUseId":"1","name":"book","input":{"seats":2}}}]`,
		`[ ]`,
	} {
		_, err := NormalizeCell(calls, good)
		require.NoError(t, err, good)
	}

	// Arguments given as a string must decode to an object, as scoring reads them, unless
	// they hold a template placeholder that is filled in later.
	for _, bad := range []string{`[{"name":"a","arguments":"not json"}]`, `[{"name":"a","arguments":"[1]"}]`} {
		_, err := NormalizeCell(calls, bad)
		require.Error(t, err, bad)
		assert.True(t, evals.IsInvalid(err), bad)
	}
	for _, good := range []string{`[{"name":"a","arguments":"{\"q\":1}"}]`, `[{"name":"a","arguments":"{{args}}"}]`} {
		_, err := NormalizeCell(calls, good)
		require.NoError(t, err, good)
	}
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

	// Exact names win over normalized ones, and a normalized name two columns share maps to neither.
	twins := []Column{{ID: "dash", Name: "customer-id", Type: ColumnVariable}, {ID: "under", Name: "customer_id", Type: ColumnVariable}}
	assert.Equal(t, []string{"dash", "under"}, SuggestMapping([]string{"customer-id", "customer_id"}, twins))
	assert.Equal(t, []string{"under", "dash"}, SuggestMapping([]string{"customer_id", "customer-id"}, twins))
	assert.Equal(t, []string{""}, SuggestMapping([]string{"Customer ID"}, twins), "an ambiguous header is left unmapped")
	// A header a CSV export escaped matches the column it came from.
	assert.Equal(t, []string{"c9"}, SuggestMapping([]string{"'-id"}, []Column{{ID: "c9", Name: "-id", Type: ColumnVariable}}))
	// A type name two columns share maps to neither, so column order cannot pick the target.
	files := []Column{{ID: "shots", Name: "screenshots", Type: ColumnFile}, {ID: "receipts", Name: "receipts", Type: ColumnFile}}
	assert.Equal(t, []string{""}, SuggestMapping([]string{"file"}, files), "an ambiguous type header is left unmapped")
	assert.Equal(t, []string{"receipts"}, SuggestMapping([]string{"receipts"}, files), "an exact name still maps")
	assert.Equal(t, []string{"shots"}, SuggestMapping([]string{"file"}, files[:1]), "a single column of the type still maps")
	// Names win over type names across headers, whatever the header order.
	pics := []Column{{ID: "pics", Name: "Pictures", Type: ColumnFile}}
	assert.Equal(t, []string{"", "pics"}, SuggestMapping([]string{"file", "PICTURES"}, pics), "a later header's name beats an earlier header's type name")
	assert.Equal(t, []string{"pics", ""}, SuggestMapping([]string{"PICTURES", "file"}, pics))
	assert.Equal(t, []string{"pics"}, SuggestMapping([]string{"file"}, pics), "the type name alone still maps")
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
		schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: new("empty"), Arguments: `null`}},
	)
	cells, skip := LogCells(log, columns)
	require.Empty(t, skip)
	assert.Equal(t, "weather in Paris?", cells["in"], "the input is the last user message")
	assert.JSONEq(t, `[{"role":"system","content":"be brief"},{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]`, cells["hist"])
	assert.Equal(t, "Let me check.", cells["out"])
	assert.JSONEq(t, `[{"name":"get_weather","arguments":{"city":"Paris"}},{"name":"empty","arguments":null}]`, cells["calls"],
		"arguments are kept as JSON, and null arguments are no arguments")
	_, filled := cells["exp"]
	assert.False(t, filled, "expected output is left for the user")

	// A call whose arguments are not a JSON object cannot be an expected tool call: the log is
	// skipped with the call named, when the dataset has an expected tool calls column.
	for _, arguments := range []string{`{"city":`, `7`} {
		bad := chatLog("bad", []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "weather?")}, "",
			schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: new("get_weather"), Arguments: `{"city":"Paris"}`}},
			schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: new("broken"), Arguments: arguments}},
		)
		_, skip = LogCells(bad, columns)
		assert.Contains(t, skip, `"broken"`, "arguments %s", arguments)
		cells, skip = LogCells(bad, columns[:1])
		assert.Empty(t, skip, "without an expected tool calls column the calls do not matter")
		assert.Equal(t, "weather?", cells["in"])
	}

	// A call with no name cannot be an expected tool call either: dropping it would change
	// the reference sequence, so the log is skipped.
	unnamed := chatLog("unnamed", []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "weather?")}, "",
		schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: new("get_weather"), Arguments: `{}`}},
		schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: new(""), Arguments: `{}`}},
	)
	cells, skip = LogCells(unnamed, columns)
	assert.Equal(t, "a tool call has no name, so it cannot be an expected tool call", skip)
	assert.Nil(t, cells)
	turns := SessionTurns([]*logstore.Log{unnamed})
	require.Len(t, turns, 1)
	assert.Len(t, turns[0].ToolCalls, 2, "a session still shows the unnamed call")

	_, skip = LogCells(&logstore.Log{ID: "no-content"}, columns)
	assert.NotEmpty(t, skip, "a log without content has nothing to copy")

	// A last user turn with no text (an image only) is not paired with an earlier text prompt.
	imageOnly := chatLog("log-2", []schemas.ChatMessage{
		chatMessage(schemas.ChatMessageRoleUser, "first"),
		chatMessage(schemas.ChatMessageRoleAssistant, "ok"),
		imageMessage(),
	}, "an image")
	cells, skip = LogCells(imageOnly, columns)
	require.Empty(t, skip)
	_, filled = cells["in"]
	assert.False(t, filled, "the input of a textless last turn is empty, not an earlier prompt")
	assert.JSONEq(t, `[{"role":"user","content":"first"},{"role":"assistant","content":"ok"}]`, cells["hist"], "every text message precedes the textless turn")
	assert.Equal(t, "an image", cells["out"])

	// Messages after the textless last turn (a tool result) are not history before it.
	afterImage := chatLog("log-3", []schemas.ChatMessage{
		chatMessage(schemas.ChatMessageRoleUser, "hi"),
		chatMessage(schemas.ChatMessageRoleAssistant, "hello"),
		imageMessage(),
		chatMessage(schemas.ChatMessageRoleTool, "42"),
	}, "done")
	cells, skip = LogCells(afterImage, columns)
	require.Empty(t, skip)
	_, filled = cells["in"]
	assert.False(t, filled, "the input of a textless last turn is empty")
	assert.JSONEq(t, `[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]`, cells["hist"], "the tool result after the textless turn is not history")
}

func imageMessage() schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{
		Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "https://example.com/a.png"},
	}}}}
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
	cells, skip := LogCells(log, columns)
	require.Empty(t, skip)
	assert.Equal(t, "book a table", cells["in"])
	assert.Equal(t, "Booked.", cells["out"])
	assert.JSONEq(t, `[{"name":"book","arguments":{"seats":2}}]`, cells["calls"])

	// A function call with no name cannot be an expected tool call.
	unnamed := &logstore.Log{
		ID:                          "resp-unnamed",
		ResponsesInputHistoryParsed: log.ResponsesInputHistoryParsed,
		ResponsesOutputParsed: []schemas.ResponsesMessage{
			{Type: &call, ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: new(""), Arguments: new(`{}`)}},
		},
	}
	cells, skip = LogCells(unnamed, columns)
	assert.Equal(t, "a tool call has no name, so it cannot be an expected tool call", skip)
	assert.Nil(t, cells)

	// Messages after a textless last user turn are not history before it.
	assistant := schemas.ResponsesInputMessageRoleAssistant
	text := func(role *schemas.ResponsesMessageRoleType, value string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{Role: role, Content: &schemas.ResponsesMessageContent{ContentStr: new(value)}}
	}
	afterImage := &logstore.Log{
		ID: "resp-2",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			text(&user, "hi"),
			text(&assistant, "hello"),
			{Role: &user, Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeImage,
			}}}},
			text(&assistant, "42"),
		},
		ResponsesOutputParsed: []schemas.ResponsesMessage{
			{Type: &message, Content: &schemas.ResponsesMessageContent{ContentStr: new("done")}},
		},
	}
	withHistory := append(columns, Column{ID: "hist", Name: "history", Type: ColumnConversationHistory})
	cells, skip = LogCells(afterImage, withHistory)
	require.Empty(t, skip)
	_, filled := cells["in"]
	assert.False(t, filled, "the input of a textless last turn is empty")
	assert.JSONEq(t, `[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]`, cells["hist"], "the message after the textless turn is not history")
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

	// A turn that ends in an image-only user message has no text request.
	third := chatLog("t2", []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "first"), imageMessage()}, "Nice picture.")
	turns = SessionTurns([]*logstore.Log{first, third})
	require.Len(t, turns, 2)
	assert.Empty(t, turns[1].Request, "an earlier prompt is not repeated as the image turn's request")
	assert.Equal(t, "Nice picture.", turns[1].Response)
}
