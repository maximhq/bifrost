#!/usr/bin/env python3
"""Generate the model-catalog wiring Postman collection.

This collection exercises the path

    HTTP mutation -> Config write -> server-side catalog hook -> read endpoint

against a real upstream provider. Every scenario stands up an isolated custom
provider (one whose name is namespaced by a per-run identifier), drives a
sequence of provider/key mutations through the management API, and asserts that
the model catalog read endpoints reflect each mutation. Because the catalog's
live-model cache is populated asynchronously (the key hooks dispatch list-models
fetches in background goroutines), every post-mutation read polls with
exponential backoff instead of asserting synchronously.

The output is machine-generated. Edit this script and re-run it; do not hand-edit
the JSON.

    python3 build-model-catalog-wiring-collection.py

writes ../collections/bifrost-model-catalog-wiring.postman_collection.json
"""

from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from typing import Dict, List, Optional

# --------------------------------------------------------------------------- #
# Constants
# --------------------------------------------------------------------------- #

# Scenarios route through a *custom* provider whose upstream is real OpenAI, so
# each scenario can own a uniquely named provider that parallel runs never
# collide on (a standard provider has a fixed name and cannot be namespaced).
BASE_PROVIDER_TYPE = "openai"
UPSTREAM_BASE_URL = "https://api.openai.com"
API_KEY_VAR = "openai_api_key"
REQUIRED_ENV_VARS = [API_KEY_VAR]

# Stable models from the upstream /v1/models listing used in subset assertions.
# Both are long-lived; assertions are subset/superset only so an upstream model
# list that grows or shrinks around these does not break the suite.
MODEL_A = "gpt-4o"
MODEL_B = "gpt-4o-mini"
# Cheapest model for the one inference call the suite makes.
INFERENCE_MODEL = "gpt-4o-mini"

# Resource-name prefix. Full provider name at runtime is
# catwiring-openai-<scenario-id>-<run-id>, where run-id is built once per run in
# the collection pre-request from {{e2e_seed_prefix}} plus a timestamp nonce.
NAME_PREFIX = "catwiring-openai-"

MAX_POLL_ATTEMPTS = 8

OUTPUT_PATH = os.path.join(
    os.path.dirname(__file__),
    "..",
    "collections",
    "bifrost-model-catalog-wiring.postman_collection.json",
)


# --------------------------------------------------------------------------- #
# Scenario DSL
# --------------------------------------------------------------------------- #


@dataclass
class KeySpec:
    """A provider key as carried through add/update mutations.

    Update steps resend the full intended key state (value, models, enabled,
    aliases) because the management API treats a key PUT as a full replacement
    of those fields, and rejects an empty credential value for OpenAI.
    """

    id: str
    models: List[str] = field(default_factory=list)
    blacklisted_models: List[str] = field(default_factory=list)
    enabled: bool = True
    aliases: Dict[str, str] = field(default_factory=dict)

    def body(self) -> dict:
        out: dict = {
            "id": self.id,
            "value": "{{" + API_KEY_VAR + "}}",
            "models": list(self.models),
            "enabled": self.enabled,
        }
        if self.blacklisted_models:
            out["blacklisted_models"] = list(self.blacklisted_models)
        if self.aliases:
            # Legacy string wire shape: {"alias-name": "wire-model-id"}.
            out["aliases"] = dict(self.aliases)
        return out


class Step:
    """Base step type. Subclasses expand into one or more Postman items."""


@dataclass
class AddProvider(Step):
    initial_keys: List[KeySpec] = field(default_factory=list)


@dataclass
class UpdateProvider(Step):
    """Re-PUT the provider config (keys are managed via the key endpoints)."""


@dataclass
class DeleteProvider(Step):
    pass


@dataclass
class AddKey(Step):
    key: KeySpec


@dataclass
class UpdateKey(Step):
    key: KeySpec


@dataclass
class DeleteKey(Step):
    id: str


@dataclass
class AssertListModels(Step):
    expected_subset: List[str] = field(default_factory=list)
    expected_superset: List[str] = field(default_factory=list)
    expected_not_present: List[str] = field(default_factory=list)
    expected_empty: bool = False
    wait_seconds: float = 0.0
    label: str = "models reflect mutation"


