"""
Tests for /v1/models endpoint filtering based on virtual key provider_configs.

Tests cover:
- Filtering by specific allowed_models
- Empty allowed_models (all models from provider)
- Empty provider_configs (all models)
- Non-existent providers
- Multiple providers
- Invalid and inactive virtual keys
"""

import pytest
import requests
from conftest import BIFROST_BASE_URL, assert_response_success, generate_unique_name


class TestModelsFiltering:
    """Test /v1/models endpoint filtering based on virtual key provider configs"""

    @pytest.mark.virtual_keys
    @pytest.mark.integration
    @pytest.mark.smoke
    def test_list_models_with_vk_filters_by_provider_config(
        self, governance_client, cleanup_tracker
    ):
        """Test that /v1/models filters models when VK has provider_configs with specific allowed_models"""
        response = requests.get(f"{BIFROST_BASE_URL}/v1/models")
        assert_response_success(response, 200)
        all_models = response.json().get("data", [])

        providers_map = {}
        for model in all_models:
            if "/" in model["id"]:
                provider, model_name = model["id"].split("/", 1)
                if provider not in providers_map:
                    providers_map[provider] = []
                providers_map[provider].append(model_name)

        if not providers_map or not any(
            len(models) >= 2 for models in providers_map.values()
        ):
            pytest.skip("Need at least one provider with 2+ models")

        test_provider = next(
            p for p, models in providers_map.items() if len(models) >= 2
        )
        selected_models = providers_map[test_provider][:2]

        vk_data = {
            "name": generate_unique_name("Test VK Specific Models"),
            "provider_configs": [
                {
                    "provider": test_provider,
                    "allowed_models": selected_models,
                    "weight": 1.0,
                }
            ],
        }
        response = governance_client.create_virtual_key(vk_data)
        assert_response_success(response, 200)
        vk = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk["id"])

        assert vk["provider_configs"] is not None
        assert len(vk["provider_configs"]) == 1
        assert vk["provider_configs"][0]["provider"] == test_provider
        assert set(vk["provider_configs"][0]["allowed_models"]) == set(selected_models)

        response = requests.get(
            f"{BIFROST_BASE_URL}/v1/models", headers={"x-bf-vk": vk["value"]}
        )
        assert_response_success(response, 200)

        filtered_models = response.json().get("data", [])
        filtered_ids = [m["id"] for m in filtered_models]
        expected_ids = [f"{test_provider}/{m}" for m in selected_models]

        assert len(filtered_models) == len(selected_models), (
            f"Expected {len(selected_models)} models, got {len(filtered_models)}"
        )
        assert set(filtered_ids) == set(expected_ids), (
            f"Model IDs mismatch. Expected: {expected_ids}, Got: {filtered_ids}"
        )

        provider_models = [
            m["id"] for m in all_models if m["id"].startswith(f"{test_provider}/")
        ]
        excluded = [m for m in provider_models if m not in expected_ids]
        for excluded_id in excluded:
            assert excluded_id not in filtered_ids

        for model in filtered_models:
            assert "id" in model
            assert "created" in model

    @pytest.mark.virtual_keys
    @pytest.mark.integration
    def test_list_models_with_empty_allowed_models_returns_all_provider_models(
        self, governance_client, cleanup_tracker
    ):
        """Test that empty allowed_models returns all models from that provider"""
        response = requests.get(f"{BIFROST_BASE_URL}/v1/models")
        assert_response_success(response, 200)
        all_models = response.json().get("data", [])

        providers = list(
            set([m["id"].split("/")[0] for m in all_models if "/" in m["id"]])
        )
        if not providers:
            pytest.skip("No providers found")

        test_provider = providers[0]
        provider_model_ids = [
            m["id"] for m in all_models if m["id"].startswith(f"{test_provider}/")
        ]

        vk_data = {
            "name": generate_unique_name("Test VK All Provider Models"),
            "provider_configs": [
                {
                    "provider": test_provider,
                    "allowed_models": [],
                    "weight": 1.0,
                }
            ],
        }
        response = governance_client.create_virtual_key(vk_data)
        assert_response_success(response, 200)
        vk = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk["id"])

        assert vk["provider_configs"] is not None
        assert len(vk["provider_configs"]) == 1
        assert vk["provider_configs"][0]["provider"] == test_provider
        assert vk["provider_configs"][0]["allowed_models"] == []

        response = requests.get(
            f"{BIFROST_BASE_URL}/v1/models", headers={"x-bf-vk": vk["value"]}
        )
        assert_response_success(response, 200)

        filtered_models = response.json().get("data", [])
        filtered_ids = [m["id"] for m in filtered_models]

        assert len(filtered_models) == len(provider_model_ids), (
            f"Expected {len(provider_model_ids)} models, got {len(filtered_models)}"
        )
        assert set(filtered_ids) == set(provider_model_ids)

        other_provider_models = [
            m["id"] for m in all_models if not m["id"].startswith(f"{test_provider}/")
        ]
        for other_model_id in other_provider_models:
            assert other_model_id not in filtered_ids

    @pytest.mark.virtual_keys
    @pytest.mark.integration
    def test_list_models_without_provider_configs_returns_all(
        self, governance_client, cleanup_tracker
    ):
        """Test that VK with no provider_configs returns all models"""
        response = requests.get(f"{BIFROST_BASE_URL}/v1/models")
        assert_response_success(response, 200)
        all_models = response.json().get("data", [])
        all_model_ids = [m["id"] for m in all_models]

        if len(all_models) == 0:
            pytest.skip("No models available")

        vk_data = {
            "name": generate_unique_name("Test VK Unrestricted"),
            "provider_configs": [],
        }
        response = governance_client.create_virtual_key(vk_data)
        assert_response_success(response, 200)
        vk = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk["id"])

        assert vk["provider_configs"] is not None
        assert len(vk["provider_configs"]) == 0

        response = requests.get(
            f"{BIFROST_BASE_URL}/v1/models", headers={"x-bf-vk": vk["value"]}
        )
        assert_response_success(response, 200)

        filtered_models = response.json().get("data", [])
        filtered_ids = [m["id"] for m in filtered_models]

        assert len(filtered_models) == len(all_models), (
            f"Expected {len(all_models)} models, got {len(filtered_models)}"
        )
        assert set(filtered_ids) == set(all_model_ids)

    @pytest.mark.virtual_keys
    @pytest.mark.integration
    def test_list_models_with_nonexistent_provider_returns_empty(
        self, governance_client, cleanup_tracker
    ):
        """Test that VK with non-existent provider returns empty list"""
        vk_data = {
            "name": generate_unique_name("Test VK Nonexistent Provider"),
            "provider_configs": [
                {
                    "provider": "nonexistent-provider-xyz-123",
                    "allowed_models": [],
                    "weight": 1.0,
                }
            ],
        }
        response = governance_client.create_virtual_key(vk_data)
        assert_response_success(response, 200)
        vk = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk["id"])

        assert vk["provider_configs"] is not None
        assert len(vk["provider_configs"]) == 1
        assert vk["provider_configs"][0]["provider"] == "nonexistent-provider-xyz-123"

        response = requests.get(
            f"{BIFROST_BASE_URL}/v1/models", headers={"x-bf-vk": vk["value"]}
        )
        assert_response_success(response, 200)

        filtered_models = response.json().get("data", [])
        assert len(filtered_models) == 0

    @pytest.mark.virtual_keys
    @pytest.mark.integration
    def test_list_models_with_multiple_providers(
        self, governance_client, cleanup_tracker
    ):
        """Test that VK with multiple providers returns models from all configured providers"""
        response = requests.get(f"{BIFROST_BASE_URL}/v1/models")
        assert_response_success(response, 200)
        all_models = response.json().get("data", [])

        available_providers = list(
            set([m["id"].split("/")[0] for m in all_models if "/" in m["id"]])
        )

        if len(available_providers) < 2:
            pytest.skip(f"Need at least 2 providers, have: {available_providers}")

        provider1 = available_providers[0]
        provider2 = available_providers[1]

        provider1_models = [
            m["id"].split("/")[1]
            for m in all_models
            if m["id"].startswith(f"{provider1}/")
        ]
        provider2_models = [
            m["id"].split("/")[1]
            for m in all_models
            if m["id"].startswith(f"{provider2}/")
        ]

        if len(provider1_models) == 0 or len(provider2_models) == 0:
            pytest.skip("Each provider needs at least one model")

        selected_model1 = provider1_models[0]
        selected_model2 = provider2_models[0]

        vk_data = {
            "name": generate_unique_name("Test VK Multi Provider"),
            "provider_configs": [
                {
                    "provider": provider1,
                    "allowed_models": [selected_model1],
                    "weight": 1.0,
                },
                {
                    "provider": provider2,
                    "allowed_models": [selected_model2],
                    "weight": 1.0,
                },
            ],
        }
        response = governance_client.create_virtual_key(vk_data)
        assert_response_success(response, 200)
        vk = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk["id"])

        assert vk["provider_configs"] is not None
        assert len(vk["provider_configs"]) == 2
        vk_providers = [pc["provider"] for pc in vk["provider_configs"]]
        assert provider1 in vk_providers
        assert provider2 in vk_providers

        response = requests.get(
            f"{BIFROST_BASE_URL}/v1/models", headers={"x-bf-vk": vk["value"]}
        )
        assert_response_success(response, 200)

        filtered_models = response.json().get("data", [])
        filtered_ids = [m["id"] for m in filtered_models]
        expected_ids = [
            f"{provider1}/{selected_model1}",
            f"{provider2}/{selected_model2}",
        ]

        assert len(filtered_models) == 2, (
            f"Expected 2 models, got {len(filtered_models)}"
        )
        assert set(filtered_ids) == set(expected_ids)

        provider1_found = any(
            m["id"].startswith(f"{provider1}/") for m in filtered_models
        )
        provider2_found = any(
            m["id"].startswith(f"{provider2}/") for m in filtered_models
        )
        assert provider1_found
        assert provider2_found

    @pytest.mark.virtual_keys
    @pytest.mark.integration
    def test_list_models_without_vk_header_returns_all(self, governance_client):
        """Test that requesting models without VK header returns all models"""
        response = requests.get(f"{BIFROST_BASE_URL}/v1/models")
        assert_response_success(response, 200)

        all_models = response.json().get("data", [])
        assert len(all_models) > 0

    @pytest.mark.virtual_keys
    @pytest.mark.integration
    def test_list_models_with_invalid_vk_header(self, governance_client):
        """Test that invalid VK header returns error or all models without filtering"""
        response_without_vk = requests.get(f"{BIFROST_BASE_URL}/v1/models")
        assert_response_success(response_without_vk, 200)
        all_models = response_without_vk.json().get("data", [])
        all_model_ids = set([m["id"] for m in all_models])

        response = requests.get(
            f"{BIFROST_BASE_URL}/v1/models", headers={"x-bf-vk": "invalid-vk-value-xyz"}
        )

        assert response.status_code in [200, 401, 403]

        if response.status_code == 200:
            models = response.json().get("data", [])
            model_ids = set([m["id"] for m in models])
            assert isinstance(models, list)
            assert model_ids == all_model_ids, "Invalid VK should not filter models"

    @pytest.mark.virtual_keys
    @pytest.mark.integration
    def test_list_models_with_inactive_vk(self, governance_client, cleanup_tracker):
        """Test that inactive VK is rejected or returns all models without filtering"""
        response = requests.get(f"{BIFROST_BASE_URL}/v1/models")
        assert_response_success(response, 200)
        all_models = response.json().get("data", [])
        all_model_ids = set([m["id"] for m in all_models])

        if len(all_models) == 0:
            pytest.skip("No models available")

        vk_data = {
            "name": generate_unique_name("Test VK Inactive"),
            "is_active": False,
            "provider_configs": [
                {
                    "provider": "test-provider",
                    "allowed_models": ["test-model"],
                    "weight": 1.0,
                }
            ],
        }
        response = governance_client.create_virtual_key(vk_data)
        assert_response_success(response, 200)
        vk = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk["id"])

        if vk["is_active"] is True:
            update_response = governance_client.update_virtual_key(
                vk["id"], {"is_active": False}
            )
            if update_response.status_code == 200:
                vk = update_response.json()["virtual_key"]
            else:
                pytest.skip("Cannot deactivate VK")

        assert vk["is_active"] is False

        response = requests.get(
            f"{BIFROST_BASE_URL}/v1/models", headers={"x-bf-vk": vk["value"]}
        )

        assert response.status_code in [200, 401, 403]

        if response.status_code == 200:
            filtered_models = response.json().get("data", [])
            filtered_ids = set([m["id"] for m in filtered_models])

            assert filtered_ids == all_model_ids, (
                f"Inactive VK should not filter models. Expected {len(all_models)} models, "
                f"got {len(filtered_models)}"
            )

    @pytest.mark.virtual_keys
    @pytest.mark.integration
    def test_list_models_with_mixed_provider_configs(
        self, governance_client, cleanup_tracker
    ):
        """Test VK with one provider having specific models and another with all models"""
        response = requests.get(f"{BIFROST_BASE_URL}/v1/models")
        assert_response_success(response, 200)
        all_models = response.json().get("data", [])

        providers = list(
            set([m["id"].split("/")[0] for m in all_models if "/" in m["id"]])
        )

        if len(providers) < 2:
            pytest.skip(f"Need at least 2 providers, have: {providers}")

        provider1 = providers[0]
        provider2 = providers[1]

        provider1_models = [
            m["id"].split("/")[1]
            for m in all_models
            if m["id"].startswith(f"{provider1}/")
        ]
        provider2_all_models = [
            m["id"] for m in all_models if m["id"].startswith(f"{provider2}/")
        ]

        if len(provider1_models) < 2 or len(provider2_all_models) == 0:
            pytest.skip("Need sufficient models")

        selected_provider1_model = provider1_models[0]

        vk_data = {
            "name": generate_unique_name("Test VK Mixed Configs"),
            "provider_configs": [
                {
                    "provider": provider1,
                    "allowed_models": [selected_provider1_model],
                    "weight": 1.0,
                },
                {
                    "provider": provider2,
                    "allowed_models": [],
                    "weight": 1.0,
                },
            ],
        }
        response = governance_client.create_virtual_key(vk_data)
        assert_response_success(response, 200)
        vk = response.json()["virtual_key"]
        cleanup_tracker.add_virtual_key(vk["id"])

        response = requests.get(
            f"{BIFROST_BASE_URL}/v1/models", headers={"x-bf-vk": vk["value"]}
        )
        assert_response_success(response, 200)

        filtered_models = response.json().get("data", [])
        filtered_ids = [m["id"] for m in filtered_models]

        expected_count = 1 + len(provider2_all_models)
        assert len(filtered_models) == expected_count, (
            f"Expected {expected_count} models, got {len(filtered_models)}"
        )

        provider1_filtered = [
            m["id"] for m in filtered_models if m["id"].startswith(f"{provider1}/")
        ]
        assert len(provider1_filtered) == 1
        assert f"{provider1}/{selected_provider1_model}" in provider1_filtered

        provider2_filtered = [
            m["id"] for m in filtered_models if m["id"].startswith(f"{provider2}/")
        ]
        assert set(provider2_filtered) == set(provider2_all_models)
