package analyzer

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/google/pprof/profile"
)

const (
	maxGraphNodes = 120
	maxGraphEdges = 260
	maxLeaks      = 25
	maxPaths      = 30
	maxCrossItems = 80
)

type ProfileSet struct {
	ID       string          `json:"id,omitempty"`
	Profiles []NamedAnalysis `json:"profiles"`
	Overall  OverallUsage    `json:"overall"`
	CrossMap []CrossMapItem  `json:"cross_map"`
}

type NamedAnalysis struct {
	Name    string    `json:"name"`
	Profile *Analysis `json:"profile"`
}

type OverallUsage struct {
	HeapBytes       int64 `json:"heap_bytes,omitempty"`
	AllocatedBytes  int64 `json:"allocated_bytes,omitempty"`
	CPUSamples      int64 `json:"cpu_samples,omitempty"`
	CPUTimeNanos    int64 `json:"cpu_time_nanos,omitempty"`
	Goroutines      int64 `json:"goroutines,omitempty"`
	ProfiledSamples int64 `json:"profiled_samples,omitempty"`
}

type CrossMapItem struct {
	Function       string   `json:"function"`
	File           string   `json:"file,omitempty"`
	HeapBytes      int64    `json:"heap_bytes,omitempty"`
	AllocatedBytes int64    `json:"allocated_bytes,omitempty"`
	CPUValue       int64    `json:"cpu_value,omitempty"`
	CPUUnit        string   `json:"cpu_unit,omitempty"`
	Goroutines     int64    `json:"goroutines,omitempty"`
	Profiles       []string `json:"profiles"`
	Score          float64  `json:"score"`
}

type Analysis struct {
	ID          string          `json:"id,omitempty"`
	ProfileType string          `json:"profile_type"`
	SampleType  string          `json:"sample_type"`
	Unit        string          `json:"unit"`
	Total       int64           `json:"total"`
	Nodes       []GraphNode     `json:"nodes"`
	Edges       []GraphEdge     `json:"edges"`
	Leaks       []LeakCandidate `json:"leaks"`
	TopPaths    []StackPath     `json:"top_paths"`
	SampleTypes []SampleType    `json:"sample_types"`
}

type SampleType struct {
	Type string `json:"type"`
	Unit string `json:"unit"`
}

type GraphNode struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	File       string  `json:"file,omitempty"`
	Line       int64   `json:"line,omitempty"`
	Flat       int64   `json:"flat"`
	Cumulative int64   `json:"cumulative"`
	Percent    float64 `json:"percent"`
	Kind       string  `json:"kind"`
}

type GraphEdge struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	Value   int64   `json:"value"`
	Percent float64 `json:"percent"`
}

type LeakCandidate struct {
	Function      string   `json:"function"`
	File          string   `json:"file,omitempty"`
	Line          int64    `json:"line,omitempty"`
	RetainedBytes int64    `json:"retained_bytes"`
	AllocBytes    int64    `json:"alloc_bytes,omitempty"`
	Objects       int64    `json:"objects,omitempty"`
	Score         float64  `json:"score"`
	Reason        string   `json:"reason"`
	Path          []string `json:"path"`
}

type StackPath struct {
	Value   int64    `json:"value"`
	Percent float64  `json:"percent"`
	Path    []string `json:"path"`
}

type nodeAgg struct {
	node GraphNode
}

type leakAgg struct {
	fn       frame
	retain   int64
	alloc    int64
	objects  int64
	bestPath []string
}

type frame struct {
	id   string
	name string
	file string
	line int64
}

