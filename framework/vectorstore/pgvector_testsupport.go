package vectorstore

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// EnsureRestrictedRole creates, if it does not exist, an ordinary login role (no superuser, no
// BYPASSRLS) that may create tables in the schema, using the connection settings of an
// administrator. It exists for tests and setup scripts that need a role PostgreSQL's row-level
// security applies to: a superuser skips every policy. The role keeps its password if it exists.
func EnsureRestrictedRole(ctx context.Context, admin PgVectorConfig, role, password string) error {
	if !pgvectorIdentifierPattern.MatchString(role) {
		return fmt.Errorf("invalid role name %q", role)
	}
	schema := strings.TrimSpace(admin.Schema.GetValue())
	if schema == "" {
		schema = pgvectorDefaultSchema
	}
	if !pgvectorIdentifierPattern.MatchString(schema) {
		return fmt.Errorf("invalid schema %q", schema)
	}
	sslMode := strings.TrimSpace(admin.SSLMode.GetValue())
	if sslMode == "" {
		sslMode = "prefer"
	}
	connURL := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(strings.TrimSpace(admin.User.GetValue()), admin.Password.GetValue()),
		Host:     net.JoinHostPort(strings.TrimSpace(admin.Host.GetValue()), strconv.Itoa(admin.Port.CoerceInt(5432))),
		Path:     "/" + strings.TrimSpace(admin.Database.GetValue()),
		RawQuery: url.Values{"sslmode": {sslMode}}.Encode(),
	}
	conn, err := pgx.Connect(ctx, connURL.String())
	if err != nil {
		return fmt.Errorf("failed to connect as the administrator: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		quoted := strings.ReplaceAll(password, "'", "''")
		if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE`, pgx.Identifier{role}.Sanitize(), quoted)); err != nil {
			// Another process may have created it a moment ago.
			if err2 := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err2 != nil || !exists {
				return fmt.Errorf("failed to create role %q: %w", role, err)
			}
		}
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`GRANT USAGE, CREATE ON SCHEMA %s TO %s`, pgx.Identifier{schema}.Sanitize(), pgx.Identifier{role}.Sanitize())); err != nil {
		return fmt.Errorf("failed to grant schema rights to %q: %w", role, err)
	}
	return nil
}
