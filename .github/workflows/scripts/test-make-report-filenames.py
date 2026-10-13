"""Exercise generated JUnit filenames through real Make recipes, without Go or APIs."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


REPO = Path(__file__).resolve().parents[3]
MAKEFILE = Path(os.environ.get("BIFROST_TEST_MAKEFILE", REPO / "Makefile"))
TARGETS = (
    ("test-core", {}, "core-all"),
    ("test-core", {"PROVIDER": "openai"}, "core-openai"),
    ("test-governance", {}, "governance"),
    ("test-mcp", {}, "mcp-all"),
    ("test-mcp", {"TYPE": "connection"}, "mcp-connection"),
)

RUNNER = r'''from pathlib import Path
import json
import os
import sys

root = Path(os.environ["FIXTURE_ROOT"])
args = sys.argv[1:]
with (root / "calls.jsonl").open("a") as stream:
    stream.write(json.dumps({"tool": Path(sys.argv[0]).name, "args": args}) + "\n")
if Path(sys.argv[0]).name == "gotestsum":
    reports = [arg.split("=", 1)[1] for arg in args if arg.startswith("--junitfile=")]
    if len(reports) != 1:
        sys.exit(91)
    failed = os.environ["FIXTURE_FAIL"] == "1"
    report = Path(reports[0])
    # Like gotestsum, require the recipe to supply an existing parent directory.
    report.write_text('<testsuites tests="2" failures="%d" errors="0">\n'
                      '<testsuite tests="2" failures="%d" errors="0" skipped="0">\n'
                      '<testcase name="TestFixture"/>\n'
                      '<testcase name="TestOther">%s</testcase>\n'
                      '</testsuite>\n</testsuites>\n' %
                      (failed, failed, '<failure message="fixture failure"/>' if failed else ''))
    sys.exit(23 if failed else 0)
elif Path(sys.argv[0]).name == "junit-viewer":
    inputs = [arg.split("=", 1)[1] for arg in args if arg.startswith("--results=")]
    outputs = [arg.split("=", 1)[1] for arg in args if arg.startswith("--save=")]
    if len(inputs) != 1 or len(outputs) != 1 or not Path(inputs[0]).is_file():
        (root / "bad-viewer-input").touch()
        sys.exit(92)
    Path(outputs[0]).write_text("fixture HTML")
else:
    (root / "unexpected-command").touch()
    sys.exit(93)
'''


class MakeReportFilenameTest(unittest.TestCase):
    def run_target(self, root, target, variables, *, pattern="", testcase="",
                   failed=False, viewer=False):
        (root / "Makefile").write_bytes(MAKEFILE.read_bytes())
        for path in ("recipes", "core/providers/openai", "tests/governance",
                     "core/internal/mcptests", "bin"):
            (root / path).mkdir(parents=True, exist_ok=True)
        for name in ("fly.mk", "ecs.mk", "local-k8s.mk"):
            (root / "recipes" / name).touch()
        (root / "core/providers/openai/openai_test.go").touch()
        (root / "core/internal/mcptests/connection_test.go").write_text(
            "func TestFixture(t *testing.T) {}\n")
        for name in ("gotestsum", "go", "dlv", "npm", "infisical", "junit-viewer"):
            path = root / "bin" / name
            path.write_text("#!" + sys.executable + "\n" + RUNNER)
            path.chmod(0o755)
        env = os.environ.copy()
        for name in ("CI", "GITHUB_ACTIONS", "GITLAB_CI", "CIRCLECI", "JENKINS_HOME"):
            env.pop(name, None)
        for name in ("MAKEFLAGS", "MFLAGS", "GNUMAKEFLAGS", "MAKEFILES", "BASH_ENV"):
            env.pop(name, None)
        env.update({"PATH": str(root / "bin") + os.pathsep + env["PATH"],
                    "FIXTURE_ROOT": str(root), "FIXTURE_FAIL": "1" if failed else "0",
                    "USE_INFISICAL": "0"})
        if not viewer:
            env["CI"] = "1"
        command = ["make", "--no-print-directory", "--assume-old=setup-mcp-tests",
                   target, "DEBUG=", "PROVIDER=", "TYPE=", "TEST_REPORTS_DIR=test-reports",
                   "PATTERN=" + pattern, "TESTCASE=" + testcase]
        command.extend(key + "=" + value for key, value in variables.items())
        result = subprocess.run(command, cwd=root, env=env, capture_output=True,
                                text=True, timeout=30)
        self.assertFalse((root / "unexpected-command").exists(), result.stdout + result.stderr)
        calls_file = root / "calls.jsonl"
        calls = [json.loads(line) for line in calls_file.read_text().splitlines()] if calls_file.exists() else []
        return result, calls

    def assert_report(self, root, result, calls, name, selector=None, *, failed=False,
                      summary=False, viewer=False):
        output = result.stdout + result.stderr
        self.assertEqual(result.returncode == 0, not failed, output)
        self.assertEqual(sorted(path.name for path in (root / "test-reports").iterdir()),
                         sorted([name + ".xml"] + ([name + ".html"] if viewer else [])), output)
        self.assertRegex(name, r"^[A-Za-z0-9._-]+$")
        runners = [call for call in calls if call["tool"] == "gotestsum"]
        self.assertEqual(len(runners), 1, output)
        args = runners[0]["args"]
        if selector is not None:
            self.assertEqual(args[args.index("-run") + 1], selector, output)
        else:
            self.assertNotIn("-run", args)
        self.assertNotIn("syntax error", output)
        self.assertNotIn("No rule to make target", output)
        if summary:
            self.assertRegex(output, r"TOTAL \(1 reports\)\s+2\s+" +
                             (r"1\s+1\s+0\s+0" if failed else r"2\s+0\s+0\s+0"))
        if viewer:
            viewers = [call for call in calls if call["tool"] == "junit-viewer"]
            self.assertEqual(len(viewers), 1, output)
            self.assertEqual(viewers[0]["args"],
                             ["--results=test-reports/" + name + ".xml",
                              "--save=test-reports/" + name + ".html"], output)
            self.assertFalse((root / "bad-viewer-input").exists())

    def test_alternation_preserves_selector_and_prints_summary(self):
        for target, variables, prefix in TARGETS:
            with self.subTest(target=target, variables=variables), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                result, calls = self.run_target(root, target, variables, pattern="TestAlpha|TestBeta")
                self.assert_report(root, result, calls, prefix + "-TestAlpha_TestBeta",
                                   ".*TestAlpha|TestBeta.*", summary=target != "test-mcp")

    def test_subtest_regex_produces_one_portable_filename(self):
        for target, variables, prefix in TARGETS:
            with self.subTest(target=target, variables=variables), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                pattern = "Test(Alpha|Beta)/Child[0-9]:fast.*"
                result, calls = self.run_target(root, target, variables, pattern=pattern)
                self.assert_report(root, result, calls, prefix + "-Test_Alpha_Beta__Child_0-9__fast._",
                                   ".*" + pattern + ".*", summary=target != "test-mcp")

    def test_testcase_subpath_preserves_exact_selector(self):
        for target, variables, prefix in TARGETS[1:]:
            with self.subTest(target=target, variables=variables), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                testcase = "TestAlpha/(First|Second)"
                result, calls = self.run_target(root, target, variables, testcase=testcase)
                suffix = "TestAlpha__First_Second_"
                selector = ("^TestOpenAI$/.*Tests/TestAlpha/(First|Second)$" if target == "test-core"
                            else "^" + testcase + "$")
                self.assert_report(root, result, calls, prefix + "-" + suffix, selector,
                                   summary=target != "test-mcp")

    def test_plain_pattern_keeps_legacy_filename(self):
        for target, variables, prefix in TARGETS:
            with self.subTest(target=target, variables=variables), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                result, calls = self.run_target(root, target, variables, pattern="TestAlpha-1.2_3")
                self.assert_report(root, result, calls, prefix + "-TestAlpha-1.2_3",
                                   ".*TestAlpha-1.2_3.*", summary=target != "test-mcp")

    def test_unfiltered_reports_keep_legacy_filename(self):
        for target, variables, prefix in TARGETS:
            with self.subTest(target=target, variables=variables), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                result, calls = self.run_target(root, target, variables)
                selector = ("^TestOpenAI$" if target == "test-core" and variables else
                            "TestFixture" if variables.get("TYPE") else None)
                name = "governance-all" if target == "test-governance" else prefix
                self.assert_report(root, result, calls, name, selector, summary=target != "test-mcp")

    def test_failing_filtered_run_still_returns_failure(self):
        for target, variables, prefix in TARGETS:
            with self.subTest(target=target, variables=variables), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                result, calls = self.run_target(root, target, variables,
                                                pattern="TestAlpha|TestBeta", failed=True)
                self.assert_report(root, result, calls, prefix + "-TestAlpha_TestBeta",
                                   ".*TestAlpha|TestBeta.*", failed=True, summary=target != "test-mcp")

    def test_html_viewer_receives_same_generated_report(self):
        for target, variables, prefix in TARGETS:
            with self.subTest(target=target, variables=variables), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                result, calls = self.run_target(root, target, variables,
                                                pattern="TestAlpha|TestBeta", viewer=True)
                self.assert_report(root, result, calls, prefix + "-TestAlpha_TestBeta",
                                   ".*TestAlpha|TestBeta.*", summary=target != "test-mcp", viewer=True)


if __name__ == "__main__":
    unittest.main(verbosity=2)
