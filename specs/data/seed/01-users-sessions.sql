-- ============================================================
-- Bifrost Test Database Seed — 01: Users & Sessions
-- Creates test users for each RBAC role + static auth tokens
-- ============================================================

-- Test users
INSERT INTO external_users (id, email, display_name, active, provisioned_via)
VALUES
    ('usr_super_001',    'superadmin@bifrost-test.local', 'Super Admin (Test)',   TRUE, 'manual'),
    ('usr_admin_002',    'admin@bifrost-test.local',      'Admin User (Test)',    TRUE, 'manual'),
    ('usr_operator_003', 'operator@bifrost-test.local',   'Operator User (Test)', TRUE, 'manual'),
    ('usr_viewer_004',   'viewer@bifrost-test.local',     'Viewer User (Test)',   TRUE, 'manual'),
    ('usr_api_005',      'apiuser@bifrost-test.local',    'API User (Test)',      TRUE, 'manual'),
    ('usr_expired_006',  'expired@bifrost-test.local',    'Expired Session User', TRUE, 'manual')
ON CONFLICT (id) DO NOTHING;

-- Static sessions (tokens are used as-is in Authorization headers)
-- In production, tokens are hashed. In test mode, Bifrost accepts these raw tokens.
-- Table name may vary; adjust to match actual configstore schema.
INSERT INTO sessions (id, user_id, token, expires_at, created_at)
VALUES
    ('sess_super_001',    'usr_super_001',    'tok_super_admin_test_12345', NOW() + INTERVAL '1 year', NOW()),
    ('sess_admin_002',    'usr_admin_002',    'tok_admin_test_67890',       NOW() + INTERVAL '1 year', NOW()),
    ('sess_operator_003', 'usr_operator_003', 'tok_operator_test_abcde',   NOW() + INTERVAL '1 year', NOW()),
    ('sess_viewer_004',   'usr_viewer_004',   'tok_viewer_test_fghij',     NOW() + INTERVAL '1 year', NOW()),
    ('sess_api_005',      'usr_api_005',      'tok_api_user_test_klmno',   NOW() + INTERVAL '1 year', NOW()),
    ('sess_expired_006',  'usr_expired_006',  'tok_expired_session_pqrst', NOW() - INTERVAL '1 hour', NOW())
ON CONFLICT (id) DO NOTHING;

-- SCIM tokens
INSERT INTO scim_tokens (id, token_hash, description, enabled, created_at)
VALUES
    ('scim_tok_001', 'scim_test_bearer_token_abc123', 'Test SCIM provisioning token', TRUE,  NOW()),
    ('scim_tok_002', 'scim_revoked_token_mnop456',    'Revoked SCIM token',           FALSE, NOW() - INTERVAL '7 days')
ON CONFLICT (id) DO NOTHING;
