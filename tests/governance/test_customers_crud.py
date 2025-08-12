"""
Comprehensive Customer CRUD Tests for Bifrost Governance Plugin

This module provides exhaustive testing of Customer operations including:
- Complete CRUD lifecycle testing
- Comprehensive field update testing (individual and batch)
- Team relationship management
- Budget management and hierarchies
- Cascading operations
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
    verify_entity_relationships,
    deep_compare_entities,
)


class TestCustomerBasicCRUD:
    """Test basic CRUD operations for Customers"""

    @pytest.mark.customers
    @pytest.mark.crud
    @pytest.mark.smoke
    def test_customer_create_minimal(self, governance_client, cleanup_tracker):
        """Test creating customer with minimal required data"""
        data = {"name": generate_unique_name("Minimal Customer")}

        response = governance_client.create_customer(data)
        assert_response_success(response, 201)

        customer_data = response.json()["customer"]
        cleanup_tracker.add_customer(customer_data["id"])

        # Verify required fields
        assert customer_data["name"] == data["name"]
        assert customer_data["id"] is not None
        assert customer_data["created_at"] is not None
        assert customer_data["updated_at"] is not None

        # Verify optional fields are None/empty
        assert customer_data["teams"] == []
        assert customer_data["virtual_keys"] is None

    @pytest.mark.customers
    @pytest.mark.crud
    @pytest.mark.budget
    def test_customer_create_with_budget(self, governance_client, cleanup_tracker):
        """Test creating customer with budget"""
        data = {
            "name": generate_unique_name("Budget Customer"),
            "budget": {
                "max_limit": 500000,  # $5000.00 in cents
                "reset_duration": "1M",
            },
        }

        response = governance_client.create_customer(data)
        assert_response_success(response, 201)

        customer_data = response.json()["customer"]
        cleanup_tracker.add_customer(customer_data["id"])

        # Verify budget was created
        assert customer_data["budget"] is not None
        assert customer_data["budget"]["max_limit"] == 500000
        assert customer_data["budget"]["reset_duration"] == "1M"
        assert customer_data["budget"]["current_usage"] == 0
        assert customer_data["budget_id"] is not None

    @pytest.mark.customers
    @pytest.mark.crud
    def test_customer_list_all(self, governance_client, sample_customer):
        """Test listing all customers"""
        response = governance_client.list_customers()
        assert_response_success(response, 200)

        data = response.json()
        assert "customers" in data
        assert "count" in data
        assert isinstance(data["customers"], list)
        assert data["count"] >= 1

        # Find our test customer
        test_customer = next(
            (
                customer
                for customer in data["customers"]
                if customer["id"] == sample_customer["id"]
            ),
            None,
        )
        assert test_customer is not None

    @pytest.mark.customers
    @pytest.mark.crud
    def test_customer_get_by_id(self, governance_client, sample_customer):
        """Test getting customer by ID with relationships loaded"""
        response = governance_client.get_customer(sample_customer["id"])
        assert_response_success(response, 200)

        customer_data = response.json()["customer"]
        assert customer_data["id"] == sample_customer["id"]
        assert customer_data["name"] == sample_customer["name"]

        # Verify teams relationship is loaded (empty list if no teams)
        assert "teams" in customer_data
        assert (
            isinstance(customer_data["teams"], list) or customer_data["teams"] is None
        )

    @pytest.mark.customers
    @pytest.mark.crud
    def test_customer_get_nonexistent(self, governance_client):
        """Test getting non-existent customer returns 404"""
        fake_id = str(uuid.uuid4())
        response = governance_client.get_customer(fake_id)
        assert response.status_code == 404

    @pytest.mark.customers
    @pytest.mark.crud
    def test_customer_delete(self, governance_client, cleanup_tracker):
        """Test deleting a customer"""
        # Create customer to delete
        data = {"name": generate_unique_name("Delete Test Customer")}
        create_response = governance_client.create_customer(data)
        assert_response_success(create_response, 201)
        customer_id = create_response.json()["customer"]["id"]

        # Delete customer
        delete_response = governance_client.delete_customer(customer_id)
        assert_response_success(delete_response, 200)

        # Verify customer is gone
        get_response = governance_client.get_customer(customer_id)
        assert get_response.status_code == 404

    @pytest.mark.customers
    @pytest.mark.crud
    def test_customer_delete_nonexistent(self, governance_client):
        """Test deleting non-existent customer returns 404"""
        fake_id = str(uuid.uuid4())
        response = governance_client.delete_customer(fake_id)
        assert response.status_code == 404


class TestCustomerValidation:
    """Test validation rules for Customer operations"""

    @pytest.mark.customers
    @pytest.mark.validation
    def test_customer_create_missing_name(self, governance_client):
        """Test creating customer without name fails"""
        data = {"budget": {"max_limit": 1000, "reset_duration": "1h"}}
        response = governance_client.create_customer(data)
        assert response.status_code == 400

    @pytest.mark.customers
    @pytest.mark.validation
    def test_customer_create_empty_name(self, governance_client):
        """Test creating customer with empty name fails"""
        data = {"name": ""}
        response = governance_client.create_customer(data)
        assert response.status_code == 400

    @pytest.mark.customers
    @pytest.mark.validation
    def test_customer_create_invalid_budget(self, governance_client):
        """Test creating customer with invalid budget data"""
        # Test negative budget
        data = {
            "name": generate_unique_name("Negative Budget Customer"),
            "budget": {"max_limit": -10000, "reset_duration": "1h"},
        }
        response = governance_client.create_customer(data)
        assert response.status_code == 400

        # Test invalid reset duration
        data = {
            "name": generate_unique_name("Invalid Duration Customer"),
            "budget": {"max_limit": 10000, "reset_duration": "invalid_duration"},
        }
        response = governance_client.create_customer(data)
        assert response.status_code == 400

    @pytest.mark.customers
    @pytest.mark.validation
    def test_customer_create_invalid_json(self, governance_client):
        """Test creating customer with invalid data types"""
        data = {
            "name": 12345,  # Should be string
            "budget": "not_an_object",  # Should be object
        }
        response = governance_client.create_customer(data)
        assert response.status_code == 400


class TestCustomerFieldUpdates:
    """Comprehensive tests for Customer field updates"""

    @pytest.mark.customers
    @pytest.mark.field_updates
    def test_customer_update_individual_fields(
        self, governance_client, cleanup_tracker
    ):
        """Test updating each customer field individually"""
        # Create customer with all fields for testing
        original_data = {
            "name": generate_unique_name("Complete Update Test Customer"),
            "budget": {"max_limit": 250000, "reset_duration": "1w"},
        }
        create_response = governance_client.create_customer(original_data)
        assert_response_success(create_response, 201)
        customer_id = create_response.json()["customer"]["id"]
        cleanup_tracker.add_customer(customer_id)

        # Get original state
        original_response = governance_client.get_customer(customer_id)
        original_customer = original_response.json()["customer"]

        # Test individual field updates
        field_test_cases = [
            {
                "field": "name",
                "update_data": {"name": "Updated Customer Name"},
                "expected_value": "Updated Customer Name",
            }
        ]

        for test_case in field_test_cases:
            # Reset customer to original state
            reset_data = {"name": original_customer["name"]}
            governance_client.update_customer(customer_id, reset_data)

            # Perform field update
            response = governance_client.update_customer(
                customer_id, test_case["update_data"]
            )
            assert_response_success(response, 200)
            updated_customer = response.json()["customer"]

            # Verify target field was updated
            if test_case.get("custom_validation"):
                test_case["custom_validation"](updated_customer)
            else:
                field_parts = test_case["field"].split(".")
                current_value = updated_customer
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
                verify_unchanged_fields(
                    updated_customer, original_customer, exclude_fields
                )

    @pytest.mark.customers
    @pytest.mark.field_updates
    @pytest.mark.budget
    def test_customer_budget_updates(self, governance_client, cleanup_tracker):
        """Test comprehensive budget creation, update, and modification"""
        # Create customer without budget
        data = {"name": generate_unique_name("Budget Update Test Customer")}
        create_response = governance_client.create_customer(data)
        assert_response_success(create_response, 201)
        customer_id = create_response.json()["customer"]["id"]
        cleanup_tracker.add_customer(customer_id)

        # Test 1: Add budget to customer without budget
        budget_data = {"max_limit": 100000, "reset_duration": "1M"}
        response = governance_client.update_customer(
            customer_id, {"budget": budget_data}
        )
        assert_response_success(response, 200)
        updated_customer = response.json()["customer"]
        assert updated_customer["budget"]["max_limit"] == 100000
        assert updated_customer["budget"]["reset_duration"] == "1M"
        assert updated_customer["budget_id"] is not None

        # Test 2: Update existing budget completely
        new_budget_data = {"max_limit": 200000, "reset_duration": "3M"}
        response = governance_client.update_customer(
            customer_id, {"budget": new_budget_data}
        )
        assert_response_success(response, 200)
        updated_customer = response.json()["customer"]
        assert updated_customer["budget"]["max_limit"] == 200000
        assert updated_customer["budget"]["reset_duration"] == "3M"

        # Test 3: Partial budget update (only max_limit)
        response = governance_client.update_customer(
            customer_id, {"budget": {"max_limit": 300000}}
        )
        assert_response_success(response, 200)
        updated_customer = response.json()["customer"]
        assert updated_customer["budget"]["max_limit"] == 300000
        assert (
            updated_customer["budget"]["reset_duration"] == "3M"
        )  # Should remain unchanged

        # Test 4: Partial budget update (only reset_duration)
        response = governance_client.update_customer(
            customer_id, {"budget": {"reset_duration": "6M"}}
        )
        assert_response_success(response, 200)
        updated_customer = response.json()["customer"]
        assert (
            updated_customer["budget"]["max_limit"] == 300000
        )  # Should remain unchanged
        assert updated_customer["budget"]["reset_duration"] == "6M"

    @pytest.mark.customers
    @pytest.mark.field_updates
    def test_customer_multiple_field_updates(self, governance_client, cleanup_tracker):
        """Test updating multiple fields simultaneously"""
        # Create customer with initial data
        initial_data = {
            "name": generate_unique_name("Multi-Field Test Customer"),
        }
        create_response = governance_client.create_customer(initial_data)
        assert_response_success(create_response, 201)
        customer_id = create_response.json()["customer"]["id"]
        cleanup_tracker.add_customer(customer_id)

        # Update multiple fields at once
        update_data = {
            "name": "Updated Multi-Field Customer Name",
            "budget": {"max_limit": 500000, "reset_duration": "1Y"},
        }

        response = governance_client.update_customer(customer_id, update_data)
        assert_response_success(response, 200)

        updated_customer = response.json()["customer"]
        assert updated_customer["name"] == "Updated Multi-Field Customer Name"
        assert updated_customer["budget"]["max_limit"] == 500000
        assert updated_customer["budget"]["reset_duration"] == "1Y"

    @pytest.mark.customers
    @pytest.mark.field_updates
    @pytest.mark.edge_cases
    def test_customer_update_edge_cases(self, governance_client, cleanup_tracker):
        """Test edge cases in customer updates"""
        # Create test customer
        data = {"name": generate_unique_name("Edge Case Customer")}
        create_response = governance_client.create_customer(data)
        assert_response_success(create_response, 201)
        customer_id = create_response.json()["customer"]["id"]
        cleanup_tracker.add_customer(customer_id)

        original_response = governance_client.get_customer(customer_id)
        original_customer = original_response.json()["customer"]

        # Test 1: Empty update (should return unchanged customer)
        response = governance_client.update_customer(customer_id, {})
        assert_response_success(response, 200)
        updated_customer = response.json()["customer"]

        # Compare ignoring timestamps
        differences = deep_compare_entities(
            updated_customer, original_customer, ignore_fields=["updated_at"]
        )
        assert len(differences) == 0, f"Empty update changed fields: {differences}"

        # Test 2: Update with same values
        response = governance_client.update_customer(
            customer_id, {"name": original_customer["name"]}
        )
        assert_response_success(response, 200)

        # Test 3: Very long customer name (test field length limits)
        long_name = "x" * 1000  # Adjust based on actual field limits
        response = governance_client.update_customer(customer_id, {"name": long_name})
        # Expected behavior depends on API validation rules

    @pytest.mark.customers
    @pytest.mark.field_updates
    def test_customer_update_nonexistent(self, governance_client):
        """Test updating non-existent customer returns 404"""
        fake_id = str(uuid.uuid4())
        response = governance_client.update_customer(fake_id, {"name": "test"})
        assert response.status_code == 404


class TestCustomerBudgetManagement:
    """Test customer budget specific functionality"""

    @pytest.mark.customers
    @pytest.mark.budget
    def test_customer_budget_creation_and_validation(
        self, governance_client, cleanup_tracker
    ):
        """Test budget creation with various configurations"""
        # Test valid budget configurations
        budget_test_cases = [
            {"max_limit": 50000, "reset_duration": "1d"},
            {"max_limit": 250000, "reset_duration": "1w"},
            {"max_limit": 1000000, "reset_duration": "1M"},
            {"max_limit": 5000000, "reset_duration": "3M"},
            {"max_limit": 10000000, "reset_duration": "1Y"},
        ]

        for budget_config in budget_test_cases:
            data = {
                "name": generate_unique_name(
                    f"Budget Customer {budget_config['reset_duration']}"
                ),
                "budget": budget_config,
            }

            response = governance_client.create_customer(data)
            assert_response_success(response, 201)

            customer_data = response.json()["customer"]
            cleanup_tracker.add_customer(customer_data["id"])

            assert customer_data["budget"]["max_limit"] == budget_config["max_limit"]
            assert (
                customer_data["budget"]["reset_duration"]
                == budget_config["reset_duration"]
            )
            assert customer_data["budget"]["current_usage"] == 0
            assert customer_data["budget"]["last_reset"] is not None

    @pytest.mark.customers
    @pytest.mark.budget
    @pytest.mark.edge_cases
    def test_customer_budget_edge_cases(self, governance_client, cleanup_tracker):
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
                    f"Edge Budget Customer {budget_config['max_limit']}"
                ),
                "budget": budget_config,
            }

            response = governance_client.create_customer(data)
            # Adjust assertions based on API validation rules
            if (
                budget_config["max_limit"] >= 0
            ):  # Assuming non-negative budgets are valid
                assert_response_success(response, 201)
                cleanup_tracker.add_customer(response.json()["customer"]["id"])
            else:
                assert response.status_code == 400

    @pytest.mark.customers
    @pytest.mark.budget
    @pytest.mark.hierarchical
    def test_customer_budget_hierarchy_foundation(
        self, governance_client, cleanup_tracker
    ):
        """Test customer budget as foundation of hierarchical budget system"""
        # Create customer with large budget (top of hierarchy)
        customer_data = {
            "name": generate_unique_name("Hierarchy Foundation Customer"),
            "budget": {"max_limit": 1000000, "reset_duration": "1M"},  # $10,000
        }
        customer_response = governance_client.create_customer(customer_data)
        assert_response_success(customer_response, 201)
        customer = customer_response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        # Create teams under this customer with smaller budgets
        team1_data = {
            "name": generate_unique_name("Sub-Team 1"),
            "customer_id": customer["id"],
            "budget": {"max_limit": 300000, "reset_duration": "1M"},  # $3,000
        }
        team1_response = governance_client.create_team(team1_data)
        assert_response_success(team1_response, 201)
        team1 = team1_response.json()["team"]
        cleanup_tracker.add_team(team1["id"])

        team2_data = {
            "name": generate_unique_name("Sub-Team 2"),
            "customer_id": customer["id"],
            "budget": {"max_limit": 200000, "reset_duration": "1M"},  # $2,000
        }
        team2_response = governance_client.create_team(team2_data)
        assert_response_success(team2_response, 201)
        team2 = team2_response.json()["team"]
        cleanup_tracker.add_team(team2["id"])

        # Create VKs under teams with even smaller budgets
        vk1_data = {
            "name": generate_unique_name("Team1 VK"),
            "team_id": team1["id"],
            "budget": {"max_limit": 100000, "reset_duration": "1M"},  # $1,000
        }
        vk1_response = governance_client.create_virtual_key(vk1_data)
        assert_response_success(vk1_response, 201)
        vk1 = vk1_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk1["id"])

        # Verify hierarchy structure
        assert customer["budget"]["max_limit"] == 1000000
        assert team1["budget"]["max_limit"] == 300000
        assert team2["budget"]["max_limit"] == 200000
        assert vk1["budget"]["max_limit"] == 100000

        # Verify relationships
        assert team1["customer_id"] == customer["id"]
        assert team2["customer_id"] == customer["id"]
        assert vk1["team_id"] == team1["id"]

    @pytest.mark.customers
    @pytest.mark.budget
    def test_customer_budget_large_scale(self, governance_client, cleanup_tracker):
        """Test customer budgets for large enterprise scenarios"""
        # Test very large budget for enterprise customer
        enterprise_data = {
            "name": generate_unique_name("Enterprise Customer"),
            "budget": {
                "max_limit": 100000000000,  # $1 billion in cents
                "reset_duration": "1Y",
            },
        }

        response = governance_client.create_customer(enterprise_data)
        assert_response_success(response, 201)
        customer = response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        assert customer["budget"]["max_limit"] == 100000000000
        assert customer["budget"]["reset_duration"] == "1Y"


class TestCustomerTeamRelationships:
    """Test customer relationships with teams"""

    @pytest.mark.customers
    @pytest.mark.relationships
    def test_customer_teams_relationship_loading(
        self, governance_client, cleanup_tracker
    ):
        """Test that customer properly loads teams relationships"""
        # Create customer
        customer_data = {"name": generate_unique_name("Team Parent Customer")}
        customer_response = governance_client.create_customer(customer_data)
        assert_response_success(customer_response, 201)
        customer = customer_response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        # Create teams under this customer
        team_names = []
        for i in range(3):
            team_name = generate_unique_name(f"Customer Team {i}")
            team_names.append(team_name)
            team_data = {"name": team_name, "customer_id": customer["id"]}
            team_response = governance_client.create_team(team_data)
            assert_response_success(team_response, 201)
            cleanup_tracker.add_team(team_response.json()["team"]["id"])

        # Fetch customer with teams loaded
        customer_response = governance_client.get_customer(customer["id"])
        assert_response_success(customer_response, 200)
        customer_with_teams = customer_response.json()["customer"]

        # Verify teams relationship loaded
        assert "teams" in customer_with_teams
        teams = customer_with_teams["teams"]
        assert isinstance(teams, list)
        assert len(teams) == 3

        # Verify all team names are present
        loaded_team_names = {team["name"] for team in teams}
        for name in team_names:
            assert name in loaded_team_names

        # Verify all teams have correct customer_id
        for team in teams:
            assert team["customer_id"] == customer["id"]

    @pytest.mark.customers
    @pytest.mark.relationships
    def test_customer_with_no_teams(self, governance_client, cleanup_tracker):
        """Test customer with no teams has empty teams list"""
        # Create customer without teams
        customer_data = {"name": generate_unique_name("No Teams Customer")}
        customer_response = governance_client.create_customer(customer_data)
        assert_response_success(customer_response, 201)
        customer = customer_response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        # Fetch customer with teams loaded
        customer_response = governance_client.get_customer(customer["id"])
        assert_response_success(customer_response, 200)
        customer_data = customer_response.json()["customer"]

        # Teams should be empty list or None
        teams = customer_data.get("teams")
        assert teams == [] or teams is None

    @pytest.mark.customers
    @pytest.mark.relationships
    def test_customer_teams_cascading_operations(
        self, governance_client, cleanup_tracker
    ):
        """Test cascading operations between customers and teams"""
        # Create customer
        customer_data = {"name": generate_unique_name("Cascade Test Customer")}
        customer_response = governance_client.create_customer(customer_data)
        assert_response_success(customer_response, 201)
        customer = customer_response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        # Create teams under customer
        team_ids = []
        for i in range(2):
            team_data = {
                "name": generate_unique_name(f"Cascade Team {i}"),
                "customer_id": customer["id"],
            }
            team_response = governance_client.create_team(team_data)
            assert_response_success(team_response, 201)
            team_id = team_response.json()["team"]["id"]
            team_ids.append(team_id)
            cleanup_tracker.add_team(team_id)

        # Create VKs under teams
        vk_ids = []
        for team_id in team_ids:
            vk_data = {"name": generate_unique_name("Cascade VK"), "team_id": team_id}
            vk_response = governance_client.create_virtual_key(vk_data)
            assert_response_success(vk_response, 201)
            vk_id = vk_response.json()["virtual_key"]["id"]
            vk_ids.append(vk_id)
            cleanup_tracker.add_virtual_key(vk_id)

        # Verify all entities exist and are properly linked
        customer_response = governance_client.get_customer(customer["id"])
        customer_with_teams = customer_response.json()["customer"]
        assert len(customer_with_teams["teams"]) == 2

        for vk_id in vk_ids:
            vk_response = governance_client.get_virtual_key(vk_id)
            vk = vk_response.json()["virtual_key"]
            assert vk["team"] is not None
            assert vk["team"]["customer_id"] == customer["id"]

    @pytest.mark.customers
    @pytest.mark.relationships
    @pytest.mark.edge_cases
    def test_customer_orphaned_teams_handling(self, governance_client, cleanup_tracker):
        """Test customer behavior when teams reference non-existent customer"""
        # This test simulates data integrity issues
        # In practice, this would be prevented by foreign key constraints

        # Create customer and team
        customer_data = {"name": generate_unique_name("Temp Customer")}
        customer_response = governance_client.create_customer(customer_data)
        assert_response_success(customer_response, 201)
        customer = customer_response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        team_data = {
            "name": generate_unique_name("Orphan Test Team"),
            "customer_id": customer["id"],
        }
        team_response = governance_client.create_team(team_data)
        assert_response_success(team_response, 201)
        team = team_response.json()["team"]
        cleanup_tracker.add_team(team["id"])

        # If we were to delete the customer, what happens to the team?
        # This depends on database constraints and API implementation
        # For now, we just verify the relationship exists correctly
        assert team["customer_id"] == customer["id"]
        assert team["customer"]["id"] == customer["id"]


class TestCustomerConcurrency:
    """Test concurrent operations on Customers"""

    @pytest.mark.customers
    @pytest.mark.concurrency
    @pytest.mark.slow
    def test_customer_concurrent_creation(self, governance_client, cleanup_tracker):
        """Test creating multiple customers concurrently"""

        def create_customer(index):
            data = {"name": generate_unique_name(f"Concurrent Customer {index}")}
            response = governance_client.create_customer(data)
            return response

        # Create 10 customers concurrently
        with ThreadPoolExecutor(max_workers=10) as executor:
            futures = [executor.submit(create_customer, i) for i in range(10)]
            responses = [future.result() for future in futures]

        # Verify all succeeded
        created_customers = []
        for response in responses:
            assert_response_success(response, 201)
            customer_data = response.json()["customer"]
            created_customers.append(customer_data)
            cleanup_tracker.add_customer(customer_data["id"])

        # Verify all customers have unique IDs
        customer_ids = [customer["id"] for customer in created_customers]
        assert len(set(customer_ids)) == 10  # All unique IDs

    @pytest.mark.customers
    @pytest.mark.concurrency
    @pytest.mark.slow
    def test_customer_concurrent_updates(self, governance_client, cleanup_tracker):
        """Test updating same customer concurrently"""
        # Create customer to update
        data = {"name": generate_unique_name("Concurrent Update Customer")}
        create_response = governance_client.create_customer(data)
        assert_response_success(create_response, 201)
        customer_id = create_response.json()["customer"]["id"]
        cleanup_tracker.add_customer(customer_id)

        # Update concurrently with different names
        def update_customer(index):
            update_data = {"name": f"Updated by thread {index}"}
            response = governance_client.update_customer(customer_id, update_data)
            return response, index

        with ThreadPoolExecutor(max_workers=5) as executor:
            futures = [executor.submit(update_customer, i) for i in range(5)]
            results = [future.result() for future in futures]

        # All updates should succeed (last one wins)
        for response, index in results:
            assert_response_success(response, 200)

        # Verify final state
        final_response = governance_client.get_customer(customer_id)
        final_customer = final_response.json()["customer"]
        assert final_customer["name"].startswith("Updated by thread")

    @pytest.mark.customers
    @pytest.mark.concurrency
    @pytest.mark.slow
    def test_customer_concurrent_budget_updates(
        self, governance_client, cleanup_tracker
    ):
        """Test concurrent budget updates on same customer"""
        # Create customer with budget
        data = {
            "name": generate_unique_name("Concurrent Budget Customer"),
            "budget": {"max_limit": 100000, "reset_duration": "1d"},
        }
        create_response = governance_client.create_customer(data)
        assert_response_success(create_response, 201)
        customer_id = create_response.json()["customer"]["id"]
        cleanup_tracker.add_customer(customer_id)

        # Update budget concurrently with different limits
        def update_budget(index):
            limit = 100000 + (index * 10000)  # Different limits
            update_data = {"budget": {"max_limit": limit}}
            response = governance_client.update_customer(customer_id, update_data)
            return response, limit

        with ThreadPoolExecutor(max_workers=5) as executor:
            futures = [executor.submit(update_budget, i) for i in range(5)]
            results = [future.result() for future in futures]

        # All updates should succeed
        for response, limit in results:
            assert_response_success(response, 200)

        # Verify final state has one of the updated limits
        final_response = governance_client.get_customer(customer_id)
        final_customer = final_response.json()["customer"]
        final_limit = final_customer["budget"]["max_limit"]
        expected_limits = [100000 + (i * 10000) for i in range(5)]
        assert final_limit in expected_limits


class TestCustomerComplexScenarios:
    """Test complex scenarios involving customers"""

    @pytest.mark.customers
    @pytest.mark.hierarchical
    @pytest.mark.slow
    def test_customer_large_hierarchy_creation(
        self, governance_client, cleanup_tracker
    ):
        """Test creating large hierarchical structure under customer"""
        # Create customer
        customer_data = {
            "name": generate_unique_name("Large Hierarchy Customer"),
            "budget": {"max_limit": 10000000, "reset_duration": "1M"},  # $100,000
        }
        customer_response = governance_client.create_customer(customer_data)
        assert_response_success(customer_response, 201)
        customer = customer_response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        # Create multiple teams
        team_ids = []
        for i in range(5):
            team_data = {
                "name": generate_unique_name(f"Large Hierarchy Team {i}"),
                "customer_id": customer["id"],
                "budget": {
                    "max_limit": 1000000,
                    "reset_duration": "1M",
                },  # $10,000 each
            }
            team_response = governance_client.create_team(team_data)
            assert_response_success(team_response, 201)
            team_id = team_response.json()["team"]["id"]
            team_ids.append(team_id)
            cleanup_tracker.add_team(team_id)

        # Create multiple VKs per team
        vk_count = 0
        for team_id in team_ids:
            for j in range(3):  # 3 VKs per team
                vk_data = {
                    "name": generate_unique_name(f"Large Hierarchy VK {team_id}-{j}"),
                    "team_id": team_id,
                    "budget": {
                        "max_limit": 100000,
                        "reset_duration": "1M",
                    },  # $1,000 each
                }
                vk_response = governance_client.create_virtual_key(vk_data)
                assert_response_success(vk_response, 201)
                vk_id = vk_response.json()["virtual_key"]["id"]
                cleanup_tracker.add_virtual_key(vk_id)
                vk_count += 1

        # Verify hierarchy structure
        customer_response = governance_client.get_customer(customer["id"])
        customer_with_teams = customer_response.json()["customer"]

        assert len(customer_with_teams["teams"]) == 5
        assert vk_count == 15  # 5 teams * 3 VKs each

        # Verify budget hierarchy makes sense
        total_team_budgets = sum(
            team.get("budget", {}).get("max_limit", 0)
            for team in customer_with_teams["teams"]
        )
        assert (
            total_team_budgets <= customer["budget"]["max_limit"]
        )  # Teams shouldn't exceed customer

    @pytest.mark.customers
    @pytest.mark.performance
    @pytest.mark.slow
    def test_customer_performance_with_many_teams(
        self, governance_client, cleanup_tracker
    ):
        """Test customer performance when loading many teams"""
        # Create customer
        customer_data = {"name": generate_unique_name("Performance Test Customer")}
        customer_response = governance_client.create_customer(customer_data)
        assert_response_success(customer_response, 201)
        customer = customer_response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        # Create many teams
        team_count = 50  # Adjust based on performance requirements
        start_time = time.time()

        for i in range(team_count):
            team_data = {
                "name": generate_unique_name(f"Perf Team {i}"),
                "customer_id": customer["id"],
            }
            team_response = governance_client.create_team(team_data)
            assert_response_success(team_response, 201)
            cleanup_tracker.add_team(team_response.json()["team"]["id"])

        creation_time = time.time() - start_time

        # Test customer loading performance
        start_time = time.time()
        customer_response = governance_client.get_customer(customer["id"])
        assert_response_success(customer_response, 200)
        load_time = time.time() - start_time

        customer_with_teams = customer_response.json()["customer"]
        assert len(customer_with_teams["teams"]) == team_count

        # Log performance metrics (adjust thresholds based on requirements)
        print(f"Created {team_count} teams in {creation_time:.2f}s")
        print(f"Loaded customer with {team_count} teams in {load_time:.2f}s")

        # Performance assertions (adjust based on requirements)
        assert (
            load_time < 5.0
        ), f"Loading customer with {team_count} teams took too long: {load_time}s"

    @pytest.mark.customers
    @pytest.mark.integration
    def test_customer_full_lifecycle_scenario(self, governance_client, cleanup_tracker):
        """Test complete customer lifecycle scenario"""
        # 1. Create customer with budget
        customer_data = {
            "name": generate_unique_name("Lifecycle Customer"),
            "budget": {"max_limit": 1000000, "reset_duration": "1M"},
        }
        customer_response = governance_client.create_customer(customer_data)
        assert_response_success(customer_response, 201)
        customer = customer_response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        # 2. Update customer name and budget
        update_data = {
            "name": "Updated Lifecycle Customer",
            "budget": {"max_limit": 2000000, "reset_duration": "3M"},
        }
        update_response = governance_client.update_customer(customer["id"], update_data)
        assert_response_success(update_response, 200)
        updated_customer = update_response.json()["customer"]
        assert updated_customer["name"] == "Updated Lifecycle Customer"
        assert updated_customer["budget"]["max_limit"] == 2000000

        # 3. Create teams under customer
        team_data = {
            "name": generate_unique_name("Lifecycle Team"),
            "customer_id": customer["id"],
            "budget": {"max_limit": 500000, "reset_duration": "1M"},
        }
        team_response = governance_client.create_team(team_data)
        assert_response_success(team_response, 201)
        team = team_response.json()["team"]
        cleanup_tracker.add_team(team["id"])

        # 4. Create VKs under team
        vk_data = {
            "name": generate_unique_name("Lifecycle VK"),
            "team_id": team["id"],
            "budget": {"max_limit": 100000, "reset_duration": "1d"},
        }
        vk_response = governance_client.create_virtual_key(vk_data)
        assert_response_success(vk_response, 201)
        vk = vk_response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk["id"])

        # 5. Verify complete hierarchy
        final_customer_response = governance_client.get_customer(customer["id"])
        final_customer = final_customer_response.json()["customer"]

        assert final_customer["name"] == "Updated Lifecycle Customer"
        assert len(final_customer["teams"]) == 1
        assert final_customer["teams"][0]["id"] == team["id"]

        final_vk_response = governance_client.get_virtual_key(vk["id"])
        final_vk = final_vk_response.json()["virtual_key"]

        # Verify VK belongs to team (customer relationship not preloaded in VK->team)
        assert final_vk["team"]["id"] == team["id"]
        assert final_vk["team"].get("customer_id") == customer["id"]

        # 6. Clean up (automatic via cleanup_tracker)
        # This tests the full CRUD lifecycle
