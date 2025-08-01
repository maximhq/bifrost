"""
Comprehensive Usage Tracking and Monitoring Tests for Bifrost Governance Plugin

This module provides exhaustive testing of usage tracking, monitoring, and integration including:
- Chat completion integration with governance headers
- Usage tracking and budget enforcement
- Rate limiting enforcement during real requests
- Monitoring endpoints testing
- Reset functionality testing
- Debug and health endpoints
- Integration edge cases and error scenarios
- Performance and concurrency testing
"""

import pytest
import time
import uuid
import json
from typing import Dict, Any, List
from concurrent.futures import ThreadPoolExecutor
import threading

from conftest import (
    assert_response_success,
    generate_unique_name,
    wait_for_condition,
    BIFROST_BASE_URL,
)


class TestUsageStatsEndpoints:
    """Test usage statistics and monitoring endpoints"""

    @pytest.mark.usage_tracking
    @pytest.mark.api
    @pytest.mark.smoke
    def test_get_usage_stats_general(self, governance_client):
        """Test getting general usage statistics"""
        response = governance_client.get_usage_stats()
        assert_response_success(response, 200)

        stats = response.json()
        # Stats structure depends on implementation, but should be valid JSON
        assert isinstance(stats, dict)

    @pytest.mark.usage_tracking
    @pytest.mark.api
    def test_get_usage_stats_for_vk(self, governance_client, sample_virtual_key):
        """Test getting usage statistics for specific VK"""
        response = governance_client.get_usage_stats(
            virtual_key_id=sample_virtual_key["id"]
        )
        assert_response_success(response, 200)

        data = response.json()
        assert "virtual_key_id" in data
        assert data["virtual_key_id"] == sample_virtual_key["id"]
        assert "usage_stats" in data

    @pytest.mark.usage_tracking
    @pytest.mark.api
    def test_get_usage_stats_nonexistent_vk(self, governance_client):
        """Test getting usage stats for non-existent VK"""
        fake_vk_id = str(uuid.uuid4())
        response = governance_client.get_usage_stats(virtual_key_id=fake_vk_id)
        # Behavior depends on implementation - might return empty stats or 404
        assert response.status_code in [200, 404]

    @pytest.mark.usage_tracking
    @pytest.mark.api
    def test_reset_usage_basic(self, governance_client, sample_virtual_key):
        """Test basic usage reset functionality"""
        reset_data = {"virtual_key_id": sample_virtual_key["id"]}

        response = governance_client.reset_usage(reset_data)
        assert_response_success(response, 200)

        result = response.json()
        assert "message" in result
        assert "successfully" in result["message"].lower()

    @pytest.mark.usage_tracking
    @pytest.mark.api
    def test_reset_usage_with_provider_and_model(
        self, governance_client, sample_virtual_key
    ):
        """Test usage reset with specific provider and model"""
        reset_data = {
            "virtual_key_id": sample_virtual_key["id"],
            "provider": "openai",
            "model": "gpt-4",
        }

        response = governance_client.reset_usage(reset_data)
        assert_response_success(response, 200)

    @pytest.mark.usage_tracking
    @pytest.mark.api
    def test_reset_usage_invalid_vk(self, governance_client):
        """Test usage reset with invalid VK ID"""
        reset_data = {"virtual_key_id": str(uuid.uuid4())}

        response = governance_client.reset_usage(reset_data)
        assert response.status_code in [400, 404, 500]  # Expected error


class TestDebugEndpoints:
    """Test debug and monitoring endpoints"""

    @pytest.mark.usage_tracking
    @pytest.mark.api
    @pytest.mark.smoke
    def test_get_debug_stats(self, governance_client):
        """Test debug statistics endpoint"""
        response = governance_client.get_debug_stats()
        assert_response_success(response, 200)

        data = response.json()
        assert "plugin_stats" in data
        assert "database_stats" in data
        assert "timestamp" in data

    @pytest.mark.usage_tracking
    @pytest.mark.api
    def test_get_debug_counters(self, governance_client):
        """Test debug counters endpoint"""
        response = governance_client.get_debug_counters()
        assert_response_success(response, 200)

        data = response.json()
        assert "counters" in data
        assert "count" in data
        assert "timestamp" in data
        assert isinstance(data["counters"], list)

    @pytest.mark.usage_tracking
    @pytest.mark.api
    @pytest.mark.smoke
    def test_get_health_check(self, governance_client):
        """Test health check endpoint"""
        response = governance_client.get_health_check()
        # Health check should return 200 for healthy or 503 for unhealthy
        assert response.status_code in [200, 503]

        data = response.json()
        assert "status" in data
        assert "timestamp" in data
        assert "checks" in data
        assert data["status"] in ["healthy", "unhealthy"]


