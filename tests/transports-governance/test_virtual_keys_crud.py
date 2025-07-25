"""
Comprehensive Virtual Key CRUD Tests for Bifrost Governance Plugin

This module provides exhaustive testing of Virtual Key operations including:
- Complete CRUD lifecycle testing
- Comprehensive field update testing (individual and batch)
- Mutual exclusivity validation (team_id vs customer_id)
- Budget and rate limit management
- Relationship testing with teams and customers
- Edge cases and validation scenarios
- Concurrency and race condition testing
"""

import pytest
import time
import uuid
from typing import Dict, Any, List
from concurrent.futures import ThreadPoolExecutor
import copy

from conftest import (
    assert_response_success,
    verify_unchanged_fields,
    generate_unique_name,
    create_complete_virtual_key_data,
    verify_entity_relationships,
    deep_compare_entities,
)


class TestVirtualKeyBasicCRUD:
    """Test basic CRUD operations for Virtual Keys"""

    @pytest.mark.virtual_keys
    @pytest.mark.crud
    @pytest.mark.smoke
    def test_vk_create_minimal(self, governance_client, cleanup_tracker):
        """Test creating VK with minimal required data"""
        data = {"name": generate_unique_name("Minimal VK")}

        response = governance_client.create_virtual_key(data)
        assert_response_success(response, 201)

        vk_data = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk_data["id"])

        # Verify required fields
        assert vk_data["name"] == data["name"]
        assert vk_data["value"] is not None  # Auto-generated
        assert vk_data["is_active"] is True  # Default value
        assert vk_data["id"] is not None
        assert vk_data["created_at"] is not None
        assert vk_data["updated_at"] is not None

        # Verify optional fields are None/empty
        assert vk_data["allowed_models"] is None
        assert vk_data["allowed_providers"] is None

    @pytest.mark.virtual_keys
    @pytest.mark.crud
    def test_vk_create_complete(self, governance_client, cleanup_tracker):
        """Test creating VK with all possible fields"""
        data = create_complete_virtual_key_data()

        response = governance_client.create_virtual_key(data)
        assert_response_success(response, 201)

        vk_data = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk_data["id"])

        # Verify all fields are set correctly
        assert vk_data["name"] == data["name"]
        assert vk_data["description"] == data["description"]
        assert vk_data["allowed_models"] == data["allowed_models"]
        assert vk_data["allowed_providers"] == data["allowed_providers"]
        assert vk_data["is_active"] == data["is_active"]

        # Verify budget was created
        assert vk_data["budget"] is not None
        assert vk_data["budget"]["max_limit"] == data["budget"]["max_limit"]
        assert vk_data["budget"]["reset_duration"] == data["budget"]["reset_duration"]

        # Verify rate limit was created
        assert vk_data["rate_limit"] is not None
        assert (
            vk_data["rate_limit"]["token_max_limit"]
            == data["rate_limit"]["token_max_limit"]
        )
        assert (
            vk_data["rate_limit"]["request_max_limit"]
            == data["rate_limit"]["request_max_limit"]
        )

    @pytest.mark.virtual_keys
    @pytest.mark.crud
    def test_vk_create_with_team(self, governance_client, cleanup_tracker, sample_team):
        """Test creating VK associated with a team"""
        data = {"name": generate_unique_name("Team VK"), "team_id": sample_team["id"]}

        response = governance_client.create_virtual_key(data)
        assert_response_success(response, 201)

        vk_data = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk_data["id"])

        # Verify team association
        assert vk_data["team_id"] == sample_team["id"]
        assert vk_data.get("customer_id") is None
        assert vk_data["team"] is not None
        assert vk_data["team"]["id"] == sample_team["id"]

    @pytest.mark.virtual_keys
    @pytest.mark.crud
    def test_vk_create_with_customer(
        self, governance_client, cleanup_tracker, sample_customer
    ):
        """Test creating VK associated with a customer"""
        data = {
            "name": generate_unique_name("Customer VK"),
            "customer_id": sample_customer["id"],
        }

        response = governance_client.create_virtual_key(data)
        assert_response_success(response, 201)

        vk_data = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk_data["id"])

        # Verify customer association
        assert vk_data["customer_id"] == sample_customer["id"]
        assert vk_data.get("team_id") is None
        assert vk_data["customer"] is not None
        assert vk_data["customer"]["id"] == sample_customer["id"]

    @pytest.mark.virtual_keys
    @pytest.mark.crud
    @pytest.mark.mutual_exclusivity
    def test_vk_create_mutual_exclusivity_violation(
        self, governance_client, sample_team, sample_customer
    ):
        """Test that VK cannot be created with both team_id and customer_id"""
        data = {
            "name": generate_unique_name("Invalid VK"),
            "team_id": sample_team["id"],
            "customer_id": sample_customer["id"],
        }

        response = governance_client.create_virtual_key(data)
        assert response.status_code == 400
        error_data = response.json()
        assert "cannot be attached to both" in error_data["error"].lower()

    @pytest.mark.virtual_keys
    @pytest.mark.crud
    def test_vk_list_all(self, governance_client, sample_virtual_key):
        """Test listing all virtual keys"""
        response = governance_client.list_virtual_keys()
        assert_response_success(response, 200)

        data = response.json()
        assert "virtual_keys" in data
        assert "count" in data
        assert isinstance(data["virtual_keys"], list)
        assert data["count"] >= 1

        # Find our test VK
        test_vk = next(
            (vk for vk in data["virtual_keys"] if vk["id"] == sample_virtual_key["id"]),
            None,
        )
        assert test_vk is not None

    @pytest.mark.virtual_keys
    @pytest.mark.crud
    def test_vk_get_by_id(self, governance_client, sample_virtual_key):
        """Test getting VK by ID with relationships loaded"""
        response = governance_client.get_virtual_key(sample_virtual_key["id"])
        assert_response_success(response, 200)

        vk_data = response.json()["virtual_key"]
        assert vk_data["id"] == sample_virtual_key["id"]
        assert vk_data["name"] == sample_virtual_key["name"]

    @pytest.mark.virtual_keys
    @pytest.mark.crud
    def test_vk_get_nonexistent(self, governance_client):
        """Test getting non-existent VK returns 404"""
        fake_id = str(uuid.uuid4())
        response = governance_client.get_virtual_key(fake_id)
        assert response.status_code == 404

    @pytest.mark.virtual_keys
    @pytest.mark.crud
    def test_vk_delete(self, governance_client, cleanup_tracker):
        """Test deleting a virtual key"""
        # Create VK to delete
        data = {"name": generate_unique_name("Delete Test VK")}
        create_response = governance_client.create_virtual_key(data)
        assert_response_success(create_response, 201)
        vk_id = create_response.json()["virtual_key"]["id"]

        # Delete VK
        delete_response = governance_client.delete_virtual_key(vk_id)
        assert_response_success(delete_response, 200)

        # Verify VK is gone
        get_response = governance_client.get_virtual_key(vk_id)
        assert get_response.status_code == 404

    @pytest.mark.virtual_keys
    @pytest.mark.crud
    def test_vk_delete_nonexistent(self, governance_client):
        """Test deleting non-existent VK returns 404"""
        fake_id = str(uuid.uuid4())
        response = governance_client.delete_virtual_key(fake_id)
        assert response.status_code == 404


