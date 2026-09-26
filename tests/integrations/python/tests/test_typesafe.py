"""
Typesafe Integration Tests - Official SDK against Bifrost

🌉 SDK DROP-IN TESTING:
This test suite uses the official TypeSafe Python SDK (typesafe-sdk) pointed at
Bifrost's /typesafe prefix via base_url, so a client written against
api.typesafe.ai must work unchanged through Bifrost. Every call in this file
goes through the SDK - no raw HTTP.

Covered scenarios:
1. system_one with all three question types (Noul, Choice, Score)
2. Structured (JSON) state and per-answer metadata (confidence, probabilities, legend)
3. Model alias resolution (jev-latest resolves to the versioned model)
4. client.models.list() against Bifrost's synthesized native listing
5. SDK exception parsing of Bifrost's native error body (TypeSafeBadRequestError)
"""

import json
import math
import os
import time

import pytest
import requests
from typesafe_sdk import (
    Choice,
    Noul,
    Score,
    TypeSafeBadRequestError,
    TypeSafeClient,
)

from .utils.common import get_bifrost_base_url
from .utils.config_loader import get_config

STATE = (
    "Customer message: I was double charged last month and nobody replied "
    "to my two emails. I want a refund today or I am cancelling."
)


@pytest.fixture
def typesafe_client():
    """Official TypeSafe SDK client pointed at Bifrost's /typesafe drop-in.

    Authenticates to Bifrost with the suite's virtual key via the x-bf-vk
    header (the cross-provider convention); Bifrost injects the real upstream
    key, so the SDK's own api_key never reaches TypeSafe.
    """
    config = get_config()
    headers = {}
    vk = config.get_virtual_key() if config.is_virtual_key_configured() else ""
    if vk:
        headers["x-bf-vk"] = vk
    client = TypeSafeClient(
        base_url=f"{get_bifrost_base_url()}/typesafe",
        api_key=vk or "dummy-key-bifrost-injects-the-real-one",
        headers=headers or None,
    )
    yield client
    client.close()