@dataclass
class AssertInference(Step):
    """Drive an inference call and assert the routed/resolved model.

    request_model is the model field sent to /v1/chat/completions (without the
    provider prefix, which the generator prepends). expected_resolved is the
    substring the upstream is expected to echo back in response.model — proving
    the alias resolved to the underlying wire model rather than being rejected.
    """

    request_model: str
    expected_resolved: str
    wait_seconds: float = 2.0
    label: str = "inference routes to expected model"


@dataclass
class Cleanup(Step):
    """Explicit teardown marker. The generator always emits a cleanup folder
    per scenario regardless; placing this in a step list is a no-op that keeps
    the intent visible in the spec."""


@dataclass
class Wait(Step):
    seconds: float


@dataclass
class Scenario:
    id: str
    title: str
    description: str
    steps: List[Step]


# --------------------------------------------------------------------------- #
# JavaScript builders
# --------------------------------------------------------------------------- #


def _js_provider_name(sid: str) -> str:
    """JS expression evaluating to the runtime provider name for scenario sid."""
    return "'" + NAME_PREFIX + sid + "-' + pm.variables.get('run_id')"


def collection_prerequest() -> List[str]:
    return [
        "// Build a single run identifier for the whole run so every resource",
        "// created below is namespaced and parallel runs never collide.",
        "if (!pm.collectionVariables.get('run_id')) {",
        "  var seed = pm.variables.get('e2e_seed_prefix') || pm.environment.get('e2e_seed_prefix') || 'local';",
        "  var nonce = Date.now().toString(36) + '-' + Math.floor(Math.random() * 1000000).toString(36);",
        "  pm.collectionVariables.set('run_id', seed + '-' + nonce);",
        "  console.log('run_id = ' + pm.collectionVariables.get('run_id'));",
        "}",
    ]


def folder_prerequest() -> List[str]:
    required_json = json.dumps(REQUIRED_ENV_VARS)
    return [
        "// Skip the scenario when its required credentials are absent, so a",
        "// developer can run with whatever providers they have configured.",
        "var required = " + required_json + ";",
        "for (var i = 0; i < required.length; i++) {",
        "  var v = required[i];",
        "  if (!pm.variables.get(v) && !pm.environment.get(v)) {",
        "    console.log('SKIP: missing ' + v);",
        "    pm.execution.skipRequest();",
        "    return;",
        "  }",
        "}",
    ]


def poll_prerequest(wait_seconds: float) -> List[str]:
    wait_ms = int(wait_seconds * 1000)
    lines = [
        "var pollKey = '__poll_' + pm.info.requestName;",
        "var attempt = parseInt(pm.collectionVariables.get(pollKey) || '0', 10);",
        "pm.collectionVariables.set('__cur_poll_key', pollKey);",
        "pm.collectionVariables.set('__cur_poll_attempt', String(attempt));",
    ]
    if wait_ms > 0:
        lines += [
            "if (attempt === 0) {",
            "  var __ws = Date.now();",
            "  while (Date.now() - __ws < " + str(wait_ms) + ") { /* initial settle */ }",
            "}",
        ]
    return lines


def poll_test(testname: str, assert_lines: List[str], cleanup_name: str) -> List[str]:
    """Wrap a throwing assertion in the exponential-backoff retry loop.

    Only the terminal attempt (success, or failure after MAX_POLL_ATTEMPTS)
    records a pm.test, so Newman's exit code reflects the real outcome while
    intermediate retries stay silent. On terminal failure we jump to the
    scenario cleanup so a wedged scenario tears its resources down instead of
    burning retries on every remaining step.
    """
    out = [
        "var maxAttempts = " + str(MAX_POLL_ATTEMPTS) + ";",
        "var pollKey = pm.collectionVariables.get('__cur_poll_key');",
        "var attempt = parseInt(pm.collectionVariables.get('__cur_poll_attempt') || '0', 10);",
        "var cleanupReq = " + json.dumps(cleanup_name) + ";",
        "function assertNow() {",
    ]
    out += ["  " + l for l in assert_lines]
    out += [
        "}",
        "var ok = true, errMsg = '';",
        "try { assertNow(); } catch (e) { ok = false; errMsg = e.message; }",
        "if (ok) {",
        "  pm.collectionVariables.set(pollKey, '0');",
        "  pm.test(" + json.dumps(testname) + ", function () { pm.expect(true, 'assertion satisfied').to.be.true; });",
        "} else if (attempt < maxAttempts) {",
        "  pm.collectionVariables.set(pollKey, String(attempt + 1));",
        "  var sleepMs = Math.min(250 * Math.pow(2, attempt), 4000);",
        "  var start = Date.now();",
        "  while (Date.now() - start < sleepMs) { /* backoff */ }",
        "  pm.execution.setNextRequest(pm.info.requestName);",
        "} else {",
        "  pm.collectionVariables.set(pollKey, '0');",
        "  pm.test(" + json.dumps(testname) + ", function () { throw new Error(errMsg); });",
        "  pm.execution.setNextRequest(cleanupReq);",
        "}",
    ]
    return out