class TestVirtualKeyValidation:
    """Test validation rules for Virtual Key operations"""

    @pytest.mark.virtual_keys
    @pytest.mark.validation
    def test_vk_create_missing_name(self, governance_client):
        """Test creating VK without name fails"""
        data = {"description": "VK without name"}
        response = governance_client.create_virtual_key(data)
        assert response.status_code == 400

    @pytest.mark.virtual_keys
    @pytest.mark.validation
    def test_vk_create_empty_name(self, governance_client):
        """Test creating VK with empty name fails"""
        data = {"name": ""}
        response = governance_client.create_virtual_key(data)
        assert response.status_code == 400

    @pytest.mark.virtual_keys
    @pytest.mark.validation
    def test_vk_create_invalid_team_id(self, governance_client):
        """Test creating VK with non-existent team_id"""
        data = {
            "name": generate_unique_name("Invalid Team VK"),
            "team_id": str(uuid.uuid4()),
        }
        response = governance_client.create_virtual_key(data)
        # Note: Depending on implementation, this might succeed with warning or fail
        # Adjust assertion based on actual API behavior

    @pytest.mark.virtual_keys
    @pytest.mark.validation
    def test_vk_create_invalid_customer_id(self, governance_client):
        """Test creating VK with non-existent customer_id"""
        data = {
            "name": generate_unique_name("Invalid Customer VK"),
            "customer_id": str(uuid.uuid4()),
        }
        response = governance_client.create_virtual_key(data)
        # Note: Adjust assertion based on actual API behavior

    @pytest.mark.virtual_keys
    @pytest.mark.validation
    def test_vk_create_invalid_json(self, governance_client):
        """Test creating VK with malformed JSON"""
        # This would be tested at the HTTP level, but pytest requests handles JSON encoding
        # So we test with invalid data types instead
        data = {
            "name": 123,  # Should be string
            "is_active": "not_boolean",  # Should be boolean
        }
        response = governance_client.create_virtual_key(data)
        assert response.status_code == 400


