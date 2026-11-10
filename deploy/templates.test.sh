#!/usr/bin/env bash
set -euo pipefail

# Static checks for the one-click templates that have no runtime of their own:
#   - app.json / heroku.yml (Heroku): container stack, Postgres addon bound as
#     DATABASE, BIFROST_CONFIG_JSON identical to deploy/config.postgres.json,
#     generated secrets, and a Dockerfile whose FROM pin matches render.yaml
#     and the DigitalOcean template;
#   - .do/deploy.template.yaml (DigitalOcean): image, DATABASE_URL binding and
#     BIFROST_CONFIG_JSON identical to the shared config;
#   - the DigitalOcean and Heroku buttons: every page that carries one points
#     at the exact template URL;
#   - every file read here is among workflow-lint.yml's pull_request paths, so
#     an edit to any of them runs this test;
#   - the Koyeb button: regenerated from deploy/koyeb/README.md's generator, its
#     BIFROST_CONFIG_B64 decodes to the shared config, and every place that
#     carries the button (README, quickstart, Koyeb guide) carries this exact URL.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }

python3 - "$ROOT" <<'PY' || FAILED=1
import json, re, sys, base64, subprocess, urllib.parse, pathlib
root = pathlib.Path(sys.argv[1])
failed = False
def fail(msg):
    global failed
    failed = True
    print("FAIL:", msg, file=sys.stderr)

shared = json.loads((root / "deploy/config.postgres.json").read_text())

# --- Heroku ---------------------------------------------------------------
app = json.loads((root / "app.json").read_text())
if app.get("stack") != "container":
    fail(f"app.json stack is {app.get('stack')!r}, expected container")
addons = app.get("addons", [])
if not any(a.get("plan", "").startswith("heroku-postgresql:") and a.get("as") == "DATABASE" for a in addons):
    fail("app.json must attach a heroku-postgresql addon as DATABASE (the entrypoint reads DATABASE_URL)")
env = app.get("env", {})
try:
    if json.loads(env["BIFROST_CONFIG_JSON"]["value"]) != shared:
        fail("app.json BIFROST_CONFIG_JSON differs from deploy/config.postgres.json")
except Exception as e:
    fail(f"app.json BIFROST_CONFIG_JSON unusable: {e}")
for k in ("BIFROST_ENCRYPTION_KEY", "BIFROST_SETUP_TOKEN"):
    if env.get(k, {}).get("generator") != "secret":
        fail(f"app.json {k} must use generator: secret")
import yaml
heroku_yml = yaml.safe_load((root / "heroku.yml").read_text()) or {}
web = heroku_yml.get("build", {}).get("docker", {}).get("web") if isinstance(heroku_yml.get("build"), dict) else None
if not isinstance(web, str):
    fail("heroku.yml has no build.docker.web entry (Heroku only builds Dockerfiles listed there)")
else:
    dockerfile = root / web
    if not dockerfile.exists():
        fail(f"heroku.yml build.docker.web points at {web}, which does not exist")
    else:
        fm = re.search(r"^FROM maximhq/bifrost:(v[0-9][0-9A-Za-z.+-]*)\s*$", dockerfile.read_text(), re.M)
        if not fm:
            fail("deploy/heroku/Dockerfile must be a single pinned FROM maximhq/bifrost:vX line")
        else:
            heroku_tag = fm.group(1)
            render_tag = re.search(r"maximhq/bifrost:(v[0-9][0-9A-Za-z.+-]*)", (root / "render.yaml").read_text())
            do_tag = re.search(r"^\s*tag:\s*(v[0-9][0-9A-Za-z.+-]*)\s*$", (root / ".do/deploy.template.yaml").read_text(), re.M)
            tags = {"heroku": heroku_tag, "render": render_tag.group(1) if render_tag else None, "digitalocean": do_tag.group(1) if do_tag else None}
            if len(set(tags.values())) != 1:
                fail(f"pinned tags differ across templates: {tags} (bump-deploy-image-tags.sh moves them together)")

# --- DigitalOcean ---------------------------------------------------------
do_spec = yaml.safe_load((root / ".do/deploy.template.yaml").read_text())["spec"]
svc = next((s for s in do_spec.get("services", []) if s.get("name") == "bifrost"), None)
if svc is None:
    fail(".do template has no bifrost service")
else:
    img = svc.get("image", {})
    if (img.get("registry"), img.get("repository")) != ("maximhq", "bifrost"):
        fail(f".do template image is {img}, expected maximhq/bifrost")
    envs = {e["key"]: e for e in svc.get("envs", [])}
    if not re.fullmatch(r"\$\{[a-z0-9-]+\.DATABASE_URL\}", str(envs.get("DATABASE_URL", {}).get("value", ""))):
        fail(".do template must bind DATABASE_URL from the database component")
    try:
        if json.loads(envs["BIFROST_CONFIG_JSON"]["value"]) != shared:
            fail(".do template BIFROST_CONFIG_JSON differs from deploy/config.postgres.json")
    except Exception as e:
        fail(f".do template BIFROST_CONFIG_JSON unusable: {e}")
    for k in ("BIFROST_ENCRYPTION_KEY", "BIFROST_SETUP_TOKEN"):
        if envs.get(k, {}).get("type") != "SECRET":
            fail(f".do template {k} must be a SECRET the review form asks for")
        if not str(envs.get(k, {}).get("value", "")).startswith("REPLACE_WITH_"):
            fail(f".do template {k} must be a REPLACE_WITH_ placeholder; the docs tell operators to replace values with that prefix")
    # The Enterprise CLI path copies this spec, so the docs must name the
    # placeholder the operator has to replace there.
    for rel in ("deploy/README.md", "docs/deployment-guides/platforms/digitalocean.mdx"):
        page = root / rel
        if page.exists():
            text = page.read_text()
            if "doctl apps create" in text and "`REPLACE_WITH_`" not in text:
                fail(f"{rel} describes the doctl path but does not name the REPLACE_WITH_ placeholders to replace first")

