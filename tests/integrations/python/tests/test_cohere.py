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

Note: Tests automatically skip for providers that don't support rerank.
"""

import os

import cohere
import pytest

from .utils.common import (
    RERANK_DOCUMENTS,
    RERANK_QUERY,
    Config,
    assert_valid_rerank_results,
    rerank_documents_as_objects,
    skip_if_no_api_key,
)
from .utils.config_loader import get_integration_url
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
