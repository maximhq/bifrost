# Bifrost Governance Plugin Test Suite

A comprehensive test suite for the Bifrost Governance Plugin, testing hierarchical governance, budgets, rate limiting, usage tracking, and CRUD operations.

## Overview

This test suite provides extensive coverage of the Bifrost governance system including:

- **Virtual Key Management**: Complete CRUD operations with comprehensive field update testing
- **Team Management**: Team CRUD with customer relationships and budget inheritance
- **Customer Management**: Customer CRUD with team hierarchies and budget controls
- **Usage Tracking**: Real-time usage monitoring and audit logging
- **Rate Limiting**: Flexible token and request rate limiting with configurable reset periods
- **Budget Enforcement**: Hierarchical budget controls (Customer → Team → Virtual Key)
- **Integration Testing**: End-to-end testing with chat completion API
- **Edge Cases**: Boundary conditions, concurrency, and error scenarios

## Test Structure

### Test Files

1. **`test_virtual_keys_crud.py`** - Virtual Key CRUD operations
   - Complete CRUD lifecycle testing
   - Comprehensive field update testing (individual and batch)
   - Mutual exclusivity validation (team_id vs customer_id)
   - Budget and rate limit management
   - Relationship testing with teams and customers

2. **`test_teams_crud.py`** - Team CRUD operations
   - Team lifecycle management
   - Customer association testing
   - Budget inheritance and conflicts
   - Comprehensive field updates
   - Filtering and relationships

3. **`test_customers_crud.py`** - Customer CRUD operations
   - Customer lifecycle management
   - Team relationship management
   - Budget management and hierarchies
   - Comprehensive field updates
   - Cascading operations

4. **`test_usage_tracking.py`** - Usage tracking and monitoring
   - Chat completion integration with governance headers
   - Usage tracking and budget enforcement
   - Rate limiting enforcement
   - Monitoring endpoints
   - Reset functionality
   - Debug and health endpoints

### Configuration Files

- **`conftest.py`** - Test fixtures, utilities, and configuration
- **`pytest.ini`** - pytest configuration with markers and settings
- **`requirements.txt`** - Test dependencies
- **`__init__.py`** - Package initialization

## Key Features

### Comprehensive Field Update Testing

Each entity (Virtual Key, Team, Customer) has exhaustive field update tests that verify:

- **Individual field updates** - Each field updated independently
- **Unchanged field verification** - Other fields remain unmodified
- **Relationship preservation** - Associated data maintained correctly
- **Timestamp validation** - updated_at changes, created_at preserved
- **Multiple field updates** - Batch field modifications
- **Nested object updates** - Budget and rate limit sub-objects
- **Edge cases** - Empty updates, null values, invalid data

### Mutual Exclusivity Testing

Critical validation of Virtual Key constraints:
- VK can have `team_id` OR `customer_id`, but NEVER both
- Switching between team and customer associations
- Validation error scenarios for invalid combinations

### Hierarchical Testing

Testing the Customer → Team → Virtual Key hierarchy:
- Budget inheritance and override scenarios
- Rate limit cascading and conflicts
- Usage tracking across hierarchy levels
- Permission and access control validation

### Integration Testing

End-to-end testing with actual chat completion requests:
- Governance header validation (`x-bf-vk`)
- Usage tracking during real requests
- Budget enforcement during streaming
- Rate limiting during concurrent requests
- Provider and model access control

## Setup and Usage

### Prerequisites

1. **Bifrost Server Running**: The governance plugin must be running on `localhost:8080`
2. **Python 3.8+**: Required for the test suite
3. **Dependencies**: Install via `pip install -r requirements.txt`

### Environment Configuration

Set the following environment variables (optional):

```bash
export BIFROST_BASE_URL="http://localhost:8080"  # Default
export GOVERNANCE_TEST_TIMEOUT="300"             # Test timeout in seconds
export GOVERNANCE_TEST_CLEANUP="true"            # Auto-cleanup entities
```

### Running Tests

```bash
# Install dependencies
pip install -r requirements.txt

# Run all governance tests
pytest

# Run specific test files
pytest test_virtual_keys_crud.py
pytest test_teams_crud.py
pytest test_customers_crud.py
pytest test_usage_tracking.py

# Run with specific markers
pytest -m "virtual_keys"
pytest -m "field_updates"
pytest -m "edge_cases"
pytest -m "integration"

# Run with coverage
pytest --cov=. --cov-report=html

# Run in parallel
pytest -n auto

# Run with verbose output
pytest -v

# Run smoke tests only
pytest -m "smoke"
```