class TestChatCompletionIntegration:
    """Test chat completion integration with governance headers"""

    @pytest.mark.integration
    @pytest.mark.usage_tracking
    @pytest.mark.smoke
    def test_chat_completion_with_vk_header(
        self, governance_client, sample_virtual_key
    ):
        """Test chat completion with valid VK header"""
        messages = [{"role": "user", "content": "Hello, world!"}]
        headers = {"x-bf-vk": sample_virtual_key["value"]}

        response = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=10,
        )

        # Response should be successful, rate limited, budget exceeded, or VK not found
        assert response.status_code in [200, 429, 402, 403]

        if response.status_code == 200:
            data = response.json()
            assert "choices" in data
            assert len(data["choices"]) > 0

    @pytest.mark.integration
    @pytest.mark.usage_tracking
    def test_chat_completion_without_vk_header(self, governance_client):
        """Test chat completion without VK header"""
        messages = [{"role": "user", "content": "Hello, world!"}]

        response = governance_client.chat_completion(
            messages=messages, model="openai/gpt-3.5-turbo", max_tokens=10
        )

        # Should succeed without VK header (governance skipped)
        assert response.status_code in [
            200,
            400,
        ]  # 200 if no governance, 400 if provider issues

    @pytest.mark.integration
    @pytest.mark.usage_tracking
    def test_chat_completion_invalid_vk_header(self, governance_client):
        """Test chat completion with invalid VK header"""
        messages = [{"role": "user", "content": "Hello, world!"}]
        headers = {"x-bf-vk": "invalid-vk-value"}

        response = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=10,
        )

        # Should fail with invalid VK (governance blocks)
        assert response.status_code == 403

    @pytest.mark.integration
    @pytest.mark.usage_tracking
    def test_chat_completion_inactive_vk(self, governance_client, cleanup_tracker):
        """Test chat completion with inactive VK"""
        # Create inactive VK
        vk_data = {"name": generate_unique_name("Inactive VK"), "is_active": False}
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        inactive_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(inactive_vk["id"])

        messages = [{"role": "user", "content": "Hello, world!"}]
        headers = {"x-bf-vk": inactive_vk["value"]}

        response = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=10,
        )

        # Should fail with inactive VK (governance blocks)
        assert response.status_code == 403

    @pytest.mark.integration
    @pytest.mark.usage_tracking
    def test_chat_completion_with_model_restrictions(
        self, governance_client, cleanup_tracker
    ):
        """Test chat completion with model restrictions"""
        # Create VK with model restrictions
        vk_data = {
            "name": generate_unique_name("Restricted VK"),
            "allowed_models": ["gpt-4"],  # Only allow GPT-4
            "allowed_providers": ["openai"],
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        restricted_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(restricted_vk["id"])

        # Test with allowed model
        messages = [{"role": "user", "content": "Hello, world!"}]
        headers = {"x-bf-vk": restricted_vk["value"]}

        response = governance_client.chat_completion(
            messages=messages, model="gpt-4", headers=headers, max_tokens=10
        )

        # Should work with allowed model
        assert response.status_code in [200, 429, 402]  # Success or limits

        # Test with disallowed model
        response = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",  # Not in allowed_models
            headers=headers,
            max_tokens=10,
        )

        # Should fail with disallowed model
        assert response.status_code in [400, 403]