class TestTypesafeSystemOne:
    def test_01_all_question_types(self, typesafe_client):
        result = typesafe_client.system_one(
            STATE,
            {
                "is_frustrated": Noul(instructions="Is the customer frustrated?"),
                "category": Choice(
                    instructions="Pick the ticket category",
                    criteria={"billing": "charges and refunds", "bug": "product defects", "other": "anything else"},
                ),
                "urgency": Score(
                    instructions="Rate how urgently this needs a human reply",
                    criteria=["can wait a week", "should be answered soon", "needs a reply today"],
                ),
            },
            model="jev-1.13.0",
        )

        assert result.model == "jev-1.13.0"
        assert 0.0 <= result.nouls["is_frustrated"].noul <= 1.0
        assert result.choices["category"].choice in {"billing", "bug", "other"}
        assert isinstance(result.scores["urgency"].score, (int, float))
        assert result.usage.input_tokens > 0

    def test_02_structured_state_and_answer_metadata(self, typesafe_client):
        result = typesafe_client.system_one(
            {"ticket": {"id": 4211, "body": "The export button crashes the app every time."}, "user_tier": "pro"},
            {
                "area": Choice(
                    instructions="Which product area does the complaint target?",
                    criteria={"camera": "capture", "stability": "crashes", "support": "service"},
                ),
                "priority": Score(
                    instructions="Bug backlog rank?",
                    criteria=["backlog", "next sprint", "this sprint", "hotfix now"],
                ),
            },
            model="jev-1.13.0",
        )

        area = result.choices["area"]
        assert area.choice in {"camera", "stability", "support"}
        assert area.probabilities is not None and abs(sum(area.probabilities.values()) - 1.0) < 0.05
        priority = result.scores["priority"]
        assert priority.legend is not None and len(priority.legend) == 4

    def test_03_model_alias_resolves(self, typesafe_client):
        result = typesafe_client.system_one(
            "Reply: Sure, sounds good, see you at 3pm.",
            {"is_confirmation": Noul(instructions="Does this reply confirm the meeting?")},
            model="jev-latest",
        )
        # Aliases resolve upstream; the response reports the versioned model.
        assert result.model.startswith("jev-")
        assert result.model != "jev-latest"

    def test_04_structured_criteria_type_matrix(self, typesafe_client):
        # The API types criteria descriptions as string | object | array for
        # noul keys and score levels, plus null for choice options. One call
        # covers every allowed type in every slot; Bifrost must forward all of
        # them losslessly instead of rejecting non-string descriptions.
        result = typesafe_client.system_one(
            STATE,
            {
                "noul_obj_arr": Noul(
                    instructions="Is the customer frustrated?",
                    criteria={
                        "true": {"meaning": "clearly upset", "signals": ["threats", "caps"]},
                        "false": ["calm", "neutral tone"],
                    },
                ),
                "noul_arr_obj": Noul(
                    instructions="Does the customer ask for a refund?",
                    criteria={
                        "true": ["asks for money back", "mentions refund"],
                        "false": {"meaning": "no refund language"},
                    },
                ),
                "noul_str": Noul(
                    instructions="Does the customer threaten to cancel?",
                    criteria={"true": "cancellation is threatened", "false": "no cancellation language"},
                ),
                "category": Choice(
                    instructions="Pick the ticket category",
                    criteria={
                        "billing": {"rubric": "charges and refunds", "examples": ["double charge"]},
                        "bug": ["crash", "product defect"],
                        "support": "service questions",
                        "other": None,
                    },
                ),
                "urgency": Score(
                    instructions="Rate how urgently this needs a human reply",
                    criteria=[
                        "can wait a week",
                        {"level": "should be answered soon"},
                        ["needs a reply today", "churn risk"],
                    ],
                ),
            },
            model="jev-1.13.0",
        )

        for name in ("noul_obj_arr", "noul_arr_obj", "noul_str"):
            assert 0.0 <= result.nouls[name].noul <= 1.0
        assert result.choices["category"].choice in {"billing", "bug", "support", "other"}
        urgency = result.scores["urgency"]
        assert isinstance(urgency.score, (int, float))
        # The legend echoes each level's description verbatim - structured
        # levels come back as objects/arrays, not stringified.
        assert urgency.legend is not None and len(urgency.legend) == 3
        assert urgency.legend[0] == "can wait a week"
        assert urgency.legend[1] == {"level": "should be answered soon"}
        assert urgency.legend[2] == ["needs a reply today", "churn risk"]


class TestTypesafeModels:
    def test_01_models_list(self, typesafe_client):
        listing = typesafe_client.models.list()
        names = [m.name for m in listing.models]
        assert "jev-1.13.0" in names
        assert "jev-latest" in names
        assert all("typesafe/" not in name for name in names)


class TestTypesafeErrors:
    def test_01_bad_request_parses_native_error(self, typesafe_client):
        # A choice question with empty criteria is rejected by Bifrost before
        # dispatch; the SDK must parse the native {"detail": {...}} body into
        # its 400 exception type exactly as it would against api.typesafe.ai.
        with pytest.raises(TypeSafeBadRequestError) as excinfo:
            typesafe_client.system_one(
                STATE,
                {"category": Choice(instructions="Pick one", criteria={})},
                model="jev-1.13.0",
            )
        assert "criteria" in str(excinfo.value)


# LLM fallback / emulation: any tool-capable chat model answers a decision
# request natively (primary) or as a fallback. Exercised through the official
# TypeSafe SDK pointed at an LLM model - the /typesafe drop-in stays the client.
LLM_DECISION_MODEL = "openai/gpt-4o-mini"


