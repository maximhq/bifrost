#!/usr/bin/env python3
"""
Dedicated test runner for Speech and Transcription functionality.
This script runs only the speech and transcription tests for easier development and debugging.

Usage:
    python test_audio.py
    python test_audio.py --verbose
    python test_audio.py --help
"""

import sys
import os
import argparse
import subprocess
from pathlib import Path

# Add the tests directory to Python path
tests_dir = Path(__file__).parent
sys.path.insert(0, str(tests_dir))


def run_speech_transcription_tests(verbose=False, specific_test=None):
    """Run speech and transcription tests"""

    # Change to the tests directory
    os.chdir(tests_dir)

    # Build pytest command
    cmd = ["python", "-m", "pytest"]

    if verbose:
        cmd.append("-v")
    else:
        cmd.append("-q")

    # Add specific test pattern for speech/transcription tests
    if specific_test:
        test_pattern = f"tests/integrations/test_openai.py::{specific_test}"
    else:
        # Run all speech and transcription related tests
        test_pattern = "tests/integrations/test_openai.py::TestOpenAIIntegration::test_14_speech_synthesis"
        cmd.extend(
            [
                "tests/integrations/test_openai.py::TestOpenAIIntegration::test_14_speech_synthesis",
                "tests/integrations/test_openai.py::TestOpenAIIntegration::test_15_transcription_audio",
                "tests/integrations/test_openai.py::TestOpenAIIntegration::test_16_transcription_streaming",
                "tests/integrations/test_openai.py::TestOpenAIIntegration::test_17_speech_transcription_round_trip",
                "tests/integrations/test_openai.py::TestOpenAIIntegration::test_18_speech_error_handling",
                "tests/integrations/test_openai.py::TestOpenAIIntegration::test_19_transcription_error_handling",
                "tests/integrations/test_openai.py::TestOpenAIIntegration::test_20_speech_different_voices_and_formats",
            ]
        )

    if not specific_test:
        # Add some useful pytest options
        cmd.extend(
            [
                "--tb=short",  # Shorter traceback format
                "--maxfail=3",  # Stop after 3 failures
                "-x",  # Stop on first failure
            ]
        )
    else:
        cmd.append(test_pattern)

    # Add environment info
    print("🎵 SPEECH & TRANSCRIPTION INTEGRATION TESTS")
    print("=" * 60)
    print(f"🔧 Running from: {tests_dir}")
    print(f"📋 Environment variables needed:")
    print("   - OPENAI_API_KEY (required)")
    print("   - BIFROST_BASE_URL (optional, defaults to http://localhost:8080)")
    print()

    # Check for required environment variables
    if not os.getenv("OPENAI_API_KEY"):
        print("❌ ERROR: OPENAI_API_KEY environment variable is required")
        print("   Set it with: export OPENAI_API_KEY=your_key_here")
        return 1

    bifrost_url = os.getenv("BIFROST_BASE_URL", "http://localhost:8080")
    print(f"🌉 Bifrost URL: {bifrost_url}")
    print(f"🤖 Testing OpenAI integration through Bifrost proxy")
    print()

    # Run the tests
    print("🚀 Starting Speech & Transcription Tests...")
    print("-" * 60)

    try:
        result = subprocess.run(cmd, cwd=tests_dir)
        return result.returncode
    except KeyboardInterrupt:
        print("\n❌ Tests interrupted by user")
        return 1
    except Exception as e:
        print(f"\n❌ Error running tests: {e}")
        return 1


def list_available_tests():
    """List all available speech and transcription tests"""
    tests = [
        "test_14_speech_synthesis",
        "test_15_transcription_audio",
        "test_16_transcription_streaming",
        "test_17_speech_transcription_round_trip",
        "test_18_speech_error_handling",
        "test_19_transcription_error_handling",
        "test_20_speech_different_voices_and_formats",
    ]

    print("🎵 Available Speech & Transcription Tests:")
    print("=" * 50)
    for i, test in enumerate(tests, 1):
        print(f"{i:2d}. {test}")
    print()
    print("Run specific test with: python test_audio.py --test <test_name>")


def main():
    parser = argparse.ArgumentParser(
        description="Run Speech and Transcription integration tests",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
    python test_audio.py                     # Run all speech/transcription tests
    python test_audio.py --verbose           # Run with verbose output
    python test_audio.py --list              # List available tests
    python test_audio.py --test test_14_speech_synthesis  # Run specific test
        """,
    )

    parser.add_argument(
        "--verbose", "-v", action="store_true", help="Enable verbose output"
    )

    parser.add_argument("--test", "-t", type=str, help="Run a specific test by name")

    parser.add_argument(
        "--list", "-l", action="store_true", help="List available tests"
    )

    args = parser.parse_args()

    if args.list:
        list_available_tests()
        return 0

    return run_speech_transcription_tests(verbose=args.verbose, specific_test=args.test)


if __name__ == "__main__":
    sys.exit(main())
