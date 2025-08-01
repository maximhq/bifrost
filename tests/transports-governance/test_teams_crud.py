"""
Comprehensive Team CRUD Tests for Bifrost Governance Plugin

This module provides exhaustive testing of Team operations including:
- Complete CRUD lifecycle testing
- Comprehensive field update testing (individual and batch)
- Customer association testing
- Budget inheritance and management
- Filtering and query operations
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


class TestTeamBasicCRUD:
    """Test basic CRUD operations for Teams"""

    @pytest.mark.teams
    @pytest.mark.crud
    @pytest.mark.smoke
    def test_team_create_minimal(self, governance_client, cleanup_tracker):
        """Test creating team with minimal required data"""
        data = {"name": generate_unique_name("Minimal Team")}

        response = governance_client.create_team(data)
        assert_response_success(response, 201)

        team_data = response.json()["team"]
        cleanup_tracker.add_team(team_data["id"])

        # Verify required fields
        assert team_data["name"] == data["name"]
        assert team_data["id"] is not None
        assert team_data["created_at"] is not None
        assert team_data["updated_at"] is not None

        # Verify optional fields are None/empty
        assert team_data["virtual_keys"] is None

    @pytest.mark.teams
    @pytest.mark.crud
    def test_team_create_with_customer(
        self, governance_client, cleanup_tracker, sample_customer
    ):
        """Test creating team associated with a customer"""
        data = {
            "name": generate_unique_name("Customer Team"),
            "customer_id": sample_customer["id"],
        }

        response = governance_client.create_team(data)
        assert_response_success(response, 201)

        team_data = response.json()["team"]
        cleanup_tracker.add_team(team_data["id"])

        # Verify customer association
        assert team_data["customer_id"] == sample_customer["id"]
        assert team_data["customer"] is not None
        assert team_data["customer"]["id"] == sample_customer["id"]
        assert team_data["customer"]["name"] == sample_customer["name"]

    @pytest.mark.teams
    @pytest.mark.crud
    @pytest.mark.budget
    def test_team_create_with_budget(self, governance_client, cleanup_tracker):
        """Test creating team with budget"""
        data = {
            "name": generate_unique_name("Budget Team"),
            "budget": {"max_limit": 25000, "reset_duration": "1d"},  # $250.00 in cents
        }

        response = governance_client.create_team(data)
        assert_response_success(response, 201)

        team_data = response.json()["team"]
        cleanup_tracker.add_team(team_data["id"])

        # Verify budget was created
        assert team_data["budget"] is not None
        assert team_data["budget"]["max_limit"] == 25000
        assert team_data["budget"]["reset_duration"] == "1d"
        assert team_data["budget"]["current_usage"] == 0
        assert team_data["budget_id"] is not None

    @pytest.mark.teams
    @pytest.mark.crud
    @pytest.mark.budget
    def test_team_create_complete(
        self, governance_client, cleanup_tracker, sample_customer
    ):
        """Test creating team with all possible fields"""
        data = {
            "name": generate_unique_name("Complete Team"),
            "customer_id": sample_customer["id"],
            "budget": {
                "max_limit": 100000,  # $1000.00 in cents
                "reset_duration": "1w",
            },
        }

        response = governance_client.create_team(data)
        assert_response_success(response, 201)

        team_data = response.json()["team"]
        cleanup_tracker.add_team(team_data["id"])

        # Verify all fields
        assert team_data["name"] == data["name"]
        assert team_data["customer_id"] == sample_customer["id"]
        assert team_data["customer"]["id"] == sample_customer["id"]
        assert team_data["budget"]["max_limit"] == 100000
        assert team_data["budget"]["reset_duration"] == "1w"

    @pytest.mark.teams
    @pytest.mark.crud
    def test_team_list_all(self, governance_client, sample_team):
        """Test listing all teams"""
        response = governance_client.list_teams()
        assert_response_success(response, 200)

        data = response.json()
        assert "teams" in data
        assert "count" in data
        assert isinstance(data["teams"], list)
        assert data["count"] >= 1

        # Find our test team
        test_team = next(
            (team for team in data["teams"] if team["id"] == sample_team["id"]), None
        )
        assert test_team is not None

    @pytest.mark.teams
    @pytest.mark.crud
    def test_team_list_filter_by_customer(
        self, governance_client, sample_team_with_customer
    ):
        """Test listing teams filtered by customer"""
        customer_id = sample_team_with_customer["customer_id"]
        response = governance_client.list_teams(customer_id=customer_id)
        assert_response_success(response, 200)

        data = response.json()
        teams = data["teams"]

        # All returned teams should belong to the specified customer
        for team in teams:
            assert team["customer_id"] == customer_id

        # Our test team should be in the results
        test_team = next(
            (team for team in teams if team["id"] == sample_team_with_customer["id"]),
            None,
        )
        assert test_team is not None

    @pytest.mark.teams
    @pytest.mark.crud
    def test_team_get_by_id(self, governance_client, sample_team):
        """Test getting team by ID with relationships loaded"""
        response = governance_client.get_team(sample_team["id"])
        assert_response_success(response, 200)

        team_data = response.json()["team"]
        assert team_data["id"] == sample_team["id"]
        assert team_data["name"] == sample_team["name"]

    @pytest.mark.teams
    @pytest.mark.crud
    def test_team_get_nonexistent(self, governance_client):
        """Test getting non-existent team returns 404"""
        fake_id = str(uuid.uuid4())
        response = governance_client.get_team(fake_id)
        assert response.status_code == 404

    @pytest.mark.teams
    @pytest.mark.crud
    def test_team_delete(self, governance_client, cleanup_tracker):
        """Test deleting a team"""
        # Create team to delete
        data = {"name": generate_unique_name("Delete Test Team")}
        create_response = governance_client.create_team(data)
        assert_response_success(create_response, 201)
        team_id = create_response.json()["team"]["id"]

        # Delete team
        delete_response = governance_client.delete_team(team_id)
        assert_response_success(delete_response, 200)

        # Verify team is gone
        get_response = governance_client.get_team(team_id)
        assert get_response.status_code == 404

    @pytest.mark.teams
    @pytest.mark.crud
    def test_team_delete_nonexistent(self, governance_client):
        """Test deleting non-existent team returns 404"""
        fake_id = str(uuid.uuid4())
        response = governance_client.delete_team(fake_id)
        assert response.status_code == 404


class TestTeamValidation:
    """Test validation rules for Team operations"""

    @pytest.mark.teams
    @pytest.mark.validation
    def test_team_create_missing_name(self, governance_client):
        """Test creating team without name fails"""
        data = {"customer_id": str(uuid.uuid4())}
        response = governance_client.create_team(data)
        assert response.status_code == 400

    @pytest.mark.teams
    @pytest.mark.validation
    def test_team_create_empty_name(self, governance_client):
        """Test creating team with empty name fails"""
        data = {"name": ""}
        response = governance_client.create_team(data)
        assert response.status_code == 400

    @pytest.mark.teams
    @pytest.mark.validation
    def test_team_create_invalid_customer_id(self, governance_client):
        """Test creating team with non-existent customer_id"""
        data = {
            "name": generate_unique_name("Invalid Customer Team"),
            "customer_id": str(uuid.uuid4()),
        }
        response = governance_client.create_team(data)
        # Note: Depending on implementation, this might succeed with warning or fail
        # Adjust assertion based on actual API behavior

    @pytest.mark.teams
    @pytest.mark.validation
    def test_team_create_invalid_budget(self, governance_client):
        """Test creating team with invalid budget data"""
        # Test negative budget (should be rejected)
        data = {
            "name": generate_unique_name("Negative Budget Team"),
            "budget": {"max_limit": -1000, "reset_duration": "1h"},
        }
        response = governance_client.create_team(data)
        assert response.status_code == 400  # API should reject negative budgets

        # Test invalid reset duration
        data = {
            "name": generate_unique_name("Invalid Duration Team"),
            "budget": {"max_limit": 1000, "reset_duration": "invalid"},
        }
        response = governance_client.create_team(data)
        assert response.status_code == 400


class TestTeamFieldUpdates:
    """Comprehensive tests for Team field updates"""

    @pytest.mark.teams
    @pytest.mark.field_updates
    def test_team_update_individual_fields(
        self, governance_client, cleanup_tracker, sample_customer
    ):
        """Test updating each team field individually"""
        # Create team with all fields for testing
        original_data = {
            "name": generate_unique_name("Complete Update Test Team"),
            "customer_id": sample_customer["id"],
            "budget": {"max_limit": 50000, "reset_duration": "1d"},
        }
        create_response = governance_client.create_team(original_data)
        assert_response_success(create_response, 201)
        team_id = create_response.json()["team"]["id"]
        cleanup_tracker.add_team(team_id)

        # Get original state
        original_response = governance_client.get_team(team_id)
        original_team = original_response.json()["team"]

        # Create another customer for testing customer_id updates
        other_customer_data = {"name": generate_unique_name("Other Customer")}
        other_customer_response = governance_client.create_customer(other_customer_data)
        assert_response_success(other_customer_response, 201)
        other_customer = other_customer_response.json()["customer"]
        cleanup_tracker.add_customer(other_customer["id"])

        # Test individual field updates
        field_test_cases = [
            {
                "field": "name",
                "update_data": {"name": "Updated Team Name"},
                "expected_value": "Updated Team Name",
            },
            {
                "field": "customer_id",
                "update_data": {"customer_id": other_customer["id"]},
                "expected_value": other_customer["id"],
                "exclude_from_unchanged_check": ["customer_id", "customer"],
            },
            {
                "field": "customer_id_clear",
                "update_data": {"customer_id": None},
                "expected_value": None,
                "exclude_from_unchanged_check": ["customer_id", "customer"],
                "custom_validation": lambda team: team["customer_id"] is None
                and team["customer"] is None,
            },
        ]

        for test_case in field_test_cases:
            # Reset team to original state
            reset_data = {
                "name": original_team["name"],
                "customer_id": original_team["customer_id"],
            }
            governance_client.update_team(team_id, reset_data)

            # Perform field update
            response = governance_client.update_team(team_id, test_case["update_data"])
            assert_response_success(response, 200)
            updated_team = response.json()["team"]

            # Verify target field was updated
            if test_case.get("custom_validation"):
                test_case["custom_validation"](updated_team)
            else:
                field_parts = test_case["field"].split(".")
                current_value = updated_team
                for part in field_parts:
                    if part != "clear":  # Skip suffix indicators
                        current_value = current_value[part]
                assert (
                    current_value == test_case["expected_value"]
                ), f"Field {test_case['field']} not updated correctly"

            # Verify other fields unchanged (if specified)
            if test_case.get("verify_unchanged", True):
                exclude_fields = test_case.get(
                    "exclude_from_unchanged_check", [test_case["field"]]
                )
                verify_unchanged_fields(updated_team, original_team, exclude_fields)

    @pytest.mark.teams
    @pytest.mark.field_updates
    @pytest.mark.budget
    def test_team_budget_updates(self, governance_client, cleanup_tracker):
        """Test comprehensive budget creation, update, and modification"""
        # Create team without budget
        data = {"name": generate_unique_name("Budget Update Test Team")}
        create_response = governance_client.create_team(data)
        assert_response_success(create_response, 201)
        team_id = create_response.json()["team"]["id"]
        cleanup_tracker.add_team(team_id)

        # Test 1: Add budget to team without budget
        budget_data = {"max_limit": 15000, "reset_duration": "1h"}
        response = governance_client.update_team(team_id, {"budget": budget_data})
        assert_response_success(response, 200)
        updated_team = response.json()["team"]
        assert updated_team["budget"]["max_limit"] == 15000
        assert updated_team["budget"]["reset_duration"] == "1h"
        assert updated_team["budget_id"] is not None

        # Test 2: Update existing budget completely
        new_budget_data = {"max_limit": 30000, "reset_duration": "2h"}
        response = governance_client.update_team(team_id, {"budget": new_budget_data})
        assert_response_success(response, 200)
        updated_team = response.json()["team"]
        assert updated_team["budget"]["max_limit"] == 30000
        assert updated_team["budget"]["reset_duration"] == "2h"

        # Test 3: Partial budget update (only max_limit)
        response = governance_client.update_team(
            team_id, {"budget": {"max_limit": 45000}}
        )
        assert_response_success(response, 200)
        updated_team = response.json()["team"]
        assert updated_team["budget"]["max_limit"] == 45000
        assert (
            updated_team["budget"]["reset_duration"] == "2h"
        )  # Should remain unchanged

        # Test 4: Partial budget update (only reset_duration)
        response = governance_client.update_team(
            team_id, {"budget": {"reset_duration": "1d"}}
        )
        assert_response_success(response, 200)
        updated_team = response.json()["team"]
        assert updated_team["budget"]["max_limit"] == 45000  # Should remain unchanged
        assert updated_team["budget"]["reset_duration"] == "1d"

    @pytest.mark.teams
    @pytest.mark.field_updates
    def test_team_multiple_field_updates(
        self, governance_client, cleanup_tracker, sample_customer
    ):
        """Test updating multiple fields simultaneously"""
        # Create team with initial data
        initial_data = {
            "name": generate_unique_name("Multi-Field Test Team"),
        }
        create_response = governance_client.create_team(initial_data)
        assert_response_success(create_response, 201)
        team_id = create_response.json()["team"]["id"]
        cleanup_tracker.add_team(team_id)

        # Update multiple fields at once
        update_data = {
            "name": "Updated Multi-Field Team Name",
            "customer_id": sample_customer["id"],
            "budget": {"max_limit": 75000, "reset_duration": "1w"},
        }

        response = governance_client.update_team(team_id, update_data)
        assert_response_success(response, 200)

        updated_team = response.json()["team"]
        assert updated_team["name"] == "Updated Multi-Field Team Name"
        assert updated_team["customer_id"] == sample_customer["id"]
        assert updated_team["customer"]["id"] == sample_customer["id"]
        assert updated_team["budget"]["max_limit"] == 75000
        assert updated_team["budget"]["reset_duration"] == "1w"

    @pytest.mark.teams
    @pytest.mark.field_updates
    @pytest.mark.edge_cases
    def test_team_update_edge_cases(self, governance_client, cleanup_tracker):
        """Test edge cases in team updates"""
        # Create test team
        data = {"name": generate_unique_name("Edge Case Team")}
        create_response = governance_client.create_team(data)
        assert_response_success(create_response, 201)
        team_id = create_response.json()["team"]["id"]
        cleanup_tracker.add_team(team_id)

        original_response = governance_client.get_team(team_id)
        original_team = original_response.json()["team"]

        # Test 1: Empty update (should return unchanged team)
        response = governance_client.update_team(team_id, {})
        assert_response_success(response, 200)
        updated_team = response.json()["team"]

        # Compare ignoring timestamps
        differences = deep_compare_entities(
            updated_team, original_team, ignore_fields=["updated_at"]
        )
        assert len(differences) == 0, f"Empty update changed fields: {differences}"

        # Test 2: Update with same values
        response = governance_client.update_team(
            team_id, {"name": original_team["name"]}
        )
        assert_response_success(response, 200)

        # Test 3: Very long team name (test field length limits)
        long_name = "x" * 1000  # Adjust based on actual field limits
        response = governance_client.update_team(team_id, {"name": long_name})
        # Expected behavior depends on API validation rules

    @pytest.mark.teams
    @pytest.mark.field_updates
    def test_team_update_nonexistent(self, governance_client):
        """Test updating non-existent team returns 404"""
        fake_id = str(uuid.uuid4())
        response = governance_client.update_team(fake_id, {"name": "test"})
        assert response.status_code == 404


class TestTeamBudgetManagement:
    """Test team budget specific functionality"""

    @pytest.mark.teams
    @pytest.mark.budget
    def test_team_budget_creation_and_validation(
        self, governance_client, cleanup_tracker
    ):
        """Test budget creation with various configurations"""
        # Test valid budget configurations
        budget_test_cases = [
            {"max_limit": 5000, "reset_duration": "1h"},
            {"max_limit": 25000, "reset_duration": "1d"},
            {"max_limit": 100000, "reset_duration": "1w"},
            {"max_limit": 500000, "reset_duration": "1M"},
        ]

        for budget_config in budget_test_cases:
            data = {
                "name": generate_unique_name(
                    f"Budget Team {budget_config['reset_duration']}"
                ),
                "budget": budget_config,
            }

            response = governance_client.create_team(data)
            assert_response_success(response, 201)

            team_data = response.json()["team"]
            cleanup_tracker.add_team(team_data["id"])

            assert team_data["budget"]["max_limit"] == budget_config["max_limit"]
            assert (
                team_data["budget"]["reset_duration"] == budget_config["reset_duration"]
            )
            assert team_data["budget"]["current_usage"] == 0
            assert team_data["budget"]["last_reset"] is not None

    @pytest.mark.teams
    @pytest.mark.budget
    @pytest.mark.edge_cases
    def test_team_budget_edge_cases(self, governance_client, cleanup_tracker):
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
                    f"Edge Budget Team {budget_config['max_limit']}"
                ),
                "budget": budget_config,
            }

            response = governance_client.create_team(data)
            # Adjust assertions based on API validation rules
            if (
                budget_config["max_limit"] >= 0
            ):  # Assuming non-negative budgets are valid
                assert_response_success(response, 201)
                cleanup_tracker.add_team(response.json()["team"]["id"])
            else:
                assert response.status_code == 400

    @pytest.mark.teams
    @pytest.mark.budget
    def test_team_budget_inheritance_simulation(
        self, governance_client, cleanup_tracker
    ):
        """Test team budget in context of hierarchical inheritance"""
        # This test simulates budget inheritance behavior
        # Actual inheritance testing would be in integration tests

        # Create customer with budget
        customer_data = {
            "name": generate_unique_name("Budget Customer"),
            "budget": {"max_limit": 100000, "reset_duration": "1d"},
        }
        customer_response = governance_client.create_customer(customer_data)
        assert_response_success(customer_response, 201)
        customer = customer_response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        # Create team with smaller budget under customer
        team_data = {
            "name": generate_unique_name("Sub-Budget Team"),
            "customer_id": customer["id"],
            "budget": {
                "max_limit": 25000,
                "reset_duration": "1d",
            },  # Smaller than customer
        }
        team_response = governance_client.create_team(team_data)
        assert_response_success(team_response, 201)
        team = team_response.json()["team"]
        cleanup_tracker.add_team(team["id"])

        # Verify both budgets exist independently
        assert team["budget"]["max_limit"] == 25000
        # Note: Customer budget not preloaded in team response (use customer endpoint to verify)
        customer_response = governance_client.get_customer(customer["id"])
        customer_with_budget = customer_response.json()["customer"]
        assert customer_with_budget["budget"]["max_limit"] == 100000

        # Create team without budget under customer (should inherit)
        no_budget_team_data = {
            "name": generate_unique_name("Inherit Budget Team"),
            "customer_id": customer["id"],
        }
        no_budget_response = governance_client.create_team(no_budget_team_data)
        assert_response_success(no_budget_response, 201)
        no_budget_team = no_budget_response.json()["team"]
        cleanup_tracker.add_team(no_budget_team["id"])

        # Team without explicit budget should not have budget field (omitempty)
        assert no_budget_team.get("budget") is None
        # Verify customer has budget (need to fetch customer directly due to preloading limits)
        customer_check = governance_client.get_customer(customer["id"])
        assert customer_check.json()["customer"]["budget"]["max_limit"] == 100000


class TestTeamRelationships:
    """Test team relationships with customers"""

    @pytest.mark.teams
    @pytest.mark.relationships
    def test_team_customer_relationship_loading(
        self, governance_client, cleanup_tracker, sample_customer
    ):
        """Test that team properly loads customer relationships"""
        data = {
            "name": generate_unique_name("Customer Relationship Team"),
            "customer_id": sample_customer["id"],
        }

        response = governance_client.create_team(data)
        assert_response_success(response, 201)
        team_data = response.json()["team"]
        cleanup_tracker.add_team(team_data["id"])

        # Verify customer relationship loaded
        assert team_data["customer"] is not None
        assert team_data["customer"]["id"] == sample_customer["id"]
        assert team_data["customer"]["name"] == sample_customer["name"]

        # Verify customer budget relationship loaded if it exists
        if sample_customer.get("budget"):
            assert team_data["customer"]["budget"] is not None

    @pytest.mark.teams
    @pytest.mark.relationships
    def test_team_orphaned_customer_reference(self, governance_client, cleanup_tracker):
        """Test team behavior with orphaned customer reference"""
        # Create team with non-existent customer_id
        fake_customer_id = str(uuid.uuid4())
        data = {
            "name": generate_unique_name("Orphaned Team"),
            "customer_id": fake_customer_id,
        }

        response = governance_client.create_team(data)
        # Behavior depends on API implementation:
        # - Might succeed with warning
        # - Might fail with validation error
        # Adjust assertion based on actual behavior

        if response.status_code == 201:
            cleanup_tracker.add_team(response.json()["team"]["id"])
            # Verify team was created but customer relationship is null/missing
            team_data = response.json()["team"]
            assert team_data.get("customer") is None
        else:
            assert response.status_code == 400  # Validation error expected

    @pytest.mark.teams
    @pytest.mark.relationships
    def test_team_customer_association_changes(
        self, governance_client, cleanup_tracker, sample_customer
    ):
        """Test changing team customer associations"""
        # Create standalone team
        data = {"name": generate_unique_name("Association Test Team")}
        create_response = governance_client.create_team(data)
        assert_response_success(create_response, 201)
        team_id = create_response.json()["team"]["id"]
        cleanup_tracker.add_team(team_id)

        # Create another customer
        other_customer_data = {"name": generate_unique_name("Other Customer")}
        other_customer_response = governance_client.create_customer(other_customer_data)
        assert_response_success(other_customer_response, 201)
        other_customer = other_customer_response.json()["customer"]
        cleanup_tracker.add_customer(other_customer["id"])

        # Test 1: Associate with first customer
        response = governance_client.update_team(
            team_id, {"customer_id": sample_customer["id"]}
        )
        assert_response_success(response, 200)
        updated_team = response.json()["team"]
        assert updated_team["customer_id"] == sample_customer["id"]
        assert updated_team["customer"]["id"] == sample_customer["id"]

        # Test 2: Switch to other customer
        response = governance_client.update_team(
            team_id, {"customer_id": other_customer["id"]}
        )
        assert_response_success(response, 200)
        updated_team = response.json()["team"]
        assert updated_team["customer_id"] == other_customer["id"]
        assert updated_team["customer"]["id"] == other_customer["id"]

        # Test 3: Remove customer association
        response = governance_client.update_team(team_id, {"customer_id": None})
        # Note: Behavior depends on API implementation
        # Adjust assertion based on actual behavior


class TestTeamConcurrency:
    """Test concurrent operations on Teams"""

    @pytest.mark.teams
    @pytest.mark.concurrency
    @pytest.mark.slow
    def test_team_concurrent_creation(self, governance_client, cleanup_tracker):
        """Test creating multiple teams concurrently"""

        def create_team(index):
            data = {"name": generate_unique_name(f"Concurrent Team {index}")}
            response = governance_client.create_team(data)
            return response

        # Create 10 teams concurrently
        with ThreadPoolExecutor(max_workers=10) as executor:
            futures = [executor.submit(create_team, i) for i in range(10)]
            responses = [future.result() for future in futures]

        # Verify all succeeded
        created_teams = []
        for response in responses:
            assert_response_success(response, 201)
            team_data = response.json()["team"]
            created_teams.append(team_data)
            cleanup_tracker.add_team(team_data["id"])

        # Verify all teams have unique IDs
        team_ids = [team["id"] for team in created_teams]
        assert len(set(team_ids)) == 10  # All unique IDs

    @pytest.mark.teams
    @pytest.mark.concurrency
    @pytest.mark.slow
    def test_team_concurrent_updates(self, governance_client, cleanup_tracker):
        """Test updating same team concurrently"""
        # Create team to update
        data = {"name": generate_unique_name("Concurrent Update Team")}
        create_response = governance_client.create_team(data)
        assert_response_success(create_response, 201)
        team_id = create_response.json()["team"]["id"]
        cleanup_tracker.add_team(team_id)

        # Update concurrently with different names
        def update_team(index):
            update_data = {"name": f"Updated by thread {index}"}
            response = governance_client.update_team(team_id, update_data)
            return response, index

        with ThreadPoolExecutor(max_workers=5) as executor:
            futures = [executor.submit(update_team, i) for i in range(5)]
            results = [future.result() for future in futures]

        # All updates should succeed (last one wins)
        for response, index in results:
            assert_response_success(response, 200)

        # Verify final state
        final_response = governance_client.get_team(team_id)
        final_team = final_response.json()["team"]
        assert final_team["name"].startswith("Updated by thread")

    @pytest.mark.teams
    @pytest.mark.concurrency
    @pytest.mark.slow
    def test_team_concurrent_customer_association(
        self, governance_client, cleanup_tracker, sample_customer
    ):
        """Test concurrent customer association updates"""
        # Create multiple teams to associate with same customer
        teams = []
        for i in range(5):
            data = {"name": generate_unique_name(f"Concurrent Association Team {i}")}
            response = governance_client.create_team(data)
            assert_response_success(response, 201)
            team_data = response.json()["team"]
            teams.append(team_data)
            cleanup_tracker.add_team(team_data["id"])

        # Associate all teams with customer concurrently
        def associate_team(team):
            update_data = {"customer_id": sample_customer["id"]}
            response = governance_client.update_team(team["id"], update_data)
            return response, team["id"]

        with ThreadPoolExecutor(max_workers=5) as executor:
            futures = [executor.submit(associate_team, team) for team in teams]
            results = [future.result() for future in futures]

        # All associations should succeed
        for response, team_id in results:
            assert_response_success(response, 200)
            updated_team = response.json()["team"]
            assert updated_team["customer_id"] == sample_customer["id"]


class TestTeamFiltering:
    """Test team filtering and query operations"""

    @pytest.mark.teams
    @pytest.mark.api
    def test_team_filter_by_customer_comprehensive(
        self, governance_client, cleanup_tracker
    ):
        """Test comprehensive customer filtering scenarios"""
        # Create customers
        customer1_data = {"name": generate_unique_name("Filter Customer 1")}
        customer1_response = governance_client.create_customer(customer1_data)
        assert_response_success(customer1_response, 201)
        customer1 = customer1_response.json()["customer"]
        cleanup_tracker.add_customer(customer1["id"])

        customer2_data = {"name": generate_unique_name("Filter Customer 2")}
        customer2_response = governance_client.create_customer(customer2_data)
        assert_response_success(customer2_response, 201)
        customer2 = customer2_response.json()["customer"]
        cleanup_tracker.add_customer(customer2["id"])

        # Create teams for customer1
        for i in range(3):
            team_data = {
                "name": generate_unique_name(f"Customer1 Team {i}"),
                "customer_id": customer1["id"],
            }
            response = governance_client.create_team(team_data)
            assert_response_success(response, 201)
            cleanup_tracker.add_team(response.json()["team"]["id"])

        # Create teams for customer2
        for i in range(2):
            team_data = {
                "name": generate_unique_name(f"Customer2 Team {i}"),
                "customer_id": customer2["id"],
            }
            response = governance_client.create_team(team_data)
            assert_response_success(response, 201)
            cleanup_tracker.add_team(response.json()["team"]["id"])

        # Create standalone team
        standalone_data = {"name": generate_unique_name("Standalone Team")}
        response = governance_client.create_team(standalone_data)
        assert_response_success(response, 201)
        cleanup_tracker.add_team(response.json()["team"]["id"])

        # Test filtering by customer1
        response = governance_client.list_teams(customer_id=customer1["id"])
        assert_response_success(response, 200)
        teams = response.json()["teams"]
        assert len(teams) == 3
        for team in teams:
            assert team["customer_id"] == customer1["id"]

        # Test filtering by customer2
        response = governance_client.list_teams(customer_id=customer2["id"])
        assert_response_success(response, 200)
        teams = response.json()["teams"]
        assert len(teams) == 2
        for team in teams:
            assert team["customer_id"] == customer2["id"]

        # Test filtering by non-existent customer
        fake_customer_id = str(uuid.uuid4())
        response = governance_client.list_teams(customer_id=fake_customer_id)
        assert_response_success(response, 200)
        teams = response.json()["teams"]
        assert len(teams) == 0

    @pytest.mark.teams
    @pytest.mark.api
    def test_team_list_pagination_and_sorting(self, governance_client, cleanup_tracker):
        """Test team list with pagination and sorting (if supported by API)"""
        # Create multiple teams for testing
        team_names = []
        for i in range(10):
            name = generate_unique_name(f"Sort Test Team {i:02d}")
            team_names.append(name)
            data = {"name": name}
            response = governance_client.create_team(data)
            assert_response_success(response, 201)
            cleanup_tracker.add_team(response.json()["team"]["id"])

        # Test basic list (should include our teams)
        response = governance_client.list_teams()
        assert_response_success(response, 200)
        teams = response.json()["teams"]
        assert len(teams) >= 10

        # Verify our teams are in the response
        response_team_names = {team["name"] for team in teams}
        for name in team_names:
            assert name in response_team_names