class TestDecisionLLMEmulation:
    def test_01_llm_primary_emulation(self, typesafe_client):
        result = typesafe_client.system_one(
            STATE,
            {
                "is_frustrated": Noul(instructions="Is the customer frustrated?"),
                "category": Choice(
                    instructions="Pick the ticket category",
                    criteria={"billing": "charges and refunds", "bug": "product defects", "other": "anything else"},
                ),
                "urgency": Score(
                    instructions="Rate urgency",
                    criteria=["low", "medium", "high"],
                ),
            },
            model=LLM_DECISION_MODEL,
        )
        # An LLM emulates the judgment via tool-calling; answers come back in the
        # native shape with the LLM-estimated confidence.
        assert 0.0 <= result.nouls["is_frustrated"].noul <= 1.0
        assert result.choices["category"].choice in {"billing", "bug", "other"}
        assert isinstance(result.scores["urgency"].score, (int, float))

    def test_02_llm_fallback(self):
        # Primary model fails at the provider; the request falls through to the
        # LLM fallback which emulates the decision. /v1/decisions is Bifrost's
        # native format (no upstream SDK), so this uses the HTTP API directly.
        config = get_config()
        headers = {"Content-Type": "application/json"}
        if config.is_virtual_key_configured():
            headers["x-bf-vk"] = config.get_virtual_key()
        response = requests.post(
            f"{get_bifrost_base_url()}/v1/decisions",
            headers=headers,
            json={
                "model": "openai/nonexistent-model-xyz",
                "fallbacks": [LLM_DECISION_MODEL],
                "state": "Reply: sounds good, see you at 3pm.",
                "questions": {"is_confirmation": {"kind": "noul", "instructions": "Does this confirm the meeting?"}},
            },
            timeout=60,
        )
        assert response.status_code == 200, response.text
        answer = response.json()["answers"]["is_confirmation"]
        assert answer["kind"] == "noul"
        assert 0.0 <= answer["value"] <= 1.0

    def test_03_native_vs_emulated_comparison(self, typesafe_client):
        # Same decision answered by the native jev judgment model and by an LLM
        # emulation; report how the two differ. Both must return well-formed
        # answers of the right kinds - the values themselves may differ, which is
        # the point of the comparison.
        questions = {
            "is_frustrated": Noul(instructions="Is the customer frustrated?"),
            "category": Choice(
                instructions="Pick the ticket category",
                criteria={"billing": "charges and refunds", "bug": "product defects", "other": "anything else"},
            ),
            "urgency": Score(instructions="Rate urgency", criteria=["low", "medium", "high"]),
        }
        native = typesafe_client.system_one(STATE, questions, model="jev-1.13.0")
        emulated = typesafe_client.system_one(STATE, questions, model=LLM_DECISION_MODEL)

        print("\n=== Decision: native jev vs LLM emulation ===")
        print(f"{'question':<14} {'kind':<8} {'jev-1.13.0':<16} {'gpt-4o-mini':<16} match")
        for name in questions:
            kind = native.answers[name].type
            if kind == "noul":
                nv, ev = native.nouls[name].noul, emulated.nouls[name].noul
            elif kind == "choice":
                nv, ev = native.choices[name].choice, emulated.choices[name].choice
            else:
                nv, ev = native.scores[name].score, emulated.scores[name].score
            print(f"{name:<14} {kind:<8} {str(nv):<16} {str(ev):<16} {'=' if nv == ev else 'DIFF'}")

        # Both engines answered every question in the right shape.
        for name, q in questions.items():
            assert native.answers[name].type == emulated.answers[name].type
        assert native.model.startswith("jev-")

    def test_04_structured_criteria_reach_the_emulating_model(self, typesafe_client):
        # The emulation encodes criteria into the forced tool schema. The choice
        # option names here are opaque codes; only the criteria descriptions
        # (object, string, null, and array shaped) carry meaning, so a correct
        # pick on a clearly-billing state proves the rubrics reached the LLM
        # rather than being dropped or rejected as non-string.
        result = typesafe_client.system_one(
            STATE,
            {
                "approve_review": Noul(
                    instructions="Should this ticket get a billing review?",
                    criteria={
                        "true": "the customer reports a concrete billing error",
                        "false": ["no billing problem is described", "general inquiry"],
                    },
                ),
                "pick": Choice(
                    instructions=(
                        "Pick the ticket category. The option names are opaque codes; "
                        "use the criteria descriptions to decide."
                    ),
                    criteria={
                        "opt_a": {"rubric": "charges and refunds"},
                        "opt_b": "product defects",
                        "opt_c": None,
                        "opt_d": ["installation help", "how-to setup questions"],
                    },
                ),
                "urgency": Score(
                    instructions="Rate how urgently this needs a human reply",
                    criteria=[
                        "can wait a week",
                        {"level": "should be answered soon"},
                        ["needs a reply today", "churn risk"],
                    ],
                ),
            },
            model=LLM_DECISION_MODEL,
        )

        assert result.nouls["approve_review"].noul >= 0.5
        pick = result.choices["pick"]
        assert pick.choice == "opt_a"
        # Probabilities are required on native choice and score answers, so the
        # emulation must always deliver them.
        assert pick.probabilities is not None and abs(sum(pick.probabilities.values()) - 1.0) < 0.05
        urgency = result.scores["urgency"]
        assert isinstance(urgency.score, (int, float))
        assert urgency.probabilities is not None and abs(sum(urgency.probabilities.values()) - 1.0) < 0.05
        # The emulated legend must match the native shape: each level's
        # description echoed verbatim (string, object, or array).
        assert urgency.legend is not None and len(urgency.legend) == 3
        assert urgency.legend[1] == {"level": "should be answered soon"}
        assert urgency.legend[2] == ["needs a reply today", "churn risk"]


