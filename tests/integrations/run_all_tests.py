#!/usr/bin/env python3
"""
Bifrost Integration End-to-End Test Runner

This script runs all integration end-to-end tests for Bifrost.
It can run tests individually or all together, providing comprehensive
reporting and flexible execution options.

Usage:
    python run_all_tests.py                    # Run all tests
    python run_all_tests.py --integration openai  # Run specific integration
    python run_all_tests.py --list             # List available integrations
    python run_all_tests.py --parallel         # Run tests in parallel
    python run_all_tests.py --verbose          # Verbose output
"""

import argparse
import subprocess
import sys
import time
import os
from pathlib import Path
from typing import List, Dict, Optional
import concurrent.futures
from dotenv import load_dotenv

# Load environment variables
load_dotenv()


class BifrostTestRunner:
    """Main test runner for Bifrost integration tests"""

    def __init__(self):
        self.test_dir = Path(__file__).parent
        self.integrations = {
            "openai": {
                "file": "tests/integrations/test_openai.py",
                "description": "OpenAI Python SDK integration tests",
                "env_vars": ["OPENAI_API_KEY"],
            },
            "anthropic": {
                "file": "tests/integrations/test_anthropic.py",
                "description": "Anthropic Python SDK integration tests",
                "env_vars": ["ANTHROPIC_API_KEY"],
            },
            "litellm": {
                "file": "tests/integrations/test_litellm.py",
                "description": "LiteLLM integration tests",
                "env_vars": ["OPENAI_API_KEY"],  # LiteLLM can use OpenAI key
            },
            "langchain": {
                "file": "tests/integrations/test_langchain.py",
                "description": "LangChain integration tests",
                "env_vars": [
                    "OPENAI_API_KEY",
                    "ANTHROPIC_API_KEY",
                ],  # LangChain uses multiple providers
            },
            "google": {
                "file": "tests/integrations/test_google.py",
                "description": "Google GenAI integration tests",
                "env_vars": ["GOOGLE_API_KEY"],
            },
            "bedrock": {
                "file": "tests/integrations/test_bedrock.py",
                "description": "Bedrock integration tests",
                "env_vars": ["AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"],
            },
        }

        self.results = {}

    def check_environment(self, integration: str) -> bool:
        """Check if required environment variables are set for an integration"""
        config = self.integrations[integration]
        missing_vars = []

        for var in config["env_vars"]:
            if not os.getenv(var):
                missing_vars.append(var)

        if missing_vars:
            print(
                f"⚠ Skipping {integration}: Missing environment variables: {', '.join(missing_vars)}"
            )
            return False

        return True

    def run_integration_test(self, integration: str, verbose: bool = False) -> Dict:
        """Run tests for a specific integration"""
        if integration not in self.integrations:
            return {"success": False, "error": f"Unknown integration: {integration}"}

        config = self.integrations[integration]
        test_file = self.test_dir / config["file"]

        if not test_file.exists():
            return {"success": False, "error": f"Test file not found: {test_file}"}

        # Check environment variables
        if not self.check_environment(integration):
            return {
                "success": False,
                "error": "Missing required environment variables",
                "skipped": True,
            }

        print(f"\n{'='*60}")
        print(f"Running {integration.upper()} Integration Tests")
        print(f"{'='*60}")
        print(f"Description: {config['description']}")
        print(f"Test file: {config['file']}")

        start_time = time.time()

        try:
            # Run the test with pytest
            cmd = [sys.executable, "-m", "pytest", str(test_file)]

            # Add pytest flags for better output
            if verbose:
                cmd.extend(["-v", "-s"])  # verbose and don't capture output
            else:
                cmd.append("-q")  # quiet mode

            if verbose:
                result = subprocess.run(
                    cmd, cwd=self.test_dir, text=True, capture_output=False, timeout=300
                )
            else:
                result = subprocess.run(
                    cmd, cwd=self.test_dir, text=True, capture_output=True, timeout=300
                )

            elapsed_time = time.time() - start_time

            success = result.returncode == 0

            return {
                "success": success,
                "return_code": result.returncode,
                "stdout": result.stdout if not verbose else "",
                "stderr": result.stderr if not verbose else "",
                "elapsed_time": elapsed_time,
            }

        except subprocess.TimeoutExpired:
            return {
                "success": False,
                "error": "Test timed out (5 minutes)",
                "elapsed_time": 300,
            }
        except Exception as e:
            return {
                "success": False,
                "error": str(e),
                "elapsed_time": time.time() - start_time,
            }

    def run_all_tests(self, parallel: bool = False, verbose: bool = False) -> None:
        """Run all integration tests"""
        print("Bifrost Integration End-to-End Test Suite")
        print("=" * 50)
        print(f"Running tests for {len(self.integrations)} integrations")
        print(f"Parallel execution: {'Enabled' if parallel else 'Disabled'}")
        print(f"Verbose output: {'Enabled' if verbose else 'Disabled'}")

        # Check Bifrost availability
        bifrost_url = os.getenv("BIFROST_BASE_URL", "http://localhost:8080")
        print(f"Bifrost URL: {bifrost_url}")

        start_time = time.time()

        if parallel:
            self._run_parallel(verbose)
        else:
            self._run_sequential(verbose)

        total_time = time.time() - start_time
        self._print_summary(total_time)

    def _run_sequential(self, verbose: bool) -> None:
        """Run tests sequentially"""
        for integration in self.integrations:
            self.results[integration] = self.run_integration_test(integration, verbose)

    def _run_parallel(self, verbose: bool) -> None:
        """Run tests in parallel"""
        print("\nRunning tests in parallel...")

        with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:
            # Submit all tests
            future_to_integration = {
                executor.submit(
                    self.run_integration_test, integration, verbose
                ): integration
                for integration in self.integrations
            }

            # Collect results
            for future in concurrent.futures.as_completed(future_to_integration):
                integration = future_to_integration[future]
                try:
                    self.results[integration] = future.result()
                except Exception as e:
                    self.results[integration] = {"success": False, "error": str(e)}

    def _print_summary(self, total_time: float) -> None:
        """Print test summary"""
        print(f"\n{'='*60}")
        print("TEST SUMMARY")
        print(f"{'='*60}")

        passed = 0
        failed = 0
        skipped = 0

        for integration, result in self.results.items():
            status = (
                "SKIPPED"
                if result.get("skipped")
                else ("PASSED" if result["success"] else "FAILED")
            )
            elapsed = result.get("elapsed_time", 0)

            if result.get("skipped"):
                skipped += 1
                print(
                    f"⚠ {integration:12} {status:8} - {result.get('error', 'Unknown error')}"
                )
            elif result["success"]:
                passed += 1
                print(f"✓ {integration:12} {status:8} - {elapsed:.2f}s")
            else:
                failed += 1
                error_msg = result.get("error", "Unknown error")
                print(f"✗ {integration:12} {status:8} - {error_msg}")

                # Print stderr if available
                if "stderr" in result and result["stderr"]:
                    print(f"  Error output: {result['stderr'][:200]}...")

        print(f"\n{'='*60}")
        print(
            f"Total: {len(self.integrations)} | Passed: {passed} | Failed: {failed} | Skipped: {skipped}"
        )
        print(f"Total time: {total_time:.2f} seconds")
        print(f"{'='*60}")

        # Exit with appropriate code
        if failed > 0:
            sys.exit(1)
        else:
            print("All tests completed successfully!")

    def list_integrations(self) -> None:
        """List available integrations"""
        print("Available Integrations:")
        print("=" * 30)

        for integration, config in self.integrations.items():
            env_status = "✓" if self.check_environment(integration) else "✗"
            print(f"{env_status} {integration:12} - {config['description']}")
            print(f"   Required env vars: {', '.join(config['env_vars'])}")
            print()


