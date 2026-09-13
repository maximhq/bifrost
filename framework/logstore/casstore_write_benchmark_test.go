package logstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// This is an opt-in performance experiment, not a production fidelity certificate.
type casBenchDiff struct {
	Checked    map[string]int64 `json:"checked_fields"`
	Raw        map[string]int64 `json:"raw_byte_diff"`
	Equivalent map[string]int64 `json:"json_semantic_equivalent"`
	Different  map[string]int64 `json:"semantic_or_non_json_diff"`
}

func newCasBenchDiff() *casBenchDiff {
	return &casBenchDiff{map[string]int64{}, map[string]int64{}, map[string]int64{}, map[string]int64{}}
}

// Decode with exact number lexemes and reject duplicate keys: float64 rounding
// and last-key-wins decoding must never hide a real difference. Only object key
// order and insignificant JSON whitespace are normalized; arrays stay ordered.
func casBenchJSON(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		m := map[string]any{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("non-string key")
			}
			if _, ok := m[key]; ok {
				return nil, fmt.Errorf("duplicate key")
			}
			v, err := casBenchJSON(d)
			if err != nil {
				return nil, err
			}
			m[key] = v
		}
		_, err := d.Token()
		return m, err
	case json.Delim('['):
		a := []any{}
		for d.More() {
			v, err := casBenchJSON(d)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err := d.Token()
		return a, err
	default:
		if _, ok := token.(json.Delim); ok {
			return nil, fmt.Errorf("unexpected delimiter")
		}
		return token, nil
	}
}

func casBenchEquivalent(a, b string) bool {
	decode := func(s string) (any, error) {
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		v, err := casBenchJSON(d)
		if err != nil {
			return nil, err
		}
		if _, err = d.Token(); err != io.EOF {
			return nil, fmt.Errorf("trailing JSON")
		}
		return v, nil
	}
	x, err := decode(a)
	if err != nil {
		return false
	}
	y, err := decode(b)
	return err == nil && reflect.DeepEqual(x, y)
}

func (d *casBenchDiff) compare(a, b map[string]string) bool {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	different := false
	for k := range keys {
		d.Checked[k]++
		x, ax := a[k]
		y, by := b[k]
		if ax == by && x == y {
			continue
		}
		d.Raw[k]++
		if ax == by && casBenchEquivalent(x, y) {
			d.Equivalent[k]++
		} else {
			d.Different[k]++
			different = true
		}
	}
	return different
}

func casBenchHash(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	require.NoError(t, err)
	return hex.EncodeToString(h.Sum(nil))
}

func TestCas_BenchmarkComparison(t *testing.T) {
	require.True(t, casBenchEquivalent(`{"b":2,"a":1}`, `{"a":1,"b":2}`))
	for _, pair := range [][2]string{{`{"a":1}`, `{"a":2}`}, {`[1,2]`, `[2,1]`}, {`9007199254740992`, `9007199254740993`}, {`{"a":1,"a":2}`, `{"a":2}`}, {`{} {}`, `{}`}, {`null`, `"null"`}} {
		require.False(t, casBenchEquivalent(pair[0], pair[1]))
	}
	d := newCasBenchDiff()
	require.True(t, d.compare(map[string]string{"params": "1"}, map[string]string{"params": "2", "metadata": ""}))
	require.EqualValues(t, 1, d.Different["params"])
	require.EqualValues(t, 1, d.Different["metadata"])
}

