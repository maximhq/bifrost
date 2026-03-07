#!/bin/bash
# Load test: detect JSON key ordering mutations in Bifrost's request proxying.
# Sends randomized payloads with different schema shapes and compares the input
# key order against what Bifrost actually sent to the provider (via
# extra_fields.raw_request with json.RawMessage preservation).
#
# Validates:
#   - Tool parameter key ordering at every nesting level (properties, $defs, nested schemas)
#   - tool_choice serialization (key ordering, no extra zero-value fields like "custom"/"allowed_tools")
#   - Multiple tool schemas, deeply nested objects, adversarial property orderings
#
# Each request randomly picks from 8 different payload shapes to maximize coverage.
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

BIFROST_URL="http://localhost:8080/litellm/v1/chat/completions"
RPS="${1:-20}"
DURATION="${2:-10}"
NUM_REQUESTS=$((RPS * DURATION))

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

# ---------------------------------------------------------------------------
# Payload 1: Standard — non-alpha properties, $defs after required, function tool_choice
# ---------------------------------------------------------------------------
cat > "$TMPDIR/payload_1.json" << 'EOF'
{
  "model": "openai/gpt-4.1",
  "messages": [{"role": "user", "content": "test"}],
  "temperature": 0,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "structured_response",
        "description": "Generate a structured response",
        "parameters": {
          "type": "object",
          "properties": {
            "reasoning": {
              "type": "string",
              "description": "Step by step reasoning",
              "title": "Reasoning"
            },
            "summary": {
              "type": "string",
              "description": "The final summary",
              "title": "Summary"
            },
            "tags": {
              "description": "Relevant tags",
              "items": {"$ref": "#/$defs/Tag"},
              "title": "Tags",
              "type": "array"
            },
            "confidence": {
              "description": "Confidence score",
              "title": "Confidence",
              "type": "number"
            }
          },
          "required": ["reasoning", "summary", "tags", "confidence"],
          "$defs": {
            "Tag": {
              "type": "object",
              "description": "A tag",
              "required": ["label"],
              "properties": {
                "label": {"description": "The tag label", "title": "Label", "type": "string"},
                "score": {"description": "Relevance score", "title": "Score", "type": "number"}
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
    "function": {"name": "structured_response"}
  }
}
EOF

# ---------------------------------------------------------------------------
# Payload 2: Reverse-alpha properties, $defs at TOP, string tool_choice "auto"
# Property names z_ y_ x_ w_ would get reordered to w_ x_ y_ z_ if sorted
# ---------------------------------------------------------------------------
cat > "$TMPDIR/payload_2.json" << 'EOF'
{
  "model": "openai/gpt-4.1",
  "messages": [{"role": "user", "content": "test"}],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "reverse_alpha_tool",
        "parameters": {
          "$defs": {
            "ZItem": {
              "type": "object",
              "properties": {
                "z_name": {"type": "string"},
                "a_value": {"type": "number"}
              },
              "required": ["z_name"]
            }
          },
          "type": "object",
          "properties": {
            "z_output": {"type": "string", "description": "Last alphabetically, first in schema"},
            "y_reasoning": {"type": "string", "description": "Second to last"},
            "x_items": {
              "type": "array",
              "items": {"$ref": "#/$defs/ZItem"},
              "description": "Third to last"
            },
            "w_confidence": {"type": "number", "description": "Fourth to last"}
          },
          "required": ["z_output", "y_reasoning"]
        }
      }
    }
  ],
  "tool_choice": "auto"
}
EOF