def main():
    parser = argparse.ArgumentParser(
        description="Run Bifrost integration end-to-end tests",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python run_all_tests.py                        # Run all tests
  python run_all_tests.py --integration openai   # Run OpenAI tests only
  python run_all_tests.py --parallel --verbose   # Run all tests in parallel with verbose output
  python run_all_tests.py --list                 # List available integrations
        """,
    )

    parser.add_argument(
        "--integration", "-i", help="Run tests for specific integration only"
    )

    parser.add_argument(
        "--list",
        "-l",
        action="store_true",
        help="List available integrations and their status",
    )

    parser.add_argument(
        "--parallel",
        "-p",
        action="store_true",
        help="Run tests in parallel (faster but less readable output)",
    )

    parser.add_argument(
        "--verbose",
        "-v",
        action="store_true",
        help="Enable verbose output (shows test output in real-time)",
    )

    args = parser.parse_args()

    runner = BifrostTestRunner()

    if args.list:
        runner.list_integrations()
        return

    if args.integration:
        if args.integration not in runner.integrations:
            print(f"Error: Unknown integration '{args.integration}'")
            print(f"Available integrations: {', '.join(runner.integrations.keys())}")
            sys.exit(1)

        result = runner.run_integration_test(args.integration, args.verbose)
        if result["success"]:
            print(f"\n✓ {args.integration} tests passed!")
        else:
            error_msg = result.get("error", "Unknown error")
            print(f"\n✗ {args.integration} tests failed: {error_msg}")

            # Show stdout/stderr if available
            if result.get("stdout"):
                print("\n--- Test Output ---")
                print(result["stdout"])
            if result.get("stderr"):
                print("\n--- Error Output ---")
                print(result["stderr"])

            sys.exit(1)
    else:
        runner.run_all_tests(args.parallel, args.verbose)


if __name__ == "__main__":
    main()
