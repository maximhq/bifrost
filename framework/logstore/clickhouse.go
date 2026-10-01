package logstore

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	clickhousedriver "gorm.io/driver/clickhouse"
	"gorm.io/gorm"
)

// ClickHouseConfig represents the configuration for a ClickHouse log store.
//
// ClickHouse is an append-only columnar OLAP store. The backend uses
// ReplacingMergeTree tables with a connection-level `final = 1` setting so
// reads transparently see the latest version of each row (see clickhousestore.go
// for the mutation strategy).
type ClickHouseConfig struct {
	Host     *schemas.SecretVar `json:"host"`
	Port     *schemas.SecretVar `json:"port"`
	Database *schemas.SecretVar `json:"database"`
	Username *schemas.SecretVar `json:"username"`
	Password *schemas.SecretVar `json:"password"`
	// Protocol selects the ClickHouse wire protocol: "native" (default, port
	// 9000/9440) or "http" (port 8123/8443). clickhouse-go derives the protocol
	// from the DSN scheme, so this maps to clickhouse:// vs http(s)://.
	Protocol string `json:"protocol,omitempty"`
	// Secure enables TLS (native: secure=true; http: switches to https).
	Secure bool `json:"secure,omitempty"`
	// DialTimeout is the connection dial timeout in milliseconds (JSON config
	// duration fields are integer milliseconds). 0 means the 10s default.
	DialTimeout int `json:"dial_timeout,omitempty"`
	// Cluster, when set, makes DDL run as `ON CLUSTER <name>` against
	// ReplicatedReplacingMergeTree engines. Empty means single-node.
	Cluster string `json:"cluster,omitempty"`
}

const (
	defaultClickHouseNativePort    = "9000"
	defaultClickHouseNativeTLSPort = "9440"
	defaultClickHouseHTTPPort      = "8123"
	defaultClickHouseHTTPSPort     = "8443"
	defaultClickHouseDatabase      = "default"
	defaultClickHouseDialTimeout   = 10 * time.Second
	clickHouseProtocolNative       = "native"
	clickHouseProtocolHTTP         = "http"
)

func secretValue(v *schemas.SecretVar) string {
	if v == nil {
		return ""
	}
	return v.GetValue()
}

