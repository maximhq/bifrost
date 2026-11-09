#!/usr/bin/env bash
set -euo pipefail

# Exercise deploy/azure/deploy.sh offline with a stubbed az CLI and Terraform:
# the tfvars file is written on the first run with a generated prefix, kept
# with the operator's edits (and that prefix) on a rerun, and the
# init/apply/output flow runs in order.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/repo/deploy/azure" "$WORK/repo/terraform/examples/azure-aci-postgres" "$WORK/bin"
cp "$SCRIPT_DIR/deploy.sh" "$WORK/repo/deploy/azure/deploy.sh"
EXAMPLE="$WORK/repo/terraform/examples/azure-aci-postgres"

cat > "$WORK/bin/az" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  "account show --query id -o tsv") echo 00000000-0000-0000-0000-000000000000 ;;
  "account show --query name -o tsv") echo "Test Subscription" ;;
  "account set --subscription "*) ;;
  "provider register "*) ;;
  *) echo "unexpected: az $*" >&2; exit 1 ;;
esac
STUB
cat > "$WORK/bin/terraform" <<'STUB'
#!/usr/bin/env bash
echo "terraform $*" >> "$TF_CALLS"
case "$1" in
  version) echo '{"terraform_version":"1.17.1"}' ;;
  init|apply) ;;
  output) case "${3:-}" in service_url) echo http://aci.example ;; setup_token) echo tok-789 ;; esac ;;
  *) echo "unexpected: terraform $*" >&2; exit 1 ;;
esac
STUB
chmod +x "$WORK/bin/"*

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }

# run [VAR=value ...]
run() {
  : > "$WORK/calls"
  env PATH="$WORK/bin:$PATH" TF_CALLS="$WORK/calls" BIFROST_AUTO_APPROVE=1 "$@" \
    bash "$WORK/repo/deploy/azure/deploy.sh" > "$WORK/log" 2>&1
}

# 1. First run writes terraform.tfvars with a generated prefix and drives init, apply, outputs.
if run; then
  grep -q '^region *= *"eastus"' "$EXAMPLE/terraform.tfvars" || fail "first run: region missing from tfvars"
  grep -qE '^name_prefix *= *"bifrost-[0-9a-f]{4}"' "$EXAMPLE/terraform.tfvars" || fail "first run: generated prefix missing from tfvars"
  grep -q '^terraform init -input=false$' "$WORK/calls" || fail "first run: init must run without -upgrade so the lock file pins providers"
  grep -q '^terraform apply -input=false -auto-approve' "$WORK/calls" || fail "first run: apply not auto-approved"
  grep -q 'tok-789' "$WORK/log" || fail "first run: setup token not printed"
else
  cat "$WORK/log" >&2; fail "first run: script exited non-zero"
fi

# 2. A rerun keeps the edited terraform.tfvars, including the first run's prefix, and reports it.
PREFIX="$(sed -n 's/^name_prefix *= *"\(.*\)"/\1/p' "$EXAMPLE/terraform.tfvars")"
printf 'database_deletion_protection = true\n' >> "$EXAMPLE/terraform.tfvars"
cp "$EXAMPLE/terraform.tfvars" "$WORK/tfvars.before"
if run; then
  cmp -s "$EXAMPLE/terraform.tfvars" "$WORK/tfvars.before" || fail "rerun: terraform.tfvars was rewritten, operator edits lost"
  grep -qi 'keeping existing' "$WORK/log" || fail "rerun: no notice that the existing tfvars is kept"
  grep -q "Name prefix: *$PREFIX" "$WORK/log" || fail "rerun: settings do not show the kept prefix $PREFIX"
else
  cat "$WORK/log" >&2; fail "rerun: script exited non-zero"
fi

# 3. A tab-separated assignment with a trailing comment in the kept file is read back as just the value.
printf 'name_prefix\t= "tabbed-prefix" # chosen on the first run\nregion = "eastus"\n' > "$EXAMPLE/terraform.tfvars"
if run; then
  grep -q "^Name prefix: *tabbed-prefix$" "$WORK/log" || fail "tab case: settings must show exactly the kept prefix from a tab-separated, commented assignment, got: $(awk '/Name prefix/' "$WORK/log")"
else
  cat "$WORK/log" >&2; fail "tab case: entrypoint exited non-zero"
fi

# 4. An explicit prefix that disagrees with the kept file is refused, not silently ignored.
if run BIFROST_NAME_PREFIX=other-prefix; then
  fail "conflict case: the script must refuse a BIFROST_NAME_PREFIX that differs from the kept terraform.tfvars"
else
  grep -q 'differs from name_prefix="tabbed-prefix"' "$WORK/log" || fail "conflict case: no explanation of the prefix conflict in the output"
  grep -q '^terraform apply' "$WORK/calls" && fail "conflict case: apply must not run on a prefix conflict"