def mutation_test(testname: str, acceptable: List[int], cleanup_name: str) -> List[str]:
    return [
        "var acceptable = " + json.dumps(acceptable) + ";",
        "var cleanupReq = " + json.dumps(cleanup_name) + ";",
        "pm.test(" + json.dumps(testname) + ", function () {",
        "  pm.expect(acceptable, 'status ' + pm.response.code + ' body ' + pm.response.text()).to.include(pm.response.code);",
        "});",
        "if (acceptable.indexOf(pm.response.code) < 0) { pm.execution.setNextRequest(cleanupReq); }",
    ]


def cleanup_test(testname: str) -> List[str]:
    return [
        "pm.test(" + json.dumps(testname) + ", function () {",
        "  pm.expect([200, 204, 404], 'cleanup status ' + pm.response.code).to.include(pm.response.code);",
        "});",
    ]


def list_models_assert_lines(sid: str, step: AssertListModels) -> List[str]:
    return [
        "var providerName = " + _js_provider_name(sid) + ";",
        "var expectSubset = " + json.dumps(step.expected_subset) + ";",
        "var expectSuperset = " + json.dumps(step.expected_superset) + ";",
        "var expectAbsent = " + json.dumps(step.expected_not_present) + ";",
        "var expectEmpty = " + ("true" if step.expected_empty else "false") + ";",
        "if (pm.response.code !== 200) { throw new Error('list models status ' + pm.response.code); }",
        "var body = pm.response.json();",
        "var names = (body.models || []).filter(function (m) { return m.provider === providerName; })",
        "  .map(function (m) { return m.name; });",
        "if (expectEmpty && names.length !== 0) {",
        "  throw new Error('expected no models for ' + providerName + ' but got ' + JSON.stringify(names));",
        "}",
        "expectSubset.concat(expectSuperset).forEach(function (m) {",
        "  if (names.indexOf(m) < 0) { throw new Error('expected model ' + m + ' in ' + JSON.stringify(names)); }",
        "});",
        "expectAbsent.forEach(function (m) {",
        "  if (names.indexOf(m) >= 0) { throw new Error('expected model ' + m + ' absent but found in ' + JSON.stringify(names)); }",
        "});",
    ]


def inference_assert_lines(step: AssertInference) -> List[str]:
    return [
        "if (pm.response.code !== 200) { throw new Error('inference status ' + pm.response.code + ' body ' + pm.response.text()); }",
        "var body = pm.response.json();",
        "if (!body.choices || body.choices.length === 0) { throw new Error('inference returned no choices'); }",
        "var used = body.model || '';",
        "if (used.indexOf(" + json.dumps(step.expected_resolved) + ") < 0) {",
        "  throw new Error('expected resolved model " + step.expected_resolved + " in response.model=' + used);",
        "}",
    ]


# --------------------------------------------------------------------------- #
# Postman item builders
# --------------------------------------------------------------------------- #


def _url(path_segments: List[str], query: Optional[List[Dict[str, str]]] = None) -> dict:
    raw = "{{base_url}}/" + "/".join(path_segments)
    if query:
        raw += "?" + "&".join(q["key"] + "=" + q["value"] for q in query)
    url: dict = {
        "raw": raw,
        "host": ["{{base_url}}"],
        "path": list(path_segments),
    }
    if query:
        url["query"] = query
    return url


def _events(prerequest: Optional[List[str]], test: Optional[List[str]]) -> List[dict]:
    events: List[dict] = []
    if prerequest is not None:
        events.append(
            {"listen": "prerequest", "script": {"type": "text/javascript", "exec": prerequest}}
        )
    if test is not None:
        events.append({"listen": "test", "script": {"type": "text/javascript", "exec": test}})
    return events


