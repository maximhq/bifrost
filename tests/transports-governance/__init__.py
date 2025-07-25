"""
Bifrost Governance Plugin Test Suite

Comprehensive test suite for the Bifrost governance system covering:
- Virtual Key CRUD operations with comprehensive field updates
- Team CRUD operations with hierarchical relationships
- Customer CRUD operations with budget management
- Usage tracking and monitoring
- Rate limiting and budget enforcement
- Integration testing with chat completions
- Edge cases and validation testing
- Concurrency and race condition testing

Test Structure:
- test_virtual_keys_crud.py: Virtual Key CRUD and field update tests
- test_teams_crud.py: Team CRUD and field update tests
- test_customers_crud.py: Customer CRUD and field update tests
- test_usage_tracking.py: Usage tracking, monitoring, and integration tests
- conftest.py: Test fixtures and utilities

Key Features:
- Comprehensive field update testing for all entities
- Mutual exclusivity validation (VK team_id vs customer_id)
- Hierarchical budget and rate limit testing
- Automatic test entity cleanup
- Concurrent testing support
- Edge case and boundary condition coverage
"""

__version__ = "1.0.0"
__author__ = "Bifrost Team"