# ---------------------------------------------------------------------------
# Payload 3: Multiple tools, deeply nested objects, string tool_choice "required"
# ---------------------------------------------------------------------------
cat > "$TMPDIR/payload_3.json" << 'EOF'
{
  "model": "openai/gpt-4.1",
  "messages": [{"role": "user", "content": "test"}],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "deep_nested_tool",
        "description": "Tool with 3-level nesting",
        "parameters": {
          "type": "object",
          "properties": {
            "output": {
              "type": "object",
              "description": "Nested output",
              "properties": {
                "verdict": {"type": "string"},
                "metadata": {
                  "type": "object",
                  "properties": {
                    "timestamp": {"type": "string"},
                    "source": {"type": "string"},
                    "confidence": {"type": "number"},
                    "author": {"type": "string"}
                  }
                },
                "score": {"type": "number"}
              }
            },
            "chain_of_thought": {"type": "string"},
            "answer": {"type": "string"}
          },
          "required": ["output", "answer"]
        }
      }
    },
    {
      "type": "function",
      "function": {
        "name": "secondary_tool",
        "description": "A second tool to verify multi-tool ordering",
        "parameters": {
          "type": "object",
          "properties": {
            "query": {"type": "string", "description": "Search query"},
            "max_results": {"type": "integer", "description": "Limit"},
            "filters": {
              "type": "object",
              "properties": {
                "date_range": {"type": "string"},
                "category": {"type": "string"},
                "active_only": {"type": "boolean"}
              }
            }
          },
          "required": ["query"]
        }
      }
    }
  ],
  "tool_choice": "required"
}
EOF

# ---------------------------------------------------------------------------
# Payload 4: Many properties in adversarial order (zigzag), no $defs, tool_choice "none"
# Names deliberately interleave early/late alphabet letters
# ---------------------------------------------------------------------------
cat > "$TMPDIR/payload_4.json" << 'EOF'
{
  "model": "openai/gpt-4.1",
  "messages": [{"role": "user", "content": "test"}],
  "temperature": 0.7,
  "max_tokens": 500,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "zigzag_tool",
        "description": "Properties in zigzag alphabetical order",
        "parameters": {
          "type": "object",
          "properties": {
            "zebra": {"type": "string"},
            "apple": {"type": "string"},
            "yarn": {"type": "number"},
            "banana": {"type": "boolean"},
            "xenon": {"type": "string"},
            "cherry": {"type": "integer"},
            "walnut": {"type": "string"},
            "date": {"type": "array", "items": {"type": "string"}},
            "violet": {"type": "number"},
            "elderberry": {"type": "string"}
          },
          "required": ["zebra", "apple", "yarn"]
        }
      }
    }
  ],
  "tool_choice": "none"
}
EOF

# ---------------------------------------------------------------------------
# Payload 5: $defs with multiple definitions, additionalProperties, nested $ref
# ---------------------------------------------------------------------------
cat > "$TMPDIR/payload_5.json" << 'EOF'
{
  "model": "openai/gpt-4.1",
  "messages": [{"role": "user", "content": "test"}],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "AnswerResponseModel",
        "description": "Realistic pydantic-generated schema",
        "parameters": {
          "$defs": {
            "Citation": {
              "type": "object",
              "properties": {
                "url": {"type": "string", "description": "Source URL"},
                "text": {"type": "string", "description": "Cited text"},
                "page_number": {"type": "integer", "description": "Page"}
              },
              "required": ["url", "text"]
            },
            "Metadata": {
              "type": "object",
              "properties": {
                "model_version": {"type": "string"},
                "latency_ms": {"type": "number"},
                "token_count": {"type": "integer"}
              },
              "required": ["model_version"]
            }
          },
          "type": "object",
          "properties": {
            "answer": {"type": "string", "description": "The answer"},
            "chain_of_thought": {"type": "string", "description": "Reasoning steps"},
            "citations": {
              "type": "array",
              "items": {"$ref": "#/$defs/Citation"},
              "description": "Supporting citations"
            },
            "is_unanswered": {"type": "boolean", "description": "Whether answerable"},
            "metadata": {"$ref": "#/$defs/Metadata"}
          },
          "required": ["answer", "is_unanswered"],
          "additionalProperties": false
        }
      }
    }
  ],
  "tool_choice": {
    "type": "function",
    "function": {"name": "AnswerResponseModel"}
  }
}
EOF

# ---------------------------------------------------------------------------
# Payload 6: Minimal single-property tool, no tool_choice — tests baseline passthrough
# Also uses top-level keys in non-standard order (tools before messages)
# ---------------------------------------------------------------------------
cat > "$TMPDIR/payload_6.json" << 'EOF'
{
  "model": "openai/gpt-4.1",
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "simple_extractor",
        "parameters": {
          "type": "object",
          "properties": {
            "result": {"type": "string", "description": "Extracted result"}
          },
          "required": ["result"]
        }
      }
    }
  ],
  "messages": [{"role": "user", "content": "test"}],
  "temperature": 0
}
EOF

