#!/usr/bin/env python3
"""Validate CAS configuration without reading any deployed configuration."""
import copy
import json
from pathlib import Path
import jsonschema

root = Path(__file__).resolve().parents[1]
schema = json.loads((root / "transports/config.schema.json").read_text())
validator = jsonschema.Draft7Validator(schema["properties"]["logs_store"])
# Test the logs_store contract in isolation; other store config branches predate CAS.
base = {"enabled": True, "type": "sqlite", "content_addressed": {"enabled": True}}
cases = [
    (base, True),
    ({**base, "type": "postgres"}, True),
    ({**base, "type": "clickhouse"}, False),
    ({**base, "content_addressed": {"enabled": False}}, True),
    ({**base, "object_storage": {"type": "s3", "bucket": "test-only"}}, False),
    ({"type": "sqlite"}, True),
]
for value in [0, 1, 1024]:
    cases.append(({**base, "content_addressed": {"enabled": True, "min_field_bytes": value, "min_chunk_bytes": value, "exclude_fields": ["raw_request"]}}, True))
for field, value in [("min_field_bytes", -1), ("min_chunk_bytes", "256"), ("enabled", "true"), ("unknown", True), ("exclude_fields", [7])]:
    config = copy.deepcopy(base)
    config["content_addressed"][field] = value
    cases.append((config, False))
for index, (config, expected) in enumerate(cases):
    actual = validator.is_valid(config)
    assert actual == expected, f"case {index}: expected valid={expected}, got {actual}"
print(f"CAS schema: {len(cases)} cases passed")