def _request(method: str, url: dict, body: Optional[dict]) -> dict:
    req: dict = {"method": method, "header": [], "url": url}
    if body is not None:
        req["header"].append({"key": "Content-Type", "value": "application/json"})
        req["body"] = {"mode": "raw", "raw": json.dumps(body)}
    return req


def _item(item_id: str, name: str, request: dict, events: List[dict]) -> dict:
    return {"id": item_id, "name": name, "event": events, "request": request}


def _provider_path_segment(sid: str) -> str:
    # The {{run_id}} placeholder is interpolated by Newman at request time.
    return NAME_PREFIX + sid + "-{{run_id}}"


# --------------------------------------------------------------------------- #
# Step expansion
# --------------------------------------------------------------------------- #


def expand_scenario(scenario: Scenario) -> dict:
    sid = scenario.id
    provider_seg = _provider_path_segment(sid)
    cleanup_name = "cleanup: delete provider [" + sid + "]"
    items: List[dict] = []
    counter = [0]
    # The ordinal pairs the display name with its id and, more importantly,
    # keeps every request name unique across the collection — the poll loop
    # re-runs itself via setNextRequest(pm.info.requestName), which resolves by
    # name, so two steps sharing a name would cross-wire.
    ordinal = [0]

    def next_id(tag: str) -> str:
        counter[0] += 1
        return "catwiring-" + sid + "-" + str(counter[0]).zfill(2) + "-" + tag

    def uniq(label: str) -> str:
        ordinal[0] += 1
        return str(ordinal[0]).zfill(2) + ". " + label + " [" + sid + "]"

    for step in scenario.steps:
        if isinstance(step, AddProvider):
            body = {
                "provider": provider_seg,
                "custom_provider_config": {
                    "base_provider_type": BASE_PROVIDER_TYPE,
                    "is_key_less": False,
                },
                "network_config": {"base_url": UPSTREAM_BASE_URL},
            }
            name = uniq("add provider")
            items.append(
                _item(
                    next_id("add-provider"),
                    name,
                    _request("POST", _url(["api", "providers"]), body),
                    _events(None, mutation_test(name, [200, 201], cleanup_name)),
                )
            )
            for key in step.initial_keys:
                items.append(_build_add_key(sid, provider_seg, cleanup_name, next_id, uniq, key))

        elif isinstance(step, UpdateProvider):
            body = {
                "keys": [],
                "network_config": {"base_url": UPSTREAM_BASE_URL},
                "concurrency_and_buffer_size": {"concurrency": 1000, "buffer_size": 5000},
                "custom_provider_config": {
                    "base_provider_type": BASE_PROVIDER_TYPE,
                    "is_key_less": False,
                },
            }
            name = uniq("update provider config")
            items.append(
                _item(
                    next_id("update-provider"),
                    name,
                    _request("PUT", _url(["api", "providers", provider_seg]), body),
                    _events(None, mutation_test(name, [200], cleanup_name)),
                )
            )

        elif isinstance(step, DeleteProvider):
            name = uniq("delete provider")
            items.append(
                _item(
                    next_id("delete-provider"),
                    name,
                    _request("DELETE", _url(["api", "providers", provider_seg]), None),
                    _events(None, mutation_test(name, [200, 204], cleanup_name)),
                )
            )

        elif isinstance(step, AddKey):
            items.append(_build_add_key(sid, provider_seg, cleanup_name, next_id, uniq, step.key))

        elif isinstance(step, UpdateKey):
            name = uniq("update key " + step.key.id)
            items.append(
                _item(
                    next_id("update-key"),
                    name,
                    _request(
                        "PUT",
                        _url(["api", "providers", provider_seg, "keys", step.key.id]),
                        step.key.body(),
                    ),
                    _events(None, mutation_test(name, [200], cleanup_name)),
                )
            )

        elif isinstance(step, DeleteKey):
            name = uniq("delete key " + step.id)
            items.append(
                _item(
                    next_id("delete-key"),
                    name,
                    _request(
                        "DELETE",
                        _url(["api", "providers", provider_seg, "keys", step.id]),
                        None,
                    ),
                    _events(None, mutation_test(name, [200, 204], cleanup_name)),
                )
            )

        elif isinstance(step, AssertListModels):
            name = uniq(step.label)
            query = [
                {"key": "provider", "value": provider_seg},
                {"key": "limit", "value": "1000"},
            ]
            items.append(
                _item(
                    next_id("assert-models"),
                    name,
                    _request("GET", _url(["api", "models"], query), None),
                    _events(
                        poll_prerequest(step.wait_seconds),
                        poll_test(name, list_models_assert_lines(sid, step), cleanup_name),
                    ),
                )
            )

        elif isinstance(step, AssertInference):
            name = uniq(step.label)
            body = {
                "model": provider_seg + "/" + step.request_model,
                "messages": [{"role": "user", "content": "Reply with the single word: ok."}],
                "max_tokens": 5,
            }
            items.append(
                _item(
                    next_id("assert-inference"),
                    name,
                    _request("POST", _url(["v1", "chat", "completions"]), body),
                    _events(
                        poll_prerequest(step.wait_seconds),
                        poll_test(name, inference_assert_lines(step), cleanup_name),
                    ),
                )
            )

        elif isinstance(step, Wait):
            name = uniq("wait " + str(step.seconds) + "s")
            wait_ms = int(step.seconds * 1000)
            pre = [
                "var __ws = Date.now();",
                "while (Date.now() - __ws < " + str(wait_ms) + ") { /* settle */ }",
            ]
            items.append(
                _item(
                    next_id("wait"),
                    name,
                    _request("GET", _url(["health"]), None),
                    _events(pre, None),
                )
            )

        elif isinstance(step, Cleanup):
            # Teardown is always appended below; nothing to emit inline.
            continue

        else:  # pragma: no cover - guards against an unhandled step type
            raise ValueError("unknown step type: " + type(step).__name__)

    # Cleanup folder — always deletes the scenario's provider, which cascades to
    # its keys. Accepts already-deleted (404) so it passes whether the scenario
    # reached its own DeleteProvider step or jumped here mid-flight.
    cleanup_item = _item(
        "catwiring-" + sid + "-cleanup",
        cleanup_name,
        _request("DELETE", _url(["api", "providers", provider_seg]), None),
        _events(None, cleanup_test(cleanup_name)),
    )
    cleanup_folder = {"name": "Cleanup", "item": [cleanup_item]}

    return {
        "name": scenario.title,
        "description": scenario.description,
        "item": items + [cleanup_folder],
        "event": _events(folder_prerequest(), None),
    }


