"""
Pytest configuration for Bifrost Governance Plugin testing.

Provides comprehensive setup, fixtures, and utilities for testing the
Bifrost governance system with hierarchical budgets, rate limiting,
usage tracking, and CRUD operations for Virtual Keys, Teams, and Customers.
"""

import pytest
import requests
import json
import uuid
import time
import os
from datetime import datetime, timedelta
from typing import Dict, List, Optional, Any, Tuple
from concurrent.futures import ThreadPoolExecutor
import threading
from dataclasses import dataclass
import copy


# Test Configuration
BIFROST_BASE_URL = os.getenv("BIFROST_BASE_URL", "http://localhost:8080")
GOVERNANCE_API_BASE = f"{BIFROST_BASE_URL}/api/governance"
COMPLETION_API_BASE = f"{BIFROST_BASE_URL}/v1"


def pytest_configure(config):
    """Configure pytest with custom markers for governance testing"""
    markers = [
        "governance: mark test as governance-related",
        "virtual_keys: mark test as virtual key test",
        "teams: mark test as team test",
        "customers: mark test as customer test",
        "budget: mark test as budget-related",
        "rate_limit: mark test as rate limit-related",
        "usage_tracking: mark test as usage tracking test",
        "crud: mark test as CRUD operation test",
        "field_updates: mark test as comprehensive field update test",
        "validation: mark test as validation test",
        "integration: mark test as integration test",
        "edge_cases: mark test as edge case test",
        "concurrency: mark test as concurrency test",
        "mutual_exclusivity: mark test as mutual exclusivity test",
        "hierarchical: mark test as hierarchical governance test",
        "slow: mark test as slow running (>5s)",
        "smoke: mark test as smoke test",
    ]

    for marker in markers:
        config.addinivalue_line("markers", marker)


@dataclass
class TestEntity:
    """Base class for test entities"""

    id: str
    created_at: Optional[str] = None
    updated_at: Optional[str] = None


@dataclass
class TestBudget(TestEntity):
    """Test budget entity"""

    max_limit: int = 0
    reset_duration: str = ""
    current_usage: int = 0
    last_reset: Optional[str] = None


@dataclass
class TestRateLimit(TestEntity):
    """Test rate limit entity"""

    token_max_limit: Optional[int] = None
    token_reset_duration: Optional[str] = None
    request_max_limit: Optional[int] = None
    request_reset_duration: Optional[str] = None
    token_current_usage: int = 0
    request_current_usage: int = 0
    token_last_reset: Optional[str] = None
    request_last_reset: Optional[str] = None


@dataclass
class TestCustomer(TestEntity):
    """Test customer entity"""

    name: str = ""
    budget_id: Optional[str] = None
    budget: Optional[TestBudget] = None
    teams: Optional[List["TestTeam"]] = None


@dataclass
class TestTeam(TestEntity):
    """Test team entity"""

    name: str = ""
    customer_id: Optional[str] = None
    budget_id: Optional[str] = None
    customer: Optional[TestCustomer] = None
    budget: Optional[TestBudget] = None


@dataclass
class TestVirtualKey(TestEntity):
    """Test virtual key entity"""

    name: str = ""
    value: str = ""
    description: str = ""
    allowed_models: Optional[List[str]] = None
    allowed_providers: Optional[List[str]] = None
    team_id: Optional[str] = None
    customer_id: Optional[str] = None
    budget_id: Optional[str] = None
    rate_limit_id: Optional[str] = None
    is_active: bool = True
    team: Optional[TestTeam] = None
    customer: Optional[TestCustomer] = None
    budget: Optional[TestBudget] = None
    rate_limit: Optional[TestRateLimit] = None


