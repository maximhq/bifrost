#!/usr/bin/env bash
set -euo pipefail

# Exercise deploy/gcp/deploy.sh offline with stubbed gcloud and Terraform:
# the tfvars file is written once and kept on rerun, and when Cloud Shell's
# Terraform is the stub that only prints install instructions, the script
# installs a pinned release into ~/.local/bin after verifying its checksum.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/repo/deploy/gcp" "$WORK/repo/terraform/examples/gcp-cloud-run-postgres" "$WORK/tools" "$WORK/tfbin" "$WORK/stubbin" "$WORK/home"
cp "$SCRIPT_DIR/deploy.sh" "$WORK/repo/deploy/gcp/deploy.sh"
EXAMPLE="$WORK/repo/terraform/examples/gcp-cloud-run-postgres"

cat > "$WORK/tools/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >> "$TF_CALLS"
case "$*" in
  "config get-value account") echo dev@example.com ;;
  "config get-value project") echo test-project ;;
  "services enable"*) ;;
  "auth application-default print-access-token") echo token ;;
  *) echo "unexpected: gcloud $*" >&2; exit 1 ;;
esac
STUB
# A working Terraform.
cat > "$WORK/tfbin/terraform" <<'STUB'
#!/usr/bin/env bash
echo "terraform $*" >> "$TF_CALLS"
case "$1" in
  version) echo '{"terraform_version":"1.17.1"}' ;;
  init|apply) ;;
  output) case "${3:-}" in service_url) echo https://run.example ;; setup_token) echo tok-456 ;; esac ;;
  *) echo "unexpected: terraform $*" >&2; exit 1 ;;
esac
STUB
# Cloud Shell's placeholder: on PATH, but every invocation fails.
cat > "$WORK/stubbin/terraform" <<'STUB'
#!/usr/bin/env bash
echo "Terraform is no longer installed by default in Cloud Shell." >&2
exit 1
STUB
cat > "$WORK/stubbin/uname" <<'STUB'
#!/usr/bin/env bash
case "$1" in -s) echo Linux ;; -m) echo "$TEST_ARCH" ;; esac
STUB
# curl "downloads" a zip holding an executable terraform; sha256sum reports the pinned digest.
cat > "$WORK/stubbin/curl" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
out=""; url=""
while [ $# -gt 0 ]; do case "$1" in -o) out="$2"; shift 2 ;; -*) shift ;; *) url="$1"; shift ;; esac; done
[[ "$url" == "https://releases.hashicorp.com/terraform/1.17.1/terraform_1.17.1_linux_${TEST_ARCH_SLUG}.zip" ]] || { echo "unexpected url $url" >&2; exit 1; }
python3 - "$out" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w") as z:
    z.writestr("terraform", "#!/usr/bin/env bash\ncase \"$1\" in version) echo '{\"terraform_version\":\"1.17.1\"}' ;; init|apply) ;; output) case \"${3:-}\" in service_url) echo https://run.example ;; setup_token) echo tok-456 ;; esac ;; esac\n")
PY
STUB
cat > "$WORK/stubbin/sha256sum" <<'STUB'
#!/usr/bin/env bash
case "$TEST_ARCH_SLUG" in
  amd64) echo "4638628058e71a55212b1c563712eac03b902128f40740f3dc8174df12135061  $1" ;;
  arm64) echo "988e950a1c23058093b0e66ab2d5fb53fc18345ecfbd5871acf51dbac9325edd  $1" ;;
esac
STUB
chmod +x "$WORK/tools/"* "$WORK/tfbin/"* "$WORK/stubbin/"*

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }

# run <path-prefix> [VAR=value ...]
run() {
  : > "$WORK/calls"
  env PATH="$1:$PATH" HOME="$WORK/home" TF_CALLS="$WORK/calls" BIFROST_AUTO_APPROVE=1 "${@:2}" \
    bash "$WORK/repo/deploy/gcp/deploy.sh" > "$WORK/log" 2>&1
}

# 1. First run writes terraform.tfvars and drives init, apply, outputs.
if run "$WORK/tfbin:$WORK/tools"; then
  grep -q '^project_id *= *"test-project"' "$EXAMPLE/terraform.tfvars" || fail "first run: project_id missing from tfvars"
  grep -q '^terraform init -input=false$' "$WORK/calls" || fail "first run: init must run without -upgrade so the lock file pins providers"
  grep -q 'tok-456' "$WORK/log" || fail "first run: setup token not printed"
else
  cat "$WORK/log" >&2; fail "first run: script exited non-zero"
fi

# 2. A rerun keeps an edited terraform.tfvars.
printf 'database_deletion_protection = true\n' >> "$EXAMPLE/terraform.tfvars"
cp "$EXAMPLE/terraform.tfvars" "$WORK/tfvars.before"
if run "$WORK/tfbin:$WORK/tools"; then
  cmp -s "$EXAMPLE/terraform.tfvars" "$WORK/tfvars.before" || fail "rerun: terraform.tfvars was rewritten, operator edits lost"
  grep -qi 'keeping existing' "$WORK/log" || fail "rerun: no notice that the existing tfvars is kept"
else
  cat "$WORK/log" >&2; fail "rerun: script exited non-zero"
fi

