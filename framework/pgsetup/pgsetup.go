// Package pgsetup provides Postgres connection bootstrap helpers: DSN assembly,
// schema-name validation, and idempotent schema creation.
package pgsetup

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

var schemaNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ResolveSchema returns the validated schema name, or an empty string when unset.
func ResolveSchema(s *schemas.EnvVar) (string, error) {
	if s == nil {
		return "", nil
	}
	name := s.GetValue()
	if name == "" {
		return "", nil
	}
	if len(name) > 63 {
		return "", fmt.Errorf("postgres schema name exceeds 63 characters: %q", name)
	}
	if !schemaNamePattern.MatchString(name) {
		return "", fmt.Errorf("postgres schema name must match [a-zA-Z_][a-zA-Z0-9_]*: %q", name)
	}
	return name, nil
}

// DSN holds the connection settings shared across stores.
type DSN struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
	SSLMode  string
	Schema   string
}

// escapeDSNValue quotes a libpq DSN value, escaping `\` and `'` when needed.
func escapeDSNValue(v string) string {
	if v == "" {
		return "''"
	}
	if !strings.ContainsAny(v, " \t\n\r\v\f'\\") {
		return v
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}

// BuildDSN renders a libpq DSN, prepending Schema to search_path when set.
func BuildDSN(d DSN) string {
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		escapeDSNValue(d.Host),
		escapeDSNValue(d.Port),
		escapeDSNValue(d.User),
		escapeDSNValue(d.Password),
		escapeDSNValue(d.DBName),
		escapeDSNValue(d.SSLMode),
	)
	if d.Schema != "" && d.Schema != "public" {
		dsn += fmt.Sprintf(` search_path="%s",public`, d.Schema)
	}
	return dsn
}

// EnsureSchema creates the named schema if missing. No-op when name is empty.
// Skips CREATE when the schema already exists. Pass name through ResolveSchema first.
func EnsureSchema(db *gorm.DB, name string) error {
	if name == "" {
		return nil
	}
	var exists bool
	if err := db.Raw(
		`SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = ?)`,
		name,
	).Scan(&exists).Error; err != nil {
		return fmt.Errorf("failed to check postgres schema %q: %w", name, err)
	}
	if exists {
		return nil
	}
	if err := db.Exec(fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q`, name)).Error; err != nil {
		return fmt.Errorf("failed to create postgres schema %q (DB user needs CREATE on the database, or pre-create the schema and grant USAGE+CREATE on it): %w", name, err)
	}
	return nil
}
