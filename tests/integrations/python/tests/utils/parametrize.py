"""
Parametrization utilities for cross-provider testing.

This module provides pytest parametrization for testing across multiple AI providers
with automatic scenario-based filtering.
"""

from typing import List, Tuple, Union
from .config_loader import get_config


def get_cross_provider_params_for_scenario(
    scenario: str,
    include_providers: List[str] | None = None,
    exclude_providers: List[str] | None = None,
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


def get_cross_provider_params_with_vk_for_scenario(
    scenario: str,
    include_providers: List[str] | None = None,
    exclude_providers: List[str] | None = None,
) -> List[Tuple[str, str, bool]]:
    """
    Get cross-provider parameters with virtual key flag for pytest parametrization.
    
    When virtual key is configured, each provider/model combo is tested twice:
    once without VK (vk_enabled=False) and once with VK (vk_enabled=True).
    
    Args:
        scenario: Test scenario name
        include_providers: Optional list of providers to include
        exclude_providers: Optional list of providers to exclude
    
    Returns:
        List of (provider, model, vk_enabled) tuples
    
    Example:
        When VK is configured:
        [
            ("openai", "gpt-4o", False),
            ("openai", "gpt-4o", True),
            ("anthropic", "claude-3", False),
            ("anthropic", "claude-3", True),
        ]
    """
    config = get_config()
    
    # Get base params without VK
    base_params = get_cross_provider_params_for_scenario(
        scenario, include_providers, exclude_providers
    )
    
    # Handle the dummy tuple case
    if base_params == [("_no_providers_", "_no_model_")]:
        return [("_no_providers_", "_no_model_", False)]
    
    # Build params list with VK flag
    params = []
    vk_configured = config.is_virtual_key_configured()
    
    for provider, model in base_params:
        # Always add the non-VK variant
        params.append((provider, model, False))
        
        # Add VK variant only if VK is configured
        if vk_configured:
            params.append((provider, model, True))
    
    return params


def format_vk_test_id(provider: str, model: str, vk_enabled: bool) -> str:
    """
    Format test ID for virtual key parameterized tests.
    
    Args:
        provider: Provider name
        model: Model name
        vk_enabled: Whether VK is enabled
    
    Returns:
        Formatted test ID string
    
    Example:
        >>> format_vk_test_id("openai", "gpt-4o", True)
        "openai-gpt-4o-with_vk"
        >>> format_vk_test_id("openai", "gpt-4o", False)
        "openai-gpt-4o-no_vk"
    """
    vk_suffix = "with_vk" if vk_enabled else "no_vk"
    return f"{provider}-{model}-{vk_suffix}"


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