// buildClickHouseDSN assembles a clickhouse-go v2 DSN. The wire protocol is
// selected via the URL scheme (clickhouse:// = native, http(s):// = HTTP), and
// unknown query params (here, `final`) are passed through as ClickHouse
// settings, so every pooled connection applies FINAL automatically.
func buildClickHouseDSN(config *ClickHouseConfig) (string, error) {
	host := secretValue(config.Host)
	if host == "" {
		return "", fmt.Errorf("clickhouse: host is required")
	}

	// Resolve protocol -> URL scheme + default port. clickhouse-go requires
	// scheme "https" (not "http" + secure) for HTTP-over-TLS.
	var scheme, defaultPort string
	switch strings.ToLower(strings.TrimSpace(config.Protocol)) {
	case "", clickHouseProtocolNative:
		scheme = "clickhouse"
		if config.Secure {
			defaultPort = defaultClickHouseNativeTLSPort
		} else {
			defaultPort = defaultClickHouseNativePort
		}
	case clickHouseProtocolHTTP:
		if config.Secure {
			scheme = "https"
			defaultPort = defaultClickHouseHTTPSPort
		} else {
			scheme = "http"
			defaultPort = defaultClickHouseHTTPPort
		}
	default:
		return "", fmt.Errorf("clickhouse: unsupported protocol %q (use %q or %q)", config.Protocol, clickHouseProtocolNative, clickHouseProtocolHTTP)
	}

	port := secretValue(config.Port)
	if port == "" {
		port = defaultPort
	}

	database := secretValue(config.Database)
	if database == "" {
		database = defaultClickHouseDatabase
	}

	dialTimeout := defaultClickHouseDialTimeout
	if config.DialTimeout > 0 {
		dialTimeout = time.Duration(config.DialTimeout) * time.Millisecond
	}

	u := url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + database,
	}
	if user := secretValue(config.Username); user != "" {
		if pass := secretValue(config.Password); pass != "" {
			u.User = url.UserPassword(user, pass)
		} else {
			u.User = url.User(user)
		}
	}

	q := url.Values{}
	// Apply FINAL to every query so ReplacingMergeTree dedup is transparent to
	// the reused analytics read path (see clickhousestore.go).
	q.Set("final", "1")
	// The GORM ClickHouse driver rewrites DELETE/UPDATE into ALTER TABLE
	// mutations, which are asynchronous by default - a read right after a
	// delete would still see the rows. mutations_sync=1 makes the connection
	// wait until the mutation is applied on the current replica.
	q.Set("mutations_sync", "1")
	// The shared analytics SQL aliases aggregates with column names
	// (SUM(cost) AS cost) while filters reference the same names in WHERE.
	// ClickHouse resolves identifiers in WHERE to SELECT aliases by default
	// (error 184: aggregate function found in WHERE); this setting restores
	// the standard-SQL column-first resolution Postgres/SQLite use.
	q.Set("prefer_column_name_to_alias", "1")
	q.Set("dial_timeout", dialTimeout.String())
	// clickhouse-go: native TLS is requested via secure=true; the https scheme
	// also requires secure=true; plain http must NOT set it.
	if config.Secure {
		q.Set("secure", "true")
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// chMinServerVersion is the oldest ClickHouse release the store supports.
// chLightweightDelete relies on the lightweight_deletes_sync setting, which
// ClickHouse introduced in 24.4; older servers reject every delete with
// UNKNOWN_SETTING, so they are refused at startup instead of on the first
// sweep.
const chMinServerVersion = "24.4"

// chServerVersionSupported parses a ClickHouse version() string such as
// "26.6.1.1193" and reports whether it is at least chMinServerVersion.
func chServerVersionSupported(version string) (bool, error) {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 2 {
		return false, fmt.Errorf("clickhouse: unrecognised server version %q", version)
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false, fmt.Errorf("clickhouse: unrecognised server version %q", version)
	}
	return major > 24 || (major == 24 && minor >= 4), nil
}

// newClickHouseLogStore creates a new ClickHouse log store. retentionDays drives
// the table TTL, which is reconciled on every start so a changed value reaches
// existing tables; values < 1 leave any TTL untouched. Independently, the
// LogsCleaner (client_config.log_retention_days) prunes with a single
// lightweight delete per run.
func newClickHouseLogStore(ctx context.Context, config *ClickHouseConfig, retentionDays int, logger schemas.Logger) (LogStore, error) {
	dsn, err := buildClickHouseDSN(config)
	if err != nil {
		return nil, err
	}

	logger.Info("logstore: opening clickhouse connection (if this step hangs, the database host/port is likely unreachable)")
	db, err := gorm.Open(clickhousedriver.Open(dsn), &gorm.Config{
		Logger: newGormLogger(logger),
	})
	if err != nil {
		logger.Error("logstore: failed to open clickhouse connection: %v", err)
		return nil, err
	}

	// Release the pool on any startup failure past this point; ownership
	// transfers to the returned store only on success.
	constructed := false
	defer func() {
		if constructed {
			return
		}
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			if closeErr := sqlDB.Close(); closeErr != nil {
				logger.Error("logstore: failed to close clickhouse pool after startup failure: %v", closeErr)
			}
		}
	}()

	if err := db.WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		logger.Error("logstore: clickhouse ping failed: %v", err)
		return nil, fmt.Errorf("clickhouse ping failed: %w", err)
	}

	var serverVersion string
	if err := db.WithContext(ctx).Raw("SELECT version()").Scan(&serverVersion).Error; err != nil {
		logger.Error("logstore: failed to read clickhouse server version: %v", err)
		return nil, fmt.Errorf("clickhouse: read server version: %w", err)
	}
	if ok, err := chServerVersionSupported(serverVersion); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("clickhouse: server version %s is not supported; Bifrost requires ClickHouse %s or newer (lightweight DELETE with lightweight_deletes_sync)", serverVersion, chMinServerVersion)
	}
	logger.Info("logstore: clickhouse server version %s", serverVersion)

	logger.Info("logstore: running clickhouse schema migrations")
	if err := triggerClickHouseMigrations(ctx, db, config.Cluster, retentionDays, logger); err != nil {
		logger.Error("logstore: clickhouse schema migrations failed: %v", err)
		return nil, err
	}
	logger.Info("logstore: clickhouse schema migrations complete")

	constructed = true
	return &ClickHouseLogStore{
		RDBLogStore: &RDBLogStore{db: db, logger: logger},
		cluster:     config.Cluster,
	}, nil
}
