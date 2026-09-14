#!/usr/bin/env bash
#
# migrate-vault-1.4.9-to-2.0.0.sh
#
# Migrates a Bifrost enterprise 1.4.9 - 1.4.13 database whose config rows were
# written by the old "vault as a storage backend" code (encryption_status='vault')
# into the shape that 2.0.0 expects.
#
# Background
# ----------
# 1.4.9 pushed secrets into the vault and wrote a literal "vault.<path>" string
# into the DB column, stamping the row encryption_status='vault'. On read it
# resolved every such column back through the vault (PR #4157).
#
# 2.0.0 removed the 'vault' encryption status entirely (PR #4398). Vault refs
# are now a property of SecretVar columns, which still resolve a raw
# "vault.<path>" value natively. Plain string columns that 1.4.9 also vaulted
# are NOT resolved anymore: they read back as the literal path string, and the
# first re-save AES-encrypts that path and cements the damage.
#
# What this script does
# ---------------------
# For every row with encryption_status='vault':
#   * plain string columns holding a "vault.<path>" ref are resolved from the
#     vault and written back as plaintext:
#         config_keys.aliases_json, config_keys.bedrock_batch_s3_config_json
#         config_plugins.config_json
#         config_providers.proxy_config_json
#         oauth_configs.code_verifier          (only if the column still exists)
#   * SecretVar columns (config_keys.value, governance_virtual_keys.value,
#     oauth_configs.client_secret, the azure/vertex/bedrock credentials, ...)
#     are left as "vault.<path>" refs. 2.0.0 resolves them natively as long as
#     config_store.vault_store stays configured with the same prefix.
#   * encryption_status is set to 'plain_text' so the 2.0.0 startup backfill
#     picks the row up and AES-encrypts it with your BIFROST_ENCRYPTION_KEY.
#
# Nothing is deleted from the vault. Every write is journaled so it can be
# rolled back with --rollback.
#
# Run it with Bifrost STOPPED, after taking a database backup, and BEFORE
# starting 2.0.0 for the first time. Postgres only (the enterprise build never
# supported vault on sqlite).
#
# Usage
# -----
#   export BIFROST_DB_DSN='postgres://user:pass@host:5432/bifrost?sslmode=require'
#
#   # dry run: resolves every secret and reports the plan, writes nothing
#   ./migrate-vault-1.4.9-to-2.0.0.sh --backend hashicorp --mount secret
#
#   # apply
#   ./migrate-vault-1.4.9-to-2.0.0.sh --backend hashicorp --mount secret --apply
#
#   # other backends
#   ./migrate-vault-1.4.9-to-2.0.0.sh --backend aws
#   ./migrate-vault-1.4.9-to-2.0.0.sh --backend gcp --gcp-project my-project
#   ./migrate-vault-1.4.9-to-2.0.0.sh --resolve-cmd 'my-fetch-secret "$1"'
#   ./migrate-vault-1.4.9-to-2.0.0.sh --secrets-file secrets.json
#
#   # undo
#   ./migrate-vault-1.4.9-to-2.0.0.sh --rollback vault-migration-journal.tsv --apply
#
# Backends use their own CLI and its usual auth:
#   hashicorp : `vault` CLI (VAULT_ADDR, VAULT_TOKEN, VAULT_NAMESPACE). KV v2,
#               field "value", mount from --mount (default "secret"), the same
#               layout 1.4.9 wrote.
#   aws       : `aws secretsmanager get-secret-value`, secret id = path.
#   gcp       : `gcloud secrets versions access latest`, secret id =
#               base64url(path) without padding, exactly as 1.4.9 named them.
#   cmd       : --resolve-cmd is run through `sh -c`, with the path (the ref
#               minus the "vault." prefix) as $1. Print the secret to stdout.
#   file      : --secrets-file is a JSON object of full ref -> plaintext, for
#               air-gapped runs.
#
set -euo pipefail

# Table spec: table|pk column|string columns to resolve|columns that must be JSON
SPECS='config_keys|id|aliases_json,bedrock_batch_s3_config_json|aliases_json,bedrock_batch_s3_config_json
governance_virtual_keys|id||
config_plugins|id|config_json|config_json
config_providers|id|proxy_config_json|proxy_config_json
oauth_configs|id|code_verifier|'

