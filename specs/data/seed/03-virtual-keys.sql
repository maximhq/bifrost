-- ============================================================
-- Bifrost Test Database Seed — 03: Virtual Keys
-- ============================================================

INSERT INTO virtual_keys (id, name, key_value, budget_max, budget_currency,
    budget_reset_duration, rate_limit_rpm, rate_limit_tpm,
    allowed_models, expires_at, enabled)
VALUES
    -- Unlimited VK for general tests
    ('vk_unlimited_test_001', 'vk-unlimited',
     'bfvk_unlimited_test_key_00000001',
     NULL, NULL, NULL, NULL, NULL, NULL, NULL, TRUE),

    -- Tight budget ($0.001)
    ('vk_tight_budget_test_002', 'vk-tight-budget',
     'bfvk_tight_budget_test_key_00002',
     0.001, 'USD', 'monthly', NULL, NULL, NULL, NULL, TRUE),

    -- Rate limited (1 req/min)
    ('vk_rate_limited_test_003', 'vk-rate-limited',
     'bfvk_rate_limited_test_key_00003',
     NULL, NULL, NULL, 1, NULL, NULL, NULL, TRUE),

    -- Expired VK
    ('vk_expired_test_004', 'vk-expired',
     'bfvk_expired_test_key_000000004',
     NULL, NULL, NULL, NULL, NULL, NULL,
     '2020-01-01 00:00:00+00', TRUE),

    -- Model restricted
    ('vk_model_restricted_test_005', 'vk-model-restricted',
     'bfvk_model_restricted_test_00005',
     NULL, NULL, NULL, NULL, NULL,
     ARRAY['gpt-4o-mini'], NULL, TRUE),

    -- Disabled
    ('vk_disabled_test_006', 'vk-disabled',
     'bfvk_disabled_test_key_000000006',
     NULL, NULL, NULL, NULL, NULL, NULL, NULL, FALSE),

    -- Token rate limit (100 tokens/min)
    ('vk_token_rate_test_007', 'vk-token-rate-limited',
     'bfvk_token_rate_test_key_000007',
     NULL, NULL, NULL, NULL, 100, NULL, NULL, TRUE),

    -- Combined limits
    ('vk_combined_limit_test_008', 'vk-combined-limits',
     'bfvk_combined_limit_test_000008',
     NULL, NULL, NULL, 10, 50, NULL, NULL, TRUE),

    -- Guardrail/PII test VK
    ('vk_guardrail_test_009', 'vk-for-guardrail-tests',
     'bfvk_guardrail_test_key_000009',
     NULL, NULL, NULL, NULL, NULL, NULL, NULL, TRUE),

    -- Performance test VK (high budget + high rate limit)
    ('vk_perf_test_010', 'vk-performance',
     'bfvk_performance_test_key_000010',
     1000.00, 'USD', 'monthly', 100000, NULL, NULL, NULL, TRUE)
ON CONFLICT (id) DO NOTHING;