class GovernanceTestClient:
    """HTTP client for governance API testing with comprehensive error handling"""

    def __init__(self, base_url: str = GOVERNANCE_API_BASE):
        self.base_url = base_url
        self.session = requests.Session()
        self.session.headers.update({"Content-Type": "application/json"})

    def request(self, method: str, endpoint: str, **kwargs) -> requests.Response:
        """Make HTTP request with comprehensive error handling"""
        url = f"{self.base_url}/{endpoint.lstrip('/')}"
        try:
            response = self.session.request(method, url, **kwargs)
            return response
        except requests.exceptions.RequestException as e:
            pytest.fail(f"Request failed: {method} {url} - {str(e)}")

    # Virtual Key operations
    def list_virtual_keys(self, **params) -> requests.Response:
        """List all virtual keys"""
        return self.request("GET", "/virtual-keys", params=params)

    def create_virtual_key(self, data: Dict[str, Any]) -> requests.Response:
        """Create a virtual key"""
        return self.request("POST", "/virtual-keys", json=data)

    def get_virtual_key(self, vk_id: str) -> requests.Response:
        """Get virtual key by ID"""
        return self.request("GET", f"/virtual-keys/{vk_id}")

    def update_virtual_key(self, vk_id: str, data: Dict[str, Any]) -> requests.Response:
        """Update virtual key"""
        return self.request("PUT", f"/virtual-keys/{vk_id}", json=data)

    def delete_virtual_key(self, vk_id: str) -> requests.Response:
        """Delete virtual key"""
        return self.request("DELETE", f"/virtual-keys/{vk_id}")

    # Team operations
    def list_teams(self, **params) -> requests.Response:
        """List all teams"""
        return self.request("GET", "/teams", params=params)

    def create_team(self, data: Dict[str, Any]) -> requests.Response:
        """Create a team"""
        return self.request("POST", "/teams", json=data)

    def get_team(self, team_id: str) -> requests.Response:
        """Get team by ID"""
        return self.request("GET", f"/teams/{team_id}")

    def update_team(self, team_id: str, data: Dict[str, Any]) -> requests.Response:
        """Update team"""
        return self.request("PUT", f"/teams/{team_id}", json=data)

    def delete_team(self, team_id: str) -> requests.Response:
        """Delete team"""
        return self.request("DELETE", f"/teams/{team_id}")

    # Customer operations
    def list_customers(self, **params) -> requests.Response:
        """List all customers"""
        return self.request("GET", "/customers", params=params)

    def create_customer(self, data: Dict[str, Any]) -> requests.Response:
        """Create a customer"""
        return self.request("POST", "/customers", json=data)

    def get_customer(self, customer_id: str) -> requests.Response:
        """Get customer by ID"""
        return self.request("GET", f"/customers/{customer_id}")

    def update_customer(
        self, customer_id: str, data: Dict[str, Any]
    ) -> requests.Response:
        """Update customer"""
        return self.request("PUT", f"/customers/{customer_id}", json=data)

    def delete_customer(self, customer_id: str) -> requests.Response:
        """Delete customer"""
        return self.request("DELETE", f"/customers/{customer_id}")

    # Monitoring and usage operations
    def get_usage_stats(self, **params) -> requests.Response:
        """Get usage statistics"""
        return self.request("GET", "/usage-stats", params=params)

    def reset_usage(self, data: Dict[str, Any]) -> requests.Response:
        """Reset usage counters"""
        return self.request("POST", "/usage-reset", json=data)

    def get_debug_stats(self) -> requests.Response:
        """Get debug statistics"""
        return self.request("GET", "/debug/stats")

    def get_debug_counters(self) -> requests.Response:
        """Get debug counters"""
        return self.request("GET", "/debug/counters")

    def get_health_check(self) -> requests.Response:
        """Get health check"""
        return self.request("GET", "/debug/health")

    # Chat completion for integration testing
    def chat_completion(
        self,
        messages: List[Dict],
        model: str = "gpt-3.5-turbo",
        headers: Optional[Dict] = None,
        **kwargs,
    ) -> requests.Response:
        """Make chat completion request"""
        data = {"model": model, "messages": messages, **kwargs}

        session_headers = self.session.headers.copy()
        if headers:
            session_headers.update(headers)

        url = f"{COMPLETION_API_BASE}/chat/completions"
        try:
            response = requests.post(url, json=data, headers=session_headers)
            return response
        except requests.exceptions.RequestException as e:
            pytest.fail(f"Chat completion request failed: {url} - {str(e)}")


