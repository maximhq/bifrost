#!/bin/bash
# Pod entrypoint: arms a self-teardown timer, then runs `vllm serve "$@"`.
# The deadline lives on the pod volume so container restarts don't reset it; overwrite the file to extend.
deadline_file=/workspace/.teardown_at
[ -f "$deadline_file" ] || echo $(( $(date +%s) + TEARDOWN_AFTER_MINUTES * 60 )) > "$deadline_file"
echo "[teardown] pod will ${TEARDOWN_ACTION} at $(date -d "@$(cat "$deadline_file")")"

(
  while [ "$(date +%s)" -lt "$(cat "$deadline_file")" ]; do sleep 30; done
  api="https://rest.runpod.io/v1/pods/${RUNPOD_POD_ID}"
  auth="Authorization: Bearer ${RUNPOD_API_KEY}"
  echo "[teardown] timeout reached, running ${TEARDOWN_ACTION}"
  if [ "$TEARDOWN_ACTION" = "terminate" ] && curl -sf -X DELETE -H "$auth" "$api"; then
    exit 0
  fi
  # Stop releases the GPU; also the fallback if the pod-scoped key can't delete.
  curl -sf -X POST -H "$auth" "$api/stop" || echo "[teardown] failed to stop pod ${RUNPOD_POD_ID}"
) &

exec vllm serve "$@"