# ---------------------------------------------------------------------------
# Payload 7: EXACT reproduction of reported bug — Issue 1 + 2 + 3 combined
# tool_choice: {type, function} with AnswerResponseModel (Issue 1)
# properties: answer, chain_of_thought, citations, is_unanswered (Issue 2)
# $defs with Citation at TOP of parameters object (Issue 3)
# ---------------------------------------------------------------------------
cat > "$TMPDIR/payload_7.json" << 'EOF'
{
  "model": "openai/gpt-4.1",
  "messages": [{"role": "user", "content": "test"}],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "AnswerResponseModel",
        "parameters": {
          "$defs": {
            "Citation": {
              "type": "object",
              "properties": {
                "url": {"type": "string"},
                "text": {"type": "string"}
              },
              "required": ["url", "text"]
            }
          },
          "properties": {
            "answer": {"type": "string", "description": "The answer"},
            "chain_of_thought": {"type": "string", "description": "Reasoning"},
            "citations": {
              "type": "array",
              "items": {"$ref": "#/$defs/Citation"}
            },
            "is_unanswered": {"type": "boolean"}
          },
          "required": ["answer", "is_unanswered"],
          "type": "object"
        }
      }
    }
  ],
  "tool_choice": {
    "type": "function",
    "function": {
      "name": "AnswerResponseModel"
    }
  }
}
EOF

# ---------------------------------------------------------------------------
# Payload 8: tool_choice string variants cycle — ensures "none"/"auto"/"required"
# pass through as strings and don't get expanded to structs
# Also: properties in exact reverse alphabetical to maximize reorder detection
# ---------------------------------------------------------------------------
cat > "$TMPDIR/payload_8.json" << 'EOF'
{
  "model": "openai/gpt-4.1",
  "messages": [{"role": "user", "content": "test"}],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "reverse_order_check",
        "parameters": {
          "type": "object",
          "properties": {
            "zulu": {"type": "string"},
            "yankee": {"type": "string"},
            "x_ray": {"type": "string"},
            "whiskey": {"type": "number"},
            "victor": {"type": "boolean"},
            "uniform": {"type": "string"},
            "tango": {"type": "integer"},
            "sierra": {"type": "string"},
            "romeo": {"type": "number"},
            "quebec": {"type": "string"},
            "papa": {"type": "boolean"},
            "oscar": {"type": "string"},
            "november": {"type": "string"},
            "mike": {"type": "number"},
            "lima": {"type": "string"},
            "kilo": {"type": "boolean"},
            "juliet": {"type": "string"},
            "india": {"type": "integer"},
            "hotel": {"type": "string"},
            "golf": {"type": "number"},
            "foxtrot": {"type": "string"},
            "echo_field": {"type": "boolean"},
            "delta": {"type": "string"},
            "charlie": {"type": "number"},
            "bravo": {"type": "string"},
            "alpha": {"type": "string"}
          },
          "required": ["zulu", "alpha"]
        }
      }
    }
  ],
  "tool_choice": "auto"
}
EOF

NUM_PAYLOADS=8

# ---------------------------------------------------------------------------
# Python analyzer — compares input vs output key ordering at every nesting level
# ---------------------------------------------------------------------------
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

def get_all_tool_parameters(payload):
    """Extract tool function parameters from ALL tools in a chat completion payload.
    Returns list of (tool_name, parameters_dict) tuples."""
    tools = payload.get("tools", [])
    result = []
    for tool in tools:
        func = tool.get("function", {})
        name = func.get("name", "unknown")
        params = func.get("parameters")
        if params is not None:
            result.append((name, params))
    return result

