"""
Configuration loader for Bifrost integration tests.

This module loads configuration from config.yml and provides utilities
for constructing integration URLs through the Bifrost gateway.
"""

import os
import yaml
from typing import Dict, Any, Optional
from dataclasses import dataclass
from pathlib import Path


@dataclass
class BifrostConfig:
    """Bifrost gateway configuration"""

    base_url: str
    endpoints: Dict[str, str]


@dataclass
class IntegrationModels:
    """Model configuration for a integration"""

    chat: str
    vision: str
    tools: str
    alternatives: list


@dataclass
class TestConfig:
    """Complete test configuration"""

    bifrost: BifrostConfig
    api: Dict[str, Any]
    models: Dict[str, IntegrationModels]
    model_capabilities: Dict[str, Dict[str, Any]]
    test_settings: Dict[str, Any]
    integration_settings: Dict[str, Any]
    environments: Dict[str, Any]
    logging: Dict[str, Any]


class ConfigLoader:
    """Configuration loader for Bifrost integration tests"""

    def __init__(self, config_path: Optional[str] = None):
        """Initialize configuration loader

        Args:
            config_path: Path to config.yml file. If None, looks for config.yml in project root.
        """
        if config_path is None:
            # Look for config.yml in project root
            project_root = Path(__file__).parent.parent.parent
            config_path = project_root / "config.yml"

        self.config_path = Path(config_path)
        self._config = None
        self._load_config()

    def _load_config(self):
        """Load configuration from YAML file"""
        if not self.config_path.exists():
            raise FileNotFoundError(f"Configuration file not found: {self.config_path}")

        with open(self.config_path, "r") as f:
            raw_config = yaml.safe_load(f)

        # Expand environment variables
        self._config = self._expand_env_vars(raw_config)

    def _expand_env_vars(self, obj):
        """Recursively expand environment variables in configuration"""
        if isinstance(obj, dict):
            return {k: self._expand_env_vars(v) for k, v in obj.items()}
        elif isinstance(obj, list):
            return [self._expand_env_vars(item) for item in obj]
        elif isinstance(obj, str):
            # Handle ${VAR:-default} syntax
            import re

            pattern = r"\$\{([^}]+)\}"

            def replace_var(match):
                var_expr = match.group(1)
                if ":-" in var_expr:
                    var_name, default_value = var_expr.split(":-", 1)
                    return os.getenv(var_name, default_value)
                else:
                    return os.getenv(var_expr, "")

            return re.sub(pattern, replace_var, obj)
        else:
            return obj

    def get_integration_url(self, integration: str) -> str:
        """Get the complete URL for a integration

        Args:
            integration: Integration name (openai, anthropic, google, litellm)

        Returns:
            Complete URL for the integration

        Examples:
            get_integration_url("openai") -> "http://localhost:8080/openai"
        """
        bifrost_config = self._config["bifrost"]
        base_url = bifrost_config["base_url"]
        endpoint = bifrost_config["endpoints"].get(integration, "")

        if not endpoint:
            raise ValueError(f"No endpoint configured for integration: {integration}")

        return f"{base_url.rstrip('/')}/{endpoint}"

    def get_bifrost_config(self) -> BifrostConfig:
        """Get Bifrost configuration"""
        bifrost_data = self._config["bifrost"]
        return BifrostConfig(
            base_url=bifrost_data["base_url"], endpoints=bifrost_data["endpoints"]
        )

    def get_model(self, integration: str, model_type: str = "chat") -> str:
        """Get model name for a integration and type"""
        if integration not in self._config["models"]:
            raise ValueError(f"Unknown integration: {integration}")

        integration_models = self._config["models"][integration]

        if model_type not in integration_models:
            raise ValueError(
                f"Unknown model type '{model_type}' for integration '{integration}'"
            )

        return integration_models[model_type]

    def get_model_alternatives(self, integration: str) -> list:
        """Get alternative models for a integration"""
        if integration not in self._config["models"]:
            raise ValueError(f"Unknown integration: {integration}")

        return self._config["models"][integration].get("alternatives", [])

    def get_model_capabilities(self, model: str) -> Dict[str, Any]:
        """Get capabilities for a specific model"""
        return self._config["model_capabilities"].get(
            model,
            {
                "chat": True,
                "tools": False,
                "vision": False,
                "max_tokens": 4096,
                "context_window": 4096,
            },
        )

    def supports_capability(self, model: str, capability: str) -> bool:
        """Check if a model supports a specific capability"""
        caps = self.get_model_capabilities(model)
        return caps.get(capability, False)

    def get_api_config(self) -> Dict[str, Any]:
        """Get API configuration (timeout, retries, etc.)"""
        return self._config["api"]

    def get_test_settings(self) -> Dict[str, Any]:
        """Get test configuration settings"""
        return self._config["test_settings"]

    def get_integration_settings(self, integration: str) -> Dict[str, Any]:
        """Get integration-specific settings"""
        return self._config["integration_settings"].get(integration, {})

    def get_environment_config(self, environment: str = None) -> Dict[str, Any]:
        """Get environment-specific configuration

        Args:
            environment: Environment name (development, production, etc.)
                        If None, uses TEST_ENV environment variable or 'development'
        """
        if environment is None:
            environment = os.getenv("TEST_ENV", "development")

        return self._config["environments"].get(environment, {})

    def get_logging_config(self) -> Dict[str, Any]:
        """Get logging configuration"""
        return self._config["logging"]

    def list_integrations(self) -> list:
        """List all configured integrations"""
        return list(self._config["bifrost"]["endpoints"].keys())

    def list_models(self, integration: str = None) -> Dict[str, Any]:
        """List all models for a integration or all integrations"""
        if integration:
            if integration not in self._config["models"]:
                raise ValueError(f"Unknown integration: {integration}")
            return {integration: self._config["models"][integration]}

        return self._config["models"]

    def validate_config(self) -> bool:
        """Validate configuration completeness"""
        required_sections = ["bifrost", "models", "api", "test_settings"]

        for section in required_sections:
            if section not in self._config:
                raise ValueError(f"Missing required configuration section: {section}")

        # Validate Bifrost configuration
        bifrost = self._config["bifrost"]
        if "base_url" not in bifrost or "endpoints" not in bifrost:
            raise ValueError("Bifrost configuration missing base_url or endpoints")

        # Validate that all integrations have model configurations
        integrations = list(bifrost["endpoints"].keys())
        for integration in integrations:
            if integration not in self._config["models"]:
                raise ValueError(
                    f"No model configuration for integration: {integration}"
                )

        return True

    def print_config_summary(self):
        """Print a summary of the configuration"""
        print("🔧 BIFROST INTEGRATION TEST CONFIGURATION")
        print("=" * 80)

        # Bifrost configuration
        bifrost = self.get_bifrost_config()
        print(f"\n🌉 BIFROST GATEWAY:")
        print(f"  Base URL: {bifrost.base_url}")
        print(f"  Endpoints:")
        for integration, endpoint in bifrost.endpoints.items():
            full_url = f"{bifrost.base_url.rstrip('/')}/{endpoint}"
            print(f"    {integration}: {full_url}")

        # Model configurations
        print(f"\n🤖 MODEL CONFIGURATIONS:")
        for integration, models in self._config["models"].items():
            print(f"  {integration.upper()}:")
            print(f"    Chat: {models['chat']}")
            print(f"    Vision: {models['vision']}")
            print(f"    Tools: {models['tools']}")
            print(f"    Alternatives: {len(models['alternatives'])} models")

        # API settings
        api_config = self.get_api_config()
        print(f"\n⚙️  API SETTINGS:")
        print(f"  Timeout: {api_config['timeout']}s")
        print(f"  Max Retries: {api_config['max_retries']}")
        print(f"  Retry Delay: {api_config['retry_delay']}s")

        print(f"\n✅ Configuration loaded successfully from: {self.config_path}")


# Global configuration instance
_config_loader = None


def get_config() -> ConfigLoader:
    """Get global configuration instance"""
    global _config_loader
    if _config_loader is None:
        _config_loader = ConfigLoader()
    return _config_loader


def get_integration_url(integration: str) -> str:
    return get_config().get_integration_url(integration)


def get_model(integration: str, model_type: str = "chat") -> str:
    """Convenience function to get model name"""
    return get_config().get_model(integration, model_type)


def get_model_capabilities(model: str) -> Dict[str, Any]:
    """Convenience function to get model capabilities"""
    return get_config().get_model_capabilities(model)


def supports_capability(model: str, capability: str) -> bool:
    """Convenience function to check model capability"""
    return get_config().supports_capability(model, capability)


if __name__ == "__main__":
    # Print configuration summary when run directly
    config = get_config()
    config.validate_config()
    config.print_config_summary()