class TestVirtualKeyFieldUpdates:
    """Comprehensive tests for Virtual Key field updates"""

    @pytest.mark.virtual_keys
    @pytest.mark.field_updates
    def test_vk_update_individual_fields(
        self, governance_client, cleanup_tracker, sample_team, sample_customer
    ):
        """Test updating each VK field individually"""
        # Create complete VK for testing
        original_data = create_complete_virtual_key_data()
        create_response = governance_client.create_virtual_key(original_data)
        assert_response_success(create_response, 201)
        vk_id = create_response.json()["virtual_key"]["id"]
        cleanup_tracker.add_virtual_key(vk_id)

        # Get original state
        original_response = governance_client.get_virtual_key(vk_id)
        original_vk = original_response.json()["virtual_key"]

        # Test individual field updates
        field_test_cases = [
            {
                "field": "description",
                "update_data": {"description": "Updated description"},
                "expected_value": "Updated description",
            },
            {
                "field": "allowed_models",
                "update_data": {"allowed_models": ["gpt-4", "claude-3-opus"]},
                "expected_value": ["gpt-4", "claude-3-opus"],
            },
            {
                "field": "allowed_providers",
                "update_data": {"allowed_providers": ["openai"]},
                "expected_value": ["openai"],
            },
            {
                "field": "is_active",
                "update_data": {"is_active": False},
                "expected_value": False,
            },
            {
                "field": "team_id",
                "update_data": {"team_id": sample_team["id"]},
                "expected_value": sample_team["id"],
                "exclude_from_unchanged_check": [
                    "team_id",
                    "customer_id",
                    "team",
                    "customer",
                ],
            },
            {
                "field": "customer_id",
                "update_data": {"customer_id": sample_customer["id"]},
                "expected_value": sample_customer["id"],
                "exclude_from_unchanged_check": [
                    "team_id",
                    "customer_id",
                    "team",
                    "customer",
                ],
            },
        ]

        for test_case in field_test_cases:
            # Reset VK to original state by updating all fields back
            reset_data = {
                "description": original_vk.get("description", ""),
                "allowed_models": original_vk["allowed_models"],
                "allowed_providers": original_vk["allowed_providers"],
                "is_active": original_vk["is_active"],
                "team_id": original_vk.get("team_id"),
                "customer_id": original_vk.get("customer_id"),
            }
            governance_client.update_virtual_key(vk_id, reset_data)

            # Perform field update
            response = governance_client.update_virtual_key(
                vk_id, test_case["update_data"]
            )
            assert_response_success(response, 200)
            updated_vk = response.json()["virtual_key"]

            # Verify target field was updated
            field_parts = test_case["field"].split(".")
            current_value = updated_vk
            for part in field_parts:
                current_value = current_value[part]
            assert (
                current_value == test_case["expected_value"]
            ), f"Field {test_case['field']} not updated correctly"

            # Verify other fields unchanged (if specified)
            if test_case.get("verify_unchanged", True):
                exclude_fields = test_case.get(
                    "exclude_from_unchanged_check", [test_case["field"]]
                )
                verify_unchanged_fields(updated_vk, original_vk, exclude_fields)

    @pytest.mark.virtual_keys
    @pytest.mark.field_updates
    def test_vk_budget_updates(self, governance_client, cleanup_tracker):
        """Test comprehensive budget creation, update, and modification"""
        # Create VK without budget
        data = {"name": generate_unique_name("Budget Test VK")}
        create_response = governance_client.create_virtual_key(data)
        assert_response_success(create_response, 201)
        vk_id = create_response.json()["virtual_key"]["id"]
        cleanup_tracker.add_virtual_key(vk_id)

        # Test 1: Add budget to VK without budget
        budget_data = {"max_limit": 10000, "reset_duration": "1h"}
        response = governance_client.update_virtual_key(vk_id, {"budget": budget_data})
        assert_response_success(response, 200)
        updated_vk = response.json()["virtual_key"]
        assert updated_vk["budget"]["max_limit"] == 10000
        assert updated_vk["budget"]["reset_duration"] == "1h"
        assert updated_vk["budget_id"] is not None

        # Test 2: Update existing budget completely
        new_budget_data = {"max_limit": 20000, "reset_duration": "2h"}
        response = governance_client.update_virtual_key(
            vk_id, {"budget": new_budget_data}
        )
        assert_response_success(response, 200)
        updated_vk = response.json()["virtual_key"]
        assert updated_vk["budget"]["max_limit"] == 20000
        assert updated_vk["budget"]["reset_duration"] == "2h"

        # Test 3: Partial budget update (only max_limit)
        response = governance_client.update_virtual_key(
            vk_id, {"budget": {"max_limit": 30000}}
        )
        assert_response_success(response, 200)
        updated_vk = response.json()["virtual_key"]
        assert updated_vk["budget"]["max_limit"] == 30000
        assert updated_vk["budget"]["reset_duration"] == "2h"  # Should remain unchanged

        # Test 4: Partial budget update (only reset_duration)
        response = governance_client.update_virtual_key(
            vk_id, {"budget": {"reset_duration": "24h"}}
        )
        assert_response_success(response, 200)
        updated_vk = response.json()["virtual_key"]
        assert updated_vk["budget"]["max_limit"] == 30000  # Should remain unchanged
        assert updated_vk["budget"]["reset_duration"] == "24h"

    @pytest.mark.virtual_keys
    @pytest.mark.field_updates
    def test_vk_rate_limit_updates(self, governance_client, cleanup_tracker):
        """Test comprehensive rate limit creation, update, and field-level modifications"""
        # Create VK without rate limit
        data = {"name": generate_unique_name("Rate Limit Test VK")}
        create_response = governance_client.create_virtual_key(data)
        assert_response_success(create_response, 201)
        vk_id = create_response.json()["virtual_key"]["id"]
        cleanup_tracker.add_virtual_key(vk_id)

        # Test 1: Add rate limit to VK
        rate_limit_data = {
            "token_max_limit": 1000,
            "token_reset_duration": "1m",
            "request_max_limit": 100,
            "request_reset_duration": "1h",
        }
        response = governance_client.update_virtual_key(
            vk_id, {"rate_limit": rate_limit_data}
        )
        assert_response_success(response, 200)
        updated_vk = response.json()["virtual_key"]
        assert updated_vk["rate_limit"]["token_max_limit"] == 1000
        assert updated_vk["rate_limit"]["request_max_limit"] == 100
        assert updated_vk["rate_limit_id"] is not None

        # Test 2: Update only token limits
        response = governance_client.update_virtual_key(
            vk_id,
            {"rate_limit": {"token_max_limit": 2000, "token_reset_duration": "2m"}},
        )
        assert_response_success(response, 200)
        updated_vk = response.json()["virtual_key"]
        assert updated_vk["rate_limit"]["token_max_limit"] == 2000
        assert updated_vk["rate_limit"]["token_reset_duration"] == "2m"
        assert updated_vk["rate_limit"]["request_max_limit"] == 100  # Unchanged
        assert updated_vk["rate_limit"]["request_reset_duration"] == "1h"  # Unchanged

        # Test 3: Update only request limits
        response = governance_client.update_virtual_key(
            vk_id,
            {"rate_limit": {"request_max_limit": 200, "request_reset_duration": "2h"}},
        )
        assert_response_success(response, 200)
        updated_vk = response.json()["virtual_key"]
        assert updated_vk["rate_limit"]["token_max_limit"] == 2000  # Unchanged
        assert updated_vk["rate_limit"]["request_max_limit"] == 200
        assert updated_vk["rate_limit"]["request_reset_duration"] == "2h"

        # Test 4: Partial rate limit update (single field)
        response = governance_client.update_virtual_key(
            vk_id, {"rate_limit": {"token_max_limit": 5000}}
        )
        assert_response_success(response, 200)
        updated_vk = response.json()["virtual_key"]
        assert updated_vk["rate_limit"]["token_max_limit"] == 5000
        assert updated_vk["rate_limit"]["token_reset_duration"] == "2m"  # Unchanged
        assert updated_vk["rate_limit"]["request_max_limit"] == 200  # Unchanged
        assert updated_vk["rate_limit"]["request_reset_duration"] == "2h"  # Unchanged

    @pytest.mark.virtual_keys
    @pytest.mark.field_updates
    def test_vk_multiple_field_updates(self, governance_client, cleanup_tracker):
        """Test updating multiple fields simultaneously"""
        # Create VK with some initial data
        initial_data = {
            "name": generate_unique_name("Multi-Field Test VK"),
            "description": "Initial description",
            "allowed_models": ["gpt-3.5-turbo"],
            "is_active": True,
        }
        create_response = governance_client.create_virtual_key(initial_data)
        assert_response_success(create_response, 201)
        vk_id = create_response.json()["virtual_key"]["id"]
        cleanup_tracker.add_virtual_key(vk_id)

        # Update multiple fields at once
        update_data = {
            "description": "Updated description via multi-field",
            "allowed_models": ["gpt-4", "claude-3-5-sonnet-20240620"],
            "allowed_providers": ["openai", "anthropic"],
            "is_active": False,
            "budget": {"max_limit": 50000, "reset_duration": "1d"},
            "rate_limit": {
                "token_max_limit": 5000,
                "request_max_limit": 500,
                "token_reset_duration": "1h",
                "request_reset_duration": "1h",
            },
        }

        response = governance_client.update_virtual_key(vk_id, update_data)
        assert_response_success(response, 200)

        updated_vk = response.json()["virtual_key"]
        assert updated_vk["description"] == "Updated description via multi-field"
        assert updated_vk["allowed_models"] == ["gpt-4", "claude-3-5-sonnet-20240620"]
        assert updated_vk["allowed_providers"] == ["openai", "anthropic"]
        assert updated_vk["is_active"] is False
        assert updated_vk["budget"]["max_limit"] == 50000
        assert updated_vk["rate_limit"]["token_max_limit"] == 5000

    @pytest.mark.virtual_keys
    @pytest.mark.field_updates
    @pytest.mark.mutual_exclusivity
    def test_vk_relationship_updates(
        self, governance_client, cleanup_tracker, sample_team, sample_customer
    ):
        """Test updating VK relationships with mutual exclusivity validation"""
        # Create standalone VK
        data = {"name": generate_unique_name("Relationship Test VK")}
        create_response = governance_client.create_virtual_key(data)
        assert_response_success(create_response, 201)
        vk_id = create_response.json()["virtual_key"]["id"]
        cleanup_tracker.add_virtual_key(vk_id)

        # Test 1: Add team relationship
        response = governance_client.update_virtual_key(
            vk_id, {"team_id": sample_team["id"]}
        )
        assert_response_success(response, 200)
        updated_vk = response.json()["virtual_key"]
        assert updated_vk["team_id"] == sample_team["id"]
        assert updated_vk.get("customer_id") is None
        assert updated_vk["team"]["id"] == sample_team["id"]

        # Test 2: Switch to customer (should clear team)
        response = governance_client.update_virtual_key(
            vk_id, {"customer_id": sample_customer["id"]}
        )
        assert_response_success(response, 200)
        updated_vk = response.json()["virtual_key"]
        assert updated_vk["customer_id"] == sample_customer["id"]
        assert updated_vk.get("team_id") is None
        assert updated_vk["customer"]["id"] == sample_customer["id"]
        assert updated_vk.get("team") is None

        # Test 3: Try to set both (should fail)
        response = governance_client.update_virtual_key(
            vk_id, {"team_id": sample_team["id"], "customer_id": sample_customer["id"]}
        )
        assert response.status_code == 400
        error_data = response.json()
        assert "cannot be attached to both" in error_data["error"].lower()

        # Test 4: Clear both relationships
        response = governance_client.update_virtual_key(
            vk_id, {"team_id": None, "customer_id": None}
        )
        # Note: Behavior depends on API implementation - adjust based on actual behavior
        # Some APIs might not support explicit null setting

    @pytest.mark.virtual_keys
    @pytest.mark.field_updates
    @pytest.mark.edge_cases
    def test_vk_update_edge_cases(self, governance_client, cleanup_tracker):
        """Test edge cases in VK updates"""
        # Create test VK
        data = {"name": generate_unique_name("Edge Case VK")}
        create_response = governance_client.create_virtual_key(data)
        assert_response_success(create_response, 201)
        vk_id = create_response.json()["virtual_key"]["id"]
        cleanup_tracker.add_virtual_key(vk_id)

        original_response = governance_client.get_virtual_key(vk_id)
        original_vk = original_response.json()["virtual_key"]

        # Test 1: Empty update (should return unchanged VK)
        response = governance_client.update_virtual_key(vk_id, {})
        assert_response_success(response, 200)
        updated_vk = response.json()["virtual_key"]

        # Compare ignoring timestamps
        differences = deep_compare_entities(
            updated_vk, original_vk, ignore_fields=["updated_at"]
        )
        assert len(differences) == 0, f"Empty update changed fields: {differences}"

        # Test 2: Invalid field values
        response = governance_client.update_virtual_key(vk_id, {"is_active": "invalid"})
        assert response.status_code == 400

        # Test 3: Update with same values (should succeed but might not change updated_at)
        response = governance_client.update_virtual_key(
            vk_id,
            {
                "description": original_vk.get("description", ""),
            },
        )
        # Note: Adjust based on API behavior for no-op updates

        # Test 4: Very long values (test field length limits)
        long_description = "x" * 10000  # Adjust based on actual field limits
        response = governance_client.update_virtual_key(
            vk_id, {"description": long_description}
        )
        # Expected behavior depends on API validation rules

    @pytest.mark.virtual_keys
    @pytest.mark.field_updates
    def test_vk_update_nonexistent(self, governance_client):
        """Test updating non-existent VK returns 404"""
        fake_id = str(uuid.uuid4())
        response = governance_client.update_virtual_key(
            fake_id, {"description": "test"}
        )
        assert response.status_code == 404


