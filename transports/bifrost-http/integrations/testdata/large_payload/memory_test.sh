#!/bin/bash
# memory_test.sh - Memory allocation comparison: streaming vs buffering
#
# Usage: ./memory_test.sh
#
# This script runs Go-level benchmarks that compare memory allocations between:
# - Streaming path (Phase A/B): Our large payload optimization
# - Buffering path: The traditional full-unmarshal approach (what happens without our changes)
#
# No running server needed - tests run at the Go level using runtime.MemStats
# and Go's built-in benchmark framework with -benchmem.
#
# The comparison is side-by-side in a single run: streaming and buffering benchmarks
# process the same payloads (1MB, 5MB, 20MB), showing allocs/op and bytes/op.
# Streaming B/op should stay constant (~200KB) while buffering scales with payload size.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$SCRIPT_DIR/../../../.."

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

echo "╔══════════════════════════════════════════════════════════════╗"
echo "║     Memory Allocation Comparison: Streaming vs Buffering    ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
echo "This test compares Go heap allocations (not OS RSS) for:"
echo "  - Streaming Phase A: Prefetch 64KB + sonic.Get + io.MultiReader"
echo "  - Streaming Phase B: Prefetch + jstream + TeeReader + io.Pipe"
echo "  - Buffering (old):   Read all + sonic.Unmarshal (full payload in memory)"
echo ""

cd "$PROJECT_ROOT"

# ═══════════════════════════════════════════════════════════════
# Part 1: runtime.MemStats Tests (definitive heap measurement)
# ═══════════════════════════════════════════════════════════════
echo -e "${BLUE}══════════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}  Part 1: Heap Allocation Tests (runtime.MemStats)${NC}"
echo -e "${BLUE}══════════════════════════════════════════════════════════════${NC}"
echo ""
echo "Running TestHeapAllocation_StreamingVsBuffering..."
echo "  Measures TotalAlloc per operation for streaming vs buffering."
echo "  Streaming should allocate <50% of what buffering allocates."
echo ""

if go test -v -run "TestHeapAllocation_StreamingVsBuffering" \
    -timeout 120s \
    ./bifrost-http/integrations/... 2>&1; then
    echo ""
    echo -e "${GREEN}✓ Heap allocation tests passed${NC}"
else
    echo ""
    echo -e "${RED}✗ Heap allocation tests failed${NC}"
    echo "  Streaming may be allocating more memory than expected."
fi

echo ""

# ═══════════════════════════════════════════════════════════════
# Part 2: Concurrent Memory Pressure Test
# ═══════════════════════════════════════════════════════════════
echo -e "${BLUE}══════════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}  Part 2: Concurrent Memory Pressure Test${NC}"
echo -e "${BLUE}══════════════════════════════════════════════════════════════${NC}"
echo ""
echo "Running TestConcurrentStreaming_MemoryBounded..."
echo "  10 concurrent goroutines x 10MB payloads."
echo "  Buffering: ~200MB total. Streaming: ~1.3MB total."
echo ""

if go test -v -run "TestConcurrentStreaming_MemoryBounded" \
    -timeout 120s \
    ./bifrost-http/integrations/... 2>&1; then
    echo ""
    echo -e "${GREEN}✓ Concurrent memory test passed${NC}"
else
    echo ""
    echo -e "${RED}✗ Concurrent memory test failed${NC}"
fi

echo ""

# ═══════════════════════════════════════════════════════════════
# Part 3: Benchmarks with -benchmem (B/op and allocs/op)
# ═══════════════════════════════════════════════════════════════
echo -e "${BLUE}══════════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}  Part 3: Go Benchmarks with -benchmem${NC}"
echo -e "${BLUE}══════════════════════════════════════════════════════════════${NC}"
echo ""
echo "Running BenchmarkMemory_* benchmarks..."
echo "  Look at B/op column: streaming should stay ~200KB,"
echo "  while buffering should scale with payload size."
echo ""

BENCH_OUTPUT=$(mktemp)

go test -bench="BenchmarkMemory" -benchmem -benchtime=3s \
    -run='^$' \
    ./bifrost-http/integrations/... 2>&1 | tee "$BENCH_OUTPUT"

echo ""

# ═══════════════════════════════════════════════════════════════
# Part 4: Summary - Parse and compare benchmark results
# ═══════════════════════════════════════════════════════════════
echo -e "${BLUE}══════════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}  Summary${NC}"
echo -e "${BLUE}══════════════════════════════════════════════════════════════${NC}"
echo ""

# Extract B/op values for comparison
echo -e "${CYAN}Bytes allocated per operation (B/op):${NC}"
echo ""
printf "  %-45s %15s\n" "Benchmark" "B/op"
printf "  %-45s %15s\n" "─────────────────────────────────────────────" "───────────────"

while IFS= read -r line; do
    if echo "$line" | grep -qE "BenchmarkMemory.*B/op"; then
        name=$(echo "$line" | awk '{print $1}')
        bop=$(echo "$line" | grep -oE '[0-9]+ B/op' | head -1)
        # Shorten the name for display
        short_name=$(echo "$name" | sed 's/BenchmarkMemory_//' | sed 's/-[0-9]*//')
        printf "  %-45s %15s\n" "$short_name" "$bop"
    fi
done < "$BENCH_OUTPUT"

echo ""
echo -e "${CYAN}Key insights:${NC}"
echo "  - Streaming B/op should be roughly CONSTANT across payload sizes"
echo "  - Buffering B/op should SCALE with payload size (~2-3x payload)"
echo "  - The larger the payload, the bigger the savings"
echo ""

rm -f "$BENCH_OUTPUT"

echo "=== Memory Test Complete ==="