func TestCas_OptInRealSnapshotWriteBenchmark(t *testing.T) {
	snapshot := os.Getenv("BIFROST_CAS_BENCH_SNAPSHOT")
	outputDir := os.Getenv("BIFROST_CAS_BENCH_OUTPUT_DIR")
	if snapshot == "" || outputDir == "" {
		t.Skip("set BIFROST_CAS_BENCH_SNAPSHOT and BIFROST_CAS_BENCH_OUTPUT_DIR")
	}
	require.Equal(t, "/data/bifrost-cas-analysis/snapshots/logs-20260912-233931.db", snapshot)
	limit := 0
	if value := os.Getenv("BIFROST_CAS_BENCH_LIMIT"); value != "" {
		n, err := strconv.Atoi(value)
		require.NoError(t, err)
		require.GreaterOrEqual(t, n, 0)
		limit = n
	}
	batch, concurrency := 25, 1
	for name, target := range map[string]*int{"BIFROST_CAS_BENCH_BATCH": &batch, "BIFROST_CAS_BENCH_CONCURRENCY": &concurrency} {
		if value := os.Getenv(name); value != "" {
			n, err := strconv.Atoi(value)
			require.NoError(t, err)
			require.Greater(t, n, 0)
			*target = n
		}
	}
	require.LessOrEqual(t, batch*concurrency, 100, "bounded streaming window")
	require.NoError(t, os.Mkdir(outputDir, 0o700))
	before := casBenchHash(t, snapshot)
	report := map[string]any{
		"scope":         "performance-only; bounded streaming SQLite BatchCreateIfNotExists, configurable batch/concurrency, CAS min_field_bytes=1024/min_chunk_bytes=256; every selected row and union of all ExtractPayload fields checked",
		"limitations":   []string{"Not a production fidelity certificate: non-ExtractPayload Log columns, list/search behavior, updates/deletes and cross-process concurrency are outside this experiment", "Snapshot replay includes production DeserializeFields/SerializeFields normalization; source normalization is separately reported and semantic differences fail", "Independent source reads and frozen serialization are outside write timers; writers still execute their normal serialization/hooks", "Single run on a shared model host; no confidence intervals or isolated storage/cache control; alternating batch write order", "Heap is process-wide end-of-run; RSS/WAL sampled every 10ms plus write boundaries while open, sampled peaks are lower bounds; CPU ticks are process-wide including sampler and GC, Linux CLK_TCK must be supplied externally; write CPU excludes source preparation and verification but includes concurrent GC"},
		"normalization": "No field-name exclusions. Raw byte differences always counted. JSON equality ignores object key order/whitespace, preserves arrays/types/exact number lexemes, rejects duplicate keys. Missing differs from empty. Invalid/non-JSON unequal bytes fail.",
		"snapshot":      snapshot, "snapshot_sha256_before": before, "batch_size": batch, "concurrency": concurrency, "limit": limit,
	}
	defer func() {
		after := casBenchHash(t, snapshot)
		report["snapshot_sha256_after"] = after
		report["snapshot_unchanged"] = before == after
		report["test_failed"] = t.Failed() || before != after
		data, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(outputDir, "report.json"), append(data, '\n'), 0o600))
		require.Equal(t, before, after, "input snapshot changed")
	}()
	source, err := gorm.Open(sqlite.Open("file:"+snapshot+"?mode=ro&immutable=1&_query_only=1"), &gorm.Config{Logger: newGormLogger(hybridTestLogger{})})
	require.NoError(t, err)
	sqlSource, err := source.DB()
	require.NoError(t, err)
	defer sqlSource.Close()
	ctx := context.Background()
	baselinePath, casPath := filepath.Join(outputDir, "baseline.db"), filepath.Join(outputDir, "cas.db")
	for _, path := range []string{baselinePath, casPath} {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	baseline, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: baselinePath}, hybridTestLogger{})
	require.NoError(t, err)
	defer baseline.Close(ctx)
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: casPath}, hybridTestLogger{})
	require.NoError(t, err)
	cas, err := newCasLogStore(ctx, inner, &ContentAddressedConfig{Enabled: true, MinFieldBytes: 1024, MinChunkBytes: 256}, hybridTestLogger{})
	require.NoError(t, err)
	defer cas.Close(ctx)
	monitor := newCasBenchMonitor(map[string]string{"baseline": baselinePath, "cas": casPath})
	defer func() { monitor.stop(); report["online_resources"] = monitor.peaks }()
	latencies := map[string][]int64{"baseline": {}, "cas": {}}
	cpuTicks := map[string]int64{"baseline": 0, "cas": 0}
	report["write_cpu_ticks"] = cpuTicks
	defer func() {
		summary := map[string]any{}
		for name, values := range latencies {
			if len(values) == 0 {
				continue
			}
			sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
			summary[name] = map[string]any{"calls": len(values), "p50_ns": values[(len(values)-1)*50/100], "p95_ns": values[(len(values)-1)*95/100], "p99_ns": values[(len(values)-1)*99/100], "max_ns": values[len(values)-1]}
		}
		report["batch_call_latency"] = summary
	}()
	comparisons := map[string]*casBenchDiff{}
	for _, name := range []string{"source_to_frozen_baseline", "source_to_frozen_cas", "frozen_between", "baseline_roundtrip", "cas_roundtrip", "between_roundtrip"} {
		comparisons[name] = newCasBenchDiff()
	}
	report["comparisons"] = comparisons
	var total, checked, mismatches int
	var baselineNS, casNS int64
	lastID := ""
	for {
		size := batch * concurrency
		if limit > 0 && limit-total < size {
			size = limit - total
		}
		if size == 0 {
			break
		}
		var raw []*Log
		query := source.Session(&gorm.Session{SkipHooks: true}).Order("id").Limit(size)
		if total > 0 {
			query = query.Where("id > ?", lastID)
		}
		require.NoError(t, query.Find(&raw).Error)
		if len(raw) == 0 {
			break
		}
		ids := make([]string, len(raw))
		for i, l := range raw {
			ids[i] = l.ID
		}
		read := func() []*Log {
			var logs []*Log
			require.NoError(t, source.Where("id IN ?", ids).Order("id").Find(&logs).Error)
			require.Len(t, logs, len(raw))
			return logs
		}
		leftInput, rightInput := read(), read()
		leftFrozen, rightFrozen := make([]map[string]string, len(raw)), make([]map[string]string, len(raw))
		bad := make([]bool, len(raw))
		compare := func(i int, name string, a, b map[string]string) {
			if comparisons[name].compare(a, b) {
				bad[i] = true
			}
		}
		for i := range raw {
			require.Equal(t, raw[i].ID, leftInput[i].ID)
			require.Equal(t, raw[i].ID, rightInput[i].ID)
			require.NoError(t, leftInput[i].SerializeFields())
			require.NoError(t, rightInput[i].SerializeFields())
			leftFrozen[i], rightFrozen[i] = ExtractPayload(leftInput[i]), ExtractPayload(rightInput[i])
			compare(i, "source_to_frozen_baseline", ExtractPayload(raw[i]), leftFrozen[i])
			compare(i, "source_to_frozen_cas", ExtractPayload(raw[i]), rightFrozen[i])
			compare(i, "frozen_between", leftFrozen[i], rightFrozen[i])
		}
		write := func(name string, store LogStore, input []*Log) int64 {
			monitor.sample()
			cpuBefore := casBenchCPUTicks()
			start := time.Now()
			count := (len(input) + batch - 1) / batch
			errors := make([]error, count)
			times := make([]int64, count)
			var wg sync.WaitGroup
			for job := 0; job < count; job++ {
				lo, hi := job*batch, min((job+1)*batch, len(input))
				wg.Add(1)
				go func(job, lo, hi int) {
					defer wg.Done()
					started := time.Now()
					errors[job] = store.BatchCreateIfNotExists(ctx, input[lo:hi])
					times[job] = time.Since(started).Nanoseconds()
				}(job, lo, hi)
			}
			wg.Wait()
			elapsed := time.Since(start).Nanoseconds()
			cpuTicks[name] += casBenchCPUTicks() - cpuBefore
			monitor.sample()
			latencies[name] = append(latencies[name], times...)
			for _, err := range errors {
				require.True(t, err == nil, "write failed (payload suppressed)")
			}
			return elapsed
		}
		if total/(batch*concurrency)%2 == 0 {
			baselineNS += write("baseline", baseline, leftInput)
			casNS += write("cas", cas, rightInput)
		} else {
			casNS += write("cas", cas, rightInput)
			baselineNS += write("baseline", baseline, leftInput)
		}
		for i := range raw {
			left, le := baseline.FindByID(ctx, raw[i].ID)
			right, re := cas.FindByID(ctx, raw[i].ID)
			if le != nil || re != nil {
				t.Errorf("row %d readback failed: baseline=%v cas=%v", total+i, le, re)
				bad[i] = true
			} else {
				compare(i, "baseline_roundtrip", leftFrozen[i], ExtractPayload(left))
				compare(i, "cas_roundtrip", rightFrozen[i], ExtractPayload(right))
				compare(i, "between_roundtrip", ExtractPayload(left), ExtractPayload(right))
				checked++
			}
			if bad[i] {
				mismatches++
			}
		}
		total += len(raw)
		lastID = ids[len(ids)-1]
	}
	require.NotZero(t, total)
	if limit == 0 {
		require.Equal(t, 3769, total)
	}
	monitor.sample()
	require.NoError(t, baseline.Close(ctx))
	require.NoError(t, cas.Close(ctx))
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	report["rows"], report["verification_rows"], report["verification_mismatch"] = total, checked, mismatches
	report["baseline_write_ns"], report["cas_write_ns"] = baselineNS, casNS
	report["heap_alloc_bytes"], report["heap_system_bytes"] = mem.HeapAlloc, mem.HeapSys
	for name, path := range map[string]string{"baseline": baselinePath, "cas": casPath} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		report[name+"_file_bytes"] = info.Size()
		var wal int64
		if info, err := os.Stat(path + "-wal"); err == nil {
			wal = info.Size()
		} else {
			require.True(t, os.IsNotExist(err))
		}
		report[name+"_wal_bytes"] = wal
	}
	fields := []string{}
	for f := range comparisons["between_roundtrip"].Checked {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	report["checked_field_names"] = fields
	require.Equal(t, total, checked)
	require.Zero(t, mismatches, "semantic/non-JSON differences retained in report; no thresholds or field exclusions")
}

