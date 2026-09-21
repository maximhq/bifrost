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

import pytest
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