class TestVirtualKeyBudgetAndRateLimit:
    """Test budget and rate limit specific functionality"""

    @pytest.mark.virtual_keys
    @pytest.mark.budget
    def test_vk_budget_creation_and_validation(
        self, governance_client, cleanup_tracker
    ):
        """Test budget creation with various configurations"""
        # Test valid budget configurations
        budget_test_cases = [
            {"max_limit": 1000, "reset_duration": "1h"},
            {"max_limit": 50000, "reset_duration": "1d"},
            {"max_limit": 100000, "reset_duration": "1w"},
            {"max_limit": 1000000, "reset_duration": "1M"},
        ]

        for budget_config in budget_test_cases:
            data = {
                "name": generate_unique_name(
                    f"Budget VK {budget_config['reset_duration']}"
                ),
                "budget": budget_config,
            }

            response = governance_client.create_virtual_key(data)
            assert_response_success(response, 201)

            vk_data = response.json()["virtual_key"]
            cleanup_tracker.add_virtual_key(vk_data["id"])

            assert vk_data["budget"]["max_limit"] == budget_config["max_limit"]
            assert (
                vk_data["budget"]["reset_duration"] == budget_config["reset_duration"]
            )
            assert vk_data["budget"]["current_usage"] == 0
            assert vk_data["budget"]["last_reset"] is not None

    @pytest.mark.virtual_keys
    @pytest.mark.budget
    @pytest.mark.edge_cases
    def test_vk_budget_edge_cases(self, governance_client, cleanup_tracker):
        """Test budget edge cases and boundary conditions"""
        # Test boundary values
        edge_case_budgets = [
            {"max_limit": 0, "reset_duration": "1h"},  # Zero budget
            {"max_limit": 1, "reset_duration": "1s"},  # Minimal values
            {"max_limit": 9223372036854775807, "reset_duration": "1h"},  # Max int64
        ]

        for budget_config in edge_case_budgets:
            data = {
                "name": generate_unique_name(
                    f"Edge Budget VK {budget_config['max_limit']}"
                ),
                "budget": budget_config,
            }

            response = governance_client.create_virtual_key(data)
            # Adjust assertions based on API validation rules
            if (
                budget_config["max_limit"] >= 0
            ):  # Assuming non-negative budgets are valid
                assert_response_success(response, 201)
                cleanup_tracker.add_virtual_key(response.json()["virtual_key"]["id"])
            else:
                assert response.status_code == 400

    @pytest.mark.virtual_keys
    @pytest.mark.rate_limit
    def test_vk_rate_limit_creation_and_validation(
        self, governance_client, cleanup_tracker
    ):
        """Test rate limit creation with various configurations"""
        # Test different rate limit configurations
        rate_limit_test_cases = [
            {
                "token_max_limit": 1000,
                "token_reset_duration": "1m",
                "request_max_limit": 100,
                "request_reset_duration": "1h",
            },
            {
                "token_max_limit": 10000,
                "token_reset_duration": "1h",
                # Only token limits
            },
            {
                "request_max_limit": 500,
                "request_reset_duration": "1d",
                # Only request limits
            },
            {
                "token_max_limit": 5000,
                "token_reset_duration": "30s",
                "request_max_limit": 1000,
                "request_reset_duration": "5m",
            },
        ]

        for rate_limit_config in rate_limit_test_cases:
            data = {
                "name": generate_unique_name("Rate Limit VK"),
                "rate_limit": rate_limit_config,
            }

            response = governance_client.create_virtual_key(data)
            assert_response_success(response, 201)

            vk_data = response.json()["virtual_key"]
            cleanup_tracker.add_virtual_key(vk_data["id"])

            rate_limit = vk_data["rate_limit"]
            for key, value in rate_limit_config.items():
                assert rate_limit[key] == value

    @pytest.mark.virtual_keys
    @pytest.mark.rate_limit
    @pytest.mark.edge_cases
    def test_vk_rate_limit_edge_cases(self, governance_client, cleanup_tracker):
        """Test rate limit edge cases and boundary conditions"""
        # Test minimal rate limits
        minimal_rate_limit = {
            "token_max_limit": 1,
            "token_reset_duration": "1s",
            "request_max_limit": 1,
            "request_reset_duration": "1s",
        }

        data = {
            "name": generate_unique_name("Minimal Rate Limit VK"),
            "rate_limit": minimal_rate_limit,
        }

        response = governance_client.create_virtual_key(data)
        assert_response_success(response, 201)
        cleanup_tracker.add_virtual_key(response.json()["virtual_key"]["id"])

        # Test large rate limits
        large_rate_limit = {
            "token_max_limit": 1000000,
            "token_reset_duration": "1h",
            "request_max_limit": 100000,
            "request_reset_duration": "1h",
        }

        data = {
            "name": generate_unique_name("Large Rate Limit VK"),
            "rate_limit": large_rate_limit,
        }

        response = governance_client.create_virtual_key(data)
        assert_response_success(response, 201)
        cleanup_tracker.add_virtual_key(response.json()["virtual_key"]["id"])