def _build_add_key(sid, provider_seg, cleanup_name, next_id, uniq, key: KeySpec) -> dict:
    name = uniq("add key " + key.id)
    return _item(
        next_id("add-key"),
        name,
        _request("POST", _url(["api", "providers", provider_seg, "keys"]), key.body()),
        _events(None, mutation_test(name, [200, 201], cleanup_name)),
    )


# --------------------------------------------------------------------------- #
# Scenarios — six canonical wiring contracts, all on real OpenAI
# --------------------------------------------------------------------------- #

SCENARIOS: List[Scenario] = [
    Scenario(
        id="add-provider-and-key",
        title="Add provider and key surfaces models",
        description="A fresh provider plus one gated key surfaces that key's allowed model in the catalog.",
        steps=[
            AddProvider(),
            AddKey(KeySpec(id="k1", models=[MODEL_A], enabled=True)),
            AssertListModels(expected_subset=[MODEL_A], wait_seconds=2, label="key model appears in catalog"),
            Cleanup(),
        ],
    ),
    Scenario(
        id="update-key-models",
        title="Updating key model set re-gates the catalog",
        description="Changing a key's allow-list invalidates the stale live entry and re-gates the surfaced models.",
        steps=[
            AddProvider(initial_keys=[KeySpec(id="k1", models=[MODEL_A], enabled=True)]),
            AssertListModels(expected_subset=[MODEL_A], wait_seconds=2, label="initial model gated in"),
            UpdateKey(KeySpec(id="k1", models=[MODEL_B], enabled=True)),
            AssertListModels(
                expected_subset=[MODEL_B],
                expected_not_present=[MODEL_A],
                wait_seconds=2,
                label="new model gated in, old gated out",
            ),
            Cleanup(),
        ],
    ),
    Scenario(
        id="disable-reenable-key",
        title="Disabled key drops models, re-enable restores",
        description="A disabled key is skipped during aggregation; re-enabling it brings its models back.",
        steps=[
            AddProvider(initial_keys=[KeySpec(id="k1", models=[MODEL_A], enabled=True)]),
            AssertListModels(expected_subset=[MODEL_A], wait_seconds=2, label="model present while enabled"),
            UpdateKey(KeySpec(id="k1", models=[MODEL_A], enabled=False)),
            AssertListModels(expected_not_present=[MODEL_A], wait_seconds=2, label="model gone while disabled"),
            UpdateKey(KeySpec(id="k1", models=[MODEL_A], enabled=True)),
            AssertListModels(expected_subset=[MODEL_A], wait_seconds=2, label="model returns when re-enabled"),
            Cleanup(),
        ],
    ),
    Scenario(
        id="delete-key-sibling-survives",
        title="Deleting one key leaves sibling models intact",
        description="With two keys gating distinct models, deleting one removes only its models; the sibling's survive.",
        steps=[
            AddProvider(
                initial_keys=[
                    KeySpec(id="k1", models=[MODEL_A], enabled=True),
                    KeySpec(id="k2", models=[MODEL_B], enabled=True),
                ]
            ),
            AssertListModels(expected_superset=[MODEL_A, MODEL_B], wait_seconds=2, label="both keys' models present"),
            DeleteKey(id="k1"),
            AssertListModels(
                expected_subset=[MODEL_B],
                expected_not_present=[MODEL_A],
                wait_seconds=2,
                label="sibling model survives delete",
            ),
            Cleanup(),
        ],
    ),
    Scenario(
        id="delete-provider",
        title="Deleting provider removes it from the catalog",
        description="Removing the provider drops all of its models from the catalog read endpoints.",
        steps=[
            AddProvider(initial_keys=[KeySpec(id="k1", models=[MODEL_A], enabled=True)]),
            AssertListModels(expected_subset=[MODEL_A], wait_seconds=2, label="model present before delete"),
            DeleteProvider(),
            AssertListModels(expected_empty=True, wait_seconds=1, label="no models after provider delete"),
            Cleanup(),
        ],
    ),
    Scenario(
        id="alias-resolution",
        title="Alias resolves to underlying model at inference",
        description="A key alias routes an inference request to the underlying wire model rather than being rejected.",
        steps=[
            AddProvider(
                initial_keys=[
                    KeySpec(
                        id="k1",
                        models=[INFERENCE_MODEL],
                        enabled=True,
                        aliases={"catwiring-alias-{{run_id}}": INFERENCE_MODEL},
                    )
                ]
            ),
            AssertListModels(expected_subset=[INFERENCE_MODEL], wait_seconds=2, label="aliased key model present"),
            AssertInference(
                request_model="catwiring-alias-{{run_id}}",
                expected_resolved=INFERENCE_MODEL,
                wait_seconds=2,
                label="alias resolves to underlying model",
            ),
            Cleanup(),
        ],
    ),
]


