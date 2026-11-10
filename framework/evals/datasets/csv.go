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
	buffered := bufio.NewReaderSize(r, 64<<10)
	if head, err := buffered.Peek(3); err == nil && bytes.Equal(head, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = buffered.Discard(3)
	}
	reader := csv.NewReader(buffered)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.ReuseRecord = true
	return reader
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

// SuggestMapping matches CSV header fields to dataset columns by name, and then by
// type name. It returns, for each header index, the column ID or "" when none fits.
func SuggestMapping(header []string, columns []Column) []string {
	byName := make(map[string]string, len(columns)*2)
	for _, column := range columns {
		if key := normalizeHeader(string(column.Type)); column.Type != ColumnVariable {
			byName[key] = column.ID
		}
	}
	// Names win over type names, so they are written second.
	for _, column := range columns {
		byName[normalizeHeader(column.Name)] = column.ID
	}
	mapping := make([]string, len(header))
	used := make(map[string]struct{}, len(header))
	for i, field := range header {
		id, ok := byName[normalizeHeader(field)]
		if !ok {
			continue
		}
		if _, taken := used[id]; taken {
			continue
		}
		used[id] = struct{}{}
		mapping[i] = id
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
			w.record[i] = column.Name
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
		for i, column := range w.columns {
			w.record[i] = w.cells[column.ID]
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
		// A structured cell is written as the JSON it holds, not as a string of JSON.
		if column.Type.Structured() && value != "" && json.Valid([]byte(value)) {
			buf.WriteString(value)
		} else {
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