func Analyze(r io.Reader, requestedSample string) (*Analysis, error) {
	data, err := io.ReadAll(io.LimitReader(r, 512<<20))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty profile")
	}

	prof, err := profile.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if err := prof.CheckValid(); err != nil {
		return nil, err
	}

	valueIdx := chooseSampleIndex(prof, requestedSample)
	if valueIdx < 0 {
		return nil, fmt.Errorf("profile has no supported sample values")
	}

	retainIdx := sampleIndex(prof, "inuse_space")
	allocIdx := sampleIndex(prof, "alloc_space")
	objectIdx := sampleIndex(prof, "inuse_objects")
	if retainIdx < 0 {
		retainIdx = valueIdx
	}
	if allocIdx < 0 {
		allocIdx = valueIdx
	}

	nodes := make(map[string]*nodeAgg)
	edges := make(map[string]*GraphEdge)
	leaks := make(map[string]*leakAgg)
	pathValues := make(map[string]*StackPath)
	var total int64

	for _, sample := range prof.Sample {
		if valueIdx >= len(sample.Value) {
			continue
		}
		value := sample.Value[valueIdx]
		if value <= 0 {
			continue
		}
		total += value

		stack := stackFrames(sample)
		if len(stack) == 0 {
			continue
		}

		leaf := stack[len(stack)-1]
		for i := range stack {
			f := stack[i]
			agg := nodes[f.id]
			if agg == nil {
				agg = &nodeAgg{node: GraphNode{
					ID:   f.id,
					Name: f.name,
					File: f.file,
					Line: f.line,
					Kind: nodeKind(f.name),
				}}
				nodes[f.id] = agg
			}
			agg.node.Cumulative += value
		}
		nodes[leaf.id].node.Flat += value

		for i := 0; i < len(stack)-1; i++ {
			key := stack[i].id + "\x00" + stack[i+1].id
			edge := edges[key]
			if edge == nil {
				edge = &GraphEdge{From: stack[i].id, To: stack[i+1].id}
				edges[key] = edge
			}
			edge.Value += value
		}

		path := frameNames(stack)
		pathKey := strings.Join(path, "\x00")
		sp := pathValues[pathKey]
		if sp == nil {
			sp = &StackPath{Path: path}
			pathValues[pathKey] = sp
		}
		sp.Value += value

		retained := sampleValue(sample, retainIdx)
		allocated := sampleValue(sample, allocIdx)
		objects := sampleValue(sample, objectIdx)
		if retained > 0 {
			la := leaks[leaf.id]
			if la == nil {
				la = &leakAgg{fn: leaf}
				leaks[leaf.id] = la
			}
			la.retain += retained
			la.alloc += allocated
			la.objects += objects
			if retained >= la.retain || len(la.bestPath) == 0 {
				la.bestPath = path
			}
		}
	}

	a := &Analysis{
		ProfileType: profileType(prof, valueIdx),
		SampleType:  prof.SampleType[valueIdx].Type,
		Unit:        prof.SampleType[valueIdx].Unit,
		Total:       total,
		SampleTypes: sampleTypes(prof),
		Nodes:       topNodes(nodes, total),
		TopPaths:    topPaths(pathValues, total),
		Leaks:       topLeaks(leaks),
	}
	a.Edges = topEdges(edges, a.Nodes, total)
	return a, nil
}

func AnalyzeSet(profiles map[string]io.Reader) (*ProfileSet, error) {
	out := &ProfileSet{}
	byName := make(map[string]*Analysis)
	for _, name := range ProfileSetNames() {
		reader := profiles[name]
		if reader == nil {
			continue
		}
		analysis, err := Analyze(reader, defaultSampleFor(name))
		if err != nil {
			return nil, fmt.Errorf("%s profile: %w", name, err)
		}
		analysis.ProfileType = name
		out.Profiles = append(out.Profiles, NamedAnalysis{Name: name, Profile: analysis})
		byName[name] = analysis
	}
	if len(out.Profiles) == 0 {
		return nil, fmt.Errorf("no profiles supplied")
	}
	out.Overall = overallUsage(byName)
	out.CrossMap = crossMap(byName)
	return out, nil
}

func ProfileSetNames() []string {
	return []string{"heap", "allocs", "cpu", "goroutine", "block", "mutex"}
}

func defaultSampleFor(name string) string {
	switch name {
	case "heap":
		return "inuse_space"
	case "allocs":
		return "alloc_space"
	case "cpu":
		return "samples"
	case "goroutine":
		return "goroutine"
	case "block":
		return "delay"
	case "mutex":
		return "contentions"
	default:
		return ""
	}
}

func chooseSampleIndex(prof *profile.Profile, requested string) int {
	if requested != "" {
		if idx := sampleIndex(prof, requested); idx >= 0 {
			return idx
		}
	}
	for _, name := range []string{"alloc_space", "inuse_space", "cpu", "samples", "goroutine", "delay", "contentions"} {
		if idx := sampleIndex(prof, name); idx >= 0 {
			return idx
		}
	}
	if len(prof.SampleType) > 0 {
		return 0
	}
	return -1
}