def check_tool_choice(input_payload, raw_request):
    """Check tool_choice serialization: key ordering and no extra fields.
    Returns a list of (description, input, output) mutation tuples.

    Catches Issue 1 from the bug report:
      - Zero-value fields injected: "custom":{"name":""}, "allowed_tools":{"mode":"","tools":null}
      - Key reordering: "type" moving from first to last position
      - String tool_choice ("auto"/"none"/"required") being expanded to struct
    """
    mutations = []
    input_tc = input_payload.get("tool_choice")
    output_tc = raw_request.get("tool_choice")
    if input_tc is None and output_tc is None:
        return mutations
    if input_tc is None and output_tc is not None:
        mutations.append(("tool_choice (injected)", None, output_tc))
        return mutations
    if input_tc is not None and output_tc is None:
        mutations.append(("tool_choice (dropped)", input_tc, None))
        return mutations

    # Issue 1: string tool_choice must stay as string, not become struct
    if isinstance(input_tc, str):
        if isinstance(output_tc, dict):
            mutations.append(("tool_choice (string->struct)", input_tc, list(output_tc.keys())))
        elif output_tc != input_tc:
            mutations.append(("tool_choice (string)", input_tc, output_tc))
        return mutations

    if isinstance(input_tc, dict) and isinstance(output_tc, dict):
        input_keys = list(input_tc.keys())
        output_keys = list(output_tc.keys())

        # Issue 1.2: Check for zero-value fields from unused union variants
        # These are the exact fields reported in the bug:
        zero_value_fields = {"custom", "allowed_tools"}
        injected = zero_value_fields & (set(output_keys) - set(input_keys))
        if injected:
            mutations.append(("tool_choice (zero-value fields injected)", sorted(injected), [
                f'{k}={json.dumps(output_tc[k])}' for k in sorted(injected)
            ]))

        # Any other extra fields
        other_extra = set(output_keys) - set(input_keys) - zero_value_fields
        if other_extra:
            mutations.append(("tool_choice (unexpected extra fields)", [], list(other_extra)))

        # Issue 1.2: Check key ordering — "type" should stay first, not move to end
        if input_keys != output_keys:
            mutations.append(("tool_choice (key order)", input_keys, output_keys))

        # Recursively check nested key orders (e.g. function object)
        input_tc_orders = extract_key_orders(input_tc, "tool_choice")
        output_tc_orders = extract_key_orders(output_tc, "tool_choice")
        for path, inp_keys in input_tc_orders.items():
            out_keys = output_tc_orders.get(path)
            if out_keys is not None and inp_keys != out_keys:
                mutations.append((path, inp_keys, out_keys))
    return mutations

def check_defs_position(input_params, output_params, tool_idx):
    """Check that $defs stays in its original position within the parameters object.

    Catches Issue 3 from the bug report:
      - $defs at top of parameters moves to bottom after round-trip
    """
    mutations = []
    input_keys = list(input_params.keys())
    output_keys = list(output_params.keys())

    if "$defs" in input_keys and "$defs" in output_keys:
        input_pos = input_keys.index("$defs")
        output_pos = output_keys.index("$defs")
        if input_pos != output_pos:
            mutations.append((
                f"tools[{tool_idx}].parameters ($defs position)",
                f"$defs at index {input_pos} in {input_keys}",
                f"$defs at index {output_pos} in {output_keys}"
            ))

    if "definitions" in input_keys and "definitions" in output_keys:
        input_pos = input_keys.index("definitions")
        output_pos = output_keys.index("definitions")
        if input_pos != output_pos:
            mutations.append((
                f"tools[{tool_idx}].parameters (definitions position)",
                f"definitions at index {input_pos} in {input_keys}",
                f"definitions at index {output_pos} in {output_keys}"
            ))

    return mutations

# Analyze response
resp_file = sys.argv[1]
idx = sys.argv[2]
payload_file = sys.argv[3]

