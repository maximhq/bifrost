-- ============================================================
-- Bifrost Test Database Seed — 02: Providers
-- ============================================================

-- Providers are typically loaded from config.json but we also
-- support seeding them directly for integration tests via API.
-- This script provides INSERT statements for the providers table
-- as populated by the Bifrost configstore after first run.

INSERT INTO providers (id, name, type, base_url, enabled, created_at, updated_at)
VALUES
    ('provider_mock_openai_001', 'mock-openai',       'openai', 'http://localhost:9090', TRUE, NOW(), NOW()),
    ('provider_fast_002',        'provider-fast',     'openai', 'http://localhost:9091', TRUE, NOW(), NOW()),
    ('provider_slow_003',        'provider-slow',     'openai', 'http://localhost:9092', TRUE, NOW(), NOW()),
    ('provider_error_004',       'provider-error',    'openai', 'http://localhost:9093', TRUE, NOW(), NOW()),
    ('provider_secondary_005',   'provider-secondary','openai', 'http://localhost:9090', TRUE, NOW(), NOW()),
    ('provider_timeout_007',     'provider-timeout',  'openai', 'http://localhost:9094', TRUE, NOW(), NOW()),
    ('provider_vault_backed_008','provider-vault',    'openai', 'http://localhost:9090', TRUE, NOW(), NOW())
ON CONFLICT (id) DO NOTHING;

-- API Keys (stored encrypted; test mode Bifrost accepts these raw in non-prod)
INSERT INTO provider_keys (id, provider_id, name, key_encrypted, weight, enabled)
VALUES
    -- mock-openai keys
    ('key_mock_001',      'provider_mock_openai_001', 'mock-key-primary',  'sk-mock-openai-primary-test-key-001',  100, TRUE),
    -- provider-fast keys
    ('key_fast_001',      'provider_fast_002',         'fast-key',          'sk-mock-fast-provider-test-key-002',   100, TRUE),
    -- provider-slow keys
    ('key_slow_001',      'provider_slow_003',         'slow-key',          'sk-mock-slow-provider-test-key-003',   100, TRUE),
    -- provider-error keys
    ('key_error_001',     'provider_error_004',        'error-key',         'sk-mock-error-provider-test-key-004',  100, TRUE),
    -- provider-secondary (shares mock-openai backend)
    ('key_secondary_001', 'provider_secondary_005',    'secondary-key',     'sk-mock-secondary-provider-key-005',   100, TRUE),
    -- weighted keys for distribution test (provider_weighted not in providers table but for reference)
    ('key_weighted_a',    'provider_mock_openai_001',  'weighted-key-a',    'sk-mock-weighted-key-a-80-weight',      80, TRUE),
    ('key_weighted_b',    'provider_mock_openai_001',  'weighted-key-b',    'sk-mock-weighted-key-b-20-weight',      20, TRUE),
    -- timeout provider
    ('key_timeout_001',   'provider_timeout_007',      'timeout-key',       'sk-mock-timeout-provider-key-007',     100, TRUE),
    -- vault-backed provider (key resolved at runtime from Vault)
    ('key_vault_001',     'provider_vault_backed_008', 'vault-key',         'vault://secret/bifrost/openai#api_key',100, TRUE)
ON CONFLICT (id) DO NOTHING;
