#!/bin/bash
# generate_json_payloads.sh - Generate all JSON test payloads for large payload testing
#
# Usage: ./generate_json_payloads.sh [output_dir]
#
# Self-contained: generates base64 data inline, no external dependencies.

set -e

OUTPUT_DIR="${1:-./json_payloads}"
mkdir -p "$OUTPUT_DIR"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo "=== JSON Payload Generator for Large Payload Testing ==="
echo "Output directory: $OUTPUT_DIR"
echo ""

# ─── Base64 data generation ───────────────────────────────────
# Generate random base64 data of a given byte count (before encoding).
# Output is ~4/3 of input size due to base64 expansion.
gen_base64() {
    local raw_bytes="$1"
    dd if=/dev/urandom bs=1024 count=$((raw_bytes / 1024)) 2>/dev/null | base64 | tr -d '\n'
}

# Deterministic base64 (repeating pattern for hash verification)
gen_base64_deterministic() {
    local raw_bytes="$1"
    python3 -c "
import base64, hashlib
target = $raw_bytes
# Build a 64KB seed block, then repeat it
seed = hashlib.sha256(b'bifrost-test-seed').digest() * 2048  # 32 * 2048 = 64KB
data = seed * ((target // len(seed)) + 1)
print(base64.b64encode(data[:target]).decode(), end='')
"
}

echo -ne "${YELLOW}Generating base64 data (15MB raw → ~20MB encoded)...${NC} "
BASE64_15MB=$(gen_base64 $((15 * 1024 * 1024)))
echo -e "${GREEN}done${NC}"

echo -ne "${YELLOW}Generating threshold data...${NC} "
# 10MB threshold = 10,485,760 bytes. base64 expands 4/3x.
# Below: 7.4MB raw → ~9.9MB file (below 10MB)
# Above: 8.0MB raw → ~10.7MB file (above 10MB)
BASE64_BELOW=$(gen_base64 $((7400 * 1024)))
BASE64_ABOVE=$(gen_base64 $((8000 * 1024)))
echo -e "${GREEN}done${NC}"

echo -ne "${YELLOW}Generating deterministic data...${NC} "
BASE64_DETERMINISTIC=$(gen_base64_deterministic $((15 * 1024 * 1024)))
echo -e "${GREEN}done${NC}"

echo ""

# ─── Helper to write a payload file ──────────────────────────
# Args: filename, json_content
write_payload() {
    local filename="$1"
    local content="$2"
    echo -ne "${YELLOW}Creating ${filename}...${NC} "
    echo "$content" > "$OUTPUT_DIR/$filename"
    local size=$(stat -f%z "$OUTPUT_DIR/$filename" 2>/dev/null || stat -c%s "$OUTPUT_DIR/$filename")
    local size_mb=$(echo "scale=2; $size / 1048576" | bc)
    echo -e "${GREEN}done (${size_mb}MB)${NC}"
}

# ═══════════════════════════════════════════════════════════════
# Phase A Payloads (metadata at START)
# ═══════════════════════════════════════════════════════════════
echo -e "${BLUE}--- Phase A Payloads (metadata at START) ---${NC}"
echo ""

write_payload "small_text_phase_a.json" '{
  "generationConfig": {
    "responseModalities": ["TEXT"],
    "temperature": 0.7
  },
  "contents": [
    {
      "parts": [
        {"text": "Hello, this is a small text request for testing."}
      ]
    }
  ]
}'

write_payload "large_audio_phase_a.json" "{
  \"generationConfig\": {
    \"responseModalities\": [\"AUDIO\"],
    \"speechConfig\": {
      \"voiceConfig\": {
        \"prebuiltVoiceConfig\": {
          \"voiceName\": \"Kore\"
        }
      }
    }
  },
  \"contents\": [
    {
      \"parts\": [
        {
          \"inlineData\": {
            \"mimeType\": \"video/mp4\",
            \"data\": \"${BASE64_15MB}\"
          }
        }
      ]
    }
  ]
}"

write_payload "large_image_phase_a.json" "{
  \"generationConfig\": {
    \"responseModalities\": [\"IMAGE\"],
    \"temperature\": 0.9
  },
  \"contents\": [
    {
      \"parts\": [
        {
          \"inlineData\": {
            \"mimeType\": \"video/mp4\",
            \"data\": \"${BASE64_15MB}\"
          }
        }
      ]
    }
  ]
}"

write_payload "large_text_phase_a.json" "{
  \"generationConfig\": {
    \"responseModalities\": [\"TEXT\"],
    \"maxOutputTokens\": 1024
  },
  \"contents\": [
    {
      \"parts\": [
        {
          \"inlineData\": {
            \"mimeType\": \"video/mp4\",
            \"data\": \"${BASE64_15MB}\"
          }
        }
      ]
    }
  ]
}"

write_payload "large_video_phase_a.json" "{
  \"generationConfig\": {
    \"responseModalities\": [\"TEXT\"],
    \"temperature\": 0.4
  },
  \"contents\": [
    {
      \"parts\": [
        {
          \"text\": \"Describe what happens in this video\"
        },
        {
          \"inlineData\": {
            \"mimeType\": \"video/mp4\",
            \"data\": \"${BASE64_15MB}\"
          }
        }
      ]
    }
  ]
}"

