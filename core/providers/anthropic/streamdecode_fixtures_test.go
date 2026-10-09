package anthropic

import (
	"bytes"
	"strconv"
	"strings"
)

// Recorded-shape Anthropic Messages streams shared by the stream-decode
// differential tests and benchmarks. Each fixture is the raw SSE body an
// upstream sends; events() splits it into the per-event data payloads the
// provider stream loop decodes.

type streamFixture struct {
	name string
	sse  []byte
}

// payloads returns the data payload of every event in the fixture, in order.
func (f streamFixture) payloads() [][]byte {
	var out [][]byte
	for _, block := range strings.Split(string(f.sse), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				out = append(out, []byte(data))
			}
		}
	}
	return out
}

type streamFixtureWriter struct {
	b bytes.Buffer
}

func (w *streamFixtureWriter) event(typ, data string) {
	w.b.WriteString("event: ")
	w.b.WriteString(typ)
	w.b.WriteString("\ndata: ")
	w.b.WriteString(data)
	w.b.WriteString("\n\n")
}

// Text deltas of the lengths Claude streams, including characters that need
// JSON escaping (quotes, newlines, backslashes, \u escapes, raw UTF-8).
var streamFixtureTextPieces = []string{
	"The", " request", " path", " reads", " the", " body", " once", ",", " then",
	" hands", " it", " to", " the", " provider", ".", `\n\n`, "Each", " attempt",
	" re", "-encodes", " the", " typed", " parameters", ` (\"max_tokens\",`,
	" `tools`", ")", " and", " streams", " back", " chunks", " of", " roughly",
	" this", " size", ":", " a", " word", " or", " two", " at", " a", " time", ".",
	`\n- `, "**Note**", ":", " caf\u00e9", ` caf\u00e9`, ` \ud83d\ude00`, ` C:\\path`, " \u65e5\u672c",
}

var streamFixtureThinkingPieces = []string{
	"Let me think about what the user is asking.", " They want the failover",
	" chain to keep the cap", " across attempts, so", " the reservation must",
	" be computed once.", " Considering the edge case where", " the head fails",
	" before the first byte", `\n\n`, "Next, check the tool schema:", " the",
	" `location` field is required", " and the unit defaults to celsius.",
}

var streamFixtureJSONPieces = []string{
	`{\"`, `query`, `\": \"`, `SELECT id, name`, ` FROM accounts`, ` WHERE region`,
	` = 'eu-west-1'`, ` AND status`, ` = 'active'`, `\", \"`, `limit`, `\": `,
	`50`, `, \"`, `filters`, `\": [{\"`, `field`, `\": \"`, `created_at`, `\", \"`,
	`op`, `\": \"`, `gte`, `\", \"`, `value`, `\": \"`, `2026-01-01`, `\"}]`,
}

var streamFixtureSignature = strings.Repeat("EqQBCkYIBhgCKkBz9fJ3x0Qm1Yc2vHk8Lw5pRt7uNa", 9)

func (w *streamFixtureWriter) messageStart() {
	w.event("message_start", `{"type":"message_start","message":{"id":"msg_01XyZaBcDeFgHiJkLmNoPqRs","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1284,"cache_creation_input_tokens":2048,"cache_read_input_tokens":18432,"cache_creation":{"ephemeral_5m_input_tokens":1536,"ephemeral_1h_input_tokens":512},"output_tokens":1,"service_tier":"standard"}}}`)
	w.event("ping", `{"type":"ping"}`)
}

func (w *streamFixtureWriter) blockStart(idx int, block string) {
	w.event("content_block_start", `{"type":"content_block_start","index":`+strconv.Itoa(idx)+`,"content_block":`+block+`}`)
}

func (w *streamFixtureWriter) blockStop(idx int) {
	w.event("content_block_stop", `{"type":"content_block_stop","index":`+strconv.Itoa(idx)+`}`)
}

func (w *streamFixtureWriter) deltas(idx, n int, kind, field string, pieces []string) {
	prefix := `{"type":"content_block_delta","index":` + strconv.Itoa(idx) + `,"delta":{"type":"` + kind + `","` + field + `":"`
	for i := range n {
		w.event("content_block_delta", prefix+pieces[i%len(pieces)]+`"}}`)
	}
}

func (w *streamFixtureWriter) text(idx, n int) {
	w.blockStart(idx, `{"type":"text","text":""}`)
	w.deltas(idx, n, "text_delta", "text", streamFixtureTextPieces)
	w.blockStop(idx)
}

func (w *streamFixtureWriter) thinking(idx, n int) {
	w.blockStart(idx, `{"type":"thinking","thinking":"","signature":""}`)
	w.deltas(idx, n, "thinking_delta", "thinking", streamFixtureThinkingPieces)
	w.event("content_block_delta", `{"type":"content_block_delta","index":`+strconv.Itoa(idx)+`,"delta":{"type":"signature_delta","signature":"`+streamFixtureSignature+`"}}`)
	w.blockStop(idx)
}

