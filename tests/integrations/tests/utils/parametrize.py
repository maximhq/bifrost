"""
Parametrization utilities for cross-provider testing.

This module provides pytest parametrization for testing across multiple AI providers
with automatic scenario-based filtering.
"""

from typing import List, Tuple
from .config_loader import get_config


def get_cross_provider_params_for_scenario(
    scenario: str,
    include_providers: List[str] = None,
    exclude_providers: List[str] = None,
) -> List[Tuple[str, str]]:
    config = get_config()
    
    # Get providers that support this scenario
    providers = config.get_providers_for_scenario(scenario)
    
    # Apply include filter
    if include_providers:
        providers = [p for p in providers if p in include_providers]
    
    # Apply exclude filter
    if exclude_providers:
        providers = [p for p in providers if p not in exclude_providers]
    
    # Generate (provider, model) tuples
    # Automatically maps: scenario → capability → model
    params = []
    for provider in sorted(providers):  # Sort for consistent test ordering
        # Map scenario to capability, then get model
        capability = config.get_scenario_capability(scenario)
        model = config.get_provider_model(provider, capability)
        
        # Only add if provider has a model for this scenario's capability
        if model:
            params.append((provider, model))
    
    # If no providers available, return a dummy tuple to avoid pytest errors
    # The test will be skipped with appropriate message
    if not params:
        params = [("_no_providers_", "_no_model_")]
    
    return params


def format_provider_model(provider: str, model: str) -> str:
    """
    Format provider and model into the standard "provider/model" format.
    
    Args:
        provider: Provider name
        model: Model name
    
    Returns:
        Formatted string "provider/model"
    
    Example:
        >>> format_provider_model("openai", "gpt-4o")
        "openai/gpt-4o"
    """
    return f"{provider}/{model}"