func sampleIndex(prof *profile.Profile, name string) int {
	for i, st := range prof.SampleType {
		if st.Type == name {
			return i
		}
	}
	return -1
}

func sampleValue(sample *profile.Sample, idx int) int64 {
	if idx < 0 || idx >= len(sample.Value) {
		return 0
	}
	return sample.Value[idx]
}

func stackFrames(sample *profile.Sample) []frame {
	out := make([]frame, 0, len(sample.Location))
	for i := len(sample.Location) - 1; i >= 0; i-- {
		loc := sample.Location[i]
		if loc == nil {
			continue
		}
		out = append(out, frameForLocation(loc))
	}
	return out
}

func frameForLocation(loc *profile.Location) frame {
	if len(loc.Line) == 0 {
		return frame{
			id:   fmt.Sprintf("addr:%x", loc.Address),
			name: fmt.Sprintf("0x%x", loc.Address),
		}
	}
	line := loc.Line[0]
	fn := "<unknown>"
	file := ""
	if line.Function != nil {
		fn = line.Function.Name
		file = line.Function.Filename
	}
	return frame{
		id:   fmt.Sprintf("%s:%d", fn, line.Line),
		name: fn,
		file: file,
		line: line.Line,
	}
}

func frameNames(stack []frame) []string {
	out := make([]string, 0, len(stack))
	for _, f := range stack {
		if f.file != "" && f.line > 0 {
			out = append(out, fmt.Sprintf("%s (%s:%d)", f.name, trimPath(f.file), f.line))
			continue
		}
		out = append(out, f.name)
	}
	return out
}

func topNodes(nodes map[string]*nodeAgg, total int64) []GraphNode {
	out := make([]GraphNode, 0, len(nodes))
	for _, agg := range nodes {
		n := agg.node
		n.Percent = pct(n.Cumulative, total)
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cumulative == out[j].Cumulative {
			return out[i].Name < out[j].Name
		}
		return out[i].Cumulative > out[j].Cumulative
	})
	if len(out) > maxGraphNodes {
		out = out[:maxGraphNodes]
	}
	return out
}

func topEdges(edges map[string]*GraphEdge, nodes []GraphNode, total int64) []GraphEdge {
	allowed := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		allowed[n.ID] = struct{}{}
	}
	out := make([]GraphEdge, 0, len(edges))
	for _, e := range edges {
		if _, ok := allowed[e.From]; !ok {
			continue
		}
		if _, ok := allowed[e.To]; !ok {
			continue
		}
		edge := *e
		edge.Percent = pct(edge.Value, total)
		out = append(out, edge)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	if len(out) > maxGraphEdges {
		out = out[:maxGraphEdges]
	}
	return out
}