### Test Markers

The test suite uses pytest markers for categorization:

- `@pytest.mark.virtual_keys` - Virtual Key related tests
- `@pytest.mark.teams` - Team related tests
- `@pytest.mark.customers` - Customer related tests
- `@pytest.mark.field_updates` - Comprehensive field update tests
- `@pytest.mark.mutual_exclusivity` - Mutual exclusivity constraint tests
- `@pytest.mark.budget` - Budget related tests
- `@pytest.mark.rate_limit` - Rate limiting tests
- `@pytest.mark.usage_tracking` - Usage tracking tests
- `@pytest.mark.integration` - Integration tests
- `@pytest.mark.edge_cases` - Edge case tests
- `@pytest.mark.concurrency` - Concurrency tests
- `@pytest.mark.slow` - Slow running tests (>5s)
- `@pytest.mark.smoke` - Quick smoke tests

## API Endpoints Tested

### Virtual Key Endpoints
- `GET /api/governance/virtual-keys` - List all VKs with relationships
- `POST /api/governance/virtual-keys` - Create VK with optional budget/rate limits
- `GET /api/governance/virtual-keys/{vk_id}` - Get specific VK
- `PUT /api/governance/virtual-keys/{vk_id}` - Update VK
- `DELETE /api/governance/virtual-keys/{vk_id}` - Delete VK

### Team Endpoints
- `GET /api/governance/teams` - List teams with optional customer filter
- `POST /api/governance/teams` - Create team with optional customer/budget
- `GET /api/governance/teams/{team_id}` - Get specific team
- `PUT /api/governance/teams/{team_id}` - Update team
- `DELETE /api/governance/teams/{team_id}` - Delete team

### Customer Endpoints
- `GET /api/governance/customers` - List customers with teams/budgets
- `POST /api/governance/customers` - Create customer with optional budget
- `GET /api/governance/customers/{customer_id}` - Get specific customer
- `PUT /api/governance/customers/{customer_id}` - Update customer
- `DELETE /api/governance/customers/{customer_id}` - Delete customer

### Monitoring Endpoints
- `GET /api/governance/usage-stats` - Usage statistics with optional VK filter
- `POST /api/governance/usage-reset` - Reset VK usage counters
- `GET /api/governance/debug/stats` - Debug statistics
- `GET /api/governance/debug/counters` - All VK usage counters
- `GET /api/governance/debug/health` - Health check

### Integration Endpoints
- `POST /v1/chat/completions` - Chat completion with governance headers

## Test Data and Schemas

### Virtual Key Request Schema
```json
{
  "name": "string (required)",
  "description": "string (optional)",
  "allowed_models": ["string"] (optional),
  "allowed_providers": ["string"] (optional),
  "team_id": "string (optional, mutually exclusive with customer_id)",
  "customer_id": "string (optional, mutually exclusive with team_id)",
  "budget": {
    "max_limit": "integer (cents)",
    "reset_duration": "string (e.g., '1h', '1d')"
  },
  "rate_limit": {
    "token_max_limit": "integer (optional)",
    "token_reset_duration": "string (optional)",
    "request_max_limit": "integer (optional)", 
    "request_reset_duration": "string (optional)"
  },
  "is_active": "boolean (optional, default true)"
}
```

### Team Request Schema
```json
{
  "name": "string (required)",
  "customer_id": "string (optional)",
  "budget": {
    "max_limit": "integer (cents)",
    "reset_duration": "string"
  }
}
```

### Customer Request Schema
```json
{
  "name": "string (required)",
  "budget": {
    "max_limit": "integer (cents)",
    "reset_duration": "string"
  }
}
```

## Edge Cases Covered

### Budget Edge Cases
- Boundary values: 0, negative, max int64, overflow
- Reset timing: exact boundaries, concurrent resets
- Hierarchical conflicts: VK vs Team vs Customer budgets
- Fractional costs: proper cents handling
- Concurrent usage: multiple requests hitting limits
- Reset during flight: budget resets while processing
- Streaming cost tracking: partial vs final costs

