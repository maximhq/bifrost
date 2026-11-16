// Package output renders command results consistently for humans and automation.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

const (
	defaultTableWidth = 120
	maxTableColumns   = 8
	maxTableCellRunes = 48
)

// Format names a supported output representation.
type Format string

const (
	// Table renders compact human-readable rows.
	Table Format = "table"
	// JSON renders indented JSON suitable for automation.
	JSON Format = "json"
	// Raw writes the response body without interpretation.
	Raw Format = "raw"
	// YAML renders structured YAML suitable for automation and configuration workflows.
	YAML Format = "yaml"
)

// ParseFormat validates a user-provided output format.
func ParseFormat(value string) (Format, error) {
	format := Format(strings.ToLower(strings.TrimSpace(value)))
	if format == "" {
		return Table, nil
	}
	switch format {
	case Table, JSON, YAML, Raw:
		return format, nil
	default:
		return "", fmt.Errorf("unsupported output format %q; use table, json, yaml, or raw", value)
	}
}

// Print writes a response body in the selected format.
func Print(writer io.Writer, body []byte, format Format) error {
	if format == Raw {
		_, err := writer.Write(body)
		return err
	}

	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || decoder.More() {
		if format == Table {
			_, err := writer.Write([]byte(sanitizeCell(string(body))))
			return err
		}
		return Print(writer, body, Raw)
	}
	if format == JSON {
		encoded, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(writer, string(encoded))
		return err
	}
	if format == YAML {
		encoded, err := yaml.Marshal(toYAMLValue(value))
		if err != nil {
			return err
		}
		_, err = writer.Write(encoded)
		return err
	}
	return printTable(writer, value)
}

// toYAMLValue recursively replaces json.Number leaves with tagged scalar
// nodes so yaml.v3 emits them as numeric scalars instead of quoted strings,
// preserving lexemes (large integers included) that a float64 round-trip
// would otherwise lose precision on.
func toYAMLValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		text := typed.String()
		tag := "!!int"
		if strings.ContainsAny(text, ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: text}
	case map[string]any:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[key] = toYAMLValue(item)
		}
		return converted
	case []any:
		converted := make([]any, len(typed))
		for index, item := range typed {
			converted[index] = toYAMLValue(item)
		}
		return converted
	default:
		return value
	}
}

// printTable chooses a stable table representation for common JSON shapes.
func printTable(writer io.Writer, value any) error {
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"data", "items", "results", "providers", "models", "virtual_keys", "teams", "customers", "users", "api_keys", "keys"} {
			if rows, ok := object[key].([]any); ok {
				return printRows(writer, rows)
			}
		}
		return printObject(writer, object)
	}
	if rows, ok := value.([]any); ok {
		return printRows(writer, rows)
	}
	_, err := fmt.Fprintln(writer, scalar(value))
	return err
}