func topLeaks(leaks map[string]*leakAgg) []LeakCandidate {
	out := make([]LeakCandidate, 0, len(leaks))
	for _, la := range leaks {
		if la.retain <= 0 {
			continue
		}
		retention := 1.0
		if la.alloc > 0 {
			retention = math.Min(1, float64(la.retain)/float64(la.alloc))
		}
		score := float64(la.retain) * (0.65 + retention*0.35)
		reason := "high retained heap on this allocation path"
		if retention > 0.75 && la.alloc > 0 {
			reason = "large retained share of allocated bytes"
		}
		out = append(out, LeakCandidate{
			Function:      la.fn.name,
			File:          la.fn.file,
			Line:          la.fn.line,
			RetainedBytes: la.retain,
			AllocBytes:    la.alloc,
			Objects:       la.objects,
			Score:         score,
			Reason:        reason,
			Path:          la.bestPath,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > maxLeaks {
		out = out[:maxLeaks]
	}
	return out
}

func topPaths(paths map[string]*StackPath, total int64) []StackPath {
	out := make([]StackPath, 0, len(paths))
	for _, p := range paths {
		p.Percent = pct(p.Value, total)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	if len(out) > maxPaths {
		out = out[:maxPaths]
	}
	return out
}

func sampleTypes(prof *profile.Profile) []SampleType {
	out := make([]SampleType, 0, len(prof.SampleType))
	for _, st := range prof.SampleType {
		out = append(out, SampleType{Type: st.Type, Unit: st.Unit})
	}
	return out
}

func profileType(prof *profile.Profile, idx int) string {
	if idx >= 0 && idx < len(prof.SampleType) {
		t := prof.SampleType[idx].Type
		if strings.Contains(t, "alloc") || strings.Contains(t, "inuse") {
			return "heap"
		}
		if t == "goroutine" {
			return "goroutine"
		}
		if t == "cpu" || t == "samples" {
			return "cpu"
		}
	}
	return "profile"
}

func overallUsage(profiles map[string]*Analysis) OverallUsage {
	var out OverallUsage
	for _, profile := range profiles {
		if profile != nil {
			out.ProfiledSamples += profile.Total
		}
	}
	if heap := profiles["heap"]; heap != nil {
		for _, st := range heap.SampleTypes {
			switch st.Type {
			case "inuse_space":
				if heap.SampleType == st.Type {
					out.HeapBytes = heap.Total
				}
			case "alloc_space":
				if heap.SampleType == st.Type {
					out.AllocatedBytes = heap.Total
				}
			}
		}
		if out.HeapBytes == 0 && heap.Unit == "bytes" {
			out.HeapBytes = heap.Total
		}
	}
	if allocs := profiles["allocs"]; allocs != nil && allocs.Unit == "bytes" {
		out.AllocatedBytes = allocs.Total
	}
	if cpu := profiles["cpu"]; cpu != nil {
		if cpu.Unit == "nanoseconds" {
			out.CPUTimeNanos = cpu.Total
		} else {
			out.CPUSamples = cpu.Total
		}
	}
	if goroutine := profiles["goroutine"]; goroutine != nil {
		out.Goroutines = goroutine.Total
	}
	return out
}

func crossMap(profiles map[string]*Analysis) []CrossMapItem {
	type agg struct {
		item CrossMapItem
		seen map[string]struct{}
	}
	rows := make(map[string]*agg)
	add := func(profileName string, n GraphNode, unit string) {
		key := normalizedFunction(n.Name)
		if key == "" {
			return
		}
		row := rows[key]
		if row == nil {
			row = &agg{item: CrossMapItem{Function: n.Name, File: n.File}, seen: make(map[string]struct{})}
			rows[key] = row
		}
		row.seen[profileName] = struct{}{}
		switch profileName {
		case "heap":
			if unit == "bytes" {
				row.item.HeapBytes += n.Cumulative
			} else {
				row.item.AllocatedBytes += n.Cumulative
			}
		case "cpu":
			row.item.CPUValue += n.Cumulative
			row.item.CPUUnit = unit
		case "goroutine":
			row.item.Goroutines += n.Cumulative
		}
	}
	for name, analysis := range profiles {
		if analysis == nil {
			continue
		}
		for _, n := range analysis.Nodes {
			add(name, n, analysis.Unit)
		}
	}
	out := make([]CrossMapItem, 0, len(rows))
	for _, row := range rows {
		for profileName := range row.seen {
			row.item.Profiles = append(row.item.Profiles, profileName)
		}
		sort.Strings(row.item.Profiles)
		if len(row.item.Profiles) < 2 {
			continue
		}
		row.item.Score = math.Log1p(float64(row.item.HeapBytes+row.item.AllocatedBytes))*1.4 +
			math.Log1p(float64(row.item.CPUValue))*1.2 +
			math.Log1p(float64(row.item.Goroutines))
		out = append(out, row.item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > maxCrossItems {
		out = out[:maxCrossItems]
	}
	return out
}

func normalizedFunction(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, "runtime.") {
		return name
	}
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

func pct(v, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(v) / float64(total) * 100
}

func trimPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) <= 3 {
		return path
	}
	return strings.Join(parts[len(parts)-3:], "/")
}

func nodeKind(name string) string {
	switch {
	case strings.HasPrefix(name, "runtime."):
		return "runtime"
	case strings.HasPrefix(name, "net/http."):
		return "http"
	case strings.Contains(name, "/vendor/"):
		return "vendor"
	case strings.Contains(name, "."):
		return "app"
	default:
		return "other"
	}
}
