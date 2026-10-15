"""Exercise the release dependency update without publishing anything."""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parent


class PluginReleaseDependenciesTest(unittest.TestCase):
    def test_updates_plugin_dependencies_before_tidy(self):
        for dependency, version in [("governance", "1.7.1"), ("mocker", "1.6.1"), (None, None)]:
            with self.subTest(dependency=dependency), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                plugin = root / "plugins" / "subject"
                plugin.mkdir(parents=True)
                (plugin / "go.mod").touch()
                requirements = [{"Path": "example.com/external", "Version": "v1.0.0"}]
                expected = [
                    "github.com/maximhq/bifrost/core@v1.8.5",
                    "github.com/maximhq/bifrost/framework@v1.6.1",
                ]
                if dependency:
                    sibling = root / "plugins" / dependency
                    sibling.mkdir()
                    (sibling / "version").write_text(version + "\n")
                    module = "github.com/maximhq/bifrost/plugins/" + dependency
                    requirements.append({"Path": module, "Version": "v1.0.0"})
                    expected.append(module + "@v" + version)
                (root / "module.json").write_text(json.dumps({"Require": requirements}))
                script = (SCRIPTS / "release-single-plugin.sh").read_text()
                block = script.split("# Update plugin dependencies\n", 1)[1].split("cd ../..", 1)[0]
                harness = '''
set -euo pipefail
PLUGIN_DIR=plugins/subject
PLUGIN_NAME=subject
CORE_VERSION=v1.8.5
FRAMEWORK_VERSION=v1.6.1
ROOT="$PWD"
git() { :; }
go() {
  if [[ "$*" == "mod edit -json" ]]; then
    cat "$ROOT/module.json"
  elif [[ "$1" == "get" ]]; then
    shift
    printf '%s\\n' "$@" >> "$ROOT/gets"
  elif [[ "$*" == "mod tidy" ]]; then
    cp "$ROOT/gets" "$ROOT/before-tidy"
  fi
}
'''
                result = subprocess.run(
                    ["bash", "-c", harness + '\nsource "$1"\n' + block,
                     "test", str(SCRIPTS / "go-utils.sh")],
                    cwd=root, capture_output=True, text=True,
                )
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual((root / "before-tidy").read_text().splitlines(), expected)


if __name__ == "__main__":
    unittest.main()