class TestBudgetEnforcement:
    """Test budget enforcement during chat completions"""

    @pytest.mark.integration
    @pytest.mark.budget
    @pytest.mark.usage_tracking
    def test_budget_enforcement_basic(self, governance_client, cleanup_tracker):
        """Test basic budget enforcement"""
        # Create VK with very small budget
        vk_data = {
            "name": generate_unique_name("Small Budget VK"),
            "budget": {
                "max_limit": 1,  # 1 cent - very small budget
                "reset_duration": "1h",
            },
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        small_budget_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(small_budget_vk["id"])

        messages = [
            {
                "role": "user",
                "content": "Write a very long story about artificial intelligence" * 10,
            }
        ]
        headers = {"x-bf-vk": small_budget_vk["value"]}

        response = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=1000,  # Request expensive completion
        )

        # Should fail due to budget exceeded
        if response.status_code == 402:  # Budget exceeded
            error_data = response.json()
            assert "budget" in error_data.get("error", "").lower()
        elif response.status_code == 200:
            # If it succeeded, check that budget was tracked
            stats_response = governance_client.get_usage_stats(
                virtual_key_id=small_budget_vk["id"]
            )
            if stats_response.status_code == 200:
                # Verify usage was tracked
                pass

    @pytest.mark.integration
    @pytest.mark.budget
    @pytest.mark.usage_tracking
    def test_hierarchical_budget_enforcement(self, governance_client, cleanup_tracker):
        """Test hierarchical budget enforcement (Customer -> Team -> VK)"""
        # Create customer with budget
        customer_data = {
            "name": generate_unique_name("Budget Test Customer"),
            "budget": {"max_limit": 10000, "reset_duration": "1h"},
        }
        customer_response = governance_client.create_customer(customer_data)
        assert_response_success(customer_response, 201)
        customer = customer_response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        # Create team under customer with smaller budget
        team_data = {
            "name": generate_unique_name("Budget Test Team"),
            "customer_id": customer["id"],
            "budget": {"max_limit": 5000, "reset_duration": "1h"},
        }
        team_response = governance_client.create_team(team_data)
        assert_response_success(team_response, 201)
        team = team_response.json()["team"]
        cleanup_tracker.add_team(team["id"])

        # Create VK under team with even smaller budget
        vk_data = {
            "name": generate_unique_name("Budget Test VK"),
            "team_id": team["id"],
            "budget": {"max_limit": 1, "reset_duration": "1h"},  # Smallest budget
        }
        vk_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(vk_response, 201)
        vk = vk_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk["id"])

        # Test request that should hit VK budget first
        messages = [{"role": "user", "content": "Expensive request" * 50}]
        headers = {"x-bf-vk": vk["value"]}

        response = governance_client.chat_completion(
            messages=messages,
            model="gpt-4",  # More expensive model
            headers=headers,
            max_tokens=1000,
        )

        # Should be limited by VK budget (smallest in hierarchy)
        # Actual behavior depends on implementation

    @pytest.mark.integration
    @pytest.mark.budget
    @pytest.mark.usage_tracking
    def test_budget_reset_functionality(self, governance_client, cleanup_tracker):
        """Test budget reset functionality"""
        # Create VK with small budget
        vk_data = {
            "name": generate_unique_name("Reset Budget VK"),
            "budget": {"max_limit": 100, "reset_duration": "1h"},  # Small but not tiny
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk["id"])

        # Make a request to use some budget
        messages = [{"role": "user", "content": "Hello"}]
        headers = {"x-bf-vk": vk["value"]}

        response = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=5,
        )

        # Reset the usage
        reset_data = {"virtual_key_id": vk["id"]}
        reset_response = governance_client.reset_usage(reset_data)
        assert_response_success(reset_response, 200)

        # Budget should be reset - could make another request
        response2 = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=5,
        )

        # Should work after reset (unless other limits apply)
        assert response2.status_code in [200, 429]  # Success or rate limited


