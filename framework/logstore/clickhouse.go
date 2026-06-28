package logstore

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

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
	// DialTimeout is a Go duration string ("10s") for the connection dial timeout.
	DialTimeout string `json:"dial_timeout,omitempty"`
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
	defaultClickHouseDialTimeout   = "10s"
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

	dialTimeout := config.DialTimeout
	if dialTimeout == "" {
		dialTimeout = defaultClickHouseDialTimeout
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
	q.Set("dial_timeout", dialTimeout)
	// clickhouse-go: native TLS is requested via secure=true; the https scheme
	// also requires secure=true; plain http must NOT set it.
	if config.Secure {
		q.Set("secure", "true")
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// newClickHouseLogStore creates a new ClickHouse log store. retentionDays drives
// the table TTL; values < 1 leave TTL unset (the LogsCleaner still prunes via
// DeleteLogsBatch).
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

	if err := db.WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		logger.Error("logstore: clickhouse ping failed: %v", err)
		return nil, fmt.Errorf("clickhouse ping failed: %w", err)
	}

	logger.Info("logstore: running clickhouse schema migrations")
	if err := clickhouseMigrate(ctx, db, config.Cluster, retentionDays, logger); err != nil {
		logger.Error("logstore: clickhouse schema migrations failed: %v", err)
		return nil, err
	}
	logger.Info("logstore: clickhouse schema migrations complete")

	return &ClickHouseLogStore{
		RDBLogStore: &RDBLogStore{db: db, logger: logger},
		cluster:     config.Cluster,
	}, nil
}
