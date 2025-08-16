"""
Model configurations for each integration.

This file now acts as a compatibility layer and convenience wrapper
around the new configuration system in config.yml and config_loader.py.

All model data is now centralized in config.yml for easier maintenance.
"""

from typing import Dict, List
from dataclasses import dataclass
from .config_loader import get_config


@dataclass
class IntegrationModels:
    """Model configuration for a integration"""

    chat: str  # Primary chat model
    vision: str  # Vision/multimodal model
    tools: str  # Function calling model
    alternatives: List[str]  # Alternative models for testing


def get_integration_models() -> Dict[str, IntegrationModels]:
    """Get all integration model configurations from config.yml"""
    config = get_config()
    integration_models = {}

    for integration in config.list_integrations():
        models_config = config.list_models(integration)
        integration_models[integration] = IntegrationModels(
            chat=models_config["chat"],
            vision=models_config["vision"],
            tools=models_config["tools"],
            alternatives=models_config["alternatives"],
        )

    return integration_models


# Backward compatibility - load from config
INTEGRATION_MODELS = get_integration_models()


def get_alternatives(integration: str) -> List[str]:
    """Get alternative models for a integration"""
    config = get_config()
    return config.get_model_alternatives(integration)


def list_all_models() -> Dict[str, Dict[str, str]]:
    """List all models by integration and type"""
    config = get_config()
    return config.list_models()


# Print model summary for documentation
def print_model_summary():
    """Print a summary of all models and their capabilities"""
    config = get_config()
    config.print_config_summary()


if __name__ == "__main__":
    print_model_summary()
