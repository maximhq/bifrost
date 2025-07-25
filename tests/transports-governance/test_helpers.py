"""
Helper utilities and test data generators for Bifrost Governance Plugin tests.

This module provides additional utilities for test data generation, validation,
and common test operations to support the comprehensive governance test suite.
"""

import pytest
import uuid
import time
import json
import random
from typing import Dict, Any, List, Optional, Union
from datetime import datetime, timedelta
from faker import Faker

from conftest import assert_response_success, generate_unique_name, GovernanceTestClient

# Initialize Faker for generating test data
fake = Faker()


class TestDataFactory:
    """Factory for generating realistic test data"""

    @staticmethod
    def generate_budget_config(
        min_limit: int = 1000,
        max_limit: int = 1000000,
        duration_options: List[str] = None,
    ) -> Dict[str, Any]:
        """Generate realistic budget configuration"""
        if duration_options is None:
            duration_options = ["1h", "1d", "1w", "1M", "3M", "6M", "1Y"]

        return {
            "max_limit": random.randint(min_limit, max_limit),
            "reset_duration": random.choice(duration_options),
        }

    @staticmethod
    def generate_rate_limit_config(
        include_tokens: bool = True, include_requests: bool = True
    ) -> Dict[str, Any]:
        """Generate realistic rate limit configuration"""
        config = {}

        if include_tokens:
            config.update(
                {
                    "token_max_limit": random.randint(100, 100000),
                    "token_reset_duration": random.choice(["1m", "5m", "1h", "1d"]),
                }
            )

        if include_requests:
            config.update(
                {
                    "request_max_limit": random.randint(10, 10000),
                    "request_reset_duration": random.choice(["1m", "5m", "1h", "1d"]),
                }
            )

        return config

    @staticmethod
    def generate_customer_data(include_budget: bool = False) -> Dict[str, Any]:
        """Generate realistic customer data"""
        data = {"name": f"{fake.company()} ({generate_unique_name('Customer')})"}

        if include_budget:
            data["budget"] = TestDataFactory.generate_budget_config(
                min_limit=100000, max_limit=10000000  # Customers have larger budgets
            )

        return data

    @staticmethod
    def generate_team_data(
        customer_id: Optional[str] = None, include_budget: bool = False
    ) -> Dict[str, Any]:
        """Generate realistic team data"""
        team_types = [
            "Engineering",
            "Marketing",
            "Sales",
            "Research",
            "Support",
            "Operations",
        ]
        data = {
            "name": f"{random.choice(team_types)} Team ({generate_unique_name('Team')})"
        }

        if customer_id:
            data["customer_id"] = customer_id

        if include_budget:
            data["budget"] = TestDataFactory.generate_budget_config(
                min_limit=10000, max_limit=1000000  # Teams have medium budgets
            )

        return data

    @staticmethod
    def generate_virtual_key_data(
        team_id: Optional[str] = None,
        customer_id: Optional[str] = None,
        include_budget: bool = False,
        include_rate_limit: bool = False,
        model_restrictions: bool = False,
    ) -> Dict[str, Any]:
        """Generate realistic virtual key data"""
        purposes = [
            "Development",
            "Production",
            "Testing",
            "Staging",
            "Demo",
            "Research",
        ]
        data = {
            "name": f"{random.choice(purposes)} VK ({generate_unique_name('VK')})",
            "description": fake.sentence(),
            "is_active": random.choice([True, True, True, False]),  # 75% active
        }

        if team_id:
            data["team_id"] = team_id
        elif customer_id:
            data["customer_id"] = customer_id

        if model_restrictions:
            all_models = [
                "gpt-4",
                "gpt-3.5-turbo",
                "gpt-4-turbo",
                "claude-3-5-sonnet-20240620",
                "claude-3-7-sonnet-20250219",
            ]
            all_providers = ["openai", "anthropic"]

            data["allowed_models"] = random.sample(
                all_models, random.randint(1, len(all_models))
            )
            data["allowed_providers"] = random.sample(
                all_providers, random.randint(1, len(all_providers))
            )

        if include_budget:
            data["budget"] = TestDataFactory.generate_budget_config(
                min_limit=1000, max_limit=100000  # VKs have smaller budgets
            )

        if include_rate_limit:
            data["rate_limit"] = TestDataFactory.generate_rate_limit_config()

        return data