# Cross-provider emulation matrix: every VK-allowed chat provider must answer a
# decision request through an LLM, for at least four current models each. The model
# list is the shared source of truth in
# tests/integrations/decision_emulation_matrix.json, read by both the Python and
# TypeScript suites and by the provider-harness generator.
#
# jev (the native typesafe judgment model) is the ground truth. Each round we take
# jev's decision on a deliberately unambiguous state, then require every reachable
# model to reach the SAME verdict: the identical choice, the same binary noul
# (rounded), and the same score level (rounded). A model that disagrees FAILS - the
# matrix exists to prove emulation matches jev, not merely that it returns a shape.
#
# Only genuine availability - a model this account cannot reach (auth, governance
# block, not-found / retired / deployment gap, rate limit, upstream connection) -
# is SKIPPED. A 400 (a request we built wrong), a 500 (emulation returned no
# decision), or a thrown request error (timeout, reset, connection failure) is
# our problem to see and FAILS.
_MATRIX_PATH = os.path.join(os.path.dirname(__file__), "..", "..", "decision_emulation_matrix.json")


def _load_matrix():
    with open(_MATRIX_PATH) as f:
        return json.load(f)


_MATRIX = _load_matrix()
_MATRIX_PAIRS = [(p, m) for p, models in _MATRIX["providers"].items() for m in models]
# One row per combination (jev included): model_id, status, agreement score,
# client-measured latency, token total, and cost. Drives the final report table.
_MATRIX_ROWS = []

# Non-200 codes that mean the model is unreachable on this account, not that our
# request or emulation is wrong. These SKIP; everything else FAILS. A provider that
# rejects a large multi-question tool schema is NOT skipped - the emulation now
# falls back to answering one question at a time (see emulateDecisionViaResponses),
# so Perplexity and similar answer the full set instead of failing.
_AVAILABILITY_CODES = {401, 403, 404, 429, 502, 503, 504}

# Some providers report an unavailable/renamed/deprecated MODEL as a 400 rather
# than a 404. That is availability, not a request we built wrong, so a 400 whose
# message matches one of these SKIPs; every other 400 (a genuinely malformed
# request) and every 500 (emulation returned no decision) still FAILS.
_AVAILABILITY_MESSAGE_MARKERS = (
    "deprecated",
    "no longer available",
    "not supported",
    "not found",
    "does not exist",
    "invalid_model",
    "model_not_found",
    "not allowed for virtual key",
)


def _is_availability(status_code, text):
    if status_code in _AVAILABILITY_CODES:
        return True
    if status_code == 400:
        low = text.lower()
        return any(marker in low for marker in _AVAILABILITY_MESSAGE_MARKERS)
    return False

# One rich decision (30 questions) is the ground-truth scenario; jev answers it and
# every reachable model must agree with jev on at least this fraction of questions.
_SCENARIO = _MATRIX["scenario"]
_MATRIX_STATE = _SCENARIO["state"]
_MATRIX_QUESTIONS = _SCENARIO["questions"]
_AGREEMENT_FAIL_THRESHOLD = _MATRIX.get("_agreement_fail_threshold", 0.7)
# Providers that reject a large multi-question decision tool schema. We do NOT split
# these up front: the batched call is tried first, and only if it fails with a
# non-availability error (Perplexity returns a bare "invalid request" when the
# 30-question schema is too large) do we retry one question at a time and merge.
_SPLIT_PROVIDERS = set(_MATRIX.get("_split_question_providers", []))
_JEV_MODEL = "typesafe/jev-1.13.0"