// Linux /proc sampling avoids platform-specific syscall types. No payload is read.
func casBenchCPUTicks() int64 {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return -1
	}
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return -1
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 13 {
		return -1
	}
	user, e1 := strconv.ParseInt(fields[11], 10, 64)
	system, e2 := strconv.ParseInt(fields[12], 10, 64)
	if e1 != nil || e2 != nil {
		return -1
	}
	return user + system
}

type casBenchMonitor struct {
	mu     sync.Mutex
	paths  map[string]string
	peaks  map[string]int64
	done   chan struct{}
	joined chan struct{}
}

func newCasBenchMonitor(paths map[string]string) *casBenchMonitor {
	m := &casBenchMonitor{paths: paths, peaks: map[string]int64{}, done: make(chan struct{}), joined: make(chan struct{})}
	m.sample()
	go func() {
		defer close(m.joined)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.sample()
			case <-m.done:
				return
			}
		}
	}()
	return m
}
func (m *casBenchMonitor) sample() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peaks["samples"]++
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		m.peaks["rss_sample_errors"]++
	} else {
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && (f[0] == "VmRSS:" || f[0] == "VmHWM:") {
				n, e := strconv.ParseInt(f[1], 10, 64)
				if e == nil {
					key := strings.TrimSuffix(f[0], ":") + "_peak_bytes"
					m.peaks[key] = max(m.peaks[key], n*1024)
				}
			}
		}
	}
	for name, path := range m.paths {
		for _, suffix := range []string{"", "-wal"} {
			info, e := os.Stat(path + suffix)
			if e == nil {
				key := name + suffix + "_peak_bytes"
				m.peaks[key] = max(m.peaks[key], info.Size())
			} else if !os.IsNotExist(e) {
				m.peaks["file_sample_errors"]++
			}
		}
	}
}
func (m *casBenchMonitor) stop() { close(m.done); <-m.joined }

