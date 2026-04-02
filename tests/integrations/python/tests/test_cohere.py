"""
Cohere Integration Tests - Cross-Provider Support

🌉 CROSS-PROVIDER TESTING:
This test suite uses the official Cohere SDK (ClientV2) against Bifrost's /cohere routes.
Tests run against every provider that declares the `rerank` scenario, routed with the
x-model-provider header, so a Cohere-shaped request served by Bedrock or Vertex must still
come back in Cohere's wire shape.

Covered scenarios:
1. Rerank with plain string documents - Cross-provider
2. Rerank with object documents carrying id/metadata - Cross-provider
3. top_n truncation - Cross-provider
4. return_documents echo - Cohere
5. Error shape on an invalid request - Cohere
6. Text embeddings (single, batch, input_type variations) - Cross-provider
7. Custom dimensions, embedding types and truncation - Cross-provider
8. Image and multimodal (text + image) embeddings - Cross-provider

Note: Tests automatically skip for providers that don't declare the scenario.
"""

import os

import cohere
import httpx
import pytest

from .utils.common import (
    BASE64_IMAGE,
    EMBEDDINGS_MULTIPLE_TEXTS,
    EMBEDDINGS_SINGLE_TEXT,
    RERANK_DOCUMENTS,
    RERANK_QUERY,
    Config,
    assert_valid_rerank_results,
    get_api_key,
    rerank_documents_as_objects,
    skip_if_no_api_key,
)
from .utils.config_loader import get_config, get_integration_url
from .utils.parametrize import (
    format_provider_model,
    get_cross_provider_params_for_scenario,
)


@pytest.fixture
def cohere_client():
    """Cohere ClientV2 pointed at Bifrost.

    The SDK appends the version path (/v2/rerank) to base_url, so it is configured with
    the bare integration URL.
    """
    return cohere.ClientV2(
        api_key=os.getenv("COHERE_API_KEY", "dummy-key"),
        base_url=get_integration_url("cohere"),
    )


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


def provider_options(provider: str, **extra_body):
    """Route the call to `provider` and pass through any non-SDK body params."""
    options = {"additional_headers": {"x-model-provider": provider}}
    if extra_body:
        options["additional_body_parameters"] = extra_body
    return options


def result_pairs(response):
    """Normalize a Cohere rerank response into (index, score) tuples."""
    return [(r.index, r.relevance_score) for r in response.results]


class TestCohereRerank:
    """Rerank via the Cohere SDK through Bifrost."""

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("rerank")
    )
    def test_01_rerank_string_documents(self, cohere_client, provider, model):
        """Plain string documents - the form every rerank SDK sends."""
        skip_if_no_api_key(provider)

        response = cohere_client.rerank(
            model=model,
            query=RERANK_QUERY,
            documents=RERANK_DOCUMENTS,
            request_options=provider_options(provider),
        )

        assert response.results, f"no results from {format_provider_model(provider, model)}"
        assert_valid_rerank_results(result_pairs(response))

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("rerank")
    )
    def test_02_rerank_object_documents(self, cohere_client, provider, model):
        """Object documents: Cohere ranks only `text`, other keys ride along.

        Documents are requested back so the id and metadata are actually checked - asserting
        the ranking alone would pass even if a converter kept `text` and dropped the rest.
        """
        skip_if_no_api_key(provider)

        documents = rerank_documents_as_objects()
        response = cohere_client.rerank(
            model=model,
            query=RERANK_QUERY,
            documents=documents,
            request_options=provider_options(provider, return_documents=True),
        )

        assert_valid_rerank_results(result_pairs(response))

        for result in response.results:
            assert result.document == documents[result.index], (
                f"document at index {result.index} did not round-trip: "
                f"got {result.document!r}, sent {documents[result.index]!r}"
            )

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("rerank")
    )
    def test_03_rerank_top_n(self, cohere_client, provider, model):
        """top_n truncates the ranking without disturbing the ordering."""
        skip_if_no_api_key(provider)

        response = cohere_client.rerank(
            model=model,
            query=RERANK_QUERY,
            documents=RERANK_DOCUMENTS,
            top_n=2,
            request_options=provider_options(provider),
        )

        assert_valid_rerank_results(result_pairs(response), expected_count=2)

    def test_04_rerank_return_documents(self, cohere_client):
        """return_documents echoes the document back on each result.

        The v2 SDK has no return_documents parameter, so it is passed through as an
        additional body parameter the way a raw Cohere caller would send it.
        """
        skip_if_no_api_key("cohere")

        response = cohere_client.rerank(
            model="rerank-v3.5",
            query=RERANK_QUERY,
            documents=RERANK_DOCUMENTS,
            request_options=provider_options("cohere", return_documents=True),
        )

        assert_valid_rerank_results(result_pairs(response))
        top = response.results[0]
        assert top.document is not None, "return_documents did not echo the document"

        # The v2 SDK does not model the document field, so it arrives untyped. Cohere always
        # returns it as an object, never a bare string - a string here means Bifrost collapsed
        # the shape and the typed SDKs would break on it.
        document = top.document
        assert isinstance(document, dict), (
            f"document must be an object, got {type(document).__name__}: {document!r}"
        )
        assert document["text"] == RERANK_DOCUMENTS[0]

    def test_05_rerank_error_shape(self, cohere_client):
        """An invalid request surfaces as a Cohere SDK error, not a raw gateway error."""
        skip_if_no_api_key("cohere")

        with pytest.raises(Exception) as exc_info:
            cohere_client.rerank(
                model="rerank-v3.5",
                query="",
                documents=RERANK_DOCUMENTS,
                request_options=provider_options("cohere"),
            )

        # The Cohere SDK only raises its typed errors when the body carries Cohere's
        # {"message": ...} shape, so this asserts Bifrost converted the error too.
        assert isinstance(exc_info.value, cohere.core.ApiError), (
            f"expected a Cohere ApiError, got {type(exc_info.value).__name__}: {exc_info.value}"
        )


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