class ValidationHelper:
    """Helper functions for validating test results"""

    @staticmethod
    def validate_entity_structure(
        entity: Dict[str, Any], entity_type: str
    ) -> List[str]:
        """Validate that entity has expected structure"""
        errors = []

        # Common fields all entities should have
        required_fields = ["id", "created_at", "updated_at"]
        for field in required_fields:
            if field not in entity:
                errors.append(f"Missing required field: {field}")
            elif entity[field] is None:
                errors.append(f"Required field is None: {field}")

        # Entity-specific validation
        if entity_type == "virtual_key":
            vk_fields = ["name", "value", "is_active"]
            for field in vk_fields:
                if field not in entity:
                    errors.append(f"VK missing field: {field}")

        elif entity_type == "team":
            team_fields = ["name"]
            for field in team_fields:
                if field not in entity:
                    errors.append(f"Team missing field: {field}")

        elif entity_type == "customer":
            customer_fields = ["name"]
            for field in customer_fields:
                if field not in entity:
                    errors.append(f"Customer missing field: {field}")

        return errors

    @staticmethod
    def validate_budget_structure(budget: Dict[str, Any]) -> List[str]:
        """Validate budget structure"""
        errors = []
        required_fields = [
            "id",
            "max_limit",
            "reset_duration",
            "current_usage",
            "last_reset",
        ]

        for field in required_fields:
            if field not in budget:
                errors.append(f"Budget missing field: {field}")

        if budget.get("max_limit") is not None and budget["max_limit"] < 0:
            errors.append("Budget max_limit cannot be negative")

        if budget.get("current_usage") is not None and budget["current_usage"] < 0:
            errors.append("Budget current_usage cannot be negative")

        return errors

    @staticmethod
    def validate_rate_limit_structure(rate_limit: Dict[str, Any]) -> List[str]:
        """Validate rate limit structure"""
        errors = []
        required_fields = ["id"]

        for field in required_fields:
            if field not in rate_limit:
                errors.append(f"Rate limit missing field: {field}")

        # At least one limit should be specified
        token_fields = ["token_max_limit", "token_reset_duration"]
        request_fields = ["request_max_limit", "request_reset_duration"]

        has_token_limits = any(
            rate_limit.get(field) is not None for field in token_fields
        )
        has_request_limits = any(
            rate_limit.get(field) is not None for field in request_fields
        )

        if not has_token_limits and not has_request_limits:
            errors.append("Rate limit must have either token or request limits")

        return errors

    @staticmethod
    def validate_hierarchy_consistency(
        customer: Dict, teams: List[Dict], vks: List[Dict]
    ) -> List[str]:
        """Validate hierarchical consistency"""
        errors = []

        # Check team customer references
        for team in teams:
            if team.get("customer_id") != customer["id"]:
                errors.append(f"Team {team['id']} has incorrect customer_id")

        # Check VK team references
        team_ids = {team["id"] for team in teams}
        for vk in vks:
            if vk.get("team_id") and vk["team_id"] not in team_ids:
                errors.append(f"VK {vk['id']} references non-existent team")

        return errors


class TestScenarioBuilder:
    """Builder for complex test scenarios"""

    def __init__(self, client: GovernanceTestClient, cleanup_tracker):
        self.client = client
        self.cleanup_tracker = cleanup_tracker
        self.created_entities = {"customers": [], "teams": [], "virtual_keys": []}

    def create_customer(self, **kwargs) -> Dict[str, Any]:
        """Create a customer with automatic cleanup tracking"""
        data = TestDataFactory.generate_customer_data(**kwargs)
        response = self.client.create_customer(data)
        assert_response_success(response, 201)

        customer = response.json()["customer"]
        self.cleanup_tracker.add_customer(customer["id"])
        self.created_entities["customers"].append(customer)
        return customer

    def create_team(
        self, customer_id: Optional[str] = None, **kwargs
    ) -> Dict[str, Any]:
        """Create a team with automatic cleanup tracking"""
        data = TestDataFactory.generate_team_data(customer_id=customer_id, **kwargs)
        response = self.client.create_team(data)
        assert_response_success(response, 201)

        team = response.json()["team"]
        self.cleanup_tracker.add_team(team["id"])
        self.created_entities["teams"].append(team)
        return team

    def create_virtual_key(
        self, team_id: Optional[str] = None, customer_id: Optional[str] = None, **kwargs
    ) -> Dict[str, Any]:
        """Create a virtual key with automatic cleanup tracking"""
        data = TestDataFactory.generate_virtual_key_data(
            team_id=team_id, customer_id=customer_id, **kwargs
        )
        response = self.client.create_virtual_key(data)
        assert_response_success(response, 201)

        vk = response.json()["virtual_key"]
        self.cleanup_tracker.add_virtual_key(vk["id"])
        self.created_entities["virtual_keys"].append(vk)
        return vk

    def create_simple_hierarchy(self) -> Dict[str, Any]:
        """Create a simple Customer -> Team -> VK hierarchy"""
        customer = self.create_customer(include_budget=True)
        team = self.create_team(customer_id=customer["id"], include_budget=True)
        vk = self.create_virtual_key(
            team_id=team["id"], include_budget=True, include_rate_limit=True
        )

        return {"customer": customer, "team": team, "virtual_key": vk}

    def create_complex_hierarchy(
        self, team_count: int = 3, vk_per_team: int = 2
    ) -> Dict[str, Any]:
        """Create a complex hierarchy with multiple teams and VKs"""
        customer = self.create_customer(include_budget=True)

        teams = []
        for i in range(team_count):
            team = self.create_team(customer_id=customer["id"], include_budget=True)
            teams.append(team)

        vks = []
        for team in teams:
            for j in range(vk_per_team):
                vk = self.create_virtual_key(
                    team_id=team["id"],
                    include_budget=True,
                    include_rate_limit=True,
                    model_restrictions=random.choice([True, False]),
                )
                vks.append(vk)

        return {"customer": customer, "teams": teams, "virtual_keys": vks}

    def create_mixed_vk_associations(self) -> Dict[str, Any]:
        """Create VKs with mixed team/customer associations"""
        customer = self.create_customer(include_budget=True)
        team = self.create_team(customer_id=customer["id"], include_budget=True)

        # VK directly associated with customer
        customer_vk = self.create_virtual_key(
            customer_id=customer["id"], include_budget=True
        )

        # VK associated with team (indirect customer association)
        team_vk = self.create_virtual_key(team_id=team["id"], include_budget=True)

        # Standalone VK
        standalone_vk = self.create_virtual_key(
            include_budget=True, include_rate_limit=True
        )

        return {
            "customer": customer,
            "team": team,
            "customer_vk": customer_vk,
            "team_vk": team_vk,
            "standalone_vk": standalone_vk,
        }


