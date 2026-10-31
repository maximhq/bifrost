#!/bin/bash
# Pod entrypoint: arms a self-teardown timer, then execs the server command passed as "$@" (vllm serve / sglang).
# The deadline lives on the pod volume so container restarts don't reset it; overwrite the file to extend.
deadline_file=/workspace/.teardown_at
[ -f "$deadline_file" ] || echo $(( $(date +%s) + TEARDOWN_AFTER_MINUTES * 60 )) > "$deadline_file"
echo "[teardown] pod will ${TEARDOWN_ACTION} at $(date -d "@$(cat "$deadline_file")")"

(
  while [ "$(date +%s)" -lt "$(cat "$deadline_file")" ]; do sleep 30; done
  api="https://rest.runpod.io/v1/pods/${RUNPOD_POD_ID}"
  auth="Authorization: Bearer ${RUNPOD_API_KEY}"
  echo "[teardown] timeout reached, running ${TEARDOWN_ACTION}"
  for attempt in 1 2 3 4 5; do
    if [ "$TEARDOWN_ACTION" = "terminate" ] && curl -sf --connect-timeout 10 --max-time 30 -X DELETE -H "$auth" "$api"; then
      rm -f "$deadline_file"
      exit 0
    fi
    # Stop releases the GPU; also the fallback if the pod-scoped key can't delete.
    curl -sf --connect-timeout 10 --max-time 30 -X POST -H "$auth" "$api/stop" && rm -f "$deadline_file" && exit 0
    echo "[teardown] attempt ${attempt} failed, retrying in 30s"
    sleep 30
  done
  echo "[teardown] failed to ${TEARDOWN_ACTION} pod ${RUNPOD_POD_ID} after 5 attempts"
) &

exec "$@"