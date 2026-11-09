#!/usr/bin/env bash
set -euo pipefail

# Exercise deploy/eks/deploy.sh offline with stubbed cloud CLIs, kubectl and
# Terraform: the tfvars file is written on the first run and kept with the
# operator's edits on a rerun, and the two-phase apply (cluster first, then the
# rest) runs in order.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/repo/deploy/eks" "$WORK/repo/terraform/examples/aws-eks-postgres" "$WORK/tools" "$WORK/tfbin" "$WORK/home"
cp "$SCRIPT_DIR/deploy.sh" "$WORK/repo/deploy/eks/deploy.sh"
EXAMPLE="$WORK/repo/terraform/examples/aws-eks-postgres"

cat > "$WORK/tools/aws" <<'STUB'
#!/usr/bin/env bash
echo "aws $*" >> "$TF_CALLS"
case "$1 $2" in
  "sts get-caller-identity") echo 123456789012 ;;
  "configure get") exit 1 ;;
  "eks update-kubeconfig") ;;
  *) echo "unexpected: aws $*" >&2; exit 1 ;;
esac
STUB
cat > "$WORK/tools/kubectl" <<'STUB'
#!/usr/bin/env bash
echo "kubectl $*" >> "$TF_CALLS"
STUB
cat > "$WORK/tfbin/terraform" <<'STUB'
#!/usr/bin/env bash
echo "terraform $*" >> "$TF_CALLS"
case "$1" in
  version) echo '{"terraform_version":"1.17.1"}' ;;
  init|apply|refresh) ;;
  output) case "${3:-}" in service_url) echo http://lb.example ;; setup_token) echo tok-k8s ;; cluster_name) echo tf-cluster ;; cluster_region) echo tf-region ;; cluster_project) echo tf-project ;; cluster_resource_group) echo tf-rg ;; esac ;;
  *) echo "unexpected: terraform $*" >&2; exit 1 ;;
esac
STUB
chmod +x "$WORK/tools/"* "$WORK/tfbin/"*

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }

run() {
  : > "$WORK/calls"
  PATH="$1:$PATH" HOME="$WORK/home" TF_CALLS="$WORK/calls" AWS_REGION=eu-west-1 BIFROST_AUTO_APPROVE=1 \
    bash "$WORK/repo/deploy/eks/deploy.sh" > "$WORK/log" 2>&1
}

# 1. First run writes terraform.tfvars and drives init, the targeted cluster apply, kubeconfig, the full apply and outputs.
if run "$WORK/tfbin:$WORK/tools"; then
  grep -q '^name_prefix *= *"bifrost"' "$EXAMPLE/terraform.tfvars" || fail "first run: name_prefix missing from tfvars"
  grep -q '^terraform init -input=false$' "$WORK/calls" || fail "first run: init must run without -upgrade so the lock file pins providers"
  grep -q '^terraform apply -input=false -auto-approve -target=' "$WORK/calls" || fail "first run: targeted cluster apply not called"
  grep -q '^aws eks update-kubeconfig --region tf-region --name tf-cluster' "$WORK/calls" || fail "first run: kubeconfig must use the cluster name and region Terraform reports, got: $(awk '/update-kubeconfig/' "$WORK/calls")"
  [[ "$(grep -c '^terraform apply ' "$WORK/calls")" == 2 ]] || fail "first run: expected two applies, got $(grep -c '^terraform apply ' "$WORK/calls")"
  grep -q 'tok-k8s' "$WORK/log" || fail "first run: setup token not printed"
  grep -q 'http://lb.example' "$WORK/log" || fail "first run: service URL not printed"
else
  cat "$WORK/log" >&2; fail "first run: script exited non-zero"
fi

# 2. A rerun keeps an edited terraform.tfvars instead of overwriting it.
printf 'database_deletion_protection = true\n' >> "$EXAMPLE/terraform.tfvars"
cp "$EXAMPLE/terraform.tfvars" "$WORK/tfvars.before"
if run "$WORK/tfbin:$WORK/tools"; then
  cmp -s "$EXAMPLE/terraform.tfvars" "$WORK/tfvars.before" || fail "rerun: terraform.tfvars was rewritten, operator edits lost"
  grep -qi 'keeping existing' "$WORK/log" || fail "rerun: no notice that the existing tfvars is kept"
else
  cat "$WORK/log" >&2; fail "rerun: script exited non-zero"
fi

if [[ $FAILED -ne 0 ]]; then
  exit 1
fi
echo "PASS: deploy/eks/deploy.sh"