class PerformanceTracker:
    """Track performance metrics during tests"""

    def __init__(self):
        self.measurements = []

    def time_operation(self, operation_name: str, operation_func, *args, **kwargs):
        """Time an operation and record the measurement"""
        start_time = time.time()
        try:
            result = operation_func(*args, **kwargs)
            success = True
            error = None
        except Exception as e:
            result = None
            success = False
            error = str(e)

        end_time = time.time()
        duration = end_time - start_time

        measurement = {
            "operation": operation_name,
            "duration": duration,
            "success": success,
            "error": error,
            "timestamp": datetime.now().isoformat(),
        }

        self.measurements.append(measurement)
        return result, measurement

    def get_stats(self) -> Dict[str, Any]:
        """Get performance statistics"""
        if not self.measurements:
            return {"count": 0}

        durations = [m["duration"] for m in self.measurements]
        successes = [m for m in self.measurements if m["success"]]
        failures = [m for m in self.measurements if not m["success"]]

        return {
            "count": len(self.measurements),
            "success_count": len(successes),
            "failure_count": len(failures),
            "success_rate": len(successes) / len(self.measurements),
            "avg_duration": sum(durations) / len(durations),
            "min_duration": min(durations),
            "max_duration": max(durations),
            "total_duration": sum(durations),
        }

    def print_report(self):
        """Print performance report"""
        stats = self.get_stats()
        if stats["count"] == 0:
            print("No measurements recorded")
            return

        print(f"\nPerformance Report:")
        print(f"  Total operations: {stats['count']}")
        print(f"  Success rate: {stats['success_rate']:.2%}")
        print(f"  Average duration: {stats['avg_duration']:.3f}s")
        print(f"  Min duration: {stats['min_duration']:.3f}s")
        print(f"  Max duration: {stats['max_duration']:.3f}s")
        print(f"  Total duration: {stats['total_duration']:.3f}s")