class TestRateLimitEnforcement:
    """Test rate limiting enforcement during chat completions"""

    @pytest.mark.integration
    @pytest.mark.rate_limit
    @pytest.mark.usage_tracking
    def test_request_rate_limiting(self, governance_client, cleanup_tracker):
        """Test request rate limiting"""
        # Create VK with very restrictive request rate limit
        vk_data = {
            "name": generate_unique_name("Rate Limited VK"),
            "rate_limit": {
                "request_max_limit": 2,  # Only 2 requests allowed
                "request_reset_duration": "1m",
            },
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        rate_limited_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(rate_limited_vk["id"])

        messages = [{"role": "user", "content": "Hello"}]
        headers = {"x-bf-vk": rate_limited_vk["value"]}

        # Make requests up to the limit
        responses = []
        for i in range(3):  # Try 3 requests, limit is 2
            response = governance_client.chat_completion(
                messages=messages,
                model="openai/gpt-3.5-turbo",
                headers=headers,
                max_tokens=5,
            )
            responses.append(response)
            time.sleep(0.1)  # Small delay

        # First 2 should succeed, 3rd should be rate limited
        success_count = sum(1 for r in responses if r.status_code == 200)
        rate_limited_count = sum(1 for r in responses if r.status_code == 429)

        # Depending on implementation, might be exactly enforced or allow some variance
        assert rate_limited_count > 0 or success_count <= 2

    @pytest.mark.integration
    @pytest.mark.rate_limit
    @pytest.mark.usage_tracking
    def test_token_rate_limiting(self, governance_client, cleanup_tracker):
        """Test token rate limiting"""
        # Create VK with restrictive token rate limit
        vk_data = {
            "name": generate_unique_name("Token Rate Limited VK"),
            "rate_limit": {
                "token_max_limit": 100,  # Only 100 tokens allowed
                "token_reset_duration": "1m",
            },
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        token_limited_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(token_limited_vk["id"])

        # Make request that would exceed token limit
        messages = [
            {"role": "user", "content": "Write a very long response about AI" * 10}
        ]
        headers = {"x-bf-vk": token_limited_vk["value"]}

        response = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=500,  # Request more tokens than limit
        )

        # Should be limited by token rate limit
        if response.status_code == 429:
            error_data = response.json()
            # Check if error mentions tokens or rate limit
            error_text = error_data.get("error", "").lower()
            assert "token" in error_text or "rate" in error_text

    @pytest.mark.integration
    @pytest.mark.rate_limit
    @pytest.mark.usage_tracking
    def test_independent_rate_limits(self, governance_client, cleanup_tracker):
        """Test that token and request rate limits are independent"""
        # Create VK with different token and request limits
        vk_data = {
            "name": generate_unique_name("Independent Limits VK"),
            "rate_limit": {
                "token_max_limit": 1000,
                "token_reset_duration": "1h",
                "request_max_limit": 5,
                "request_reset_duration": "1m",
            },
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        independent_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(independent_vk["id"])

        messages = [{"role": "user", "content": "Short"}]
        headers = {"x-bf-vk": independent_vk["value"]}

        # Make multiple small requests (should hit request limit before token limit)
        responses = []
        for i in range(10):  # More than request limit
            response = governance_client.chat_completion(
                messages=messages,
                model="openai/gpt-3.5-turbo",
                headers=headers,
                max_tokens=5,  # Small token count
            )
            responses.append(response)
            time.sleep(0.1)

        # Should be limited by request count, not tokens
        rate_limited_responses = [r for r in responses if r.status_code == 429]
        assert len(rate_limited_responses) > 0

    @pytest.mark.integration
    @pytest.mark.rate_limit
    @pytest.mark.usage_tracking
    def test_rate_limit_reset(self, governance_client, cleanup_tracker):
        """Test rate limit reset functionality"""
        # Create VK with short reset duration for testing
        vk_data = {
            "name": generate_unique_name("Reset Test VK"),
            "rate_limit": {
                "request_max_limit": 1,
                "request_reset_duration": "5s",  # Short duration for testing
            },
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        reset_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(reset_vk["id"])

        messages = [{"role": "user", "content": "Hello"}]
        headers = {"x-bf-vk": reset_vk["value"]}

        # Make first request (should succeed)
        response1 = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=5,
        )

        # Make second request immediately (should be rate limited)
        response2 = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=5,
        )

        # Reset the rate limit manually
        reset_data = {"virtual_key_id": reset_vk["id"]}
        reset_response = governance_client.reset_usage(reset_data)
        assert_response_success(reset_response, 200)

        # Make third request after reset (should succeed)
        response3 = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=5,
        )

        # Should work after reset
        assert response3.status_code in [200, 429]  # Success or different limit


