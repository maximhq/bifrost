"""
Pytest configuration for integration-specific tests.
"""

import pytest
import os
import logging


def pytest_configure(config):
    """Configure pytest with custom markers and logging"""
    # Configure logging
    logging.basicConfig(
        level=logging.ERROR,
        format='%(asctime)s [%(levelname)8s] %(name)s: %(message)s',
        datefmt='%Y-%m-%d %H:%M:%S'
    )
    
    # Add custom markers
    config.addinivalue_line("markers", "openai: mark test as requiring OpenAI API key")
    config.addinivalue_line(
        "markers", "anthropic: mark test as requiring Anthropic API key"
    )
    config.addinivalue_line("markers", "google: mark test as requiring Google API key")
    config.addinivalue_line("markers", "litellm: mark test as requiring LiteLLM setup")
    config.addinivalue_line("markers", "azure: Azure OpenAI integration tests")
    config.addinivalue_line(
        "markers", "flaky: mark test as flaky with automatic retries (reruns=3, reruns_delay=2)"
    )


def pytest_collection_modifyitems(config, items):
    """Modify test collection to add markers based on test file names"""
    # Add flaky marker to all tests for retry on failure
    flaky_marker = pytest.mark.flaky(reruns=3, reruns_delay=2)
    
    for item in items:
        # Add flaky marker to all tests
        item.add_marker(flaky_marker)
        
        # Add markers based on test file location
        if "test_openai" in item.nodeid:
            item.add_marker(pytest.mark.openai)
        elif "test_anthropic" in item.nodeid:
            item.add_marker(pytest.mark.anthropic)
        elif "test_google" in item.nodeid:
            item.add_marker(pytest.mark.google)
        elif "test_litellm" in item.nodeid:
            item.add_marker(pytest.mark.litellm)
        elif "test_azure" in item.nodeid:
            item.add_marker(pytest.mark.azure)


@pytest.fixture(scope="session")
def api_keys():
    """Collect all available API keys"""
    return {
        "openai": os.getenv("OPENAI_API_KEY"),
        "anthropic": os.getenv("ANTHROPIC_API_KEY"),
        "google": os.getenv("GOOGLE_API_KEY"),
        "litellm": os.getenv("LITELLM_API_KEY"),
        "azure": os.getenv("AZURE_API_KEY"),
    }


@pytest.fixture(scope="session")
def available_integrations(api_keys):
    """Determine which integrations are available based on API keys"""
    available = []

    if api_keys["openai"]:
        available.append("openai")
    if api_keys["anthropic"]:
        available.append("anthropic")
    if api_keys["google"]:
        available.append("google")
    if api_keys["litellm"]:
        available.append("litellm")
    if api_keys["azure"]:
        available.append("azure")

    return available


@pytest.fixture
def test_summary():
    """Fixture to collect test results for summary reporting"""
    results = {"passed": [], "failed": [], "skipped": []}
    return results


@pytest.hookimpl(hookwrapper=True)
def pytest_runtest_makereport(item, call):
    """Capture finalized call outcomes without counting retries as failures."""
    outcome = yield
    report = outcome.get_result()

    # pytest-rerunfailures changes unsuccessful attempts to the non-terminal
    # "rerun" outcome. Ignore those and record only the final call report.
    if report.when == "call" and report.outcome in {"passed", "failed", "skipped"}:
        # Extract integration and test info
        integration = None
        if "test_openai" in item.nodeid:
            integration = "openai"
        elif "test_anthropic" in item.nodeid:
            integration = "anthropic"
        elif "test_google" in item.nodeid:
            integration = "google"
        elif "test_litellm" in item.nodeid:
            integration = "litellm"
        elif "test_azure" in item.nodeid:
            integration = "azure"

        test_name = item.name

        # Store result info
        result_info = {
            "integration": integration,
            "test": test_name,
            "nodeid": item.nodeid,
        }

        if hasattr(item.session, "test_results_by_nodeid"):
            if report.passed:
                result_outcome = "passed"
            elif report.skipped:
                result_outcome = "skipped"
            elif report.failed:
                result_info["error"] = str(call.excinfo.value) if call.excinfo else report.longreprtext
                result_outcome = "failed"

            # Retries emit multiple reports for the same node ID. Keeping only
            # the latest report lets a successful retry replace earlier failures.
            item.session.test_results_by_nodeid[item.nodeid] = (
                result_outcome,
                result_info,
            )


def pytest_sessionstart(session):
    """Initialize test results collection"""
    session.test_results_by_nodeid = {}


def pytest_sessionfinish(session, exitstatus):
    """Print test summary at the end"""
    results = {"passed": [], "failed": [], "skipped": []}
    for result_outcome, result_info in session.test_results_by_nodeid.values():
        results[result_outcome].append(result_info)

    print("\n" + "=" * 80)
    print("INTEGRATION TEST SUMMARY")
    print("=" * 80)

    # Group results by integration
    integration_results = {}

    for result in results["passed"] + results["failed"] + results["skipped"]:
        integration = result.get("integration", "unknown")
        if integration and integration not in integration_results:
            integration_results[integration] = {"passed": 0, "failed": 0, "skipped": 0}

    for result in results["passed"]:
        integration = result.get("integration", "unknown")
        if integration and integration in integration_results:
            integration_results[integration]["passed"] += 1

    for result in results["failed"]:
        integration = result.get("integration", "unknown")
        if integration and integration in integration_results:
            integration_results[integration]["failed"] += 1

    for result in results["skipped"]:
        integration = result.get("integration", "unknown")
        if integration and integration in integration_results:
            integration_results[integration]["skipped"] += 1

    # Print summary by integration
    for integration, counts in integration_results.items():
        total = counts["passed"] + counts["failed"] + counts["skipped"]
        if total > 0:
            print(f"\n{integration.upper()} Integration:")
            print(f"  ✅ Passed: {counts['passed']}")
            print(f"  ❌ Failed: {counts['failed']}")
            print(f"  ⏭️  Skipped: {counts['skipped']}")
            print(f"  📊 Total: {total}")

            if counts["passed"] > 0:
                success_rate = (
                    (counts["passed"] / (counts["passed"] + counts["failed"])) * 100
                    if (counts["passed"] + counts["failed"]) > 0
                    else 0
                )
                print(f"  🎯 Success Rate: {success_rate:.1f}%")

    # Print failed tests details
    if results["failed"]:
        print(f"\n❌ FAILED TESTS ({len(results['failed'])}):")
        for result in results["failed"]:
            print(f"  • {result['integration']}: {result['test']}")
            if "error" in result:
                print(f"    Error: {result['error']}")

    print("\n" + "=" * 80)