class ChatCompletionHelper:
    """Helper for chat completion testing"""

    @staticmethod
    def generate_test_messages(
        complexity: str = "simple", token_count_estimate: int = None
    ) -> List[Dict[str, str]]:
        """Generate test messages of varying complexity"""
        if complexity == "simple":
            return [{"role": "user", "content": "Hello, how are you?"}]

        elif complexity == "medium":
            return [
                {"role": "user", "content": "Can you explain quantum computing?"},
                {
                    "role": "assistant",
                    "content": "Quantum computing is a type of computation that harnesses quantum mechanics...",
                },
                {
                    "role": "user",
                    "content": "How does it differ from classical computing?",
                },
            ]

        elif complexity == "complex":
            content = fake.text(max_nb_chars=2000)
            return [
                {"role": "system", "content": "You are a helpful AI assistant."},
                {"role": "user", "content": content},
                {
                    "role": "assistant",
                    "content": "I understand. Let me help you with that.",
                },
                {"role": "user", "content": "Please provide a detailed analysis."},
            ]

        elif complexity == "custom" and token_count_estimate:
            # Rough estimate: 4 characters per token
            char_count = token_count_estimate * 4
            content = fake.text(max_nb_chars=char_count)
            return [{"role": "user", "content": content}]

        else:
            return [{"role": "user", "content": fake.sentence()}]

    @staticmethod
    def make_test_request(
        client: GovernanceTestClient,
        vk_value: str,
        model: str = "gpt-3.5-turbo",
        max_tokens: int = 50,
        **kwargs,
    ) -> Dict[str, Any]:
        """Make a standardized test chat completion request"""
        messages = (
            kwargs.get("messages") or ChatCompletionHelper.generate_test_messages()
        )
        headers = {"x-bf-vk": vk_value}

        response = client.chat_completion(
            messages=messages,
            model=model,
            headers=headers,
            max_tokens=max_tokens,
            **{k: v for k, v in kwargs.items() if k != "messages"},
        )

        return {
            "response": response,
            "status_code": response.status_code,
            "success": response.status_code == 200,
            "rate_limited": response.status_code == 429,
            "budget_exceeded": response.status_code == 402,
            "unauthorized": response.status_code in [401, 403],
            "data": (
                response.json()
                if response.headers.get("content-type", "").startswith(
                    "application/json"
                )
                else response.text
            ),
        }


# Pytest fixtures for helpers


@pytest.fixture
def test_data_factory():
    """Test data factory fixture"""
    return TestDataFactory()


@pytest.fixture
def validation_helper():
    """Validation helper fixture"""
    return ValidationHelper()


@pytest.fixture
def scenario_builder(governance_client, cleanup_tracker):
    """Test scenario builder fixture"""
    return TestScenarioBuilder(governance_client, cleanup_tracker)


@pytest.fixture
def performance_tracker():
    """Performance tracker fixture"""
    return PerformanceTracker()


@pytest.fixture
def chat_completion_helper():
    """Chat completion helper fixture"""
    return ChatCompletionHelper()


# Test helper usage examples
class TestHelperExamples:
    """Examples of how to use the test helpers"""

    @pytest.mark.helpers
    def test_data_factory_usage(
        self, test_data_factory, governance_client, cleanup_tracker
    ):
        """Example of using TestDataFactory"""
        # Generate and create customer
        customer_data = test_data_factory.generate_customer_data(include_budget=True)
        customer_response = governance_client.create_customer(customer_data)
        assert_response_success(customer_response, 201)
        customer = customer_response.json()["customer"]
        cleanup_tracker.add_customer(customer["id"])

        # Verify data structure
        assert customer["name"].endswith("Customer")
        assert customer["budget"] is not None

    @pytest.mark.helpers
    def test_scenario_builder_usage(self, scenario_builder):
        """Example of using TestScenarioBuilder"""
        # Create simple hierarchy
        hierarchy = scenario_builder.create_simple_hierarchy()

        # Verify hierarchy structure
        assert hierarchy["customer"]["id"] is not None
        assert hierarchy["team"]["customer_id"] == hierarchy["customer"]["id"]
        assert hierarchy["virtual_key"]["team_id"] == hierarchy["team"]["id"]

    @pytest.mark.helpers
    def test_validation_helper_usage(self, validation_helper, sample_virtual_key):
        """Example of using ValidationHelper"""
        # Validate VK structure
        errors = validation_helper.validate_entity_structure(
            sample_virtual_key, "virtual_key"
        )
        assert len(errors) == 0, f"VK validation errors: {errors}"

        # Validate budget if present
        if sample_virtual_key.get("budget"):
            budget_errors = validation_helper.validate_budget_structure(
                sample_virtual_key["budget"]
            )
            assert len(budget_errors) == 0, f"Budget validation errors: {budget_errors}"

    @pytest.mark.helpers
    def test_performance_tracker_usage(self, performance_tracker, governance_client):
        """Example of using PerformanceTracker"""
        # Time an operation
        result, measurement = performance_tracker.time_operation(
            "list_customers", governance_client.list_customers
        )

        assert measurement["success"] is True
        assert measurement["duration"] > 0

        # Get performance stats
        stats = performance_tracker.get_stats()
        assert stats["count"] == 1
        assert stats["success_rate"] == 1.0

    @pytest.mark.helpers
    def test_chat_completion_helper_usage(
        self, chat_completion_helper, governance_client, sample_virtual_key
    ):
        """Example of using ChatCompletionHelper"""
        # Generate test messages
        simple_messages = chat_completion_helper.generate_test_messages("simple")
        assert len(simple_messages) == 1
        assert simple_messages[0]["role"] == "user"

        # Make test request
        result = chat_completion_helper.make_test_request(
            governance_client, sample_virtual_key["value"], max_tokens=10
        )

        assert "status_code" in result
        assert "success" in result
        assert isinstance(result["success"], bool)