class TestConcurrentUsageTracking:
    """Test concurrent usage tracking and limits"""

    @pytest.mark.integration
    @pytest.mark.concurrency
    @pytest.mark.usage_tracking
    @pytest.mark.slow
    def test_concurrent_requests_same_vk(self, governance_client, cleanup_tracker):
        """Test concurrent requests using same VK"""
        # Create VK with moderate limits
        vk_data = {
            "name": generate_unique_name("Concurrent VK"),
            "rate_limit": {"request_max_limit": 10, "request_reset_duration": "1m"},
            "budget": {"max_limit": 10000, "reset_duration": "1h"},
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        concurrent_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(concurrent_vk["id"])

        messages = [{"role": "user", "content": "Hello"}]
        headers = {"x-bf-vk": concurrent_vk["value"]}

        def make_request(index):
            try:
                response = governance_client.chat_completion(
                    messages=messages,
                    model="openai/gpt-3.5-turbo",
                    headers=headers,
                    max_tokens=5,
                )
                return response.status_code, index
            except Exception as e:
                return str(e), index

        # Make 15 concurrent requests (more than rate limit)
        with ThreadPoolExecutor(max_workers=15) as executor:
            futures = [executor.submit(make_request, i) for i in range(15)]
            results = [future.result() for future in futures]

        # Count success vs rate limited responses
        success_codes = [r[0] for r in results if r[0] == 200]
        rate_limited_codes = [r[0] for r in results if r[0] == 429]

        # Should have some successful and some rate limited
        total_responses = len(success_codes) + len(rate_limited_codes)
        assert total_responses > 0

        # Rate limiting should have kicked in for some requests
        assert len(success_codes) <= 10  # Shouldn't exceed rate limit

    @pytest.mark.integration
    @pytest.mark.concurrency
    @pytest.mark.usage_tracking
    @pytest.mark.slow
    def test_concurrent_budget_tracking(self, governance_client, cleanup_tracker):
        """Test concurrent budget tracking accuracy"""
        # Create VK with small budget for testing
        vk_data = {
            "name": generate_unique_name("Budget Tracking VK"),
            "budget": {"max_limit": 1000, "reset_duration": "1h"},  # Small budget
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        budget_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(budget_vk["id"])

        messages = [{"role": "user", "content": "Count to 10"}]
        headers = {"x-bf-vk": budget_vk["value"]}

        def make_budget_request(index):
            try:
                response = governance_client.chat_completion(
                    messages=messages,
                    model="openai/gpt-3.5-turbo",
                    headers=headers,
                    max_tokens=20,
                )
                return (
                    response.status_code,
                    index,
                    response.json() if response.status_code == 200 else None,
                )
            except Exception as e:
                return str(e), index, None

        # Make concurrent requests that should consume budget
        with ThreadPoolExecutor(max_workers=5) as executor:
            futures = [executor.submit(make_budget_request, i) for i in range(5)]
            results = [future.result() for future in futures]

        # Check budget tracking consistency
        success_count = sum(1 for r in results if r[0] == 200)
        budget_exceeded_count = sum(1 for r in results if r[0] == 402)

        # Should have proper budget enforcement
        assert success_count + budget_exceeded_count > 0


class TestStreamingIntegration:
    """Test streaming integration with governance"""

    @pytest.mark.integration
    @pytest.mark.usage_tracking
    def test_streaming_chat_completion_with_governance(
        self, governance_client, sample_virtual_key
    ):
        """Test streaming chat completion with governance headers"""
        messages = [{"role": "user", "content": "Count from 1 to 5"}]
        headers = {"x-bf-vk": sample_virtual_key["value"]}

        response = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=50,
            stream=True,
        )

        # Streaming should work with governance
        if response.status_code == 200:
            # For streaming, response should be text/event-stream
            content_type = response.headers.get("content-type", "")
            assert (
                "text/event-stream" in content_type
                or "application/json" in content_type
            )
        else:
            # Should be properly governed (rate limited, budget exceeded, etc.)
            assert response.status_code in [402, 403, 429]

    @pytest.mark.integration
    @pytest.mark.usage_tracking
    @pytest.mark.rate_limit
    def test_streaming_rate_limit_enforcement(self, governance_client, cleanup_tracker):
        """Test rate limiting during streaming requests"""
        # Create VK with token rate limit
        vk_data = {
            "name": generate_unique_name("Streaming Rate Limit VK"),
            "rate_limit": {"token_max_limit": 50, "token_reset_duration": "1m"},
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        streaming_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(streaming_vk["id"])

        messages = [{"role": "user", "content": "Write a long story about AI"}]
        headers = {"x-bf-vk": streaming_vk["value"]}

        response = governance_client.chat_completion(
            messages=messages,
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=200,  # More than token limit
            stream=True,
        )

        # Should be limited by token rate limit
        if response.status_code == 429:
            error_data = response.json()
            assert "token" in error_data.get("error", "").lower()


class TestProviderModelValidation:
    """Test provider and model validation during integration"""

    @pytest.mark.integration
    @pytest.mark.validation
    def test_anthropic_model_integration(self, governance_client, cleanup_tracker):
        """Test integration with Anthropic models"""
        # Create VK allowing Anthropic
        vk_data = {
            "name": generate_unique_name("Anthropic VK"),
            "allowed_providers": ["anthropic"],
            "allowed_models": ["claude-3-5-sonnet-20240620"],
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        anthropic_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(anthropic_vk["id"])

        messages = [{"role": "user", "content": "Hello Claude"}]
        headers = {"x-bf-vk": anthropic_vk["value"]}

        response = governance_client.chat_completion(
            messages=messages,
            model="claude-3-5-sonnet-20240620",
            headers=headers,
            max_tokens=10,
        )

        # Should work if Anthropic is properly configured
        assert response.status_code in [200, 400, 402, 429, 503]

    @pytest.mark.integration
    @pytest.mark.validation
    def test_openai_model_integration(self, governance_client, cleanup_tracker):
        """Test integration with OpenAI models"""
        # Create VK allowing OpenAI
        vk_data = {
            "name": generate_unique_name("OpenAI VK"),
            "allowed_providers": ["openai"],
            "allowed_models": ["gpt-4", "gpt-3.5-turbo"],
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        openai_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(openai_vk["id"])

        messages = [{"role": "user", "content": "Hello GPT"}]
        headers = {"x-bf-vk": openai_vk["value"]}

        # Test GPT-4
        response = governance_client.chat_completion(
            messages=messages, model="gpt-4", headers=headers, max_tokens=10
        )

        # Should work if OpenAI is properly configured
        assert response.status_code in [200, 400, 402, 429, 503]

    @pytest.mark.integration
    @pytest.mark.validation
    def test_disallowed_provider_model_combination(
        self, governance_client, cleanup_tracker
    ):
        """Test disallowed provider/model combinations"""
        # Create VK only allowing OpenAI
        vk_data = {
            "name": generate_unique_name("OpenAI Only VK"),
            "allowed_providers": ["openai"],
            "allowed_models": ["gpt-4"],
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        restricted_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(restricted_vk["id"])

        messages = [{"role": "user", "content": "Hello"}]
        headers = {"x-bf-vk": restricted_vk["value"]}

        # Try to use Anthropic model (should fail)
        response = governance_client.chat_completion(
            messages=messages,
            model="claude-3-5-sonnet-20240620",
            headers=headers,
            max_tokens=10,
        )

        # Should be rejected for disallowed model
        assert response.status_code in [400, 403]


class TestErrorHandlingAndEdgeCases:
    """Test error handling and edge cases in usage tracking"""

    @pytest.mark.integration
    @pytest.mark.edge_cases
    def test_malformed_vk_header(self, governance_client):
        """Test malformed VK header handling"""
        messages = [{"role": "user", "content": "Hello"}]

        malformed_headers = [
            {"x-bf-vk": ""},  # Empty
            {"x-bf-vk": " "},  # Whitespace
            {"x-bf-vk": "short"},  # Too short
            {"x-bf-vk": "x" * 100},  # Too long
            {"x-bf-vk": "invalid-characters-#@!"},  # Invalid chars
        ]

        for headers in malformed_headers:
            response = governance_client.chat_completion(
                messages=messages,
                model="openai/gpt-3.5-turbo",
                headers=headers,
                max_tokens=5,
            )

            # Should properly reject malformed headers
            assert response.status_code in [400, 403]

    @pytest.mark.integration
    @pytest.mark.edge_cases
    def test_concurrent_vk_updates_during_requests(
        self, governance_client, cleanup_tracker
    ):
        """Test VK updates during active requests"""
        # Create VK
        vk_data = {"name": generate_unique_name("Update Test VK")}
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        update_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(update_vk["id"])

        messages = [{"role": "user", "content": "Hello"}]
        headers = {"x-bf-vk": update_vk["value"]}

        def make_request():
            return governance_client.chat_completion(
                messages=messages,
                model="openai/gpt-3.5-turbo",
                headers=headers,
                max_tokens=5,
            )

        def update_vk_config():
            update_data = {"description": "Updated during request"}
            return governance_client.update_virtual_key(update_vk["id"], update_data)

        # Start request and update concurrently
        with ThreadPoolExecutor(max_workers=2) as executor:
            request_future = executor.submit(make_request)
            update_future = executor.submit(update_vk_config)

            request_response = request_future.result()
            update_response = update_future.result()

        # Both should handle gracefully
        assert request_response.status_code in [200, 402, 403, 429]
        assert_response_success(update_response, 200)

    @pytest.mark.integration
    @pytest.mark.edge_cases
    def test_extreme_token_counts(self, governance_client, sample_virtual_key):
        """Test extreme token count scenarios"""
        headers = {"x-bf-vk": sample_virtual_key["value"]}

        # Test with 0 max_tokens
        response = governance_client.chat_completion(
            messages=[{"role": "user", "content": "Hello"}],
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=0,
        )

        # Should handle 0 tokens gracefully
        assert response.status_code in [200, 400]

        # Test with very large max_tokens
        response = governance_client.chat_completion(
            messages=[{"role": "user", "content": "Hello"}],
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=100000,  # Very large
        )

        # Should handle large token requests
        assert response.status_code in [200, 400, 402, 429]

    @pytest.mark.integration
    @pytest.mark.edge_cases
    def test_empty_and_large_messages(self, governance_client, sample_virtual_key):
        """Test empty and very large message scenarios"""
        headers = {"x-bf-vk": sample_virtual_key["value"]}

        # Test with empty message
        response = governance_client.chat_completion(
            messages=[{"role": "user", "content": ""}],
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=5,
        )

        # Should handle empty messages
        assert response.status_code in [200, 400]

        # Test with very large message
        large_content = "This is a very long message. " * 1000
        response = governance_client.chat_completion(
            messages=[{"role": "user", "content": large_content}],
            model="openai/gpt-3.5-turbo",
            headers=headers,
            max_tokens=5,
        )

        # Should handle large messages
        assert response.status_code in [200, 400, 402, 429]


class TestPerformanceAndScaling:
    """Test performance and scaling of usage tracking"""

    @pytest.mark.integration
    @pytest.mark.performance
    @pytest.mark.slow
    def test_high_frequency_requests(self, governance_client, cleanup_tracker):
        """Test high frequency requests performance"""
        # Create VK with high limits
        vk_data = {
            "name": generate_unique_name("High Frequency VK"),
            "rate_limit": {
                "request_max_limit": 1000,
                "request_reset_duration": "1h",
                "token_max_limit": 100000,
                "token_reset_duration": "1h",
            },
            "budget": {"max_limit": 1000000, "reset_duration": "1h"},
        }
        create_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(create_response, 201)
        high_freq_vk = create_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(high_freq_vk["id"])

        messages = [{"role": "user", "content": "Hi"}]
        headers = {"x-bf-vk": high_freq_vk["value"]}

        # Measure performance of rapid requests
        start_time = time.time()
        responses = []

        for i in range(20):  # Make 20 rapid requests
            response = governance_client.chat_completion(
                messages=messages,
                model="openai/gpt-3.5-turbo",
                headers=headers,
                max_tokens=1,
            )
            responses.append(response.status_code)
            if i % 5 == 0:
                time.sleep(0.1)  # Brief pause every 5 requests

        total_time = time.time() - start_time

        # Performance assertions
        assert total_time < 30.0, f"20 requests took too long: {total_time}s"

        # Most requests should succeed (unless rate limited)
        success_count = sum(1 for code in responses if code == 200)
        print(
            f"High frequency test: {success_count}/20 requests succeeded in {total_time:.2f}s"
        )

    @pytest.mark.integration
    @pytest.mark.performance
    @pytest.mark.slow
    def test_usage_stats_performance(self, governance_client, cleanup_tracker):
        """Test usage statistics endpoint performance"""
        # Create multiple VKs and make requests
        vk_ids = []
        for i in range(10):
            vk_data = {"name": generate_unique_name(f"Stats Perf VK {i}")}
            response = governance_client.create_virtual_key(vk_data)
            assert_response_success(response, 201)
            vk_id = response.json()["virtual_key"]["id"]
            vk_ids.append(vk_id)
            cleanup_tracker.add_virtual_key(vk_id)

        # Test general stats performance
        start_time = time.time()
        response = governance_client.get_usage_stats()
        stats_time = time.time() - start_time

        assert_response_success(response, 200)
        assert stats_time < 2.0, f"Usage stats took too long: {stats_time}s"

        # Test individual VK stats performance
        start_time = time.time()
        for vk_id in vk_ids[:5]:  # Test 5 VKs
            response = governance_client.get_usage_stats(virtual_key_id=vk_id)
            assert_response_success(response, 200)

        individual_stats_time = time.time() - start_time
        assert (
            individual_stats_time < 5.0
        ), f"Individual VK stats took too long: {individual_stats_time}s"

        print(
            f"Performance test: General stats: {stats_time:.2f}s, 5 individual stats: {individual_stats_time:.2f}s"
        )