// printObject renders an object as sorted key/value rows.
func printObject(writer io.Writer, object map[string]any) error {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tw := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	for _, key := range keys {
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", sanitizeCell(strings.ToUpper(key)), scalar(object[key])); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// printRows renders arrays of objects as a table and other arrays one item per line.
func printRows(writer io.Writer, rows []any) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(writer, "No results.")
		return err
	}
	objects := make([]map[string]any, 0, len(rows))
	columnsSet := map[string]struct{}{}
	for _, row := range rows {
		object, ok := row.(map[string]any)
		if !ok {
			for _, item := range rows {
				if _, err := fmt.Fprintln(writer, scalar(item)); err != nil {
					return err
				}
			}
			return nil
		}
		objects = append(objects, object)
		for key := range object {
			columnsSet[key] = struct{}{}
		}
	}
	columns := populatedColumns(objects, prioritizedColumns(columnsSet))
	totalColumns := len(columns)
	columns = fitColumns(writer, objects, columns)
	tw := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	for index, column := range columns {
		if index > 0 {
			_, _ = fmt.Fprint(tw, "\t")
		}
		_, _ = fmt.Fprint(tw, sanitizeCell(strings.ToUpper(column)))
	}
	_, _ = fmt.Fprintln(tw)
	for _, object := range objects {
		for index, column := range columns {
			if index > 0 {
				_, _ = fmt.Fprint(tw, "\t")
			}
			_, _ = fmt.Fprint(tw, tableCell(object[column]))
		}
		_, _ = fmt.Fprintln(tw)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if totalColumns > len(columns) {
		_, err := fmt.Fprintf(writer, "\nShowing %d of %d fields. Use --output json or --output yaml for the complete response.\n", len(columns), totalColumns)
		return err
	}
	return nil
}

// fitColumns preserves the highest-value columns which fit in the current
// terminal. A deterministic fallback width keeps redirected output and tests
// readable too.
func fitColumns(writer io.Writer, objects []map[string]any, columns []string) []string {
	available := defaultTableWidth
	if fdWriter, ok := writer.(interface{ Fd() uintptr }); ok {
		if width, _, err := term.GetSize(int(fdWriter.Fd())); err == nil && width > 0 {
			available = width
		}
	}

	selected := make([]string, 0, min(len(columns), maxTableColumns))
	used := 0
	for _, column := range columns {
		width := utf8.RuneCountInString(strings.ToUpper(column))
		for _, object := range objects {
			if cellWidth := utf8.RuneCountInString(tableCell(object[column])); cellWidth > width {
				width = cellWidth
			}
		}
		separator := 0
		if len(selected) > 0 {
			separator = 2
		}
		if len(selected) > 0 && used+separator+width > available {
			continue
		}
		selected = append(selected, column)
		used += separator + width
		if len(selected) == maxTableColumns {
			break
		}
	}
	return selected
}

// populatedColumns removes fields which are empty in every row. APIs often
// return large schemas containing dozens of optional null or empty fields;
// rendering those fields makes the default table wider without adding useful
// information.
func populatedColumns(objects []map[string]any, columns []string) []string {
	populated := make([]string, 0, len(columns))
	for _, column := range columns {
		for _, object := range objects {
			if hasDisplayValue(object[column]) {
				populated = append(populated, column)
				break
			}
		}
	}
	return populated
}

func hasDisplayValue(value any) bool {
	if value == nil {
		return false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		// Zero and false are meaningful values and must remain visible.
		return true
	}
}

// prioritizedColumns returns stable columns with common identity fields first.
func prioritizedColumns(set map[string]struct{}) []string {
	columns := make([]string, 0, len(set))
	for key := range set {
		columns = append(columns, key)
	}
	sort.Strings(columns)
	priority := map[string]int{
		"id": 0, "name": 1, "email": 2, "provider": 3, "model": 4,
		"status": 5, "role": 6, "roles": 7, "type": 8, "user_type": 9,
		"enabled": 10, "is_active": 11, "created_at": 12, "updated_at": 13,
		"description": 14,
	}
	sort.SliceStable(columns, func(i, j int) bool {
		left, leftOK := priority[columns[i]]
		right, rightOK := priority[columns[j]]
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK {
			return left < right
		}
		return columns[i] < columns[j]
	})
	return columns
}

// tableCell converts nested JSON into a concise human-readable value. The
// machine-oriented formats continue to return the complete unmodified data.
func tableCell(value any) string {
	return truncateCell(summarize(value), maxTableCellRunes)
}

func summarize(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return sanitizeCell(typed)
	case float64, bool, json.Number:
		return fmt.Sprint(typed)
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if rendered := summarize(item); rendered != "" {
				values = append(values, rendered)
			}
		}
		return strings.Join(values, ", ")
	case map[string]any:
		for _, key := range []string{"name", "email", "label", "id"} {
			if rendered := summarize(typed[key]); rendered != "" {
				return rendered
			}
		}

		keys := make([]string, 0, len(typed))
		for key, item := range typed {
			if hasDisplayValue(item) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		values := make([]string, 0, len(keys))
		for _, key := range keys {
			if rendered := summarize(typed[key]); rendered != "" {
				values = append(values, sanitizeCell(key)+"="+rendered)
			}
			if len(values) == 3 {
				break
			}
		}
		if len(keys) > len(values) {
			values = append(values, "…")
		}
		return strings.Join(values, ", ")
	default:
		return sanitizeCell(fmt.Sprint(typed))
	}
}

// sanitizeCell neutralizes newline, carriage-return, and tab into a readable
// form and strips any other C0/DEL control character (ESC included) so a
// human-oriented rendering cannot carry terminal escape sequences. Raw output
// bypasses this entirely and stays byte-for-byte unfiltered.
func sanitizeCell(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\\n")
	value = strings.ReplaceAll(value, "\r", "\\n")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\t", " ")
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func truncateCell(value string, limit int) string {
	if limit < 1 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

// scalar converts a JSON value to a single printable cell.
func scalar(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return sanitizeCell(typed)
	case float64, bool, json.Number:
		return fmt.Sprint(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}
