#!/bin/bash
# Load test: detect JSON key ordering mutations in Bifrost's tool parameter proxying.
# Compares the input payload's tool parameter key order against what Bifrost actually
# sent to the provider (via extra_fields.raw_request with json.RawMessage preservation).
#
# This catches both:
#   - Consistent mutations (struct field order overriding client order) — 100% rate
#   - Sporadic mutations (sync.Pool reuse, concurrency bugs) — variable rate
#
# Prerequisites:
#   - Bifrost running with send_back_raw_request: true on the openai provider
#   - OpenAI provider pointed at a mock server (any 200 response works)
#
# Usage: ./tests/load_test_parameter_ordering.sh [rps] [duration]
#   rps      - requests per second (default: 20)
#   duration - how many seconds to run (default: 10)

BIFROST_URL="http://localhost:8080/v1/chat/completions"
RPS="${1:-20}"
DURATION="${2:-10}"
NUM_REQUESTS=$((RPS * DURATION))

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

PAYLOAD=$(cat <<'ENDJSON'
{
  "model": "openai/gpt-4.1",
  "messages": [
    {
      "role": "system",
      "content": "Use the provided tool to structure your response."
    },
    {
      "role": "user",
      "content": "Summarize the key points from the meeting notes."
    }
  ],
  "temperature": 0,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "structured_response",
        "description": "Generate a structured response with reasoning and summary",
        "parameters": {
          "type": "object",
          "properties": {
            "reasoning": {
              "type": "string",
              "description": "Step by step reasoning process",
              "title": "Reasoning"
            },
            "summary": {
              "type": "string",
              "description": "The final summary",
              "title": "Summary"
            },
            "tags": {
              "description": "Relevant tags for categorization",
              "items": {
                "$ref": "#/$defs/Tag"
              },
              "title": "Tags",
              "type": "array"
            },
            "confidence": {
              "description": "Confidence score between 0 and 1",
              "title": "Confidence",
              "type": "number"
            }
          },
          "required": [
            "reasoning",
            "summary",
            "tags",
            "confidence"
          ],
          "$defs": {
            "Tag": {
              "type": "object",
              "description": "A categorization tag with label and relevance score.",
              "required": [
                "label"
              ],
              "properties": {
                "label": {
                  "description": "The tag label",
                  "title": "Label",
                  "type": "string"
                },
                "score": {
                  "description": "Relevance score from 0 to 1",
                  "title": "Score",
                  "type": "number"
                }
              },
              "title": "Tag"
            }
          }
        }
      }
    }
  ],
  "tool_choice": {
    "type": "function",
    "function": {
      "name": "structured_response"
    }
  }
}
ENDJSON
)

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

# Save input payload for the analyzer to read
echo "$PAYLOAD" > "$TMPDIR/input_payload.json"

# Write the analysis script — compares input vs output key ordering at every nesting level
cat > "$TMPDIR/analyze.py" << 'PYEOF'
import json, sys, os
from collections import OrderedDict

def extract_key_orders(obj, path=""):
    """Recursively extract key orders from all nested dicts.
    Returns a dict of {path: [keys]} for every object in the tree."""
    if not isinstance(obj, dict):
        return {}
    result = {path: list(obj.keys())}
    for key, val in obj.items():
        child_path = f"{path}.{key}" if path else key
        if isinstance(val, dict):
            result.update(extract_key_orders(val, child_path))
        elif isinstance(val, list):
            for i, item in enumerate(val):
                if isinstance(item, dict):
                    result.update(extract_key_orders(item, f"{child_path}[{i}]"))
    return result

def get_tool_parameters(payload):
    """Extract tool function parameters from a chat completion payload."""
    tools = payload.get("tools", [])
    if not tools:
        return None
    func = tools[0].get("function", {})
    return func.get("parameters")

# Load input payload (the known-good key order)
input_file = os.path.join(os.environ["TMPDIR"], "input_payload.json")
with open(input_file) as f:
    # json.load preserves insertion order in Python 3.7+
    input_payload = json.load(f, object_pairs_hook=OrderedDict)

input_params = get_tool_parameters(input_payload)
if input_params is None:
    print("ERROR:0:no tool parameters in input payload", file=sys.stderr)
    sys.exit(1)

input_orders = extract_key_orders(input_params)

# Analyze response
resp_file = sys.argv[1]
idx = sys.argv[2]

try:
    with open(resp_file) as f:
        resp = json.load(f, object_pairs_hook=OrderedDict)

    raw_request = resp.get("extra_fields", OrderedDict()).get("raw_request")
    if raw_request is None:
        print(f"NO_RAW_REQUEST:{idx}")
        sys.exit(0)

    output_params = get_tool_parameters(raw_request)
    if output_params is None:
        print(f"PARSE_ERROR:{idx}:no tool parameters in raw_request")
        sys.exit(0)

    output_orders = extract_key_orders(output_params)

    # Compare key orders at every path that exists in both
    mutations = []
    for path, input_keys in input_orders.items():
        output_keys = output_orders.get(path)
        if output_keys is None:
            continue  # path missing in output, different issue
        if input_keys != output_keys:
            mutations.append((path, input_keys, output_keys))

    if not mutations:
        print(f"OK:{idx}")
    else:
        print(f"MUTATED:{idx}")
        for path, inp, out in mutations:
            label = path if path else "parameters"
            print(f"  DETAIL:{idx}:{label}: input={inp} -> output={out}", file=sys.stderr)