DSN="${BIFROST_DB_DSN:-${DATABASE_URL:-}}"
BACKEND=""
MOUNT="secret"
GCP_PROJECT=""
RESOLVE_CMD=""
SECRETS_FILE=""
APPLY=0
LIMIT=0
JOURNAL="vault-migration-journal.tsv"
ROLLBACK=""

usage() {
  sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
log() { printf '%s\n' "$*"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --dsn)           DSN="$2"; shift 2 ;;
    --backend)       BACKEND="$2"; shift 2 ;;
    --mount)         MOUNT="$2"; shift 2 ;;
    --gcp-project)   GCP_PROJECT="$2"; shift 2 ;;
    --resolve-cmd)   RESOLVE_CMD="$2"; BACKEND="cmd"; shift 2 ;;
    --secrets-file)  SECRETS_FILE="$2"; BACKEND="file"; shift 2 ;;
    --apply)         APPLY=1; shift ;;
    --limit)         LIMIT="$2"; shift 2 ;;
    --journal)       JOURNAL="$2"; shift 2 ;;
    --rollback)      ROLLBACK="$2"; shift 2 ;;
    -h|--help)       usage 0 ;;
    *)               printf 'unknown argument: %s\n\n' "$1" >&2; usage 1 ;;
  esac
done

[ -n "$DSN" ] || die "no database DSN: pass --dsn or set BIFROST_DB_DSN"
command -v psql >/dev/null || die "psql not found in PATH"
command -v jq >/dev/null || die "jq not found in PATH (used to validate JSON columns)"

# Values never go into SQL text. They are loaded into psql variables from
# files (so secrets stay out of argv) and interpolated with :'var', which
# quotes them safely.
PSQL=(psql "$DSN" -X -q -At -v ON_ERROR_STOP=1)
sql()  { "${PSQL[@]}" -c "$1"; }
sqlf() { "${PSQL[@]}" -f -; }
has_table()  { [ "$(sql "SELECT 1 FROM information_schema.tables  WHERE table_schema = current_schema() AND table_name = '$1'")" = 1 ]; }
has_column() { [ "$(sql "SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = '$1' AND column_name = '$2'")" = 1 ]; }
pk_col_for() { printf '%s\n' "$SPECS" | awk -F'|' -v t="$1" '$1 == t { print $2 }'; }
is_json_col() { case ",$1," in *",$2,"*) return 0 ;; esac; return 1; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/vault-migration.XXXXXX")"
chmod 700 "$WORK"
trap 'rm -rf "$WORK"' EXIT
SEP="$(printf '\x1f')"

sql "SELECT 1" >/dev/null || die "cannot connect to database"

# ---------------------------------------------------------------------------
# Rollback: restore the vault refs and 'vault' status recorded in a journal.
# ---------------------------------------------------------------------------
if [ -n "$ROLLBACK" ]; then
  [ -f "$ROLLBACK" ] || die "journal not found: $ROLLBACK"
  n=0; restored=0
  while IFS="$(printf '\t')" read -r table pk column ref; do
    [ -n "$table" ] || continue
    n=$((n + 1))
    pkcol="$(pk_col_for "$table")"
    [ -n "$pkcol" ] || die "unknown table in journal: $table"
    if [ -n "$column" ]; then
      log "ROLLBACK  $table $pkcol=$pk  $column <- $ref, encryption_status <- vault"
    else
      log "ROLLBACK  $table $pkcol=$pk  encryption_status <- vault"
    fi
    [ "$APPLY" = 1 ] || continue
    printf '%s' "$pk"  > "$WORK/pk"
    printf '%s' "$ref" > "$WORK/ref"
    set_clause="encryption_status = 'vault'"
    [ -z "$column" ] || set_clause="$column = :'ref', $set_clause"
    out="$(sqlf <<EOF
