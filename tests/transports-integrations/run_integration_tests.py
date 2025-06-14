#!/usr/bin/env python3
"""
Integration-specific test runner for Bifrost integration tests.

This script runs tests for each integration independently using their native SDKs.
No more complex gateway conversions - just direct testing!
"""

import os
import sys
import argparse
import subprocess
from typing import List, Optional


def check_api_keys():
    """Check which API keys are available"""
    keys = {
        "openai": os.getenv("OPENAI_API_KEY"),
        "anthropic": os.getenv("ANTHROPIC_API_KEY"),
        "google": os.getenv("GOOGLE_API_KEY"),
        "litellm": os.getenv("LITELLM_API_KEY"),
    }

    available = [integration for integration, key in keys.items() if key]
    missing = [integration for integration, key in keys.items() if not key]

    return available, missing


def run_integration_tests(
    integrations: List[str], test_pattern: Optional[str] = None, verbose: bool = False
):
    """Run tests for specified integrations"""

    results = {}

    for integration in integrations:
        print(f"\n{'='*60}")
        print(f"🧪 TESTING {integration.upper()} INTEGRATION")
        print(f"{'='*60}")

        # Build pytest command
        cmd = ["python", "-m", "pytest", f"tests/integrations/test_{integration}.py"]

        if test_pattern:
            cmd.extend(["-k", test_pattern])

        if verbose:
            cmd.append("-v")
        else:
            cmd.append("-q")

        # Remove integration-specific marker (not needed for file-based selection)
        # cmd.extend(["-m", integration])

        # Run the tests
        try:
            result = subprocess.run(cmd, capture_output=True, text=True)
            results[integration] = {
                "returncode": result.returncode,
                "stdout": result.stdout,
                "stderr": result.stderr,
            }

            # Print results
            if result.returncode == 0:
                print(f"✅ {integration.upper()} tests PASSED")
            else:
                print(f"❌ {integration.upper()} tests FAILED")

            if verbose or result.returncode != 0:
                print(result.stdout)
                if result.stderr:
                    print("STDERR:", result.stderr)

        except Exception as e:
            print(f"❌ Error running {integration} tests: {e}")
            results[integration] = {"error": str(e)}

    return results


def print_summary(
    results: dict, available_integrations: List[str], missing_integrations: List[str]
):
    """Print final summary"""
    print(f"\n{'='*80}")
    print("🎯 FINAL SUMMARY")
    print(f"{'='*80}")

    # API Key Status
    print(f"\n🔑 API Key Status:")
    for integration in available_integrations:
        print(f"  ✅ {integration.upper()}: Available")

    for integration in missing_integrations:
        print(f"  ❌ {integration.upper()}: Missing API key")

    # Test Results
    print(f"\n📊 Test Results:")
    passed_integrations = []
    failed_integrations = []

    for integration, result in results.items():
        if "error" in result:
            print(f"  💥 {integration.upper()}: Error - {result['error']}")
            failed_integrations.append(integration)
        elif result["returncode"] == 0:
            print(f"  ✅ {integration.upper()}: All tests passed")
            passed_integrations.append(integration)
        else:
            print(f"  ❌ {integration.upper()}: Some tests failed")
            failed_integrations.append(integration)

    # Overall Status
    total_tested = len(results)
    total_passed = len(passed_integrations)

    print(f"\n🏆 Overall Results:")
    print(f"  Integrations tested: {total_tested}")
    print(f"  Integrations passed: {total_passed}")
    print(
        f"  Success rate: {(total_passed/total_tested)*100:.1f}%"
        if total_tested > 0
        else "  Success rate: N/A"
    )

    if failed_integrations:
        print(f"\n⚠️  Failed integrations: {', '.join(failed_integrations)}")
        print("   Check the detailed output above for specific test failures.")


def main():
    parser = argparse.ArgumentParser(
        description="Run integration-specific integration tests"
    )
    parser.add_argument(
        "--integrations",
        nargs="+",
        choices=["openai", "anthropic", "google", "litellm", "all"],
        default=["all"],
        help="Integrations to test (default: all available)",
    )
    parser.add_argument(
        "--test", help="Run specific test pattern (e.g., 'test_01_simple_chat')"
    )
    parser.add_argument("-v", "--verbose", action="store_true", help="Verbose output")
    parser.add_argument(
        "--check-keys", action="store_true", help="Only check API key availability"
    )
    parser.add_argument(
        "--show-models",
        action="store_true",
        help="Show model configuration for all integrations",
    )

    args = parser.parse_args()

    # Check API keys
    available_integrations, missing_integrations = check_api_keys()

    if args.check_keys:
        print("🔑 API Key Status:")
        for integration in available_integrations:
            print(f"  ✅ {integration.upper()}: Available")
        for integration in missing_integrations:
            print(f"  ❌ {integration.upper()}: Missing")
        return

    if args.show_models:
        # Import and show model configuration
        from tests.utils.models import print_model_summary

        print_model_summary()
        return

    # Determine which integrations to test
    if "all" in args.integrations:
        integrations_to_test = available_integrations
    else:
        integrations_to_test = [
            p for p in args.integrations if p in available_integrations
        ]

    if not integrations_to_test:
        print("❌ No integrations available for testing. Please set API keys.")
        print("\nRequired environment variables:")
        print("  - OPENAI_API_KEY")
        print("  - ANTHROPIC_API_KEY")
        print("  - GOOGLE_API_KEY")
        print("  - LITELLM_API_KEY")
        sys.exit(1)

    # Show what we're about to test
    print("🚀 Starting integration tests...")
    print(f"📋 Testing integrations: {', '.join(integrations_to_test)}")
    if missing_integrations:
        print(
            f"⏭️  Skipping integrations (no API key): {', '.join(missing_integrations)}"
        )

    # Run tests
    results = run_integration_tests(integrations_to_test, args.test, args.verbose)

    # Print summary
    print_summary(results, available_integrations, missing_integrations)

    # Exit with appropriate code
    failed_count = sum(
        1 for r in results.values() if r.get("returncode", 1) != 0 or "error" in r
    )
    sys.exit(failed_count)


if __name__ == "__main__":
    main()