# --------------------------------------------------------------------------- #
# Assembly
# --------------------------------------------------------------------------- #


def build_collection() -> dict:
    return {
        "info": {
            "_postman_id": "bifrost-model-catalog-wiring",
            "name": "Bifrost Model Catalog Wiring",
            "description": (
                "End-to-end wiring tests between the management API and the model "
                "catalog. Each scenario stands up an isolated, run-namespaced custom "
                "provider backed by real OpenAI, mutates its providers/keys, and "
                "asserts the catalog read endpoints reflect each mutation. Reads that "
                "depend on the asynchronously populated live-model cache poll with "
                "exponential backoff. Machine-generated by "
                "runners/build-model-catalog-wiring-collection.py — do not hand-edit."
            ),
            "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
        },
        "variable": [
            {"key": "base_url", "value": "http://localhost:8080", "type": "string"},
            {"key": "run_id", "value": "", "type": "string"},
        ],
        "event": _events(collection_prerequest(), None),
        "item": [expand_scenario(s) for s in SCENARIOS],
    }


def main() -> None:
    collection = build_collection()
    out_path = os.path.abspath(OUTPUT_PATH)
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(collection, f, indent=2)
        f.write("\n")
    scenario_count = len(SCENARIOS)
    print("wrote " + out_path)
    print("scenarios: " + str(scenario_count))
    for s in SCENARIOS:
        print("  - " + s.id)


if __name__ == "__main__":
    main()
