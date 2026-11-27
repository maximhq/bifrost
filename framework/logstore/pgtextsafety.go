package logstore

import (
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// pgNonJSONTextColumns are never cast to jsonb, so only the bytes Postgres TEXT
// rejects are removed from them and their text is otherwise kept as written:
// request and response bodies kept verbatim for debugging, and plain-text
// columns that can still start with "{" or "[" (routing_engine_logs always
// does, content_summary whenever a message starts with JSON).
var pgNonJSONTextColumns = map[string]struct{}{
	"raw_request":               {},
	"raw_response":              {},
	"passthrough_request_body":  {},
	"passthrough_response_body": {},
	"content_summary":           {},
	"routing_engine_logs":       {},
}

// pgTextFieldCache maps a *schema.Schema to the []*schema.Field of its
// persisted string and *string columns.
var pgTextFieldCache sync.Map

// pgSafeText makes s storable in a Postgres TEXT or VARCHAR column. Postgres
// rejects a NUL byte and invalid UTF-8 with SQLSTATE 22021, failing the whole
// multi-row INSERT, so NUL bytes are dropped and invalid sequences become
// U+FFFD. A value made only of NUL bytes becomes U+FFFD rather than "": GORM's
// struct Updates skips "" as a zero value, which would keep the previous column
// value (and report no row updated when it was the only field). A clean string
// is returned as-is without allocating.
func pgSafeText(s string) string {
	if strings.IndexByte(s, 0) < 0 && utf8.ValidString(s) {
		return s
	}
	safe := strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "\uFFFD")
	if safe == "" {
		return "\uFFFD"
	}
	return safe
}

// pgSafeColumnValue applies pgSafeText to a column value and, for a column that
// holds serialized JSON, also sanitizeJSONForJSONB: GIN expression indexes cast
// metadata and the owner ID columns with ::jsonb during the INSERT itself, and
// ranking queries cast error_details, attempt_trail and others on read.
func pgSafeColumnValue(column, s string) string {
	s = pgSafeText(s)
	if _, notJSON := pgNonJSONTextColumns[column]; notJSON {
		return s
	}
	// Postgres IS JSON, and so the jsonb casts it guards, accepts leading JSON
	// whitespace, so classify by the first non-whitespace byte.
	trimmed := strings.TrimLeft(s, " \t\r\n")
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return s
	}
	return s[:len(s)-len(trimmed)] + sanitizeJSONForJSONB(trimmed)
}

// registerPGTextSafetyCallbacks installs GORM callbacks that clean every string
// column of a row before it is written to Postgres, so one request carrying a
// NUL byte, invalid UTF-8, or a JSON escape jsonb refuses cannot fail the
// writer's batch INSERT. They run after the model's BeforeCreate/BeforeSave
// hooks, because SerializeFields is what produces the JSON column strings.
// SQLite and ClickHouse accept these bytes, so only the Postgres store installs
// them.
func registerPGTextSafetyCallbacks(db *gorm.DB) {
	db.Callback().Create().After("gorm:before_create").Before("gorm:create").Register("bifrost:pg_text_safety", pgTextSafetyCallback)
	db.Callback().Update().After("gorm:before_update").Before("gorm:update").Register("bifrost:pg_text_safety", pgTextSafetyCallback)
}

func pgTextSafetyCallback(tx *gorm.DB) {
	stmt := tx.Statement
	if tx.Error != nil || stmt.Schema == nil {
		return
	}
	if updates := pgUpdateMap(stmt.Dest); updates != nil {
		for key, value := range updates {
			// GORM accepts a Go field name as an update key and resolves it to
			// its column, so classify by that column.
			column := key
			if field := stmt.Schema.LookUpField(key); field != nil && field.DBName != "" {
				column = field.DBName
			}
			switch v := value.(type) {
			case string:
				if safe := pgSafeColumnValue(column, v); safe != v {
					updates[key] = safe
				}
			case *string:
				if v != nil {
					if safe := pgSafeColumnValue(column, *v); safe != *v {
						updates[key] = &safe
					}
				}
			}
		}
	}
	fields := pgTextFields(stmt.Schema)
	if len(fields) == 0 {
		return
	}
	pgSanitizeRows(stmt, fields, stmt.ReflectValue)
	// Updates(&Log{...}) on a Model(&Log{}) statement takes its values from
	// Dest, not from ReflectValue.
	if dest := reflect.ValueOf(stmt.Dest); dest.Kind() == reflect.Pointer && !dest.IsNil() && dest.Elem().Kind() == reflect.Struct {
		if !stmt.ReflectValue.CanAddr() || stmt.ReflectValue.Addr().Pointer() != dest.Pointer() {
			pgSanitizeRows(stmt, fields, dest.Elem())
		}
	}
}

// pgUpdateMap returns dest when it is an update map, or the map a pointer to
// one refers to (GORM unwraps pointers to the map it updates from), and nil
// otherwise.
func pgUpdateMap(dest interface{}) map[string]interface{} {
	switch d := dest.(type) {
	case map[string]interface{}:
		return d
	case *map[string]interface{}:
		if d != nil {
			return *d
		}
	}
	return nil
}

// pgTextFields returns the persisted string and *string fields of s.
func pgTextFields(s *schema.Schema) []*schema.Field {
	if cached, ok := pgTextFieldCache.Load(s); ok {
		return cached.([]*schema.Field)
	}
	var fields []*schema.Field
	for _, field := range s.Fields {
		if field.DBName == "" {
			continue
		}
		t := field.FieldType
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() == reflect.String {
			fields = append(fields, field)
		}
	}
	pgTextFieldCache.Store(s, fields)
	return fields
}

// pgSanitizeRows cleans the text fields of rv, which is one row of the
// statement's model or a slice or array of rows.
func pgSanitizeRows(stmt *gorm.Statement, fields []*schema.Field, rv reflect.Value) {
	rv = reflect.Indirect(rv)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			pgSanitizeRows(stmt, fields, rv.Index(i))
		}
	case reflect.Struct:
		if rv.Type() != stmt.Schema.ModelType {
			return
		}
		for _, field := range fields {
			fv := field.ReflectValueOf(stmt.Context, rv)
			if !fv.CanSet() {
				continue
			}
			if fv.Kind() == reflect.Pointer {
				// Swap in a new pointer: the old pointee may be shared with
				// caller state (cached names, config), so it is never written.
				if fv.IsNil() {
					continue
				}
				if s := fv.Elem().String(); s != "" {
					if safe := pgSafeColumnValue(field.DBName, s); safe != s {
						nv := reflect.New(fv.Type().Elem())
						nv.Elem().SetString(safe)
						fv.Set(nv)
					}
				}
				continue
			}
			if s := fv.String(); s != "" {
				if safe := pgSafeColumnValue(field.DBName, s); safe != s {
					fv.SetString(safe)
				}
			}
		}
	}
}