\\set pk \`cat '$WORK/pk'\`
\\set ref \`cat '$WORK/ref'\`
UPDATE $table SET $set_clause WHERE $pkcol = :'pk' RETURNING 1;
EOF
    )" || die "rollback failed on $table $pkcol=$pk"
    [ "$out" = 1 ] || die "rollback: no row matched $table $pkcol=$pk"
    restored=$((restored + 1))
  done < "$ROLLBACK"
  if [ "$APPLY" = 1 ]; then
    log "rolled back $restored/$n journal entries"
  else
    log "dry run: $n journal entries would be rolled back (re-run with --apply)"
  fi
  exit 0
fi

# ---------------------------------------------------------------------------
# Resolver
# ---------------------------------------------------------------------------
case "$BACKEND" in
  hashicorp) command -v vault  >/dev/null || die "vault CLI not found" ;;
  aws)       command -v aws    >/dev/null || die "aws CLI not found" ;;
  gcp)       command -v gcloud >/dev/null || die "gcloud CLI not found"
             [ -n "$GCP_PROJECT" ] || die "--gcp-project is required for --backend gcp" ;;
  cmd)       [ -n "$RESOLVE_CMD" ] || die "--resolve-cmd is empty" ;;
  file)      [ -f "$SECRETS_FILE" ] || die "secrets file not found: $SECRETS_FILE" ;;
  "")        die "choose a resolver: --backend hashicorp|aws|gcp, --resolve-cmd, or --secrets-file" ;;
  *)         die "unknown backend: $BACKEND" ;;
esac

# resolve_ref REF OUTFILE: writes the plaintext to OUTFILE; fails closed.
resolve_ref() {
  ref="$1"; out="$2"
  case "$ref" in vault.*) ;; *) die "not a vault reference: $ref" ;; esac
  path="${ref#vault.}"
  [ -n "$path" ] || die "empty path in reference: $ref"
  case "$path" in *'#'*) die "reference has a #fragment, which 1.4.9 never wrote; refusing: $ref" ;; esac
  case "$BACKEND" in
    hashicorp) vault kv get -mount="$MOUNT" -field=value -- "$path" > "$out" ;;
    aws)       aws secretsmanager get-secret-value --secret-id "$path" --query SecretString --output text > "$out" ;;
    gcp)       sid="$(printf '%s' "$path" | base64 | tr -d '\n' | tr '+/' '-_' | tr -d '=')"
               gcloud secrets versions access latest --secret="$sid" --project="$GCP_PROJECT" > "$out" ;;
    cmd)       sh -c "$RESOLVE_CMD" _ "$path" > "$out" ;;
    file)      jq -j --arg k "$ref" 'if has($k) then .[$k] else error("missing: " + $k) end' "$SECRETS_FILE" > "$out" ;;
  esac || die "failed to resolve $ref"
  [ -s "$out" ] || die "resolved an empty secret for $ref; refusing to write it"
}

# ---------------------------------------------------------------------------
# Migrate
# ---------------------------------------------------------------------------
log "database : $(sql "SELECT current_database() || ' as ' || current_user")"
log "resolver : $BACKEND"
log "mode     : $([ "$APPLY" = 1 ] && echo APPLY || echo 'DRY RUN (nothing is written)')"
[ "$LIMIT" -gt 0 ] && log "limit    : $LIMIT rows per table"
[ "$APPLY" = 1 ] && log "journal  : $JOURNAL"
log ""

LIMIT_SQL=""
[ "$LIMIT" -gt 0 ] && LIMIT_SQL="LIMIT $LIMIT"

while IFS='|' read -r table pkcol cols jsoncols; do
  [ -n "$table" ] || continue
  if ! has_table "$table"; then log "== $table: table not present, skipping"; continue; fi
  if ! has_column "$table" encryption_status; then log "== $table: no encryption_status column, skipping"; continue; fi

  # Drop columns that no longer exist (oauth_configs.code_verifier was removed
  # by a later migration; harmless if the DB was already partially upgraded).
  live_cols=""
  for c in $(printf '%s' "$cols" | tr ',' ' '); do
    if has_column "$table" "$c"; then live_cols="$live_cols $c"
    else log "== $table: column $c not present, skipping it"; fi
  done

  count="$(sql "SELECT count(*) FROM $table WHERE encryption_status = 'vault'")"
  log "== $table: $count row(s) with encryption_status='vault'"
  [ "$count" != 0 ] || continue

  # Only pull a column's value when it is a vault ref; anything else becomes a
  # marker so unexpected content cannot break line-oriented parsing.
  select_cols="$pkcol"
  for c in $live_cols; do
    select_cols="$select_cols, CASE WHEN $c LIKE 'vault.%' THEN $c WHEN coalesce($c, '') = '' THEN '' ELSE '__NONREF__' END"
  done
  "${PSQL[@]}" -F "$SEP" -c "SELECT $select_cols FROM $table WHERE encryption_status = 'vault' ORDER BY $pkcol $LIMIT_SQL" > "$WORK/rows"

  while IFS= read -r line; do
    [ -n "$line" ] || continue
    pk="${line%%"$SEP"*}"
    rest="${line#*"$SEP"}"; [ "$rest" != "$line" ] || rest=""

    set_clause=""; where_clause=""; journal_lines=""; ncols=0; i=0
    for c in $live_cols; do
      i=$((i + 1))
      val="${rest%%"$SEP"*}"
      if [ "$rest" = "$val" ]; then rest=""; else rest="${rest#*"$SEP"}"; fi
      case "$val" in
        "") ;;
        __NONREF__)
          log "   WARN    $table $pkcol=$pk  $c holds a non-ref value under status 'vault'; leaving it untouched" ;;
        vault.*)
          ncols=$((ncols + 1))
          resolve_ref "$val" "$WORK/plain.$i"
          if is_json_col "$jsoncols" "$c"; then
            jq -e . "$WORK/plain.$i" >/dev/null 2>&1 \
              || die "$table $pkcol=$pk: secret behind $val is not valid JSON; refusing to write it into $c"
          fi
          printf '%s' "$val" > "$WORK/ref.$i"
          set_clause="${set_clause:+$set_clause, }$c = :'plain$i'"
          where_clause="$where_clause AND $c = :'ref$i'"
          journal_lines="$journal_lines$(printf '%s\t%s\t%s\t%s' "$table" "$pk" "$c" "$val")
"
          log "   RESOLVE $table $pkcol=$pk  $c <- $val ($(wc -c < "$WORK/plain.$i" | tr -d ' ') bytes)" ;;
      esac
    done

    if [ "$ncols" = 0 ]; then
      log "   STATUS  $table $pkcol=$pk  encryption_status vault -> plain_text (nothing to resolve)"
      journal_lines="$(printf '%s\t%s\t\t' "$table" "$pk")
"
    fi

    if [ "$APPLY" = 1 ]; then
      printf '%s' "$pk" > "$WORK/pk"
      {
        printf '\\set pk `cat %s`\n' "'$WORK/pk'"
        j=1
        while [ "$j" -le "$i" ]; do
          if [ -f "$WORK/plain.$j" ]; then
            printf '\\set plain%s `cat %s`\n' "$j" "'$WORK/plain.$j'"
            printf '\\set ref%s `cat %s`\n' "$j" "'$WORK/ref.$j'"
          fi
          j=$((j + 1))
        done
        printf "UPDATE %s SET %sencryption_status = 'plain_text' WHERE %s = :'pk' AND encryption_status = 'vault'%s RETURNING 1;\n" \
          "$table" "${set_clause:+$set_clause, }" "$pkcol" "$where_clause"
      } > "$WORK/stmt.sql"
      out="$(sqlf < "$WORK/stmt.sql")" || die "update failed for $table $pkcol=$pk; rows written so far are in $JOURNAL"
      [ "$out" = 1 ] || die "$table $pkcol=$pk changed underneath us (no row matched); rows written so far are in $JOURNAL"
      printf '%s' "$journal_lines" >> "$JOURNAL"
      log "   WROTE   $table $pkcol=$pk"
    fi
    rm -f "$WORK"/plain.* "$WORK"/ref.*
  done < "$WORK/rows"
done <<EOF
$SPECS
EOF

log ""
if [ "$APPLY" = 1 ]; then
  log "done. Remaining rows with encryption_status='vault':"
else
  log "dry run complete; nothing written. Re-run with --apply to migrate."
  log "Rows with encryption_status='vault':"
fi
while IFS='|' read -r table _ _ _; do
  [ -n "$table" ] || continue
  has_table "$table" || continue
  has_column "$table" encryption_status || continue
  log "   $table: $(sql "SELECT count(*) FROM $table WHERE encryption_status = 'vault'")"
done <<EOF
$SPECS
EOF
if [ "$APPLY" = 1 ]; then
  log ""
  log "next: start Bifrost 2.0.0 with config_store.vault_store still configured (same prefix)."
  log "      Its startup backfill AES-encrypts the rows this script set to plain_text."
  log "      To undo before that: $0 --rollback $JOURNAL --apply"
fi
