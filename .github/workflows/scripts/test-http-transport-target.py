"""Exercise the real HTTP transport Make target with offline command fixtures."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


REPO = Path(__file__).resolve().parents[3]
MAKEFILE = Path(os.environ.get("BIFROST_TEST_MAKEFILE", REPO / "Makefile"))
PACKAGES = ["handlers", "integrations", "lib", "server", "server/realtime", "websocket"]

GOTESTSUM = r'''#!/usr/bin/env bash
set -eu
package=${PWD#"$FIXTURE_ROOT/transports/bifrost-http/"}
printf '%s\n' "$package" >> "$FIXTURE_ROOT/invocations"
report=
for argument in "$@"; do
  case "$argument" in --junitfile=*) report=${argument#--junitfile=} ;; esac
done
test -n "$report"
mkdir -p "$(dirname "$report")"
printf '<testsuites tests="1" failures="0" errors="0"/>\n' > "$report"
if [ "$package" = "${FAIL_PACKAGE:-}" ]; then exit 23; fi
'''

VIEWER = r'''#!/usr/bin/env bash
set -eu
source=
for argument in "$@"; do
  case "$argument" in --results=*) source=${argument#--results=} ;; esac
done
if [ ! -f "$source" ]; then
  printf '%s\n' "$source" >> "$FIXTURE_ROOT/missing-viewer-input"
fi
printf '%s\n' "$source" >> "$FIXTURE_ROOT/viewer-inputs"
exit "${VIEWER_EXIT:-0}"
'''


class HttpTransportTargetTest(unittest.TestCase):
    def run_target(self, root, *, failure="", packages=PACKAGES, reports="test-reports",
                   viewer=False, viewer_exit=0, missing_transport=False, aggregate=False,
                   discovery_failure=""):
        root = root.resolve()
        (root / "Makefile").write_bytes(MAKEFILE.read_bytes())
        # Unrelated deployment recipes are never invoked by this target.
        recipes = root / "recipes"
        recipes.mkdir()
        for name in ("fly.mk", "ecs.mk", "local-k8s.mk"):
            (recipes / name).touch()
        transport = root / "transports" / "bifrost-http"
        if not missing_transport:
            transport.mkdir(parents=True)
            for package in packages:
                directory = transport / package
                directory.mkdir(parents=True, exist_ok=True)
                (directory / "fixture_test.go").touch()
        binaries = root / "bin"
        binaries.mkdir()
        scripts = {"gotestsum": GOTESTSUM}
        if viewer:
            scripts["junit-viewer"] = VIEWER
        if discovery_failure == "find":
            scripts["find"] = "#!/usr/bin/env bash\nprintf '%s\\n' './handlers' './lib'\nexit 47\n"
        elif discovery_failure == "sort":
            scripts["sort"] = "#!/usr/bin/env bash\nprintf '%s\\n' './sort-only-sentinel'\nexit 53\n"
        for name in ("go", "infisical"):
            scripts[name] = '#!/usr/bin/env bash\necho "$0" >> "$FIXTURE_ROOT/unexpected-command"\nexit 91\n'
        for name, script in scripts.items():
            path = binaries / name
            path.write_text(script)
            path.chmod(0o755)
        env = os.environ.copy()
        for name in ("CI", "GITHUB_ACTIONS", "GITLAB_CI", "CIRCLECI", "JENKINS_HOME"):
            env.pop(name, None)
        env.update({"PATH": str(binaries) + os.pathsep + env["PATH"],
                    "FIXTURE_ROOT": str(root), "FAIL_PACKAGE": failure,
                    "USE_INFISICAL": "0", "VIEWER_EXIT": str(viewer_exit)})
        if not viewer:
            env["CI"] = "1"
        command = ["make", "--no-print-directory", f"TEST_REPORTS_DIR={reports}"]
        if aggregate:
            command += ["--assume-old=" + target for target in
                        ("test-core", "test-framework", "test-plugins", "test", "test-cli")]
            command += ["test-all"]
        else:
            command += ["test-http-transport"]
        result = subprocess.run(command, cwd=root, env=env, capture_output=True, text=True, timeout=30)
        self.assertFalse((root / "unexpected-command").exists(), result.stdout + result.stderr)
        calls = (root / "invocations").read_text().splitlines() if (root / "invocations").exists() else []
        return result, calls

    def assert_reports(self, root, directory, packages=PACKAGES):
        destination = Path(directory)
        if not destination.is_absolute():
            destination = root / destination
        actual = sorted(p.name for p in destination.glob("http-transport-*.xml"))
        expected = sorted("http-transport-" + package.replace("/", "-") + ".xml" for package in packages)
        self.assertEqual(actual, expected)

    def test_all_packages_pass_and_nested_reports_reach_root(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            result, calls = self.run_target(root)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(calls, PACKAGES)
            self.assert_reports(root, "test-reports")

    def test_failure_in_first_middle_or_last_package_is_not_swallowed(self):
        for failure in ("handlers", "lib", "websocket"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                result, calls = self.run_target(root, failure=failure)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(calls, PACKAGES, result.stdout + result.stderr)
                self.assert_reports(root, "test-reports")

    def test_aggregate_target_propagates_transport_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            result, calls = self.run_target(root, failure="lib", aggregate=True)
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(calls, PACKAGES, result.stdout + result.stderr)

    def test_local_viewer_failure_cannot_hide_package_failure(self):
        for failure in ("", "lib"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                result, calls = self.run_target(root, failure=failure, viewer=True, viewer_exit=7)
                self.assertEqual(result.returncode == 0, not failure, result.stdout + result.stderr)
                self.assertEqual(calls, PACKAGES)
                self.assertFalse((root / "missing-viewer-input").exists())
                self.assertEqual(len((root / "viewer-inputs").read_text().splitlines()), len(PACKAGES))

    def test_absolute_and_space_containing_report_directory(self):
        for absolute in (False, True):
            with self.subTest(absolute=absolute), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp) / "transport target"
                root.mkdir()
                reports = str(root / "custom reports") if absolute else "custom reports"
                result, calls = self.run_target(root, reports=reports)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(calls, PACKAGES)
                self.assert_reports(root, reports)

    def test_empty_transport_directory_is_not_an_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            result, calls = self.run_target(root, packages=[])
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(calls, [])

    def test_missing_transport_directory_is_an_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            result, calls = self.run_target(root, missing_transport=True)
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(calls, [])

    def test_find_failure_runs_partial_discovery_but_fails_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            result, calls = self.run_target(root, discovery_failure="find")
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(calls, ["handlers", "lib"], result.stdout + result.stderr)

    def test_sort_failure_runs_unsorted_discovery_but_fails_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            result, calls = self.run_target(root, discovery_failure="sort")
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertCountEqual(calls, PACKAGES)


if __name__ == "__main__":
    if not shutil.which("make") or not shutil.which("bash"):
        raise SystemExit("GNU make and bash are required for these offline regression tests")
    unittest.main()