class TestVirtualKeyConcurrency:
    """Test concurrent operations on Virtual Keys"""

    @pytest.mark.virtual_keys
    @pytest.mark.concurrency
    @pytest.mark.slow
    def test_vk_concurrent_creation(self, governance_client, cleanup_tracker):
        """Test creating multiple VKs concurrently"""

        def create_vk(index):
            data = {"name": generate_unique_name(f"Concurrent VK {index}")}
            response = governance_client.create_virtual_key(data)
            return response

        # Create 10 VKs concurrently
        with ThreadPoolExecutor(max_workers=10) as executor:
            futures = [executor.submit(create_vk, i) for i in range(10)]
            responses = [future.result() for future in futures]

        # Verify all succeeded
        created_vks = []
        for response in responses:
            assert_response_success(response, 201)
            vk_data = response.json()["virtual_key"]
            created_vks.append(vk_data)
            cleanup_tracker.add_virtual_key(vk_data["id"])

        # Verify all VKs have unique IDs and values
        vk_ids = [vk["id"] for vk in created_vks]
        vk_values = [vk["value"] for vk in created_vks]
        assert len(set(vk_ids)) == 10  # All unique IDs
        assert len(set(vk_values)) == 10  # All unique values

    @pytest.mark.virtual_keys
    @pytest.mark.concurrency
    @pytest.mark.slow
    def test_vk_concurrent_updates(self, governance_client, cleanup_tracker):
        """Test updating same VK concurrently"""
        # Create VK to update
        data = {"name": generate_unique_name("Concurrent Update VK")}
        create_response = governance_client.create_virtual_key(data)
        assert_response_success(create_response, 201)
        vk_id = create_response.json()["virtual_key"]["id"]
        cleanup_tracker.add_virtual_key(vk_id)

        # Update concurrently with different descriptions
        def update_vk(index):
            update_data = {"description": f"Updated by thread {index}"}
            response = governance_client.update_virtual_key(vk_id, update_data)
            return response, index

        with ThreadPoolExecutor(max_workers=5) as executor:
            futures = [executor.submit(update_vk, i) for i in range(5)]
            results = [future.result() for future in futures]

        # All updates should succeed (last one wins)
        for response, index in results:
            assert_response_success(response, 200)

        # Verify final state
        final_response = governance_client.get_virtual_key(vk_id)
        final_vk = final_response.json()["virtual_key"]
        assert final_vk["description"].startswith("Updated by thread")