class CleanupTracker:
    """Tracks entities created during tests for cleanup"""

    def __init__(self):
        self.virtual_keys = []
        self.teams = []
        self.customers = []
        self._lock = threading.Lock()

    def add_virtual_key(self, vk_id: str):
        """Add virtual key for cleanup"""
        with self._lock:
            if vk_id not in self.virtual_keys:
                self.virtual_keys.append(vk_id)

    def add_team(self, team_id: str):
        """Add team for cleanup"""
        with self._lock:
            if team_id not in self.teams:
                self.teams.append(team_id)

    def add_customer(self, customer_id: str):
        """Add customer for cleanup"""
        with self._lock:
            if customer_id not in self.customers:
                self.customers.append(customer_id)

    def cleanup(self, client: GovernanceTestClient):
        """Cleanup all tracked entities"""
        with self._lock:
            # Delete in dependency order: VKs -> Teams -> Customers
            for vk_id in self.virtual_keys:
                try:
                    client.delete_virtual_key(vk_id)
                except Exception:
                    pass  # Ignore cleanup errors

            for team_id in self.teams:
                try:
                    client.delete_team(team_id)
                except Exception:
                    pass

            for customer_id in self.customers:
                try:
                    client.delete_customer(customer_id)
                except Exception:
                    pass

            # Clear lists
            self.virtual_keys.clear()
            self.teams.clear()
            self.customers.clear()


# Fixtures


@pytest.fixture(scope="session")
def governance_client():
    """Governance API client for the session"""
    return GovernanceTestClient()


@pytest.fixture
def cleanup_tracker():
    """Cleanup tracker for test entities"""
    return CleanupTracker()


@pytest.fixture(autouse=True)
def auto_cleanup(cleanup_tracker, governance_client):
    """Automatically cleanup test entities after each test"""
    yield
    cleanup_tracker.cleanup(governance_client)


@pytest.fixture
def sample_budget_data():
    """Sample budget data for testing"""
    return {"max_limit": 10000, "reset_duration": "1h"}  # $100.00 in cents


@pytest.fixture
def sample_rate_limit_data():
    """Sample rate limit data for testing"""
    return {
        "token_max_limit": 1000,
        "token_reset_duration": "1m",
        "request_max_limit": 100,
        "request_reset_duration": "1h",
    }


@pytest.fixture
def sample_customer(governance_client, cleanup_tracker):
    """Create a sample customer for testing"""
    data = {"name": f"Test Customer {uuid.uuid4().hex[:8]}"}
    response = governance_client.create_customer(data)
    assert response.status_code == 201
    customer_data = response.json()["customer"]
    cleanup_tracker.add_customer(customer_data["id"])
    return customer_data


@pytest.fixture
def sample_team(governance_client, cleanup_tracker):
    """Create a sample team for testing"""
    data = {"name": f"Test Team {uuid.uuid4().hex[:8]}"}
    response = governance_client.create_team(data)
    assert response.status_code == 201
    team_data = response.json()["team"]
    cleanup_tracker.add_team(team_data["id"])
    return team_data


@pytest.fixture
def sample_team_with_customer(governance_client, cleanup_tracker, sample_customer):
    """Create a sample team associated with a customer"""
    data = {
        "name": f"Test Team with Customer {uuid.uuid4().hex[:8]}",
        "customer_id": sample_customer["id"],
    }
    response = governance_client.create_team(data)
    assert response.status_code == 201
    team_data = response.json()["team"]
    cleanup_tracker.add_team(team_data["id"])
    return team_data


@pytest.fixture
def sample_virtual_key(governance_client, cleanup_tracker):
    """Create a sample virtual key for testing"""
    data = {"name": f"Test VK {uuid.uuid4().hex[:8]}"}
    response = governance_client.create_virtual_key(data)
    assert response.status_code == 201
    vk_data = response.json()["virtual_key"]
    cleanup_tracker.add_virtual_key(vk_data["id"])
    return vk_data


@pytest.fixture
def sample_virtual_key_with_team(governance_client, cleanup_tracker, sample_team):
    """Create a sample virtual key associated with a team"""
    data = {
        "name": f"Test VK with Team {uuid.uuid4().hex[:8]}",
        "team_id": sample_team["id"],
    }
    response = governance_client.create_virtual_key(data)
    assert response.status_code == 201
    vk_data = response.json()["virtual_key"]
    cleanup_tracker.add_virtual_key(vk_data["id"])
    return vk_data


