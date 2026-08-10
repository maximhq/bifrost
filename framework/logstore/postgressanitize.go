package logstore

import (
	"reflect"
	"strings"
)

// sanitizePostgresModelTextFields removes NUL bytes from persisted string
// fields. PostgreSQL rejects U+0000 in text and varchar values, while JSON
// payload fields remain intact because their NUL characters are escaped.
func sanitizePostgresModelTextFields(model any) {
	value := reflect.ValueOf(model)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return
	}
	value = value.Elem()
	if value.Kind() != reflect.Struct {
		return
	}

	modelType := value.Type()
	for i := 0; i < value.NumField(); i++ {
		if modelType.Field(i).Tag.Get("gorm") == "-" {
			continue
		}

		field := value.Field(i)
		switch {
		case field.Kind() == reflect.String && field.CanSet():
			field.SetString(sanitizePostgresText(field.String()))
		case field.Kind() == reflect.Pointer && !field.IsNil() && field.Type().Elem().Kind() == reflect.String && field.CanSet():
			original := field.Elem().String()
			sanitized := sanitizePostgresText(original)
			if sanitized != original {
				copyValue := reflect.New(field.Type().Elem())
				copyValue.Elem().SetString(sanitized)
				field.Set(copyValue)
			}
		}
	}
}

func sanitizePostgresText(value string) string {
	if !strings.ContainsRune(value, '\x00') {
		return value
	}
	return strings.ReplaceAll(value, "\x00", "")
}

// sanitizePostgresUpdateEntry copies map updates before sanitizing them so
// persistence does not mutate caller-owned update data.
func sanitizePostgresUpdateEntry(entry any) any {
	switch value := entry.(type) {
	case Log:
		copyEntry := value
		sanitizePostgresModelTextFields(&copyEntry)
		return copyEntry
	case MCPToolLog:
		copyEntry := value
		sanitizePostgresModelTextFields(&copyEntry)
		return copyEntry
	case map[string]interface{}:
		copyEntry := make(map[string]interface{}, len(value))
		for key, field := range value {
			copyEntry[key] = sanitizePostgresUpdateValue(field)
		}
		return copyEntry
	case map[string]string:
		copyEntry := make(map[string]string, len(value))
		for key, field := range value {
			copyEntry[key] = sanitizePostgresText(field)
		}
		return copyEntry
	default:
		sanitizePostgresModelTextFields(entry)
		return entry
	}
}

func sanitizePostgresUpdateValue(value any) any {
	switch field := value.(type) {
	case string:
		return sanitizePostgresText(field)
	case *string:
		if field == nil {
			return field
		}
		sanitized := sanitizePostgresText(*field)
		return &sanitized
	default:
		return value
	}
}
