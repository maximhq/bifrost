-- ============================================================
-- Bifrost Test Database Seed — 05: Guardrails & PII Policies
-- ============================================================

-- ---- Guardrail Policies ----
INSERT INTO guardrail_policies (id, name, description, type, enabled, priority, action, scope, config)
VALUES
    (
        'policy_keyword_block_001',
        'block-dangerous-keywords',
        'Policy A — keyword block on request scope',
        'keyword', TRUE, 10, 'block',
        ARRAY['request'],
        '{"keywords":["bomb","weapon","explosive","assassination","terror"],"case_sensitive":false}'::jsonb
    ),
    (
        'policy_regex_ssn_002',
        'redact-ssn-in-response',
        'Policy B — SSN regex redact on response; used by TC-006-004',
        'regex', TRUE, 20, 'transform',
        ARRAY['response'],
        '{"patterns":["\\\\b\\\\d{3}-\\\\d{2}-\\\\d{4}\\\\b","\\\\b\\\\d{3}\\\\s\\\\d{2}\\\\s\\\\d{4}\\\\b"],"replacement":"[REDACTED_SSN]"}'::jsonb
    ),
    (
        'policy_keyword_flag_003',
        'flag-competitor-mentions',
        'Policy C — flag only, both scopes',
        'keyword', TRUE, 30, 'flag',
        ARRAY['request','response'],
        '{"keywords":["competitor_name","rival_product","switch_provider"],"case_sensitive":false}'::jsonb
    ),
    (
        'policy_disabled_004',
        'disabled-test-policy',
        'Policy D — disabled; must not fire',
        'keyword', FALSE, 5, 'block',
        ARRAY['request'],
        '{"keywords":["disabled_word","should_not_block"]}'::jsonb
    ),
    (
        'policy_priority_flag_006',
        'high-priority-flag',
        'Policy P1 — priority=1, flag; used in TC-006-008',
        'keyword', TRUE, 1, 'flag',
        ARRAY['request'],
        '{"keywords":["hello","greet"]}'::jsonb
    ),
    (
        'policy_priority_block_007',
        'low-priority-block',
        'Policy P2 — priority=2, block; must NOT fire if P1 matched first',
        'keyword', TRUE, 2, 'block',
        ARRAY['request'],
        '{"keywords":["hello","greet"]}'::jsonb
    )
ON CONFLICT (id) DO NOTHING;

-- ---- PII Rules ----
INSERT INTO pii_rules (id, entity_type, redaction_mode, enabled, regex_override)
VALUES
    ('pii_rule_email',  'EMAIL',       'mask', TRUE, NULL),
    ('pii_rule_phone',  'PHONE',       'mask', TRUE, NULL),
    ('pii_rule_ssn',    'SSN',         'mask', TRUE, NULL),
    ('pii_rule_cc',     'CREDIT_CARD', 'mask', TRUE, NULL),
    ('pii_rule_ip',     'IP_ADDRESS',  'mask', TRUE, NULL),
    ('pii_rule_custom', 'CUSTOM',      'mask', TRUE, 'EMP-\\d{6}')
ON CONFLICT (id) DO NOTHING;
