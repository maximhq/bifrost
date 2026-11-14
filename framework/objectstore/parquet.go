package objectstore

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress"
)

// Parquet key-value metadata written by EncodeJSONObjectToParquet.
const (
	parquetSchemaVersionKey = "bifrost.schema_version"
	parquetJSONColumnsKey   = "bifrost.json_columns"
	parquetSchemaVersion    = "1"
	parquetSchemaName       = "bifrost_object"
)

var parquetMagic = []byte("PAR1")

// IsParquet reports whether data starts with the parquet file magic.
func IsParquet(data []byte) bool {
	return len(data) >= 8 && bytes.Equal(data[:4], parquetMagic)
}

// parquetCodec maps a Config compression name to a parquet page codec.
func parquetCodec(compression string) (compress.Codec, error) {
	switch compression {
	case "", CompressionNone:
		return &parquet.Uncompressed, nil
	case CompressionGzip:
		return &parquet.Gzip, nil
	case CompressionSnappy:
		return &parquet.Snappy, nil
	case CompressionZstd:
		return &parquet.Zstd, nil
	default:
		return nil, fmt.Errorf("objectstore: unsupported parquet compression %q", compression)
	}
}

// EncodeJSONObjectToParquet converts a top-level JSON object into a parquet
// file holding one row. Every key becomes an optional UTF8 column (columns are
// sorted by name). String values are stored unquoted so the columns are
// directly queryable; any other value (object, array, number, bool) is stored
// as its raw JSON text and its column is listed in the bifrost.json_columns
// metadata so DecodeParquetToJSONObject can restore it exactly. JSON null is
// stored as a parquet null.
func EncodeJSONObjectToParquet(data []byte, compression string) ([]byte, error) {
	codec, err := parquetCodec(compression)
	if err != nil {
		return nil, err
	}
	var fields map[string]sonic.NoCopyRawMessage
	if err := sonic.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("objectstore: parquet encode: payload is not a JSON object: %w", err)
	}
	if len(fields) == 0 {
		return nil, errors.New("objectstore: parquet encode: payload has no fields")
	}

	keys := make([]string, 0, len(fields))
	group := make(parquet.Group, len(fields))
	for k := range fields {
		if k == "" {
			return nil, errors.New("objectstore: parquet encode: empty field name")
		}
		keys = append(keys, k)
		group[k] = parquet.Optional(parquet.String())
	}
	sort.Strings(keys) // parquet.Group orders leaf columns by name

	row := make(parquet.Row, len(keys))
	var jsonColumns []string
	for i, k := range keys {
		raw := bytes.TrimSpace(fields[k])
		switch {
		case len(raw) == 0 || string(raw) == "null":
			row[i] = parquet.NullValue().Level(0, 0, i)
		case raw[0] == '"':
			var s string
			if err := sonic.Unmarshal(raw, &s); err != nil {
				return nil, fmt.Errorf("objectstore: parquet encode: field %q: %w", k, err)
			}
			row[i] = parquet.ByteArrayValue([]byte(s)).Level(0, 1, i)
		default:
			jsonColumns = append(jsonColumns, k)
			row[i] = parquet.ByteArrayValue(raw).Level(0, 1, i)
		}
	}

	var buf bytes.Buffer
	opts := []parquet.WriterOption{
		parquet.NewSchema(parquetSchemaName, group),
		parquet.Compression(codec),
		// Min/max bounds of a single value are the value itself: they would
		// copy every payload field into the page headers and the footer, so
		// they are skipped for every column.
		parquet.DataPageStatistics(false),
		parquet.KeyValueMetadata(parquetSchemaVersionKey, parquetSchemaVersion),
	}
	if len(jsonColumns) > 0 {
		encoded, err := sonic.Marshal(jsonColumns)
		if err != nil {
			return nil, fmt.Errorf("objectstore: parquet encode: %w", err)
		}
		opts = append(opts, parquet.KeyValueMetadata(parquetJSONColumnsKey, string(encoded)))
	}
	for _, k := range keys {
		opts = append(opts, parquet.SkipPageBounds(k))
	}
	w := parquet.NewWriter(&buf, opts...)
	if _, err := w.WriteRows([]parquet.Row{row}); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("objectstore: parquet encode: write row: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("objectstore: parquet encode: close: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeParquetToJSONObject reverses EncodeJSONObjectToParquet and returns the
// row as a JSON object with keys in sorted order.
func DecodeParquetToJSONObject(data []byte) ([]byte, error) {
	f, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)),
		parquet.SkipBloomFilters(true),
		parquet.ReadBufferSize(4096),
	)
	if err != nil {
		return nil, fmt.Errorf("objectstore: parquet decode: %w", err)
	}
	if f.NumRows() != 1 {
		return nil, fmt.Errorf("objectstore: parquet decode: expected 1 row, found %d", f.NumRows())
	}
	jsonColumns := map[string]struct{}{}
	if v, ok := f.Lookup(parquetJSONColumnsKey); ok && v != "" {
		var names []string
		if err := sonic.UnmarshalString(v, &names); err != nil {
			return nil, fmt.Errorf("objectstore: parquet decode: %s metadata: %w", parquetJSONColumnsKey, err)
		}
		for _, n := range names {
			jsonColumns[n] = struct{}{}
		}
	}

	columns := f.Schema().Columns()
	r := parquet.NewReader(f)
	defer r.Close()
	rows := make([]parquet.Row, 1)
	n, err := r.ReadRows(rows)
	if n != 1 {
		if err == nil {
			err = errors.New("no row read")
		}
		return nil, fmt.Errorf("objectstore: parquet decode: read row: %w", err)
	}

	values := make(map[string]parquet.Value, len(rows[0]))
	names := make([]string, 0, len(rows[0]))
	for _, v := range rows[0] {
		col := v.Column()
		if col < 0 || col >= len(columns) || len(columns[col]) != 1 {
			return nil, fmt.Errorf("objectstore: parquet decode: unexpected column %d", col)
		}
		name := columns[col][0]
		values[name] = v
		names = append(names, name)
	}
	sort.Strings(names)

	var out strings.Builder
	out.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			out.WriteByte(',')
		}
		key, err := sonic.Marshal(name)
		if err != nil {
			return nil, fmt.Errorf("objectstore: parquet decode: %w", err)
		}
		out.Write(key)
		out.WriteByte(':')
		v := values[name]
		switch {
		case v.IsNull():
			out.WriteString("null")
		default:
			if _, raw := jsonColumns[name]; raw {
				out.Write(v.ByteArray())
				continue
			}
			s, err := sonic.Marshal(string(v.ByteArray()))
			if err != nil {
				return nil, fmt.Errorf("objectstore: parquet decode: field %q: %w", name, err)
			}
			out.Write(s)
		}
	}
	out.WriteByte('}')
	return []byte(out.String()), nil
}
