-- ============================================================
-- Bifrost Test Database Seed — 00: Schema Extensions
-- Run this AFTER the main Bifrost migration
-- ============================================================

-- Ensure test schema is clean
DROP SCHEMA IF EXISTS bifrost_test CASCADE;
CREATE SCHEMA IF NOT EXISTS public;

-- ============================================================
-- Enterprise tables (would be created by GORM AutoMigrate)
-- Listed here for explicitness in test environment
-- ============================================================

-- RBAC
CREATE TABLE IF NOT EXISTS rbac_roles (
    id          VARCHAR(64) PRIMARY KEY,
    name        VARCHAR(64) UNIQUE NOT NULL,
    description TEXT,
    is_system   BOOLEAN DEFAULT FALSE,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS rbac_permissions (
    id          VARCHAR(64) PRIMARY KEY,
    role_id     VARCHAR(64) REFERENCES rbac_roles(id),
    resource    VARCHAR(128) NOT NULL,
    action      VARCHAR(64) NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS rbac_user_roles (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64) NOT NULL,
    role_id     VARCHAR(64) REFERENCES rbac_roles(id),
    assigned_by VARCHAR(64),
    expires_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- Audit Logs (append-only enforced via trigger)
CREATE TABLE IF NOT EXISTS audit_logs (
    id            VARCHAR(64)  PRIMARY KEY,
    sequence      BIGSERIAL    UNIQUE,
    actor_id      VARCHAR(64),
    actor_email   VARCHAR(256),
    actor_role    VARCHAR(64),
    actor_ip      VARCHAR(64),
    action        VARCHAR(64)  NOT NULL,
    resource      VARCHAR(128) NOT NULL,
    resource_id   VARCHAR(64),
    resource_name VARCHAR(256),
    success       BOOLEAN      DEFAULT TRUE,
    error_message TEXT,
    before_state  JSONB,
    after_state   JSONB,
    prev_hash     VARCHAR(64),
    entry_hash    VARCHAR(64),
    timestamp     TIMESTAMPTZ  DEFAULT NOW()
);

-- Prevent UPDATE and DELETE on audit_logs
CREATE OR REPLACE FUNCTION audit_logs_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs are immutable — no UPDATE or DELETE allowed';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_audit_no_update ON audit_logs;
CREATE TRIGGER trg_audit_no_update
    BEFORE UPDATE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION audit_logs_immutable();

DROP TRIGGER IF EXISTS trg_audit_no_delete ON audit_logs;
CREATE TRIGGER trg_audit_no_delete
    BEFORE DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION audit_logs_immutable();

-- Users (external/SSO provisioned)
CREATE TABLE IF NOT EXISTS external_users (
    id               VARCHAR(64)  PRIMARY KEY,
    email            VARCHAR(256) UNIQUE NOT NULL,
    display_name     VARCHAR(256),
    external_id      VARCHAR(256),
    idp_source       VARCHAR(64),
    active           BOOLEAN      DEFAULT TRUE,
    provisioned_via  VARCHAR(32),
    created_at       TIMESTAMPTZ  DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  DEFAULT NOW()
);

-- Guardrail policies
CREATE TABLE IF NOT EXISTS guardrail_policies (
    id              VARCHAR(64)  PRIMARY KEY,
    name            VARCHAR(256) NOT NULL,
    description     TEXT,
    type            VARCHAR(32)  NOT NULL,
    enabled         BOOLEAN      DEFAULT TRUE,
    priority        INTEGER      DEFAULT 100,
    action          VARCHAR(32)  NOT NULL,
    scope           TEXT[]       NOT NULL,
    config          JSONB,
    created_at      TIMESTAMPTZ  DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  DEFAULT NOW()
);

-- Alert channels
CREATE TABLE IF NOT EXISTS alert_channels (
    id           VARCHAR(64)  PRIMARY KEY,
    name         VARCHAR(256) NOT NULL,
    type         VARCHAR(32)  NOT NULL,
    config       JSONB        NOT NULL,
    enabled      BOOLEAN      DEFAULT TRUE,
    events       TEXT[]       NOT NULL,
    created_at   TIMESTAMPTZ  DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  DEFAULT NOW()
);

-- Cluster nodes
CREATE TABLE IF NOT EXISTS cluster_nodes (
    id             VARCHAR(64)  PRIMARY KEY,
    hostname       VARCHAR(256) NOT NULL,
    address        VARCHAR(256) NOT NULL,
    version        VARCHAR(32),
    status         VARCHAR(32)  DEFAULT 'healthy',
    last_heartbeat TIMESTAMPTZ  DEFAULT NOW(),
    registered_at  TIMESTAMPTZ  DEFAULT NOW()
);

-- MCP Tool Groups
CREATE TABLE IF NOT EXISTS mcp_tool_groups (
    id          VARCHAR(64)  PRIMARY KEY,
    name        VARCHAR(256) UNIQUE NOT NULL,
    description TEXT,
    created_at  TIMESTAMPTZ  DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  DEFAULT NOW()
);

-- Data Connectors
CREATE TABLE IF NOT EXISTS data_connectors (
    id             VARCHAR(64)  PRIMARY KEY,
    name           VARCHAR(256) NOT NULL,
    type           VARCHAR(32)  NOT NULL,
    config         JSONB        NOT NULL,
    enabled        BOOLEAN      DEFAULT TRUE,
    last_sync_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ  DEFAULT NOW(),
    updated_at     TIMESTAMPTZ  DEFAULT NOW()
);

COMMENT ON TABLE audit_logs IS 'Immutable audit trail — protected by triggers';