# The provider-side request timeout is up to 300s and the split path makes up
# to 30 sequential calls, so every matrix fetch carries one shared deadline:
# the whole _fetch_decision (batched call plus any split) must finish inside
# this budget. Mirrors the TypeScript suite's MATRIX_CALL_BUDGET_MS: a blown
# budget FAILS the case with a readable reason - never a skip, no synthetic
# statuses.
_MATRIX_CALL_BUDGET_S = 85.0


class _BudgetExceededError(Exception):
    """Raised when the shared per-model budget runs out; fails the case."""


def _remaining(deadline, model_id):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise _BudgetExceededError(f"client-side budget of {_MATRIX_CALL_BUDGET_S:.0f}s exceeded waiting on {model_id}")
    return remaining


def _post_raw(model_id, questions, deadline):
    """One POST to /v1/decisions for the given question set; returns (response, latency_ms)."""
    config = get_config()
    headers = {"Content-Type": "application/json"}
    if config.is_virtual_key_configured():
        headers["x-bf-vk"] = config.get_virtual_key()
    start = time.perf_counter()
    response = requests.post(
        f"{get_bifrost_base_url()}/v1/decisions",
        headers=headers,
        json={"model": model_id, "state": _MATRIX_STATE, "questions": questions},
        timeout=min(90.0, _remaining(deadline, model_id)),
    )
    return response, int((time.perf_counter() - start) * 1000)


def _usage_cost(body):
    """(total_tokens, cost) from a decision body; cost is '-' when the config
    computes none (this profile has no pricing wired for several providers)."""
    usage = body.get("usage") or {}
    tokens = usage.get("total_tokens")
    cost = usage.get("cost")
    if isinstance(cost, dict):
        cost = cost.get("total") if cost.get("total") is not None else cost
    return tokens, cost if cost is not None else "-"


def _fetch_decision(model_id):
    """Fetch a decision for the full scenario. Tries the batched 30-question call
    first; only for split-eligible providers, and only when that batched call fails
    with a non-availability error, retries one question at a time and merges.
    Returns a normalized dict: status, answers, latency_ms, tokens, cost, text, split."""
    deadline = time.monotonic() + _MATRIX_CALL_BUDGET_S
    resp, latency_ms = _post_raw(model_id, _MATRIX_QUESTIONS, deadline)
    if resp.status_code == 200:
        body = resp.json()
        tokens, cost = _usage_cost(body)
        return {"status": 200, "answers": body["answers"], "latency_ms": latency_ms, "tokens": tokens, "cost": cost, "text": "", "split": False}
    # Batched call failed. Availability -> report as-is (caller decides skip).
    if _is_availability(resp.status_code, resp.text):
        return {"status": resp.status_code, "answers": None, "latency_ms": latency_ms, "tokens": None, "cost": "-", "text": resp.text, "split": False}
    # Non-availability failure: split-eligible providers retry one question at a time.
    provider = model_id.split("/", 1)[0]
    if provider in _SPLIT_PROVIDERS and len(_MATRIX_QUESTIONS) > 1:
        return _fetch_decision_split(model_id, latency_ms, deadline)
    return {"status": resp.status_code, "answers": None, "latency_ms": latency_ms, "tokens": None, "cost": "-", "text": resp.text, "split": False}


def _fetch_decision_split(model_id, batched_latency_ms, deadline):
    """Answer each question in its own call and merge; latency and tokens accumulate
    (the failed batched attempt's latency is included, since it was really spent).
    The shared deadline bounds the whole sequential walk."""
    merged = {}
    total_latency = batched_latency_ms
    total_tokens = 0
    for name, q in _MATRIX_QUESTIONS.items():
        resp, latency_ms = _post_raw(model_id, {name: q}, deadline)
        total_latency += latency_ms
        if resp.status_code != 200:
            return {"status": resp.status_code, "answers": None, "latency_ms": total_latency, "tokens": None, "cost": "-", "text": resp.text, "split": True}
        body = resp.json()
        merged.update(body["answers"])
        tokens, _ = _usage_cost(body)
        if isinstance(tokens, int):
            total_tokens += tokens
    return {"status": 200, "answers": merged, "latency_ms": total_latency, "tokens": total_tokens or None, "cost": "-", "text": "", "split": True}


