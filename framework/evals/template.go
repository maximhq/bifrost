package evals

import (
	"bytes"
	"strings"
	"sync"
)

// maxPooledBufferBytes keeps one oversized payload from pinning memory in the pool.
const maxPooledBufferBytes = 1 << 20

// bufferPool recycles the scratch buffers used to render requests and read responses.
// A test run renders the same templates thousands of times, so they are not allocated
// fresh for each entry.
var bufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func getBuffer() *bytes.Buffer {
	return bufferPool.Get().(*bytes.Buffer)
}

func putBuffer(buf *bytes.Buffer) {
	if buf == nil || buf.Cap() > maxPooledBufferBytes {
		return
	}
	buf.Reset()
	bufferPool.Put(buf)
}

// template is text with {{name}} placeholders, split once so rendering is a walk over
// its parts instead of a regular expression pass per use.
type template struct {
	literals []string // len(names)+1 pieces of fixed text
	names    []string // the variable between literals[i] and literals[i+1]
}

// compileTemplate splits text at its placeholders.
func compileTemplate(text string) template {
	matches := variablePattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return template{literals: []string{text}}
	}
	t := template{literals: make([]string, 0, len(matches)+1), names: make([]string, 0, len(matches))}
	last := 0
	for _, match := range matches {
		t.literals = append(t.literals, text[last:match[0]])
		t.names = append(t.names, strings.TrimSpace(text[match[2]:match[3]]))
		last = match[1]
	}
	t.literals = append(t.literals, text[last:])
	return t
}

// render writes the template into buf with each variable passed through escape. A
// variable with no value renders empty. escape may be nil.
func (t template) render(buf *bytes.Buffer, in Input, escape func(string) string) {
	for i, literal := range t.literals {
		buf.WriteString(literal)
		if i >= len(t.names) {
			break
		}
		value := in.Variables[t.names[i]]
		if escape != nil {
			value = escape(value)
		}
		buf.WriteString(value)
	}
}

// renderJSON renders a JSON template to a string. A placeholder inside a JSON string
// literal is escaped as string content, so a value with quotes or backslashes keeps the
// document valid. A placeholder outside a string is written raw, so it can hold a JSON
// fragment such as an object. String state is read from the literal parts only.
func (t template) renderJSON(in Input) string {
	if len(t.names) == 0 {
		return t.literals[0]
	}
	buf := getBuffer()
	defer putBuffer(buf)
	inString, escaped := false, false
	for i, literal := range t.literals {
		buf.WriteString(literal)
		for j := 0; j < len(literal); j++ {
			c := literal[j]
			switch {
			case escaped:
				escaped = false
			case inString && c == '\\':
				escaped = true
			case c == '"':
				inString = !inString
			}
		}
		if i >= len(t.names) {
			break
		}
		value := in.Variables[t.names[i]]
		if inString {
			value = jsonStringEscape(value)
		}
		buf.WriteString(value)
	}
	return buf.String()
}

// renderString renders the template to a string. A template with no placeholders
// returns its text without copying.
func (t template) renderString(in Input, escape func(string) string) string {
	if len(t.names) == 0 {
		return t.literals[0]
	}
	buf := getBuffer()
	defer putBuffer(buf)
	t.render(buf, in, escape)
	return buf.String()
}

// CSVCell neutralises a cell a spreadsheet would run as a formula: one that starts with
// =, +, - or @, or with a tab or carriage return that some spreadsheets skip first. A
// leading quote makes it text and keeps the value readable. Only text that comes from
// rows, models and evaluators goes through it; numbers an export writes itself do not.
func CSVCell(value string) string {
	if value != "" && strings.ContainsRune("=+-@\t\r", rune(value[0])) {
		return "'" + value
	}
	return value
}