# 2b. A kept terraform.tfvars that targets another project drives the API
# preflight, and a trailing comment on that line does not leak into the value.
sed -i.bak 's/^project_id *=.*/project_id       = "other-project" # moved here/' "$EXAMPLE/terraform.tfvars" && rm -f "$EXAMPLE/terraform.tfvars.bak"
if run "$WORK/tfbin:$WORK/tools"; then
  grep -q '^gcloud services enable .*--project other-project$' "$WORK/calls" || fail "tfvars project: APIs must be enabled in the project terraform.tfvars targets, got: $(awk '/services enable/' "$WORK/calls")"
  grep -q 'targets project other-project' "$WORK/log" || fail "tfvars project: no notice that the kept file's project is used"
else
  cat "$WORK/log" >&2; fail "tfvars project: script exited non-zero"
fi

# 2c. An explicit GOOGLE_CLOUD_PROJECT that disagrees with the kept file is refused, not displayed and ignored.
if run "$WORK/tfbin:$WORK/tools" GOOGLE_CLOUD_PROJECT=third-project; then
  fail "project conflict: the script must refuse a GOOGLE_CLOUD_PROJECT that differs from the kept terraform.tfvars"
else
  grep -q 'differs from project_id="other-project"' "$WORK/log" || fail "project conflict: no explanation of the conflict in the output, got: $(tail -2 "$WORK/log")"
  grep -q '^gcloud services enable' "$WORK/calls" && fail "project conflict: APIs must not be enabled on a project conflict"
  grep -q '^terraform apply' "$WORK/calls" && fail "project conflict: apply must not run on a project conflict"
fi
# The same project given explicitly is fine.
if run "$WORK/tfbin:$WORK/tools" GOOGLE_CLOUD_PROJECT=other-project; then
  grep -q '^gcloud services enable .*--project other-project$' "$WORK/calls" || fail "matching project: APIs should be enabled in other-project"
else
  cat "$WORK/log" >&2; fail "matching project: script exited non-zero"
fi

# 2d. Sources that outrank terraform.tfvars for project_id (TF_VAR_project_id,
# TF_CLI_ARGS*, *.auto.tfvars) would make the API preflight target the wrong
# project, so the script refuses them before touching anything.
if run "$WORK/tfbin:$WORK/tools" TF_VAR_project_id=shadow-project; then
  fail "TF_VAR_project_id: the script must refuse a project source it cannot follow"
else
  grep -q 'TF_VAR_project_id' "$WORK/log" || fail "TF_VAR_project_id: no explanation, got: $(tail -2 "$WORK/log")"
  grep -q '^gcloud services enable' "$WORK/calls" && fail "TF_VAR_project_id: APIs must not be enabled"
fi
if run "$WORK/tfbin:$WORK/tools" TF_CLI_ARGS_apply=-var=project_id=shadow-project; then
  fail "TF_CLI_ARGS_apply: the script must refuse a project source it cannot follow"
else
  grep -q 'TF_CLI_ARGS' "$WORK/log" || fail "TF_CLI_ARGS_apply: no explanation, got: $(tail -2 "$WORK/log")"
fi
printf 'project_id = "shadow-project"\n' > "$EXAMPLE/shadow.auto.tfvars"
if run "$WORK/tfbin:$WORK/tools"; then
  fail "auto.tfvars: the script must refuse a project source it cannot follow"
else
  grep -q 'shadow.auto.tfvars' "$WORK/log" || fail "auto.tfvars: the offending file must be named, got: $(tail -2 "$WORK/log")"
  grep -q '^gcloud services enable' "$WORK/calls" && fail "auto.tfvars: APIs must not be enabled"
fi
rm -f "$EXAMPLE/shadow.auto.tfvars"

# 2e. An indented project_id assignment in the kept file is still read.
sed -i.bak 's/^project_id *=.*/  project_id = "other-project"/' "$EXAMPLE/terraform.tfvars" && rm -f "$EXAMPLE/terraform.tfvars.bak"
if run "$WORK/tfbin:$WORK/tools"; then
  grep -q '^gcloud services enable .*--project other-project$' "$WORK/calls" || fail "indented project_id: APIs must be enabled in the project the kept file targets, got: $(awk '/services enable/' "$WORK/calls")"
else
  cat "$WORK/log" >&2; fail "indented project_id: script exited non-zero"
fi

# 3. Cloud Shell stub on PATH: install the pinned release into ~/.local/bin and use it.
for pair in x86_64:amd64 aarch64:arm64; do
  export TEST_ARCH="${pair%%:*}" TEST_ARCH_SLUG="${pair##*:}"
  rm -rf "$WORK/home/.local" "$EXAMPLE/terraform.tfvars"
  if run "$WORK/stubbin:$WORK/tools"; then
    [[ -x "$WORK/home/.local/bin/terraform" ]] || fail "$TEST_ARCH: terraform was not installed into ~/.local/bin"
    grep -q 'Installing Terraform 1.17.1' "$WORK/log" || fail "$TEST_ARCH: no install message"
    grep -q 'tok-456' "$WORK/log" || fail "$TEST_ARCH: the installed terraform did not run the deployment"
  else
    cat "$WORK/log" >&2; fail "$TEST_ARCH: script exited non-zero with the Cloud Shell stub"
  fi
done

if [[ $FAILED -ne 0 ]]; then
  exit 1
fi
echo "PASS: deploy/gcp/deploy.sh"