// TestCas_OptInSingleWriterBudget separates the CAS-only streaming write phase
// from full-field verification, avoiding baseline and frozen copies in its peak.
func TestCas_OptInSingleWriterBudget(t *testing.T) {
	snapshot, output := os.Getenv("BIFROST_CAS_BENCH_SNAPSHOT"), os.Getenv("BIFROST_CAS_BENCH_OUTPUT_DIR")
	if snapshot == "" || output == "" {
		t.Skip("opt-in private snapshot budget")
	}
	require.Equal(t, "/data/bifrost-cas-analysis/snapshots/logs-20260912-233931.db", snapshot)
	require.NoError(t, os.Mkdir(output, 0o700))
	before := casBenchHash(t, snapshot)
	report := map[string]any{"scope": "CAS-only batch1/c1 full streaming replay; write phase includes one source row and production serialization, no baseline or frozen copies; verification is a separate phase", "snapshot_sha256_before": before, "gomemlimit": os.Getenv("GOMEMLIMIT"), "gomaxprocs": runtime.GOMAXPROCS(0)}
	defer func() {
		after := casBenchHash(t, snapshot)
		report["snapshot_sha256_after"] = after
		report["snapshot_unchanged"] = before == after
		report["test_failed"] = t.Failed() || before != after
		data, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(output, "report.json"), append(data, '\n'), 0o600))
		require.Equal(t, before, after)
	}()
	source, err := gorm.Open(sqlite.Open("file:"+snapshot+"?mode=ro&immutable=1&_query_only=1"), &gorm.Config{Logger: newGormLogger(hybridTestLogger{})})
	require.NoError(t, err)
	sqlSource, err := source.DB()
	require.NoError(t, err)
	defer sqlSource.Close()
	ctx := context.Background()
	path := filepath.Join(output, "cas.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, hybridTestLogger{})
	require.NoError(t, err)
	cas, err := newCasLogStore(ctx, inner, &ContentAddressedConfig{Enabled: true, MinFieldBytes: 1024, MinChunkBytes: 256}, hybridTestLogger{})
	require.NoError(t, err)
	defer cas.Close(ctx)
	monitor := newCasBenchMonitor(map[string]string{"cas": path})
	stopped := false
	defer func() {
		if !stopped {
			monitor.stop()
		}
		report["write_phase_online_resources"] = monitor.peaks
	}()
	started := time.Now()
	cpuStart := casBenchCPUTicks()
	var writeNS, cpuTicks int64
	latencies := []int64{}
	lastID := ""
	total := 0
	for {
		var logs []*Log
		q := source.Order("id").Limit(1)
		if total > 0 {
			q = q.Where("id > ?", lastID)
		}
		require.NoError(t, q.Find(&logs).Error)
		if len(logs) == 0 {
			break
		}
		lastID = logs[0].ID
		cpu := casBenchCPUTicks()
		start := time.Now()
		err := cas.BatchCreateIfNotExists(ctx, logs)
		elapsed := time.Since(start).Nanoseconds()
		cpuTicks += casBenchCPUTicks() - cpu
		require.True(t, err == nil, "write failed, payload suppressed")
		writeNS += elapsed
		latencies = append(latencies, elapsed)
		total++
		monitor.sample()
	}
	monitor.stop()
	stopped = true
	report["write_phase_wall_ns"] = time.Since(started).Nanoseconds()
	report["write_phase_cpu_ticks"] = casBenchCPUTicks() - cpuStart
	report["write_call_ns"] = writeNS
	report["write_call_cpu_ticks"] = cpuTicks
	report["rows"] = total
	require.Equal(t, 3769, total)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	report["batch_call_latency"] = map[string]int64{"p50_ns": latencies[(total-1)*50/100], "p95_ns": latencies[(total-1)*95/100], "p99_ns": latencies[(total-1)*99/100], "max_ns": latencies[total-1]}
	// Restart a separate sampler: VmHWM remains the process lifetime high-water,
	// while write_phase_online_resources was frozen before verification began.
	verification := newCasBenchMonitor(map[string]string{"cas": path})
	defer func() { verification.stop(); report["verification_online_resources"] = verification.peaks }()
	diff := newCasBenchDiff()
	report["source_to_cas_readback"] = diff
	lastID = ""
	checked, bad := 0, 0
	for {
		var logs []*Log
		q := source.Session(&gorm.Session{SkipHooks: true}).Order("id").Limit(1)
		if checked > 0 {
			q = q.Where("id > ?", lastID)
		}
		require.NoError(t, q.Find(&logs).Error)
		if len(logs) == 0 {
			break
		}
		lastID = logs[0].ID
		got, err := cas.FindByID(ctx, lastID)
		require.True(t, err == nil, "readback failed, payload suppressed")
		if diff.compare(ExtractPayload(logs[0]), ExtractPayload(got)) {
			bad++
		}
		checked++
	}
	report["verification_rows"] = checked
	report["verification_mismatch"] = bad
	require.NoError(t, cas.Close(ctx))
	info, err := os.Stat(path)
	require.NoError(t, err)
	report["cas_file_bytes"] = info.Size()
	require.Equal(t, total, checked)
	require.Zero(t, bad)
}