def _half_up(x):
    """Round half away from zero toward positive infinity, matching JS Math.round.

    Python's round() rounds half to even (0.5 -> 0, 2.5 -> 2) while the TS suite
    uses Math.round (0.5 -> 1, 2.5 -> 3); the two matrix suites must agree on the
    same verdict for identical model output, and half-up is the agreed rule.
    """
    return math.floor(x + 0.5)


def _valid_numeric(kind, x):
    """A genuine number for the kind: bools and numeric strings are wrong types,
    and a noul outside [0, 1] violates the contract regardless of rounding."""
    if isinstance(x, bool) or not isinstance(x, (int, float)):
        return False
    if kind == "noul" and not 0.0 <= x <= 1.0:
        return False
    return True


def _matches(kind, ref, got):
    """One question: choice matches exactly, noul/score match on the rounded value."""
    if kind == "choice":
        return got == ref
    if not _valid_numeric(kind, got):
        return False
    try:
        return _half_up(float(ref)) == _half_up(float(got))
    except (TypeError, ValueError):
        return False


def _agreement(reference, answers):
    """(matched, total, mismatched_names) of answers vs the jev reference across
    all 30 questions."""
    total = len(_MATRIX_QUESTIONS)
    mismatches = []
    for name, q in _MATRIX_QUESTIONS.items():
        ref = (reference.get(name) or {}).get("value")
        got = (answers.get(name) or {}).get("value")
        if not _matches(q["kind"], ref, got):
            mismatches.append(f"{name}(jev={ref},got={got})")
    return total - len(mismatches), total, mismatches


@pytest.fixture(scope="module")
def jev_reference():
    # jev is the ground truth for the whole matrix; if it cannot answer, the
    # comparison is meaningless, so fail loudly rather than skip.
    r = _fetch_decision(_JEV_MODEL)
    assert r["status"] == 200, f"jev reference decision failed: HTTP {r['status']}: {r['text']}"
    answers = r["answers"]
    assert len(answers) == len(_MATRIX_QUESTIONS), f"jev answered {len(answers)}/{len(_MATRIX_QUESTIONS)} questions"
    total = len(_MATRIX_QUESTIONS)
    _MATRIX_ROWS.append({"model_id": _JEV_MODEL, "status": "REF", "agree": total, "total": total, "latency_ms": r["latency_ms"], "tokens": r["tokens"], "cost": r["cost"], "detail": "ground truth"})
    return answers