@pytest.fixture
def sample_virtual_key_with_customer(
    governance_client, cleanup_tracker, sample_customer
):
    """Create a sample virtual key associated with a customer"""
    data = {
        "name": f"Test VK with Customer {uuid.uuid4().hex[:8]}",
        "customer_id": sample_customer["id"],
    }
    response = governance_client.create_virtual_key(data)
    assert response.status_code == 201
    vk_data = response.json()["virtual_key"]
    cleanup_tracker.add_virtual_key(vk_data["id"])
    return vk_data


# Utility functions


def generate_unique_name(prefix: str = "Test") -> str:
    """Generate a unique name for testing"""
    return f"{prefix} {uuid.uuid4().hex[:8]} {int(time.time())}"


def wait_for_condition(
    condition_func, timeout: float = 5.0, interval: float = 0.1
) -> bool:
    """Wait for a condition to be true"""
    start_time = time.time()
    while time.time() - start_time < timeout:
        if condition_func():
            return True
        time.sleep(interval)
    return False


def assert_response_success(response: requests.Response, expected_status: int = 200):
    """Assert that response is successful with expected status"""
    if response.status_code != expected_status:
        try:
            error_data = response.json()
            pytest.fail(
                f"Expected status {expected_status}, got {response.status_code}: {error_data}"
            )
        except:
            pytest.fail(
                f"Expected status {expected_status}, got {response.status_code}: {response.text}"
            )


def assert_field_unchanged(actual_value, expected_value, field_name: str):
    """Assert that a field value hasn't changed"""
    if actual_value != expected_value:
        pytest.fail(
            f"Field '{field_name}' changed unexpectedly. Expected: {expected_value}, Got: {actual_value}"
        )


def deep_compare_entities(
    entity1: Dict, entity2: Dict, ignore_fields: List[str] = None
) -> List[str]:
    """Deep compare two entities and return list of differences"""
    if ignore_fields is None:
        ignore_fields = ["updated_at", "created_at"]

    differences = []

    def compare_values(path: str, val1, val2):
        if isinstance(val1, dict) and isinstance(val2, dict):
            for key in set(val1.keys()) | set(val2.keys()):
                if key in ignore_fields:
                    continue
                new_path = f"{path}.{key}" if path else key
                if key not in val1:
                    differences.append(f"{new_path}: missing in first entity")
                elif key not in val2:
                    differences.append(f"{new_path}: missing in second entity")
                else:
                    compare_values(new_path, val1[key], val2[key])
        elif isinstance(val1, list) and isinstance(val2, list):
            if len(val1) != len(val2):
                differences.append(
                    f"{path}: list length differs ({len(val1)} vs {len(val2)})"
                )
            else:
                for i, (item1, item2) in enumerate(zip(val1, val2)):
                    compare_values(f"{path}[{i}]", item1, item2)
        elif val1 != val2:
            differences.append(f"{path}: {val1} != {val2}")

    compare_values("", entity1, entity2)
    return differences


def create_complete_virtual_key_data(
    name: str = None,
    team_id: str = None,
    customer_id: str = None,
    include_budget: bool = True,
    include_rate_limit: bool = True,
) -> Dict[str, Any]:
    """Create complete virtual key data for testing"""
    data = {
        "name": name or generate_unique_name("Complete VK"),
        "description": "Complete test virtual key with all fields",
        "allowed_models": ["gpt-4", "claude-3-5-sonnet-20240620"],
        "allowed_providers": ["openai", "anthropic"],
        "is_active": True,
    }

    if team_id:
        data["team_id"] = team_id
    elif customer_id:
        data["customer_id"] = customer_id

    if include_budget:
        data["budget"] = {
            "max_limit": 50000,  # $500.00 in cents
            "reset_duration": "1d",
        }

    if include_rate_limit:
        data["rate_limit"] = {
            "token_max_limit": 5000,
            "token_reset_duration": "1h",
            "request_max_limit": 500,
            "request_reset_duration": "1h",
        }

    return data