func (w *streamFixtureWriter) toolUse(idx, n int, id, name string) {
	w.blockStart(idx, `{"type":"tool_use","id":"`+id+`","name":"`+name+`","input":{}}`)
	// Anthropic opens every tool_use block with an empty partial_json marker.
	w.event("content_block_delta", `{"type":"content_block_delta","index":`+strconv.Itoa(idx)+`,"delta":{"type":"input_json_delta","partial_json":""}}`)
	w.deltas(idx, n, "input_json_delta", "partial_json", streamFixtureJSONPieces)
	w.blockStop(idx)
}

func (w *streamFixtureWriter) end(stop string, outputTokens int) {
	w.event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+stop+`","stop_sequence":null},"usage":{"output_tokens":`+strconv.Itoa(outputTokens)+`}}`)
	w.event("message_stop", `{"type":"message_stop"}`)
}

func (w *streamFixtureWriter) fixture(name string) streamFixture {
	return streamFixture{name: name, sse: bytes.Clone(w.b.Bytes())}
}

// streamFixtures are the recorded-shape sequences: plain text, tool calls with
// input_json_delta, extended thinking with a signature, an agent turn mixing
// all three, redacted thinking, a server tool (web search) with its result
// block and cache/server-tool usage, and a mid-stream error.
func streamFixtures() []streamFixture {
	var text streamFixtureWriter
	text.messageStart()
	text.text(0, 480)
	text.end("end_turn", 960)

	var tool streamFixtureWriter
	tool.messageStart()
	tool.text(0, 40)
	tool.toolUse(1, 200, "toolu_01AbCdEfGhIjKlMnOpQrStUv", "run_query")
	tool.toolUse(2, 200, "toolu_01ZyXwVuTsRqPoNmLkJiHgFe", "run_query")
	tool.end("tool_use", 1200)

	var think streamFixtureWriter
	think.messageStart()
	think.thinking(0, 300)
	think.text(1, 180)
	think.end("end_turn", 2400)

	var agent streamFixtureWriter
	agent.messageStart()
	agent.thinking(0, 160)
	agent.text(1, 100)
	agent.toolUse(2, 110, "toolu_01AbCdEfGhIjKlMnOpQrStUv", "run_query")
	agent.toolUse(3, 110, "toolu_01ZyXwVuTsRqPoNmLkJiHgFe", "get_weather")
	agent.end("tool_use", 3100)

	var redacted streamFixtureWriter
	redacted.messageStart()
	redacted.blockStart(0, `{"type":"redacted_thinking","data":"EmwKAhgBEgy3va3pzix/LafPsn4aDFIT2Xlxh0L5L8rLVyIwxtE3rAFBa8cr3qpPkNRj2YfWXGmKDxH4mPnZ5sQ7vB5URj"}`)
	redacted.blockStop(0)
	redacted.thinking(1, 20)
	redacted.text(2, 30)
	redacted.end("end_turn", 300)

	var server streamFixtureWriter
	server.messageStart()
	server.text(0, 10)
	server.blockStart(1, `{"type":"server_tool_use","id":"srvtoolu_014hJH82Qum7Td6UV8gDXThB","name":"web_search","input":{}}`)
	server.event("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`)
	server.event("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\": \"weather"}}`)
	server.event("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" in Paris\"}"}}`)
	server.blockStop(1)
	server.blockStart(2, `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_014hJH82Qum7Td6UV8gDXThB","content":[{"type":"web_search_result","title":"Paris weather","url":"https://example.com/paris","encrypted_content":"EqgfCioIARgBIiQ3YTAwMjY1Mi1mZjM5","page_age":"1 day ago"}]}`)
	server.blockStop(2)
	server.blockStart(3, `{"type":"text","text":""}`)
	server.event("content_block_delta", `{"type":"content_block_delta","index":3,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","cited_text":"Sunny, 21C","url":"https://example.com/paris","title":"Paris weather","encrypted_index":"Eo8BCioIAhgBIiQyYjQ0OWJmZi1lNm"}}}`)
	server.deltas(3, 25, "text_delta", "text", streamFixtureTextPieces)
	server.blockStop(3)
	server.event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":2048,"cache_creation_input_tokens":0,"cache_read_input_tokens":1024,"output_tokens":412,"server_tool_use":{"web_search_requests":1}}}`)
	server.event("message_stop", `{"type":"message_stop"}`)

	var errored streamFixtureWriter
	errored.messageStart()
	errored.text(0, 12)
	errored.event("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)

	return []streamFixture{
		text.fixture("text"),
		tool.fixture("tool_use"),
		think.fixture("thinking"),
		agent.fixture("agent_turn"),
		redacted.fixture("redacted_thinking"),
		server.fixture("server_tool_use"),
		errored.fixture("error"),
	}
}
