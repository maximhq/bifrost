"""
Integration tests for Cohere SDK with Bifrost.

Covers embedding scenarios only:
  - Text embeddings (single, batch, input_type variations)
  - Custom dimensions and embedding types
  - Truncation
  - Image embeddings
  - Multimodal mixed inputs (text + image)
"""

import httpx
import pytest
import cohere

from .utils.common import (
    BASE64_IMAGE,
    EMBEDDINGS_SINGLE_TEXT,
    EMBEDDINGS_MULTIPLE_TEXTS,
    Config,
    get_api_key,
)
from .utils.config_loader import get_config, get_integration_url
from .utils.parametrize import format_provider_model, get_cross_provider_params_for_scenario


def get_provider_cohere_client(provider: str = "cohere") -> cohere.ClientV2:
    """Create Cohere ClientV2 pointed at Bifrost with x-model-provider header."""
    api_key = get_api_key(provider)
    base_url = get_integration_url("cohere")
    config = get_config()
    api_config = config.get_api_config()
    timeout = api_config.get("timeout", 30)

    return cohere.ClientV2(
        api_key=api_key,
        base_url=base_url,
        httpx_client=httpx.Client(
            headers={"x-model-provider": provider},
            timeout=float(timeout),
        ),
    )


@pytest.fixture
def test_config():
    return Config()


def assert_valid_cohere_embedding_response(response, expected_count: int, expected_dimensions: int | None = None):
    """Assert a Cohere embed response contains valid float embeddings."""
    assert response is not None, "Response should not be None"
    assert response.embeddings is not None, "Response should have embeddings"
    assert response.embeddings.float is not None, "Response embeddings should have float vectors"
    vectors = response.embeddings.float
    assert len(vectors) == expected_count, (
        f"Expected {expected_count} embeddings, got {len(vectors)}"
    )
    for i, vec in enumerate(vectors):
        assert isinstance(vec, list), f"Embedding {i} should be a list"
        assert len(vec) > 0, f"Embedding {i} should not be empty"
        assert all(isinstance(v, float) for v in vec), f"Embedding {i} values should be floats"
        if expected_dimensions is not None:
            assert len(vec) == expected_dimensions, (
                f"Embedding {i}: expected {expected_dimensions} dims, got {len(vec)}"
            )


