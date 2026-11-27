package datasets

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/maximhq/bifrost/framework/evals"
)

// bufferPool holds the scratch buffers import and export build each row in, so a
// 50,000 row file does not allocate a buffer per row.
var bufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func getBuffer() *bytes.Buffer {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

func putBuffer(buf *bytes.Buffer) {
	// A buffer that grew for one huge cell is not worth keeping around.
	if buf.Cap() <= 1<<20 {
		bufferPool.Put(buf)
	}
}

// NewCSVReader reads CSV the way spreadsheets write it: an optional byte order mark,
// quoted fields with embedded newlines, and rows of uneven length. The record slice
// is reused between reads, so a caller must not keep it.
func NewCSVReader(r io.Reader) *csv.Reader {
	reader, _ := newTrackedCSVReader(r, false)
	return reader
}

// newTrackedCSVReader is NewCSVReader that can also return a quoteTracker fed the bytes
// the parser reads, after the byte order mark. With track false the tracker is nil.
func newTrackedCSVReader(r io.Reader, track bool) (*csv.Reader, *quoteTracker) {
	buffered := bufio.NewReaderSize(r, 64<<10)
	if head, err := buffered.Peek(3); err == nil && bytes.Equal(head, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = buffered.Discard(3)
	}
	var source io.Reader = buffered
	var tracker *quoteTracker
	if track {
		tracker = &quoteTracker{r: buffered}
		source = tracker
	}
	reader := csv.NewReader(source)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.ReuseRecord = true
	return reader, tracker
}

// quoteState is where a quoteTracker stands in the CSV grammar.
type quoteState uint8

const (
	quoteFieldStart    quoteState = iota // at the start of a field
	quoteUnquoted                        // inside a field that did not open with a quote
	quoteQuoted                          // inside a quoted field
	quoteQuoteInQuoted                   // just after a quote inside a quoted field
	quoteCRAfterQuote                    // a carriage return right after that quote
)

// quoteTracker follows the quoting of the bytes read through it the way encoding/csv
// lexes them with LazyQuotes. The parser returns a field cut by the end of the input
// inside its quotes as if it were closed; the tracker's state at the end says whether
// that happened. Counting quotes cannot tell, because a lazy bare quote in an unquoted
// field (5" screen) is valid and makes the count odd.
type quoteTracker struct {
	r     io.Reader
	state quoteState
}

func (q *quoteTracker) Read(p []byte) (int, error) {
	n, err := q.r.Read(p)
	for _, c := range p[:n] {
		switch q.state {
		case quoteFieldStart:
			switch c {
			case '"':
				q.state = quoteQuoted
			case ',', '\n':
			default:
				q.state = quoteUnquoted
			}
		case quoteUnquoted:
			if c == ',' || c == '\n' {
				q.state = quoteFieldStart
			}
		case quoteQuoted:
			if c == '"' {
				q.state = quoteQuoteInQuoted
			}
		case quoteQuoteInQuoted:
			switch c {
			case '"':
				q.state = quoteQuoted
			case ',', '\n':
				q.state = quoteFieldStart
			case '\r':
				q.state = quoteCRAfterQuote
			default:
				q.state = quoteQuoted // a lazy quote inside the field
			}
		case quoteCRAfterQuote:
			// The parser turns only a CR before the line feed into a line end, so a CR
			// followed by anything else leaves the quote lazy and the field open.
			switch c {
			case '\n':
				q.state = quoteFieldStart
			case '"':
				q.state = quoteQuoteInQuoted
			default:
				q.state = quoteQuoted
			}
		}
	}
	return n, err
}

// insideQuotes reports whether the bytes read so far end inside a quoted field. A CR
// pending after a closing quote at the end reads as closed, as the parser drops a final CR.
func (q *quoteTracker) insideQuotes() bool {
	return q.state == quoteQuoted
}

// normalizeHeader folds a header or column name for matching: case, spaces,
// underscores and hyphens do not count.
func normalizeHeader(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if r == ' ' || r == '_' || r == '-' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// unescapeCSVCell undoes evals.CSVCell: a value that starts with a quote followed by
// =, +, -, @, a tab or a carriage return loses the quote. It is applied to header fields
// only, so a column exported as CSV maps back on import; cells stay as imported.
func unescapeCSVCell(s string) string {
	if len(s) >= 2 && s[0] == '\'' && strings.IndexByte("=+-@\t\r", s[1]) >= 0 {
		return s[1:]
	}
	return s
}

// SuggestMapping matches CSV header fields to dataset columns by name, and then by
// type name. It returns, for each header index, the column ID or "" when none fits.
// A header that equals a column name is matched first. The rest match by normalized
// name only when one column has that normalized name; a name two columns share
// (customer-id and customer_id) leaves the header unmapped, and so does a type name two
// columns share (two file columns). A column is used once.
func SuggestMapping(header []string, columns []Column) []string {
	exact := make(map[string]string, len(columns))
	byName := make(map[string]string, len(columns))
	ambiguous := make(map[string]struct{})
	byType := make(map[string]string, len(columns))
	ambiguousType := make(map[string]struct{})
	for _, column := range columns {
		exact[column.Name] = column.ID
		key := normalizeHeader(column.Name)
		if other, seen := byName[key]; seen && other != column.ID {
			ambiguous[key] = struct{}{}
		}
		byName[key] = column.ID
		if column.Type != ColumnVariable {
			typeKey := normalizeHeader(string(column.Type))
			if other, seen := byType[typeKey]; seen && other != column.ID {
				ambiguousType[typeKey] = struct{}{}
			}
			byType[typeKey] = column.ID
		}
	}
	mapping := make([]string, len(header))
	used := make(map[string]struct{}, len(header))
	claim := func(i int, id string) {
		if _, taken := used[id]; taken {
			return
		}
		used[id] = struct{}{}
		mapping[i] = id
	}
	for i, field := range header {
		if id, ok := exact[strings.TrimSpace(unescapeCSVCell(field))]; ok {
			claim(i, id)
		}
	}
	// Names win over type names across all headers: every header is matched by
	// normalized name before any is matched by type name, so header order cannot
	// let a type name take the column a later header names.
	keys := make([]string, len(header))
	for i, field := range header {
		if mapping[i] != "" {
			continue
		}
		key := normalizeHeader(unescapeCSVCell(field))
		keys[i] = key
		if _, unclear := ambiguous[key]; unclear {
			continue
		}
		if id, ok := byName[key]; ok {
			claim(i, id)
		}
	}
	for i := range header {
		if mapping[i] != "" {
			continue
		}
		key := keys[i]
		if _, named := byName[key]; named {
			continue
		}
		if _, unclear := ambiguousType[key]; unclear {
			continue
		}
		if id, ok := byType[key]; ok {
			claim(i, id)
		}
	}
	return mapping
}

const hexDigits = "0123456789abcdef"

// appendJSONString appends s as a JSON string. It exists so a row's cells can be
// written into a pooled buffer without an allocation per cell.
func appendJSONString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			buf.WriteString(s[start:i])
			switch c {
			case '"', '\\':
				buf.WriteByte('\\')
				buf.WriteByte(c)
			case '\n':
				buf.WriteString(`\n`)
			case '\r':
				buf.WriteString(`\r`)
			case '\t':
				buf.WriteString(`\t`)
			default:
				buf.WriteString(`\u00`)
				buf.WriteByte(hexDigits[c>>4])
				buf.WriteByte(hexDigits[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			buf.WriteString(s[start:i])
			buf.WriteString(`�`)
			i += size
			start = i
			continue
		}
		i += size
	}
	buf.WriteString(s[start:])
	buf.WriteByte('"')
}

// EncodeCells writes the cells JSON object for one row: column ID to value, in the
// order given, leaving out empty values. ids and values run in parallel; an entry
// with an empty ID is skipped.
func EncodeCells(buf *bytes.Buffer, ids, values []string) {
	buf.WriteByte('{')
	first := true
	for i, id := range ids {
		if id == "" || i >= len(values) || values[i] == "" {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		appendJSONString(buf, id)
		buf.WriteByte(':')
		appendJSONString(buf, values[i])
	}
	buf.WriteByte('}')
}

// EncodeCellsMap is EncodeCells for a map, written in column order so the stored
// JSON is the same for the same row every time.
func EncodeCellsMap(columns []Column, cells map[string]string) string {
	buf := getBuffer()
	defer putBuffer(buf)
	buf.WriteByte('{')
	first := true
	for _, column := range columns {
		value := cells[column.ID]
		if value == "" {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		appendJSONString(buf, column.ID)
		buf.WriteByte(':')
		appendJSONString(buf, value)
	}
	buf.WriteByte('}')
	return buf.String()
}

// Export formats.
const (
	FormatCSV   = "csv"
	FormatJSONL = "jsonl"
)

// RowWriter writes dataset rows to a stream as CSV or JSONL. It keeps one record
// slice and one buffer for the whole export.
type RowWriter struct {
	columns []Column
	format  string
	out     io.Writer
	csv     *csv.Writer
	record  []string
	cells   map[string]string
}

// NewRowWriter returns a writer for the given columns. For CSV it writes the header.
func NewRowWriter(out io.Writer, format string, columns []Column) (*RowWriter, error) {
	w := &RowWriter{columns: columns, format: format, out: out, cells: make(map[string]string, len(columns))}
	switch format {
	case FormatCSV:
		w.csv = csv.NewWriter(out)
		w.record = make([]string, len(columns))
		for i, column := range columns {
			w.record[i] = evals.CSVCell(column.Name)
		}
		if err := w.csv.Write(w.record); err != nil {
			return nil, err
		}
	case FormatJSONL:
	default:
		return nil, invalid("unknown export format %q; use csv or jsonl", format)
	}
	return w, nil
}

// Write writes one row from its stored cells JSON.
func (w *RowWriter) Write(cellsJSON string) error {
	clear(w.cells)
	if cellsJSON != "" {
		if err := json.Unmarshal([]byte(cellsJSON), &w.cells); err != nil {
			return err
		}
	}
	if w.format == FormatCSV {
		// A cell a spreadsheet would run as a formula is written as text; JSONL keeps it as stored.
		// A structured cell drops its leading JSON whitespace, which has no meaning, so a
		// stored "\t[]" is not quoted as a formula and imports back as the same JSON.
		for i, column := range w.columns {
			value := w.cells[column.ID]
			if column.Type.Structured() {
				value = strings.TrimLeft(value, " \t\r\n")
			}
			w.record[i] = evals.CSVCell(value)
		}
		return w.csv.Write(w.record)
	}
	buf := getBuffer()
	defer putBuffer(buf)
	buf.WriteByte('{')
	for i, column := range w.columns {
		if i > 0 {
			buf.WriteByte(',')
		}
		appendJSONString(buf, column.Name)
		buf.WriteByte(':')
		value := w.cells[column.ID]
		// A structured cell is written as the JSON it holds, not as a string of JSON, and
		// compact, since a cell typed with line breaks would split the row's line. Compact
		// writes to buf only when the value is valid JSON.
		if !column.Type.Structured() || value == "" || json.Compact(buf, []byte(value)) != nil {
			appendJSONString(buf, value)
		}
	}
	buf.WriteString("}\n")
	_, err := w.out.Write(buf.Bytes())
	return err
}

// Flush writes anything buffered and reports a write error.
func (w *RowWriter) Flush() error {
	if w.csv != nil {
		w.csv.Flush()
		return w.csv.Error()
	}
	return nil
}
