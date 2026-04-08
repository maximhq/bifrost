-- ============================================================
-- Bifrost Test Database Seed — 04: RBAC Roles & Permissions
-- Seeds system roles, permission matrix, and user assignments
-- ============================================================

-- ---- System Roles ----
INSERT INTO rbac_roles (id, name, description, is_system)
VALUES
    ('role_super_admin', 'super_admin', 'Full access to all resources including user management and license', TRUE),
    ('role_admin',       'admin',       'Full resource management except user management and license',       TRUE),
    ('role_operator',    'operator',    'Can manage virtual keys and view providers; cannot create providers',TRUE),
    ('role_viewer',      'viewer',      'Read-only access to all resources',                                 TRUE),
    ('role_api_user',    'api_user',    'Inference API access only; cannot access management endpoints',     TRUE)
ON CONFLICT (id) DO NOTHING;

-- ---- Permission Matrix ----
-- super_admin: all resources, all actions
INSERT INTO rbac_permissions (id, role_id, resource, action)
VALUES
    ('perm_sa_001', 'role_super_admin', '*', '*')
ON CONFLICT (id) DO NOTHING;

-- admin: read+write all except users and license
INSERT INTO rbac_permissions (id, role_id, resource, action)
VALUES
    ('perm_adm_001', 'role_admin', 'providers',    'read'),
    ('perm_adm_002', 'role_admin', 'providers',    'write'),
    ('perm_adm_003', 'role_admin', 'virtual_keys', 'read'),
    ('perm_adm_004', 'role_admin', 'virtual_keys', 'write'),
    ('perm_adm_005', 'role_admin', 'guardrails',   'read'),
    ('perm_adm_006', 'role_admin', 'guardrails',   'write'),
    ('perm_adm_007', 'role_admin', 'audit_logs',   'read'),
    ('perm_adm_008', 'role_admin', 'audit_logs',   'export'),
    ('perm_adm_009', 'role_admin', 'config',       'read'),
    ('perm_adm_010', 'role_admin', 'config',       'write'),
    ('perm_adm_011', 'role_admin', 'pii',          'read'),
    ('perm_adm_012', 'role_admin', 'pii',          'write'),
    ('perm_adm_013', 'role_admin', 'sso',          'read'),
    ('perm_adm_014', 'role_admin', 'sso',          'write'),
    ('perm_adm_015', 'role_admin', 'alerts',       'read'),
    ('perm_adm_016', 'role_admin', 'alerts',       'write'),
    ('perm_adm_017', 'role_admin', 'rbac_roles',   'read'),
    ('perm_adm_018', 'role_admin', 'logs',         'read'),
    ('perm_adm_019', 'role_admin', 'cluster',      'read'),
    ('perm_adm_020', 'role_admin', 'session',      'read')
ON CONFLICT (id) DO NOTHING;

-- operator: manage VKs, view providers
INSERT INTO rbac_permissions (id, role_id, resource, action)
VALUES
    ('perm_op_001', 'role_operator', 'providers',    'read'),
    ('perm_op_002', 'role_operator', 'virtual_keys', 'read'),
    ('perm_op_003', 'role_operator', 'virtual_keys', 'write'),
    ('perm_op_004', 'role_operator', 'audit_logs',   'read'),
    ('perm_op_005', 'role_operator', 'rbac_roles',   'read'),
    ('perm_op_006', 'role_operator', 'logs',         'read'),
    ('perm_op_007', 'role_operator', 'session',      'read')
ON CONFLICT (id) DO NOTHING;

-- viewer: read-only everywhere
INSERT INTO rbac_permissions (id, role_id, resource, action)
VALUES
    ('perm_vw_001', 'role_viewer', 'providers',    'read'),
    ('perm_vw_002', 'role_viewer', 'virtual_keys', 'read'),
    ('perm_vw_003', 'role_viewer', 'guardrails',   'read'),
    ('perm_vw_004', 'role_viewer', 'audit_logs',   'read'),
    ('perm_vw_005', 'role_viewer', 'rbac_roles',   'read'),
    ('perm_vw_006', 'role_viewer', 'logs',         'read'),
    ('perm_vw_007', 'role_viewer', 'config',       'read'),
    ('perm_vw_008', 'role_viewer', 'session',      'read')
ON CONFLICT (id) DO NOTHING;

-- api_user: inference only (no management)
INSERT INTO rbac_permissions (id, role_id, resource, action)
VALUES
    ('perm_api_001', 'role_api_user', 'inference', 'call')
ON CONFLICT (id) DO NOTHING;

-- ---- User ↔ Role Assignments ----
INSERT INTO rbac_user_roles (id, user_id, role_id, assigned_by)
VALUES
    ('ur_001', 'usr_super_001',    'role_super_admin', 'system'),
    ('ur_002', 'usr_admin_002',    'role_admin',       'system'),
    ('ur_003', 'usr_operator_003', 'role_operator',    'system'),
    ('ur_004', 'usr_viewer_004',   'role_viewer',      'system'),
    ('ur_005', 'usr_api_005',      'role_api_user',    'system')
ON CONFLICT (id) DO NOTHING;