### Rate Limiting Edge Cases
- Independent limits: token vs request limits with different resets
- Sub-second precision: very short reset durations
- Burst scenarios: simultaneous requests
- Provider variations: different limits per provider/model
- Streaming rate limits: token counting across chunks
- Reset race conditions: limits resetting during validation

### Relationship Edge Cases
- Orphaned entities: VKs without parent relationships
- Invalid references: team_id pointing to non-existent team
- Mutual exclusivity: VK with both team_id and customer_id (MUST FAIL)
- Circular dependencies: prevention testing
- Deep hierarchies: Customer → Team → VK inheritance

### Update Edge Cases
- Partial updates: only some fields updated
- Null handling: null values clearing optional fields
- Type validation: wrong data types in requests
- Concurrent updates: multiple clients updating same entity
- Cache invalidation: in-memory cache updates after DB changes
- Rollback scenarios: failed updates don't leave partial changes

### Integration Edge Cases
- Missing headers: requests without x-bf-vk header
- Invalid headers: malformed or non-existent VK values
- Provider/model validation: invalid combinations
- Error propagation: governance vs completion errors
- Streaming interruption: governance blocking mid-stream
- Context preservation: headers passed through request lifecycle

## Utilities and Helpers

### Test Fixtures
- `governance_client` - API client for governance endpoints
- `cleanup_tracker` - Automatic entity cleanup after tests
- `sample_customer` - Pre-created customer for testing
- `sample_team` - Pre-created team for testing
- `sample_virtual_key` - Pre-created virtual key for testing
- `field_update_tester` - Helper for comprehensive field update testing

### Utility Functions
- `generate_unique_name()` - Generate unique test entity names
- `wait_for_condition()` - Wait for async conditions
- `assert_response_success()` - Assert HTTP response success
- `deep_compare_entities()` - Deep comparison of entity data
- `verify_unchanged_fields()` - Verify fields remain unchanged
- `create_complete_virtual_key_data()` - Generate complete VK data

### Error Handling
- Comprehensive error assertion helpers
- Automatic retry for transient failures
- Detailed error logging and reporting
- Clean failure modes with proper cleanup

## Performance and Concurrency

### Performance Testing
- Response time benchmarks for all endpoints
- Memory usage monitoring during tests
- Database query optimization validation
- Cache performance verification

### Concurrency Testing
- Race condition detection
- Concurrent entity creation/updates
- Simultaneous budget usage scenarios
- Rate limit burst testing
- Cache consistency under load

## Debugging and Monitoring

### Test Logging
- Comprehensive test execution logging
- API request/response logging
- Error details and stack traces
- Performance metrics and timing

### Debug Endpoints
- Test coverage of debug/stats endpoint
- Usage counter validation
- Health check verification
- Database state inspection

## Contributing

When adding new tests:

1. **Follow naming conventions**: `test_<feature>_<scenario>.py`
2. **Use appropriate markers**: Mark tests with relevant pytest markers
3. **Include cleanup**: Use `cleanup_tracker` fixture for entity cleanup
4. **Document edge cases**: Comment complex test scenarios
5. **Add field update tests**: For any new entity fields, add comprehensive update tests
6. **Test relationships**: Verify entity relationships and cascading effects
7. **Include negative tests**: Test validation and error scenarios

### Test Development Guidelines

1. **Comprehensive Coverage**: Test all CRUD operations, field updates, and edge cases
2. **Isolation**: Tests should be independent and not rely on other test state
3. **Cleanup**: Always clean up created entities to avoid test interference
4. **Documentation**: Comment complex test logic and expected behaviors
5. **Performance**: Mark slow tests appropriately and optimize where possible
6. **Error Scenarios**: Test both success and failure paths
7. **Relationships**: Verify entity relationships are properly maintained

## Troubleshooting

### Common Issues

1. **Server Not Running**: Ensure Bifrost server is running on localhost:8080
2. **Permission Errors**: Check that test has access to create/delete entities
3. **Cleanup Failures**: Manually clean up test entities if auto-cleanup fails
4. **Timeout Errors**: Increase timeout for slow-running tests
5. **Concurrency Issues**: Use appropriate locks for shared resource tests

### Debug Commands

```bash
# Run with maximum verbosity
pytest -vvv --tb=long

# Run single test with debugging
pytest -s test_virtual_keys_crud.py::test_vk_create_basic

# Run with profiling
pytest --profile-svg

# Check test coverage
pytest --cov=. --cov-report=term-missing
```