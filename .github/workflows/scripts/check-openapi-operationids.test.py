#!/usr/bin/env python3
"""Tests for check-openapi-operationids.py. Run: python3 check-openapi-operationids.test.py"""

import importlib.util
import unittest
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "check_openapi_operationids", Path(__file__).with_name("check-openapi-operationids.py")
)
check = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(check)


def spec(**paths):
    return {"paths": paths}


class ExposedOperationIDs(unittest.TestCase):
    def test_only_current_oss_api_operations_count(self):
        s = spec(**{
            "/api/providers": {"get": {"operationId": "listProviders"}, "parameters": []},
            "/api/teams": {"get": {"operationId": "listTeamsLegacy", "deprecated": True}},
            "/v1/chat/completions": {"post": {"operationId": "chatCompletion"}},
            "/api/version": {"get": {}},
            "/api/governance/users": {"get": {"operationId": "listUsers", "x-mint": {"metadata": {"tag": "Enterprise"}}}},
        })
        self.assertEqual(check.exposed_operation_ids(s), {"listProviders"})


class UnannouncedRemovals(unittest.TestCase):
    base = {"listProviders", "getProvider", "deleteProvider"}

    def test_nothing_gone(self):
        self.assertEqual(check.unannounced_removals(self.base, self.base | {"newOne"}, ""), [])

    def test_removal_without_note_fails(self):
        self.assertEqual(check.unannounced_removals(self.base, {"listProviders"}, ""), ["deleteProvider", "getProvider"])

    def test_note_under_breaking_heading_announces(self):
        changelog = "## 🚨 Breaking Changes\n\n- `getProvider` was renamed to `fetchProvider`.\n\n## Features\n- `deleteProvider` docs\n"
        # Named under the breaking heading: announced. Named only under Features: not.
        self.assertEqual(check.unannounced_removals(self.base, {"listProviders"}, changelog), ["deleteProvider"])

    def test_names_match_whole_words_only(self):
        changelog = "### Breaking\n- `getProviders` was removed\n"
        self.assertEqual(check.unannounced_removals({"getProvider"}, set(), changelog), ["getProvider"])


class BreakingSections(unittest.TestCase):
    def test_collects_every_breaking_section(self):
        text = "# Release\n## Breaking A\nfirst\n## Other\nskip\n### breaking B\nsecond\n"
        self.assertEqual(check.breaking_sections(text).split(), ["first", "second"])


if __name__ == "__main__":
    unittest.main()
