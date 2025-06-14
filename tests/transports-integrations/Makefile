# Bifrost Python E2E Test Makefile
# Provides convenient commands for running tests

.PHONY: help install test test-all test-parallel test-verbose clean lint format check-env

# Default target
help:
	@echo "Bifrost Python E2E Test Commands:"
	@echo ""
	@echo "Setup:"
	@echo "  install      Install Python dependencies"
	@echo "  check-env    Check environment variables"
	@echo ""
	@echo "Testing:"
	@echo "  test         Run all tests using master runner"
	@echo "  test-all     Run all tests with pytest"
	@echo "  test-parallel Run tests in parallel"
	@echo "  test-verbose Run tests with verbose output"
	@echo "  test-openai  Run OpenAI integration tests only"
	@echo "  test-anthropic Run Anthropic integration tests only"
	@echo "  test-litellm Run LiteLLM integration tests only"
	@echo "  test-langchain Run LangChain integration tests only"
	@echo "  test-langgraph Run LangGraph integration tests only"
	@echo "  test-mistral Run Mistral integration tests only"
	@echo "  test-genai   Run Google GenAI integration tests only"
	@echo ""
	@echo "Development:"
	@echo "  lint         Run code linting"
	@echo "  format       Format code with black"
	@echo "  clean        Clean up temporary files"

# Setup commands
install:
	pip install -r requirements.txt

check-env:
	@echo "Checking environment variables..."
	@python -c "import os; print('✓ BIFROST_BASE_URL:', os.getenv('BIFROST_BASE_URL', 'http://localhost:8080'))"
	@python -c "import os; print('✓ OPENAI_API_KEY:', 'Set' if os.getenv('OPENAI_API_KEY') else 'Not set')"
	@python -c "import os; print('✓ ANTHROPIC_API_KEY:', 'Set' if os.getenv('ANTHROPIC_API_KEY') else 'Not set')"
	@python -c "import os; print('✓ MISTRAL_API_KEY:', 'Set' if os.getenv('MISTRAL_API_KEY') else 'Not set')"
	@python -c "import os; print('✓ GOOGLE_API_KEY:', 'Set' if os.getenv('GOOGLE_API_KEY') else 'Not set')"

# Testing commands using master runner
test:
	python run_all_tests.py

test-parallel:
	python run_all_tests.py --parallel

test-verbose:
	python run_all_tests.py --verbose

test-list:
	python run_all_tests.py --list

# Individual integration tests
test-openai:
	python run_all_tests.py --integration openai --verbose

test-anthropic:
	python run_all_tests.py --integration anthropic --verbose

test-litellm:
	python run_all_tests.py --integration litellm --verbose

test-langchain:
	python run_all_tests.py --integration langchain --verbose

test-langgraph:
	python run_all_tests.py --integration langgraph --verbose

test-mistral:
	python run_all_tests.py --integration mistral --verbose

test-genai:
	python run_all_tests.py --integration genai --verbose

# Pytest commands
test-all:
	pytest -v

test-pytest-parallel:
	pytest -v -n auto

test-coverage:
	pytest --cov=. --cov-report=html --cov-report=term

# Development commands
lint:
	@echo "Running flake8..."
	-flake8 *.py
	@echo "Running mypy..."
	-mypy *.py

format:
	@echo "Formatting code with black..."
	black *.py

clean:
	@echo "Cleaning up temporary files..."
	rm -rf __pycache__/
	rm -rf .pytest_cache/
	rm -rf .coverage
	rm -rf htmlcov/
	rm -rf .mypy_cache/
	find . -name "*.pyc" -delete
	find . -name "*.pyo" -delete

# Quick commands for common workflows
quick-test: check-env test

all-tests: install check-env test-parallel

dev-setup: install check-env
	@echo "Development environment ready!"
	@echo "Run 'make test' to execute all tests" 