write_payload "large_speech_config_only_phase_a.json" "{
  \"generationConfig\": {
    \"speechConfig\": {
      \"voiceConfig\": {
        \"prebuiltVoiceConfig\": {
          \"voiceName\": \"Puck\"
        }
      }
    }
  },
  \"contents\": [
    {
      \"parts\": [
        {
          \"inlineData\": {
            \"mimeType\": \"video/mp4\",
            \"data\": \"${BASE64_15MB}\"
          }
        }
      ]
    }
  ]
}"

echo ""

# ═══════════════════════════════════════════════════════════════
# Phase B Payloads (metadata at END)
# ═══════════════════════════════════════════════════════════════
echo -e "${BLUE}--- Phase B Payloads (metadata at END) ---${NC}"
echo ""

write_payload "large_audio_phase_b.json" "{
  \"contents\": [
    {
      \"parts\": [
        {
          \"inlineData\": {
            \"mimeType\": \"video/mp4\",
            \"data\": \"${BASE64_15MB}\"
          }
        }
      ]
    }
  ],
  \"generationConfig\": {
    \"responseModalities\": [\"AUDIO\"],
    \"speechConfig\": {
      \"voiceConfig\": {
        \"prebuiltVoiceConfig\": {
          \"voiceName\": \"Kore\"
        }
      }
    }
  }
}"

write_payload "large_image_phase_b.json" "{
  \"contents\": [
    {
      \"parts\": [
        {
          \"inlineData\": {
            \"mimeType\": \"video/mp4\",
            \"data\": \"${BASE64_15MB}\"
          }
        }
      ]
    }
  ],
  \"generationConfig\": {
    \"responseModalities\": [\"IMAGE\"]
  }
}"

write_payload "large_text_phase_b.json" "{
  \"contents\": [
    {
      \"parts\": [
        {
          \"inlineData\": {
            \"mimeType\": \"video/mp4\",
            \"data\": \"${BASE64_15MB}\"
          }
        }
      ]
    }
  ],
  \"generationConfig\": {
    \"responseModalities\": [\"TEXT\"]
  }
}"

write_payload "large_video_phase_b.json" "{
  \"contents\": [
    {
      \"parts\": [
        {
          \"text\": \"Describe what happens in this video\"
        },
        {
          \"inlineData\": {
            \"mimeType\": \"video/mp4\",
            \"data\": \"${BASE64_15MB}\"
          }
        }
      ]
    }
  ],
  \"generationConfig\": {
    \"responseModalities\": [\"TEXT\"],
    \"temperature\": 0.4
  }
}"

write_payload "large_speech_config_only_phase_b.json" "{
  \"contents\": [
    {
      \"parts\": [
        {
          \"inlineData\": {
            \"mimeType\": \"video/mp4\",
            \"data\": \"${BASE64_15MB}\"
          }
        }
      ]
    }
  ],
  \"generationConfig\": {
    \"speechConfig\": {
      \"voiceConfig\": {
        \"prebuiltVoiceConfig\": {
          \"voiceName\": \"Puck\"
        }
      }
    }
  }
}"

echo ""

# ═══════════════════════════════════════════════════════════════
# Threshold Boundary Payloads
# ═══════════════════════════════════════════════════════════════
echo -e "${BLUE}--- Threshold Boundary Payloads ---${NC}"
echo ""

write_payload "threshold_below.json" "{
  \"generationConfig\": {\"responseModalities\": [\"AUDIO\"]},
  \"contents\": [{\"parts\": [{\"inlineData\": {\"mimeType\": \"video/mp4\", \"data\": \"${BASE64_BELOW}\"}}]}]
}"

write_payload "threshold_above.json" "{
  \"generationConfig\": {\"responseModalities\": [\"AUDIO\"]},
  \"contents\": [{\"parts\": [{\"inlineData\": {\"mimeType\": \"video/mp4\", \"data\": \"${BASE64_ABOVE}\"}}]}]
}"

echo ""

# ═══════════════════════════════════════════════════════════════
# Special Payloads
# ═══════════════════════════════════════════════════════════════
echo -e "${BLUE}--- Special Payloads ---${NC}"
echo ""

write_payload "large_no_metadata.json" "{
  \"contents\": [
    {
      \"parts\": [
        {
          \"inlineData\": {
            \"mimeType\": \"video/mp4\",
            \"data\": \"${BASE64_15MB}\"
          }
        }
      ]
    }
  ]
}"

write_payload "deterministic_phase_a.json" "{
  \"generationConfig\": {\"responseModalities\": [\"AUDIO\"]},
  \"contents\": [{\"parts\": [{\"inlineData\": {\"mimeType\": \"video/mp4\", \"data\": \"${BASE64_DETERMINISTIC}\"}}]}]
}"

echo ""
echo "=== Payload generation complete ==="
echo ""
echo "Files created in $OUTPUT_DIR:"
ls -lh "$OUTPUT_DIR"
echo ""
echo "Payload summary:"
echo "  Phase A (metadata at start): large_*_phase_a.json"
echo "  Phase B (metadata at end):   large_*_phase_b.json"
echo "  Threshold tests:             threshold_*.json"
echo "  No metadata:                 large_no_metadata.json"
echo "  Deterministic:               deterministic_phase_a.json"
