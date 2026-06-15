#!/usr/bin/env bash
set -euxo pipefail

MODEL="${MODEL:-anthropic/claude-sonnet-4}"

PROMPT=$(cat <<'EOF'
Read PRD.md and progress.txt.

1. Find the next incomplete task.
2. Implement it.
3. Commit changes.
4. Update progress.txt.

ONLY DO ONE TASK AT A TIME.
EOF
)

opencode run \
  --model "$MODEL" \
  --dangerously-skip-permissions \
  --print-logs \
  --log-level DEBUG \
  "$PROMPT"

echo "Exit code: $?"