class TestCohereIntegration:
    """Cohere SDK embedding tests via Bifrost."""

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("embeddings"))
    def test_01_single_text_embedding(self, test_config, provider, model):
        """Single string with input_type=search_document."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for embeddings scenario")

        client = get_provider_cohere_client(provider)
        response = client.embed(
            model=format_provider_model(provider, model),
            texts=[EMBEDDINGS_SINGLE_TEXT],
            input_type="search_document",
            embedding_types=["float"],
        )

        assert_valid_cohere_embedding_response(response, expected_count=1)
        print(f"✓ Single text embedding: provider={provider} dims={len(response.embeddings.float[0])}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("embeddings"))
    def test_02_batch_text_embeddings(self, test_config, provider, model):
        """Batch of 3 strings with input_type=search_document."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for embeddings scenario")

        texts = EMBEDDINGS_MULTIPLE_TEXTS[:3]
        client = get_provider_cohere_client(provider)
        response = client.embed(
            model=format_provider_model(provider, model),
            texts=texts,
            input_type="search_document",
            embedding_types=["float"],
        )

        assert_valid_cohere_embedding_response(response, expected_count=3)
        print(f"✓ Batch text embeddings: provider={provider} count=3 dims={len(response.embeddings.float[0])}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("embeddings"))
    def test_03_search_query_embedding(self, test_config, provider, model):
        """Single string with input_type=search_query."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for embeddings scenario")

        client = get_provider_cohere_client(provider)
        response = client.embed(
            model=format_provider_model(provider, model),
            texts=["What is machine learning?"],
            input_type="search_query",
            embedding_types=["float"],
        )

        assert_valid_cohere_embedding_response(response, expected_count=1)
        print(f"✓ Search query embedding: provider={provider}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("embeddings"))
    def test_04_classification_embedding(self, test_config, provider, model):
        """Single string with input_type=classification."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for embeddings scenario")

        client = get_provider_cohere_client(provider)
        response = client.embed(
            model=format_provider_model(provider, model),
            texts=["This is a positive review."],
            input_type="classification",
            embedding_types=["float"],
        )

        assert_valid_cohere_embedding_response(response, expected_count=1)
        print(f"✓ Classification embedding: provider={provider}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("embeddings"))
    def test_05_clustering_embedding(self, test_config, provider, model):
        """Single string with input_type=clustering."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for embeddings scenario")

        client = get_provider_cohere_client(provider)
        response = client.embed(
            model=format_provider_model(provider, model),
            texts=["Renewable energy sources include solar and wind."],
            input_type="clustering",
            embedding_types=["float"],
        )

        assert_valid_cohere_embedding_response(response, expected_count=1)
        print(f"✓ Clustering embedding: provider={provider}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("embeddings"))
    def test_06_custom_dimensions_embedding(self, test_config, provider, model):
        """Single string with output_dimension=512 (embed-v4.0 only)."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for embeddings scenario")

        client = get_provider_cohere_client(provider)
        response = client.embed(
            model=format_provider_model(provider, model),
            texts=[EMBEDDINGS_SINGLE_TEXT],
            input_type="search_document",
            embedding_types=["float"],
            output_dimension=512,
        )

        assert_valid_cohere_embedding_response(response, expected_count=1, expected_dimensions=512)
        print(f"✓ Custom dimensions embedding: provider={provider} dims=512")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("embeddings"))
    def test_07_multiple_embedding_types(self, test_config, provider, model):
        """Single string requesting float and int8 embedding types."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for embeddings scenario")

        client = get_provider_cohere_client(provider)
        response = client.embed(
            model=format_provider_model(provider, model),
            texts=[EMBEDDINGS_SINGLE_TEXT],
            input_type="search_document",
            embedding_types=["float", "int8"],
        )

        assert response is not None, "Response should not be None"
        assert response.embeddings is not None, "Response should have embeddings"
        assert response.embeddings.float is not None, "Response should include float embeddings"
        assert response.embeddings.int8 is not None, "Response should include int8 embeddings"
        assert len(response.embeddings.float) == 1
        assert len(response.embeddings.int8) == 1
        print(f"✓ Multiple embedding types: provider={provider}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("embeddings"))
    def test_08_truncation_embedding(self, test_config, provider, model):
        """Long text with truncate=END to verify truncation is handled."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for embeddings scenario")

        long_text = " ".join(EMBEDDINGS_MULTIPLE_TEXTS) * 10

        client = get_provider_cohere_client(provider)
        response = client.embed(
            model=format_provider_model(provider, model),
            texts=[long_text],
            input_type="search_document",
            embedding_types=["float"],
            truncate="END",
        )

        assert_valid_cohere_embedding_response(response, expected_count=1)
        print(f"✓ Truncation embedding: provider={provider}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multimodal_embeddings"))
    def test_09_image_embedding(self, test_config, provider, model):
        """Single image data URI with input_type=image."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for multimodal_embeddings scenario")

        image_data_uri = f"data:image/png;base64,{BASE64_IMAGE}"

        client = get_provider_cohere_client(provider)
        response = client.embed(
            model=format_provider_model(provider, model),
            images=[image_data_uri],
            input_type="image",
            embedding_types=["float"],
        )

        assert_valid_cohere_embedding_response(response, expected_count=1)
        print(f"✓ Image embedding: provider={provider} dims={len(response.embeddings.float[0])}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multimodal_embeddings"))
    def test_10_multimodal_mixed_inputs_embedding(self, test_config, provider, model):
        """Mixed text + image content via inputs field."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for multimodal_embeddings scenario")

        image_data_uri = f"data:image/png;base64,{BASE64_IMAGE}"

        mixed_input = cohere.EmbedInput(
            content=[
                {"type": "text", "text": "A colorful geometric pattern"},
                {"type": "image_url", "image_url": {"url": image_data_uri}},
            ]
        )

        client = get_provider_cohere_client(provider)
        response = client.embed(
            model=format_provider_model(provider, model),
            inputs=[mixed_input],
            input_type="search_document",
            embedding_types=["float"],
        )

        assert_valid_cohere_embedding_response(response, expected_count=1)
        print(f"✓ Multimodal mixed inputs embedding: provider={provider} dims={len(response.embeddings.float[0])}")