class TestVirtualKeyRelationships:
    """Test VK relationships with teams and customers"""

    @pytest.mark.virtual_keys
    @pytest.mark.relationships
    def test_vk_team_relationship_loading(
        self, governance_client, cleanup_tracker, sample_team_with_customer
    ):
        """Test that VK properly loads team and customer relationships"""
        data = {
            "name": generate_unique_name("Relationship VK"),
            "team_id": sample_team_with_customer["id"],
        }

        response = governance_client.create_virtual_key(data)
        assert_response_success(response, 201)
        vk_data = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk_data["id"])

        # Verify team relationship loaded
        assert vk_data["team"] is not None
        assert vk_data["team"]["id"] == sample_team_with_customer["id"]
        assert vk_data["team"]["name"] == sample_team_with_customer["name"]

        # Verify team's customer_id is present (nested customer not preloaded)
        if sample_team_with_customer.get("customer_id"):
            # Note: API only preloads one level deep, so customer object isn't nested here
            assert (
                vk_data["team"].get("customer_id")
                == sample_team_with_customer["customer_id"]
            )

    @pytest.mark.virtual_keys
    @pytest.mark.relationships
    def test_vk_customer_relationship_loading(
        self, governance_client, cleanup_tracker, sample_customer
    ):
        """Test that VK properly loads customer relationships"""
        data = {
            "name": generate_unique_name("Customer Relationship VK"),
            "customer_id": sample_customer["id"],
        }

        response = governance_client.create_virtual_key(data)
        assert_response_success(response, 201)
        vk_data = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk_data["id"])

        # Verify customer relationship loaded
        assert vk_data["customer"] is not None
        assert vk_data["customer"]["id"] == sample_customer["id"]
        assert vk_data["customer"]["name"] == sample_customer["name"]

    @pytest.mark.virtual_keys
    @pytest.mark.relationships
    def test_vk_orphaned_relationships(self, governance_client, cleanup_tracker):
        """Test VK behavior with orphaned team/customer references"""
        # Create VK with non-existent team_id
        fake_team_id = str(uuid.uuid4())
        data = {"name": generate_unique_name("Orphaned VK"), "team_id": fake_team_id}

        response = governance_client.create_virtual_key(data)
        # Behavior depends on API implementation:
        # - Might succeed with warning
        # - Might fail with validation error
        # Adjust assertion based on actual behavior

        if response.status_code == 201:
            cleanup_tracker.add_virtual_key(response.json()["virtual_key"]["id"])
            # Verify VK was created but team relationship is null/missing
            vk_data = response.json()["virtual_key"]
            assert vk_data.get("team") is None
        else:
            assert response.status_code == 400  # Validation error expected