class TestDecisionEmulationMatrix:
    @pytest.mark.parametrize("provider,model", _MATRIX_PAIRS, ids=[f"{p}/{m}" for p, m in _MATRIX_PAIRS])
    def test_provider_model_matches_jev(self, provider, model, jev_reference):
        model_id = f"{provider}/{model}"
        total = len(_MATRIX_QUESTIONS)
        try:
            r = _fetch_decision(model_id)
        except (requests.RequestException, _BudgetExceededError) as exc:
            # A thrown request error - a timeout, reset, or connection failure -
            # FAILS the case with its reason; it is never a skip. Mirrors the
            # TypeScript suite so both matrices report the same verdict for the
            # identical condition.
            _MATRIX_ROWS.append({"model_id": model_id, "status": "FAIL", "agree": 0, "total": total, "latency_ms": None, "tokens": None, "cost": "-", "detail": f"request error: {exc}"})
            pytest.fail(f"{model_id}: request error: {exc}")

        latency_ms = r["latency_ms"]
        if r["status"] != 200:
            detail = f"HTTP {r['status']}: {r['text'][:160]}"
            if _is_availability(r["status"], r["text"]):
                _MATRIX_ROWS.append({"model_id": model_id, "status": "SKIP", "agree": 0, "total": total, "latency_ms": latency_ms, "tokens": None, "cost": "-", "detail": detail})
                pytest.skip(f"{model_id}: {detail}")
            # Capability-limited providers (e.g. Perplexity's sonar rejects choice/score
            # decision schemas): the split was attempted and could not complete the set,
            # so skip rather than fail - the identical schema works on every other provider.
            if provider in _SPLIT_PROVIDERS:
                _MATRIX_ROWS.append({"model_id": model_id, "status": "SKIP", "agree": 0, "total": total, "latency_ms": latency_ms, "tokens": None, "cost": "-", "detail": "capability-limited - " + detail})
                pytest.skip(f"{model_id}: capability-limited - {detail}")
            # A malformed request we built (400) or emulation returning no decision (500): defect.
            _MATRIX_ROWS.append({"model_id": model_id, "status": "FAIL", "agree": 0, "total": total, "latency_ms": latency_ms, "tokens": None, "cost": "-", "detail": detail})
            pytest.fail(f"{model_id}: {detail}")

        answers = r["answers"]
        tokens, cost = r["tokens"], r["cost"]
        # Emulation must answer every question, each with the right kind discriminator
        # and a numeric confidence (a malformed response must not pass).
        assert len(answers) == total, f"{model_id} answered {len(answers)}/{total} questions: {answers}"
        for name, q in _MATRIX_QUESTIONS.items():
            assert answers[name].get("kind") == q["kind"], f"{name} kind = {answers[name].get('kind')}, expected {q['kind']}: {answers}"
            assert isinstance(answers[name].get("confidence"), (int, float)), f"{name} confidence not numeric: {answers}"

        matched, total, mismatches = _agreement(jev_reference, answers)
        ratio = matched / total
        row = {"model_id": model_id, "status": "PASS", "agree": matched, "total": total, "latency_ms": latency_ms, "tokens": tokens, "cost": cost, "detail": f"{matched}/{total} vs jev"}
        if ratio < _AGREEMENT_FAIL_THRESHOLD:
            row["status"] = "FAIL"
            row["detail"] = f"{matched}/{total} < {_AGREEMENT_FAIL_THRESHOLD:.0%}; diffs: " + ", ".join(mismatches[:8])
            _MATRIX_ROWS.append(row)
            pytest.fail(f"{model_id} agrees with jev on only {matched}/{total}: {', '.join(mismatches)}")
        _MATRIX_ROWS.append(row)

    def test_zzz_report(self):
        # Runs after the parametrized cases (alphabetical order within the class)
        # to print the overall table: one row per combination (jev included) with
        # its agreement score vs jev over the 30-question scenario, latency, and cost.
        def fmt(v):
            return "-" if v is None else (f"{v:.2f}" if isinstance(v, float) else str(v))

        header = f"{'combination':<50} {'score':<8} {'latency_ms':<11} {'tokens':<7} {'cost':<8} status"
        print(f"\n=== Decision emulation matrix vs jev (1 scenario, {len(_MATRIX_QUESTIONS)} questions) ===")
        print(header)
        print("-" * len(header))
        # jev pinned first as the reference, then combinations sorted by name.
        rows = sorted(_MATRIX_ROWS, key=lambda r: (r["status"] != "REF", r["model_id"]))
        for r in rows:
            score = f"{r['agree']}/{r['total']}"
            print(f"{r['model_id']:<50} {score:<8} {fmt(r['latency_ms']):<11} {fmt(r['tokens']):<7} {str(r['cost']):<8} {r['status']}")
        passed = [r for r in _MATRIX_ROWS if r["status"] == "PASS"]
        failed = [r for r in _MATRIX_ROWS if r["status"] == "FAIL"]
        skipped = [r for r in _MATRIX_ROWS if r["status"] == "SKIP"]
        print(f"\nscore = questions agreeing with jev out of {len(_MATRIX_QUESTIONS)}; PASS threshold = {_AGREEMENT_FAIL_THRESHOLD:.0%}")
        print(f"PASS={len(passed)}  FAIL={len(failed)}  SKIP={len(skipped)}  TOTAL={len(_MATRIX_PAIRS)} (+jev reference)")
