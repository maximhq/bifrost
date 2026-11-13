//go:build dev

package handlers

import (
	"bytes"
	"regexp"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseGoroutineProfileReference is the parser as first written. It is kept here as the oracle: the
// in-place parser must group a profile exactly as this one does.
func parseGoroutineProfileReference(profile string) []GoroutineGroup {
	// Regex to match goroutine header: "goroutine N [state, wait reason]:"
	// Examples:
	//   goroutine 1 [running]:
	//   goroutine 42 [select, 5 minutes]:
	//   goroutine 100 [chan receive]:
	headerRegex := regexp.MustCompile(`goroutine \d+ \[([^\]]+)\]:`)

	// Split by "goroutine " to get individual goroutine blocks
	blocks := strings.Split(profile, "goroutine ")

	// Map to group goroutines by stack signature
	groupMap := make(map[string]*GoroutineGroup)

	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}

		// Re-add "goroutine " prefix for regex matching
		fullBlock := "goroutine " + block

		// Extract state from header
		matches := headerRegex.FindStringSubmatch(fullBlock)
		if len(matches) < 2 {
			continue
		}

		stateInfo := matches[1]
		state := stateInfo
		waitReason := ""

		// Parse state and wait reason (e.g., "select, 5 minutes" -> state="select", waitReason="5 minutes")
		if idx := strings.Index(stateInfo, ","); idx != -1 {
			state = strings.TrimSpace(stateInfo[:idx])
			waitReason = strings.TrimSpace(stateInfo[idx+1:])
		}

		// Get stack trace (everything after the header line)
		lines := strings.Split(block, "\n")
		if len(lines) < 2 {
			continue
		}

		// Extract stack frames (skip the header line which is lines[0])
		var stackLines []string
		var topFunc string
		for i := 1; i < len(lines); i++ {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			stackLines = append(stackLines, line)

			// First function line (not a file:line) is the top function
			if topFunc == "" && !strings.HasPrefix(line, "/") && !strings.Contains(line, ".go:") {
				topFunc = line
			}
		}

		if len(stackLines) == 0 {
			continue
		}

		// Create a signature from the stack (top 10 frames for grouping)
		maxFrames := 10
		if len(stackLines) < maxFrames {
			maxFrames = len(stackLines)
		}
		signature := state + "|" + strings.Join(stackLines[:maxFrames], "|")

		// Group by signature
		if existing, ok := groupMap[signature]; ok {
			existing.Count++
		} else {
			groupMap[signature] = &GoroutineGroup{
				Count:      1,
				State:      state,
				WaitReason: waitReason,
				TopFunc:    topFunc,
				Stack:      stackLines,
			}
		}
	}

	// Convert map to slice
	groups := make([]GoroutineGroup, 0, len(groupMap))
	for _, group := range groupMap {
		groups = append(groups, *group)
	}

	return groups
}

func sortedGoroutineGroups(groups []GoroutineGroup) []GoroutineGroup {
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i].State+"|"+strings.Join(groups[i].Stack, "|"), groups[j].State+"|"+strings.Join(groups[j].Stack, "|")
		return a < b
	})
	return groups
}

// syntheticGoroutineProfile is a debug=2 dump of n idle workers spread over a few providers, plus a
// handful of other goroutines: the shape a gateway with many providers produces.
func syntheticGoroutineProfile(n int) []byte {
	var b strings.Builder
	b.WriteString("goroutine 1 [running]:\nmain.main()\n\t/app/main.go:10 +0x1c\n\n")
	b.WriteString("goroutine 7 [chan receive, 12 minutes]:\nexample.com/x.(*Pool).loop(0x14000010000)\n\t/app/pool.go:42 +0x88\ncreated by example.com/x.NewPool in goroutine 1\n\t/app/pool.go:20 +0x64\n\n")
	for i := 0; i < n; i++ {
		provider := i % 4
		b.WriteString("goroutine " + strconv.Itoa(100+i) + " [select, 3 minutes]:\n")
		b.WriteString("github.com/maximhq/bifrost/core.(*Bifrost).requestWorker(0x750ed251cc08, {0x108c0402" + strconv.Itoa(provider) + ", 0x750ed4d2a8f0})\n")
		b.WriteString("\t/src/core/bifrost.go:8213 +0xef\n")
		b.WriteString("created by github.com/maximhq/bifrost/core.(*Bifrost).prepareProvider in goroutine " + strconv.Itoa(20+provider) + "\n")
		b.WriteString("\t/src/core/bifrost.go:7000 +0x44\n\n")
	}
	return []byte(b.String())
}

// TestParseGoroutineProfileMatchesTheReference pins the grouping of the in-place parser against the
// original on a synthetic dump and on this process's own live dump.
func TestParseGoroutineProfileMatchesTheReference(t *testing.T) {
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-stop
		}()
	}
	var live bytes.Buffer
	require.NoError(t, pprof.Lookup("goroutine").WriteTo(&live, 2))
	close(stop)
	wg.Wait()

	for name, profile := range map[string][]byte{"synthetic": syntheticGoroutineProfile(500), "live": live.Bytes(), "empty": nil} {
		want := sortedGoroutineGroups(parseGoroutineProfileReference(string(profile)))
		got := sortedGoroutineGroups(parseGoroutineProfile(profile))
		require.Equal(t, len(want), len(got), name)
		assert.Equal(t, want, got, name)
	}
	groups := parseGoroutineProfile(syntheticGoroutineProfile(500))
	total := 0
	for _, group := range groups {
		total += group.Count
	}
	assert.Equal(t, 502, total)
	assert.Len(t, groups, 6, "four providers, main and the pool")
}

// TestParseGoroutineProfileAllocatesPerStackNotPerGoroutine pins the cost of a poll of the profiler
// panel. A gateway with many providers parks tens of thousands of identical workers, and the panel
// polls: allocating for each goroutine made the profiler the largest allocator in the process.
func TestParseGoroutineProfileAllocatesPerStackNotPerGoroutine(t *testing.T) {
	small, large := syntheticGoroutineProfile(100), syntheticGoroutineProfile(20_000)
	few := testing.AllocsPerRun(5, func() { parseGoroutineProfile(small) })
	many := testing.AllocsPerRun(5, func() { parseGoroutineProfile(large) })
	assert.LessOrEqual(t, many, few+8, "200x the goroutines of the same six stacks: %.0f allocations against %.0f", many, few)
}

// BenchmarkParseGoroutineProfile measures one poll's parse of 56,000 parked workers.
func BenchmarkParseGoroutineProfile(b *testing.B) {
	profile := syntheticGoroutineProfile(56_000)
	b.ReportAllocs()
	b.SetBytes(int64(len(profile)))
	for b.Loop() {
		parseGoroutineProfile(profile)
	}
}
