"""
Configuration loader for Bifrost integration tests.

This module loads configuration from config.yml and provides utilities
for constructing integration URLs through the Bifrost gateway.
"""

import os
import yaml
from typing import Dict, Any, Optional, List
from dataclasses import dataclass
from pathlib import Path


# Integration to provider mapping
# Maps integration names to their underlying provider configurations
INTEGRATION_TO_PROVIDER_MAP = {
    "openai": "openai",
    "anthropic": "anthropic",
    "google": "gemini",  # Google integration uses Gemini provider
    "litellm": "openai",  # LiteLLM defaults to OpenAI
    "langchain": "openai",  # LangChain defaults to OpenAI
}

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
        """Get model name for an integration and type
        
        Maps integration names to provider configurations.
        
        Args:
            integration: Integration name (openai, anthropic, google, litellm, langchain)
            model_type: Model type (chat, vision, tools, etc.)
        
        Returns:
            Model name for the integration and type
        """
        # Map integration to provider
        provider = INTEGRATION_TO_PROVIDER_MAP.get(integration)
        if not provider:
            raise ValueError(
                f"Unknown integration: {integration}. "
                f"Valid integrations: {list(INTEGRATION_TO_PROVIDER_MAP.keys())}"
            )
        
        # Get model from provider configuration
        return self.get_provider_model(provider, model_type)

    def get_model_alternatives(self, integration: str) -> list:
        """Get alternative models for an integration"""
        # Map integration to provider
        provider = INTEGRATION_TO_PROVIDER_MAP.get(integration)
        if not provider:
            return []
        
        # Get alternatives from provider configuration
        if "providers" not in self._config:
            return []
        
        if provider not in self._config["providers"]:
            return []
        
        return self._config["providers"][provider].get("alternatives", [])

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
        return list(INTEGRATION_TO_PROVIDER_MAP.keys())

    def list_models(self, integration: str = None) -> Dict[str, Any]:
        """List all models for an integration or all integrations"""
        if integration:
            # Map integration to provider
            provider = INTEGRATION_TO_PROVIDER_MAP.get(integration)
            if not provider:
                raise ValueError(f"Unknown integration: {integration}")
            
            if "providers" not in self._config or provider not in self._config["providers"]:
                raise ValueError(f"No provider configuration for: {provider}")
            
            return {integration: self._config["providers"][provider]}
        
        # Return all providers mapped to their integration names
        result = {}
        for integration, provider in INTEGRATION_TO_PROVIDER_MAP.items():
            if "providers" in self._config and provider in self._config["providers"]:
                result[integration] = self._config["providers"][provider]
        
        return result

    def validate_config(self) -> bool:
        """Validate configuration completeness"""
        required_sections = ["bifrost", "providers", "api", "test_settings"]

        for section in required_sections:
            if section not in self._config:
                raise ValueError(f"Missing required configuration section: {section}")

        # Validate Bifrost configuration
        bifrost = self._config["bifrost"]
        if "base_url" not in bifrost or "endpoints" not in bifrost:
            raise ValueError("Bifrost configuration missing base_url or endpoints")

        # Validate that all integrations map to valid providers
        for integration, provider in INTEGRATION_TO_PROVIDER_MAP.items():
            if provider not in self._config["providers"]:
                raise ValueError(
                    f"Integration '{integration}' maps to provider '{provider}' "
                    f"which is not configured in providers section"
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
        print(f"\n🤖 MODEL CONFIGURATIONS (via providers):")
        for integration, provider in INTEGRATION_TO_PROVIDER_MAP.items():
            if "providers" in self._config and provider in self._config["providers"]:
                models = self._config["providers"][provider]
                print(f"  {integration.upper()} → {provider}:")
                print(f"    Chat: {models.get('chat', 'N/A')}")
                print(f"    Vision: {models.get('vision', 'N/A')}")
                print(f"    Tools: {models.get('tools', 'N/A')}")
                alternatives = models.get('alternatives', [])
                print(f"    Alternatives: {len(alternatives)} models")

        # API settings
        api_config = self.get_api_config()
        print(f"\n⚙️  API SETTINGS:")
        print(f"  Timeout: {api_config['timeout']}s")
        print(f"  Max Retries: {api_config['max_retries']}")
        print(f"  Retry Delay: {api_config['retry_delay']}s")

        print(f"\n✅ Configuration loaded successfully from: {self.config_path}")

    def get_provider_model(self, provider: str, capability: str = "chat") -> str:
        """Get model name for a provider and capability
        
        Args:
            provider: Provider name (e.g., 'openai', 'anthropic', 'gemini')
            capability: Capability type (default: 'chat')
        
        Returns:
            Model name suitable for the provider and capability
        """
        if "providers" not in self._config:
            # Fallback to old behavior if providers section doesn't exist
            return ""
        
        providers = self._config["providers"]
        if provider not in providers:
            return ""
        
        provider_models = providers[provider]
        return provider_models.get(capability, "")

    def get_provider_api_key_env(self, provider: str) -> str:
        """Get the environment variable name for a provider's API key
        
        Args:
            provider: Provider name
            
        Returns:
            Environment variable name
        """
        if "provider_api_keys" not in self._config:
            return ""
        
        return self._config["provider_api_keys"].get(provider, "")

    def is_provider_available(self, provider: str) -> bool:
        """Check if a provider is available (has API key in environment)
        
        Args:
            provider: Provider name
            
        Returns:
            True if provider's API key is set in environment
        """
        env_var = self.get_provider_api_key_env(provider)
        if not env_var:
            return False
        
        api_key = os.getenv(env_var)
        return api_key is not None and api_key.strip() != ""

    def get_available_providers(self) -> List[str]:
        """Get list of providers that are available (have API keys configured)
        
        Returns:
            List of available provider names
        """
        if "providers" not in self._config:
            return []
        
        available = []
        for provider in self._config["providers"].keys():
            if self.is_provider_available(provider):
                available.append(provider)
        
        return available

    def provider_supports_scenario(self, provider: str, scenario: str) -> bool:
        """Check if a provider supports a specific test scenario
        
        Args:
            provider: Provider name
            scenario: Scenario name
            
        Returns:
            True if provider supports the scenario
        """
        if "provider_scenarios" not in self._config:
            return False
        
        if provider not in self._config["provider_scenarios"]:
            return False
        
        scenarios = self._config["provider_scenarios"][provider]
        return scenarios.get(scenario, False)

    def get_providers_for_scenario(self, scenario: str) -> List[str]:
        """Get list of available providers that support a specific scenario
        
        Args:
            scenario: Scenario name
            
        Returns:
            List of provider names that support the scenario
        """
        available_providers = self.get_available_providers()
        providers = []
        
        for provider in available_providers:
            if self.provider_supports_scenario(provider, scenario):
                providers.append(provider)
        
        return providers

    def get_scenario_capability(self, scenario: str) -> str:
        """Get the capability type for a scenario
        
        Args:
            scenario: Scenario name
            
        Returns:
            Capability type (e.g., 'chat', 'vision', 'tools')
        """
        if "scenario_capabilities" not in self._config:
            return "chat"  # Default
        
        return self._config["scenario_capabilities"].get(scenario, "chat")


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


def get_provider_model(provider: str, capability: str = "chat") -> str:
    """Convenience function to get provider model"""
    return get_config().get_provider_model(provider, capability)


def is_provider_available(provider: str) -> bool:
    """Convenience function to check provider availability"""
    return get_config().is_provider_available(provider)


def get_available_providers() -> List[str]:
    """Convenience function to get available providers"""
    return get_config().get_available_providers()


def provider_supports_scenario(provider: str, scenario: str) -> bool:
    """Convenience function to check scenario support"""
    return get_config().provider_supports_scenario(provider, scenario)


def get_providers_for_scenario(scenario: str) -> List[str]:
    """Convenience function to get providers for scenario"""
    return get_config().get_providers_for_scenario(scenario)


if __name__ == "__main__":
    # Print configuration summary when run directly
    config = get_config()
    config.validate_config()
    config.print_config_summary()