except Exception as e:
    print(f"PARSE_ERROR:{idx}:{e}")
PYEOF

echo -e "${CYAN}Tool Parameter Key Order — Input vs Output Validator${NC}"
echo "=========================================================="
echo "Target:      $BIFROST_URL"
echo "RPS:         $RPS"
echo "Duration:    ${DURATION}s"
echo "Total:       $NUM_REQUESTS requests"
echo ""
echo "Validates that Bifrost preserves the client's original JSON"
echo "key ordering at every nesting level of tool parameters."
echo "=========================================================="
echo ""

# Send a single request and analyze
send_and_check() {
    local idx=$1
    local outfile="$TMPDIR/resp_${idx}.json"

    local httpcode
    httpcode=$(curl -s -o "$outfile" -w "%{http_code}" \
        -X POST "$BIFROST_URL" \
        -H "Content-Type: application/json" \
        -d "$PAYLOAD" \
        --max-time 30 2>/dev/null)

    if [ "$httpcode" != "200" ]; then
        echo "HTTP_ERROR:${idx}:${httpcode}"
        return
    fi

    python3 "$TMPDIR/analyze.py" "$outfile" "$idx"
}

export -f send_and_check
export BIFROST_URL PAYLOAD TMPDIR

# Send RPS requests per second for DURATION seconds
idx=0
for sec in $(seq 1 "$DURATION"); do
    for _ in $(seq 1 "$RPS"); do
        ((idx++))
        send_and_check "$idx" >> "$TMPDIR/results.txt" 2>>"$TMPDIR/details.log" &
    done
    echo -e "  Second $sec/$DURATION — launched $RPS requests"
    sleep 1
done

# Wait for all background jobs to finish
wait

results=$(cat "$TMPDIR/results.txt" 2>/dev/null)

OK=0
MUTATED=0
HTTP_ERRORS=0
PARSE_ERRORS=0
NO_RAW=0

while IFS= read -r line; do
    case "$line" in
        OK:*)           ((OK++)) ;;
        MUTATED:*)
            ((MUTATED++))
            idx=$(echo "$line" | cut -d: -f2)
            echo -e "${RED}  [MUTATED] Request #${idx}${NC}"
            ;;
        HTTP_ERROR:*)
            ((HTTP_ERRORS++))
            idx=$(echo "$line" | cut -d: -f2)
            code=$(echo "$line" | cut -d: -f3)
            echo -e "${YELLOW}  [HTTP ${code}] Request #${idx}${NC}"
            ;;
        NO_RAW_REQUEST:*)
            ((NO_RAW++))
            echo -e "${YELLOW}  [NO RAW REQUEST] Request #$(echo "$line" | cut -d: -f2) - is send_back_raw_request enabled?${NC}"
            ;;
        PARSE_ERROR:*)
            ((PARSE_ERRORS++))
            echo -e "${YELLOW}  [PARSE ERROR] ${line}${NC}"
            ;;
    esac
done <<< "$results"

TOTAL=$((OK + MUTATED + HTTP_ERRORS + PARSE_ERRORS + NO_RAW))

echo ""
echo "=========================================================="
echo -e "${CYAN}Results (${TOTAL}/${NUM_REQUESTS} completed):${NC}"
echo -e "  ${GREEN}OK (order preserved):  $OK${NC}"
echo -e "  ${RED}MUTATED (reordered):   $MUTATED${NC}"
echo -e "  ${YELLOW}HTTP errors:           $HTTP_ERRORS${NC}"
echo -e "  ${YELLOW}No raw request:        $NO_RAW${NC}"
echo -e "  ${YELLOW}Parse errors:          $PARSE_ERRORS${NC}"
echo "=========================================================="

if [ "$MUTATED" -gt 0 ]; then
    RATE=$(python3 -c "print(f'{$MUTATED/$TOTAL*100:.1f}')" 2>/dev/null || echo "?")
    echo ""
    echo -e "${RED}MUTATION RATE: ${RATE}% ($MUTATED / $TOTAL)${NC}"
    echo ""
    echo -e "${CYAN}Key order mutations (input vs output):${NC}"
    cat "$TMPDIR/details.log" 2>/dev/null | head -50
    exit 1
elif [ "$NO_RAW" -gt 0 ]; then
    echo ""
    echo -e "${YELLOW}WARNING: No raw_request in responses. Enable send_back_raw_request in provider config.${NC}"
    exit 2
else
    echo ""
    echo -e "${GREEN}All $OK requests preserved the original JSON key ordering.${NC}"
    exit 0
fi
