#!/usr/bin/env python3
"""Regression coverage for v2.0.0 migration seed SQL; no services required."""
import re
import sqlite3
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("run-migration-tests.sh")
MISSING = {
    "config_keys": "bedrock_endpoints_json bedrock_mantle_endpoints_json",
    "config_mcp_clients": "needs_session_stickiness token_exchange_json pending_oauth_config_json",
    "governance_model_pricing": "input_cost_per_token_ultrafast output_cost_per_token_ultrafast cache_read_input_token_cost_ultrafast cache_creation_input_token_cost_ultrafast output_cost_per_image_above_4_megapixels output_cost_per_image_above_8_megapixels output_cost_per_image_above_16_megapixels output_cost_per_image_above_32_megapixels output_cost_per_image_above_64_megapixels input_cost_per_query cost_per_request",
    "logs": "user_agent app video_edit_input guardrail_debug upstream_latency overhead_latency overhead_breakdown input_cost output_cost additional_cost batch_debug",
    "mcp_tool_logs": "user_agent app plugin_logs redaction_mapping device_id app_key decision source",
    "batch_jobs": "id provider batch_id accounting_status created_at updated_at",
    "mcp_oauth_flows": "id mcp_client_id oauth_config_id state flow_mode status expires_at created_at updated_at",
    "mcp_oauth_tokens": "id auth_mode mcp_client_id oauth_config_id access_token token_type created_at updated_at",
    "notifications": "id audience severity title message expires_at created_at",
    "user_agent_mappings": "id pattern match_type app is_active created_at updated_at",
}
NEW_TABLES = {"batch_jobs", "mcp_oauth_flows", "mcp_oauth_tokens", "notifications", "user_agent_mappings"}


def generate(backend, present=True):
    functions = "\n".join(re.findall(r"^(?:append_v200_fixtures|v200_[a-z_]+)\(\) \{\n.*?^\}", SCRIPT.read_text(), re.M | re.S))
    with tempfile.TemporaryDirectory() as tmp:
        output = Path(tmp) / "faker.sql"
        output.touch()
        stub = "return 0" if present else "return 1"
        shell = functions + f"\ncolumn_exists_postgres() {{ {stub}; }}\ncolumn_exists_sqlite() {{ {stub}; }}\n"
        shell += "if declare -F append_v200_fixtures >/dev/null; then append_v200_fixtures \"$1\" \"$2\" \"'2099-01-01 00:00:00'\" \"'2099-01-02 00:00:00'\" /tmp/config.db /tmp/logs.db; fi\n"
        subprocess.run(["bash", "-eu", "-c", shell, "fixtures", backend, str(output)], check=True, capture_output=True, text=True)
        return output.read_text()


class MigrationFixturesTest(unittest.TestCase):
    def test_v200_coverage_and_sql(self):
        for backend in ("postgres", "sqlite"):
            with self.subTest(backend=backend):
                sql = generate(backend)
                covered = {}
                for table, columns in re.findall(r"INSERT INTO (\w+) \(([^)]+)\)", sql):
                    covered.setdefault(table, set()).update(c.strip() for c in columns.split(","))
                for table, column in re.findall(r"UPDATE (\w+) SET (\w+) =", sql):
                    covered.setdefault(table, set()).add(column)
                for table, columns in MISSING.items():
                    self.assertFalse(set(columns.split()) - covered.get(table, set()), table)
                # Execute every generated statement, then prove new-table fixtures
                # contain rows, rather than merely satisfying the coverage parser.
                db = sqlite3.connect(":memory:")
                for table, columns in covered.items():
                    all_columns = columns | {"id", "name", "client_id"}
                    db.execute(f"CREATE TABLE {table} (" + ", ".join(f'"{c}"' for c in all_columns) + ")")
                db.executescript(sql)
                for table in NEW_TABLES:
                    self.assertGreater(db.execute(f"SELECT count(*) FROM {table}").fetchone()[0], 0, table)
                db.close()

    def test_older_schemas_omit_unavailable_tables_and_columns(self):
        for backend in ("postgres", "sqlite"):
            self.assertEqual(generate(backend, present=False).strip(), "")


if __name__ == "__main__":
    unittest.main()