# --- Button targets -------------------------------------------------------
# Every page that carries a DigitalOcean or Heroku button must point at the
# exact template URL. Pages missing in this checkout are skipped because the
# platform guides are added one branch up the stack; at the top every page
# exists and is checked.
BUTTONS = {
    "cloud.digitalocean.com/apps/new": "https://cloud.digitalocean.com/apps/new?repo=https://github.com/maximhq/bifrost/tree/main",
    "heroku.com/deploy": "https://heroku.com/deploy?template=https://github.com/maximhq/bifrost",
}
for rel in ["README.md", "docs/quickstart/gateway/setting-up.mdx",
            "docs/deployment-guides/platforms/digitalocean.mdx", "docs/deployment-guides/platforms/heroku.mdx"]:
    path = root / rel
    if not path.exists():
        continue
    text = path.read_text()
    for marker, expected in BUTTONS.items():
        if marker in text and expected not in text:
            fail(f"{rel} carries a {marker} button that does not point at {expected}")

# --- Workflow trigger paths -----------------------------------------------
# Every file this test reads must trigger workflow-lint.yml, which runs it;
# otherwise an edit to one of them could skip these checks.
wf = yaml.safe_load((root / ".github/workflows/workflow-lint.yml").read_text())
wf_paths = (wf.get(True) or wf.get("on"))["pull_request"]["paths"]
def triggered(rel):
    return any(pat == rel or (pat.endswith("/**") and rel.startswith(pat[:-2])) for pat in wf_paths)
checked_files = ["app.json", "heroku.yml", "render.yaml", ".do/deploy.template.yaml", "deploy/config.postgres.json",
                 "deploy/koyeb/README.md", "deploy/templates.test.sh", "README.md", "docs/quickstart/gateway/setting-up.mdx",
                 "docs/deployment-guides/platforms/koyeb.mdx", "docs/deployment-guides/platforms/digitalocean.mdx",
                 "docs/deployment-guides/platforms/heroku.mdx"]
for rel in checked_files:
    if (root / rel).exists() and not triggered(rel):
        fail(f"{rel} is checked by this test but does not trigger .github/workflows/workflow-lint.yml; add it to pull_request.paths")

# --- Koyeb ----------------------------------------------------------------
readme = (root / "deploy/koyeb/README.md").read_text()
gm = re.search(r"```bash\n(python3 - <<'PY'\n[\s\S]*?\nPY)\n```", readme)
if not gm:
    fail("deploy/koyeb/README.md has no python3 generator block")
else:
    out = subprocess.run(["bash", "-c", gm.group(1)], cwd=root, capture_output=True, text=True)
    if out.returncode != 0:
        fail(f"Koyeb generator failed: {out.stderr.strip()}")
    else:
        url = out.stdout.strip()
        qs = urllib.parse.parse_qs(urllib.parse.urlsplit(url).query, keep_blank_values=True)
        try:
            if json.loads(base64.b64decode(qs["env[BIFROST_CONFIG_B64]"][0])) != shared:
                fail("Koyeb BIFROST_CONFIG_B64 differs from deploy/config.postgres.json")
        except Exception as e:
            fail(f"Koyeb BIFROST_CONFIG_B64 unusable: {e}")
        for k in ("env[BIFROST_ENCRYPTION_KEY]", "env[BIFROST_SETUP_TOKEN]", "env[DATABASE_URL]"):
            if not qs.get(k, [""])[0].startswith("CHANGE_ME"):
                fail(f"Koyeb {k} must be a CHANGE_ME placeholder the review form forces users to replace")
        if qs.get("ports", [""])[0] != "8080;http;/":
            fail(f"Koyeb ports is {qs.get('ports')}, expected 8080;http;/")
        carriers = ["README.md", "docs/quickstart/gateway/setting-up.mdx", "docs/deployment-guides/platforms/koyeb.mdx"]
        for rel in carriers:
            path = root / rel
            if not path.exists():
                continue
            text = path.read_text()
            if "app.koyeb.com/deploy" in text and url not in text:
                fail(f"{rel} carries a Koyeb button URL that differs from the generator output; regenerate it")
sys.exit(1 if failed else 0)
PY

if [[ $FAILED -ne 0 ]]; then
  exit 1
fi
echo "PASS: one-click templates (Heroku, DigitalOcean, Koyeb)"