try:
    # Load input payload (the known-good key order)
    with open(payload_file) as f:
        input_payload = json.load(f, object_pairs_hook=OrderedDict)

    input_tool_params = get_all_tool_parameters(input_payload)
    if not input_tool_params:
        print(f"PARSE_ERROR:{idx}:no tool parameters in input payload")
        sys.exit(0)

    with open(resp_file) as f:
        resp = json.load(f, object_pairs_hook=OrderedDict)

    raw_request = resp.get("extra_fields", OrderedDict()).get("raw_request")
    if raw_request is None:
        print(f"NO_RAW_REQUEST:{idx}")
        sys.exit(0)

    output_tool_params = get_all_tool_parameters(raw_request)
    if not output_tool_params:
        print(f"PARSE_ERROR:{idx}:no tool parameters in raw_request")
        sys.exit(0)

    mutations = []

    # Compare each tool's parameter key ordering (Issue 2: properties reordering)
    for i, (inp_name, inp_params) in enumerate(input_tool_params):
        if i >= len(output_tool_params):
            mutations.append((f"tool[{i}] missing", inp_name, "MISSING"))
            continue
        out_name, out_params = output_tool_params[i]

        input_orders = extract_key_orders(inp_params, f"tools[{i}].parameters")
        output_orders = extract_key_orders(out_params, f"tools[{i}].parameters")

        for path, input_keys in input_orders.items():
            output_keys = output_orders.get(path)
            if output_keys is None:
                continue
            if input_keys != output_keys:
                mutations.append((path, input_keys, output_keys))

        # Issue 3: $defs position within parameters object
        mutations.extend(check_defs_position(inp_params, out_params, i))

    # Issue 1: tool_choice serialization (zero-value fields, key ordering)
    mutations.extend(check_tool_choice(input_payload, raw_request))

    if not mutations:
        print(f"OK:{idx}")
    else:
        payload_num = os.path.basename(payload_file).replace("payload_", "").replace(".json", "")
        print(f"MUTATED:{idx}")
        for path, inp, out in mutations:
            label = path if path else "parameters"
            print(f"  DETAIL:{idx}:P{payload_num}:{label}: input={inp} -> output={out}", file=sys.stderr)

except Exception as e:
    print(f"PARSE_ERROR:{idx}:{e}")
PYEOF

echo -e "${CYAN}JSON Serialization Fidelity — Input vs Output Validator${NC}"
echo "=========================================================="
echo "Target:      $BIFROST_URL"
echo "RPS:         $RPS"
echo "Duration:    ${DURATION}s"
echo "Total:       $NUM_REQUESTS requests"
echo "Payloads:    $NUM_PAYLOADS variants (randomly selected per request)"
echo ""
echo "Validates that Bifrost preserves the client's original JSON"
echo "key ordering (tool parameters + tool_choice) and doesn't"
echo "inject extra zero-value fields."
echo ""
echo "Payload variants:"
echo "  P1: Standard — non-alpha properties, \$defs after required, function tool_choice"
echo "  P2: Reverse-alpha properties, \$defs at TOP, tool_choice \"auto\""
echo "  P3: Multiple tools, 3-level nested objects, tool_choice \"required\""
echo "  P4: 10 zigzag-ordered properties, no \$defs, tool_choice \"none\""
echo "  P5: Multiple \$defs, additionalProperties, pydantic-style schema"
echo "  P6: Minimal single-property tool, no tool_choice, non-standard top-level order"
echo "  P7: EXACT bug report reproduction — all 3 issues in one payload"
echo "  P8: 26 reverse-alpha NATO properties — maximum reorder detection"
echo "=========================================================="
echo ""

# Send a single request with a random payload and analyze
send_and_check() {
    local idx=$1
    local payload_num=$(( (RANDOM % NUM_PAYLOADS) + 1 ))
    local payload_file="$TMPDIR/payload_${payload_num}.json"
    local outfile="$TMPDIR/resp_${idx}.json"

    local httpcode
    httpcode=$(curl -s -o "$outfile" -w "%{http_code}" \
        -X POST "$BIFROST_URL" \
        -H "Content-Type: application/json" \
        -d @"$payload_file" \
        --max-time 30 2>/dev/null)

    if [ "$httpcode" != "200" ]; then
        echo "HTTP_ERROR:${idx}:${httpcode}"
        return
    fi

    python3 "$TMPDIR/analyze.py" "$outfile" "$idx" "$payload_file"
}

export -f send_and_check
export BIFROST_URL TMPDIR NUM_PAYLOADS

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
    echo -e "${GREEN}All $OK requests preserved the original JSON key ordering across all $NUM_PAYLOADS payload variants.${NC}"
    exit 0
fi