fi
# The same prefix given explicitly is fine.
if run BIFROST_NAME_PREFIX=tabbed-prefix; then
  grep -q "Name prefix: *tabbed-prefix" "$WORK/log" || fail "matching case: prefix should be shown"
else
  cat "$WORK/log" >&2; fail "matching case: entrypoint exited non-zero"
fi

# 5. The kept terraform.tfvars records the subscription that created the stack;
# another subscription must not reuse its state (azurerm would recreate the
# resource group there and orphan the originals).
rm -f "$EXAMPLE/terraform.tfvars"
run || { cat "$WORK/log" >&2; fail "fresh first run for the subscription case exited non-zero"; }
grep -q '^# deployed_to_subscription = "00000000-0000-0000-0000-000000000000"' "$EXAMPLE/terraform.tfvars" || fail "first run must record the subscription in terraform.tfvars"
if run AZURE_SUBSCRIPTION_ID=11111111-1111-1111-1111-111111111111; then
  fail "subscription mismatch: the script must refuse to reuse state created in another subscription"
else
  grep -q 'created for subscription 00000000-0000-0000-0000-000000000000' "$WORK/log" || fail "subscription mismatch: no explanation in the output, got: $(tail -2 "$WORK/log")"
  grep -q '^terraform apply' "$WORK/calls" && fail "subscription mismatch: apply must not run"
  grep -q '^terraform init' "$WORK/calls" && fail "subscription mismatch: the check must come before init"
fi

# 5b. A marker edited to a different comment shape is still read.
sed -i.bak 's/^# deployed_to_subscription = /#deployed_to_subscription= /' "$EXAMPLE/terraform.tfvars" && rm -f "$EXAMPLE/terraform.tfvars.bak"
if run AZURE_SUBSCRIPTION_ID=11111111-1111-1111-1111-111111111111; then
  fail "tampered marker: a marker without the space after # must still bind the state"
else
  grep -q 'created for subscription 00000000-0000-0000-0000-000000000000' "$WORK/log" || fail "tampered marker: no explanation in the output, got: $(tail -2 "$WORK/log")"
fi

# 5c. No marker at all (a file from before the marker existed): every azurerm
# resource ID in the state names the subscription, so it still binds.
sed -i.bak '/deployed_to_subscription/d' "$EXAMPLE/terraform.tfvars" && rm -f "$EXAMPLE/terraform.tfvars.bak"
printf '{"version":4,"resources":[{"instances":[{"attributes":{"id":"/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/bifrost-rg"}}]}]}\n' > "$EXAMPLE/terraform.tfstate"
if run AZURE_SUBSCRIPTION_ID=11111111-1111-1111-1111-111111111111; then
  fail "marker-less state: the subscription in the state file must bind it"
else
  grep -q 'created for subscription 00000000-0000-0000-0000-000000000000' "$WORK/log" || fail "marker-less state: no explanation in the output, got: $(tail -2 "$WORK/log")"
  grep -q '^terraform apply' "$WORK/calls" && fail "marker-less state: apply must not run"
fi
if run; then
  grep -q '^terraform apply' "$WORK/calls" || fail "marker-less state, same subscription: apply should run"
else
  cat "$WORK/log" >&2; fail "marker-less state, same subscription: script exited non-zero"
fi
rm -f "$EXAMPLE/terraform.tfstate"

# 6. A pre-created terraform.tfvars that only holds the Enterprise image values
# (the documented way to supply registry credentials before the first run) is
# kept, so the script must add the prefix and region it displays; otherwise
# Terraform would silently use the defaults, including the shared "bifrost"
# prefix, while the output claims another.
rm -f "$EXAMPLE/terraform.tfvars"
printf 'image_repository = "myorg.azurecr.io/bifrost-enterprise"\nimage_tag = "v1"\n' > "$EXAMPLE/terraform.tfvars"
if run AZURE_LOCATION=westeurope; then
  shown=$(awk '/^Name prefix:/ {print $NF}' "$WORK/log")
  grep -q "^name_prefix[[:space:]]*=[[:space:]]*\"$shown\"" "$EXAMPLE/terraform.tfvars" || fail "pre-created tfvars: the displayed prefix '$shown' must be written into the kept file, got: $(cat "$EXAMPLE/terraform.tfvars")"
  grep -q '^region[[:space:]]*=[[:space:]]*"westeurope"' "$EXAMPLE/terraform.tfvars" || fail "pre-created tfvars: the region must be written into the kept file"
  grep -q '^image_repository = "myorg.azurecr.io/bifrost-enterprise"' "$EXAMPLE/terraform.tfvars" || fail "pre-created tfvars: the operator's values must be kept"
  grep -qi 'added name_prefix' "$WORK/log" || fail "pre-created tfvars: no notice that settings were added to the kept file"
else
  cat "$WORK/log" >&2; fail "pre-created tfvars: script exited non-zero"
fi

if [[ $FAILED -ne 0 ]]; then
  exit 1
fi
echo "PASS: deploy/azure/deploy.sh"
