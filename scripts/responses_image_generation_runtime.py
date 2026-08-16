#!/usr/bin/env python3
"""Live Responses image_generation probe for a configured Bifrost instance.

The script deliberately uses a non-Sol Codex model by default. It reports each
mode independently because a provider may support streaming but reject unary
Responses requests for reasons unrelated to Bifrost's schema decoder.
"""

import os
import sys

import httpx
from openai import OpenAI


def run_case(client: OpenAI, model: str, stream: bool) -> tuple[bool, bool]:
    label = "stream" if stream else "non-stream"
    try:
        result = client.responses.create(
            model=model,
            input="Generate an image of a cute orange cat sitting in a coffee shop.",
            tools=[{"type": "image_generation"}],
            store=False,
            stream=stream,
        )
        if stream:
            output_types = []
            for event in result:
                event_type = getattr(event, "type", "")
                output_types.append(event_type)
            print(f"PASS {label}: {', '.join(output_types)}")
        else:
            output_types = [getattr(item, "type", "") for item in result.output]
            print(f"PASS {label}: output={output_types}")
        return True, False
    except Exception as exc:  # noqa: BLE001 - this is a diagnostic probe
        message = str(exc)
        if "failed to peek at type field" in message or "failed to unmarshal response" in message:
            print(f"FAIL {label}: Bifrost schema regression: {message}", file=sys.stderr)
            return False, True
        else:
            print(f"RESULT {label}: provider/API limitation or request error: {message}")
        return False, False


def run_image_batch_case(client: OpenAI, model: str, count: int = 2) -> tuple[bool, bool]:
    """Exercise the OpenAI Images API batch parameter through Bifrost.

    This is intentionally separate from Responses image_generation: Responses
    exposes partial previews, whereas the Images API uses ``n`` for multiple
    final images in one request.
    """
    try:
        result = client.images.generate(
            model=model,
            prompt="A simple geometric sunset over a calm ocean, minimal style.",
            n=count,
            size="1024x1024",
        )
        actual = len(result.data or [])
        if actual != count:
            print(f"FAIL images-batch: requested={count}, returned={actual}", file=sys.stderr)
            return False, False
        print(f"PASS images-batch: requested={count}, returned={actual}")
        return True, False
    except Exception as exc:  # noqa: BLE001 - diagnostic probe
        message = str(exc)
        if "failed to peek at type field" in message or "failed to unmarshal" in message:
            print(f"FAIL images-batch: Bifrost schema regression: {message}", file=sys.stderr)
            return False, True
        print(f"RESULT images-batch: provider/API limitation or request error: {message}")
        return False, False


def main() -> int:
    base_url = os.environ.get("BIFROST_BASE_URL", "http://bifrost.localdev.com/v1")
    api_key = os.environ.get("BIFROST_API_KEY")
    model = os.environ.get("BIFROST_RESPONSES_IMAGE_MODEL", "codex/gpt-5.6-luna")
    if not api_key:
        print("BIFROST_API_KEY is required", file=sys.stderr)
        return 2

    # Some older Bifrost dashboard-auth deployments require a valid session
    # cookie in addition to the virtual key. Login is opt-in and credentials
    # remain process-local; neither the cookie nor password is written out.
    root_url = base_url.removesuffix("/v1").removesuffix("/")
    transport_client = httpx.Client(
        base_url=root_url,
        timeout=httpx.Timeout(30.0, read=120.0),
    )
    admin_user = os.environ.get("BIFROST_ADMIN_USERNAME")
    admin_password = os.environ.get("BIFROST_ADMIN_PASSWORD")
    if admin_user and admin_password:
        login = transport_client.post(
            "/api/session/login",
            json={"username": admin_user, "password": admin_password},
        )
        if login.status_code >= 300:
            print(f"Bifrost admin login failed: HTTP {login.status_code}", file=sys.stderr)
            return 2

    # Route the virtual key in the Bifrost-native header. The deployed v1.5.x
    # auth middleware treats an SDK-generated Bearer header as a dashboard
    # session token, so remove that generated header before transport.
    def strip_sdk_authorization(request: httpx.Request) -> None:
        request.headers.pop("authorization", None)

    transport_client.event_hooks = {"request": [strip_sdk_authorization]}
    client = OpenAI(
        base_url=base_url,
        api_key="bifrost-sdk-placeholder",
        default_headers={"x-bf-vk": api_key},
        http_client=transport_client,
        timeout=120,
        max_retries=0,
    )
    results = [
        run_case(client, model, stream=False),
        run_case(client, model, stream=True),
        run_image_batch_case(client, model, count=2),
    ]
    # A provider capability error is useful diagnostic output, but the old
    # scalar-action parser error is a hard failure.
    return 1 if any(schema_failure for _, schema_failure in results) else 0


if __name__ == "__main__":
    raise SystemExit(main())