def verify_entity_relationships(
    entity: Dict[str, Any], expected_relationships: Dict[str, Any]
):
    """Verify that entity has expected relationship data loaded"""
    for rel_name, expected_data in expected_relationships.items():
        if expected_data is None:
            assert entity.get(rel_name) is None, f"Expected {rel_name} to be None"
        else:
            assert entity.get(rel_name) is not None, f"Expected {rel_name} to be loaded"
            if isinstance(expected_data, dict):
                for key, value in expected_data.items():
                    assert (
                        entity[rel_name].get(key) == value
                    ), f"Expected {rel_name}.{key} to be {value}"


def verify_unchanged_fields(
    updated_entity: Dict, original_entity: Dict, exclude_fields: List[str]
):
    """Verify that all fields except specified ones remain unchanged"""
    ignore_fields = ["updated_at", "created_at"] + exclude_fields

    def check_field(path: str, updated_val, original_val):
        if path in ignore_fields:
            return

        if isinstance(updated_val, dict) and isinstance(original_val, dict):
            for key in original_val.keys():
                if key not in ignore_fields:
                    new_path = f"{path}.{key}" if path else key
                    if key in updated_val:
                        check_field(new_path, updated_val[key], original_val[key])
        elif updated_val != original_val:
            pytest.fail(
                f"Field '{path}' should not have changed. Expected: {original_val}, Got: {updated_val}"
            )

    for field in original_entity.keys():
        if field not in ignore_fields:
            if field in updated_entity:
                check_field(field, updated_entity[field], original_entity[field])


class FieldUpdateTester:
    """Helper class for comprehensive field update testing"""

    def __init__(self, client: GovernanceTestClient, cleanup_tracker: CleanupTracker):
        self.client = client
        self.cleanup_tracker = cleanup_tracker

    def test_individual_field_updates(
        self, entity_type: str, entity_id: str, field_test_cases: List[Dict]
    ):
        """Test updating individual fields one by one"""

        # Get original entity state
        if entity_type == "virtual_key":
            original_response = self.client.get_virtual_key(entity_id)
            update_func = self.client.update_virtual_key
        elif entity_type == "team":
            original_response = self.client.get_team(entity_id)
            update_func = self.client.update_team
        elif entity_type == "customer":
            original_response = self.client.get_customer(entity_id)
            update_func = self.client.update_customer
        else:
            raise ValueError(f"Unknown entity type: {entity_type}")

        assert original_response.status_code == 200
        original_entity = original_response.json()[entity_type]

        for test_case in field_test_cases:
            # Reset entity to original state if needed
            if test_case.get("reset_before", True):
                self._reset_entity_state(entity_type, entity_id, original_entity)

            # Perform field update
            update_data = test_case["update_data"]
            response = update_func(entity_id, update_data)

            # Verify update succeeded
            assert (
                response.status_code == 200
            ), f"Field update failed for {test_case['field']}: {response.json()}"
            updated_entity = response.json()[entity_type]

            # Verify target field was updated
            if test_case.get("custom_validation"):
                test_case["custom_validation"](updated_entity)
            else:
                self._verify_field_updated(
                    updated_entity, test_case["field"], test_case["expected_value"]
                )

            # Verify other fields unchanged if specified
            if test_case.get("verify_unchanged", True):
                exclude_fields = test_case.get(
                    "exclude_from_unchanged_check", [test_case["field"]]
                )
                verify_unchanged_fields(updated_entity, original_entity, exclude_fields)

    def _reset_entity_state(self, entity_type: str, entity_id: str, target_state: Dict):
        """Reset entity to target state"""
        # This would require implementing a reset mechanism
        # For now, we'll rely on test isolation
        pass

    def _verify_field_updated(self, entity: Dict, field_path: str, expected_value):
        """Verify that a field was updated to expected value"""
        field_parts = field_path.split(".")
        current_value = entity

        for part in field_parts:
            if isinstance(current_value, dict):
                current_value = current_value.get(part)
            else:
                pytest.fail(f"Cannot access field '{field_path}' in entity")

        assert (
            current_value == expected_value
        ), f"Field '{field_path}' not updated correctly. Expected: {expected_value}, Got: {current_value}"


@pytest.fixture
def field_update_tester(governance_client, cleanup_tracker):
    """Field update testing helper"""
    return FieldUpdateTester(governance_client, cleanup_tracker)
