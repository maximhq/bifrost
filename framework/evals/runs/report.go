package runs

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"sync"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/evals/datasets"
)

// ============================================================================
// Messages for prompt and model targets
// ============================================================================

// templateVariable matches {{name}} in a prompt message.
var templateVariable = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.\-]*)\s*\}\}`)

// renderTemplate fills {{name}} from the variables. A name the row does not have
// becomes empty text.
func renderTemplate(text string, variables map[string]string) string {
	return compileText(text).render(variables)
}

// usesVariable reports whether any of the texts reads {{name}}.
func usesVariable(texts []string, name string) bool {
	for _, text := range texts {
		if compileText(text).uses(name) {
			return true
		}
	}
	return false
}

// ============================================================================
// Summary
// ============================================================================

// latencySummary describes a list of latencies. It sorts the list in place and reads
// the percentiles by nearest rank, which needs no histogram library.
func latencySummary(latencies []int64) *LatencySummary {
	if len(latencies) == 0 {
		return nil
	}
	slices.Sort(latencies)
	rank := func(percent float64) int64 {
		index := int(math.Ceil(percent/100*float64(len(latencies)))) - 1
		return latencies[min(max(index, 0), len(latencies)-1)]
	}
	var sum int64
	for _, latency := range latencies {
		sum += latency
	}
	return &LatencySummary{
		Count: len(latencies), Min: latencies[0], Max: latencies[len(latencies)-1], Mean: float64(sum) / float64(len(latencies)),
		P50: rank(50), P90: rank(90), P95: rank(95), P99: rank(99),
	}
}

func summaryOf(definition evals.Definition) EvaluatorSummary {
	return EvaluatorSummary{EvaluatorID: definition.ID, Name: definition.Name, Type: definition.Type, Version: definition.Version}
}

// isPending reports whether a result is still to come.
func isPending(status string) bool {
	return status == tables.EvalRunResultQueued || status == tables.EvalRunResultRunning || status == tables.EvalRunResultAwaitingReview
}

// liveSummary is the summary of a run that is still active. It is built from counts
// alone, so it costs the same however many entries the run has: it has pass rates
// and progress, and leaves means and percentiles to the summary a finished run stores.
func (s *Service) liveSummary(ctx context.Context, runID string, definitions []evals.Definition) (*Summary, error) {
	counts, err := s.store.CountEvalRunResultsByEvaluator(ctx, runID)
	if err != nil {
		return nil, err
	}
	summary := &Summary{Evaluators: make([]EvaluatorSummary, len(definitions))}
	at := make(map[string]int, len(definitions))
	for i, definition := range definitions {
		summary.Evaluators[i] = summaryOf(definition)
		at[definition.ID] = i
	}
	for _, count := range counts {
		index, ok := at[count.EvaluatorID]
		if !ok {
			continue
		}
		evaluator := &summary.Evaluators[index]
		n := int(count.Count)
		switch {
		case isPending(count.Status):
			evaluator.Pending += n
			continue
		case count.Status == tables.EvalRunResultNotApplicable:
			evaluator.NA += n
		case count.Status == tables.EvalRunResultFailed:
			evaluator.Errors += n
		default:
			evaluator.Scored += n
			if count.Pass != nil && *count.Pass {
				evaluator.Passed += n
			}
		}
		evaluator.Total += n
	}
	for i := range summary.Evaluators {
		if evaluator := &summary.Evaluators[i]; evaluator.Scored > 0 {
			evaluator.PassRate = float64(evaluator.Passed) / float64(evaluator.Scored) * 100
		}
	}
	if summary.Totals, err = s.store.GetEvalRunTotals(ctx, runID); err != nil {
		return nil, err
	}
	return summary, nil
}

// maxPooledResults is the largest buffer kept for the next summary. A run with more
// results than this grows its own and gives it to the collector afterwards.
const maxPooledResults = 8192

// resultBuffers holds the slices a finished run's results are read into, one
// evaluator at a time.
var resultBuffers = sync.Pool{New: func() any {
	buffer := make([]evals.Result, 0, 1024)
	return &buffer
}}

// resultPage is how many results are read at a time when a run is summarised.
const resultPage = 1000

// finalSummary is the summary of a run with nothing left to do: every evaluator's
// mean, median, range and pass rate, its run-level verdict, the target's latency
// percentiles, and the tokens and cost spent.
func (s *Service) finalSummary(ctx context.Context, runID string, definitions []evals.Definition) (*Summary, error) {
	summary := &Summary{Evaluators: make([]EvaluatorSummary, len(definitions))}
	buffer := resultBuffers.Get().(*[]evals.Result)
	defer func() {
		if cap(*buffer) > maxPooledResults {
			return
		}
		clear(*buffer)
		*buffer = (*buffer)[:0]
		resultBuffers.Put(buffer)
	}()
	for i, definition := range definitions {
		evaluator := summaryOf(definition)
		*buffer = (*buffer)[:0]
		after := ""
		for {
			// Only the score and the verdict are read: a result's reasoning is most of
			// its size and no part of a summary.
			page, err := s.store.ListEvalRunResultRefs(ctx, configstore.EvalRunResultListParams{RunID: runID, EvaluatorID: definition.ID, AfterID: after, Limit: resultPage})
			if err != nil {
				return nil, err
			}
			for j := range page {
				row := &page[j]
				result := evals.Result{Pass: row.Pass}
				switch {
				case isPending(row.Status):
					evaluator.Pending++
					continue
				case row.Status == tables.EvalRunResultNotApplicable:
					result.Score = evals.NAScore()
				case row.Status == tables.EvalRunResultFailed:
					result.Score = evals.ErrorScore()
				case row.ScoreJSON == nil:
					result.Score = evals.ErrorScore()
				default:
					if err := json.Unmarshal([]byte(*row.ScoreJSON), &result.Score); err != nil {
						result.Score = evals.ErrorScore()
					}
				}
				*buffer = append(*buffer, result)
			}
			if len(page) < resultPage {
				break
			}
			after = page[len(page)-1].ID
		}
		evaluator.Aggregation = evals.Aggregate(*buffer)
		if cfg, err := evals.ParseConfig(definition.Type, definition.Config); err == nil && evaluator.Scored > 0 {
			if criterion := cfg.Common().PassCriteria.RunLevel; criterion.Name != "" {
				evaluator.RunPass = new(evals.VerdictOf(criterion, evaluator.Aggregation).Pass)
			}
		}
		summary.Evaluators[i] = evaluator
	}
	latencies, err := s.store.ListEvalRunTargetLatencies(ctx, runID)
	if err != nil {
		return nil, err
	}
	summary.Latency = latencySummary(latencies)
	if summary.Totals, err = s.store.GetEvalRunTotals(ctx, runID); err != nil {
		return nil, err
	}
	return summary, nil
}

// writeSummary stores a run's final summary together with fields, while the run's
// status is one of from (any status when from is empty).
func (s *Service) writeSummary(ctx context.Context, runID string, definitions []evals.Definition, from []string, fields map[string]any) error {
	summary, err := s.finalSummary(ctx, runID, definitions)
	if err != nil {
		return err
	}
	data, err := providerUtils.MarshalSorted(summary)
	if err != nil {
		return err
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["summary_json"] = string(data)
	_, err = s.store.TransitionEvalRun(ctx, runID, from, fields)
	return err
}

// ============================================================================
// Comparison
// ============================================================================

// Compare puts two to five runs on the same dataset side by side: their summaries,
// and a page of dataset rows with each run's entry for the row. A row that only some
// of the runs have is still listed.
func (s *Service) Compare(ctx context.Context, ids []string, offset, limit int) (*Comparison, error) {
	// The same run named twice is compared once, in the order first given.
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(unique, id) {
			unique = append(unique, id)
		}
	}
	ids = unique
	if len(ids) < MinComparedRuns || len(ids) > MaxComparedRuns {
		return nil, invalid("a comparison takes %d to %d different runs", MinComparedRuns, MaxComparedRuns)
	}
	if offset < 0 {
		return nil, invalid("offset must not be negative")
	}
	comparison := &Comparison{Runs: make([]RunView, len(ids))}
	for i, id := range ids {
		view, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if view.DatasetID != comparison.Runs[0].DatasetID && i > 0 {
			return nil, invalid("runs on different datasets cannot be compared")
		}
		comparison.Runs[i] = *view
	}
	comparison.Changes = make(map[string]map[string]ScoreChange, len(ids)-1)
	for _, id := range ids[1:] {
		changes, err := s.store.CountEvalRunScoreChanges(ctx, ids[0], id)
		if err != nil {
			return nil, err
		}
		byEvaluator := make(map[string]ScoreChange, len(changes))
		for _, change := range changes {
			byEvaluator[change.EvaluatorID] = ScoreChange{Improved: change.Improved, Regressed: change.Regressed, Compared: change.Compared}
		}
		comparison.Changes[id] = byEvaluator
	}
	rowIDs, total, err := s.store.ListEvalRunRowIDs(ctx, ids, offset, pageSize(limit))
	if err != nil {
		return nil, err
	}
	comparison.Total = total
	comparison.Rows = make([]ComparedRow, len(rowIDs))
	at := make(map[string]int, len(rowIDs))
	for i, rowID := range rowIDs {
		comparison.Rows[i] = ComparedRow{DatasetRowID: rowID, Entries: make([]*EntryView, len(ids))}
		at[rowID] = i
	}
	if len(rowIDs) == 0 {
		return comparison, nil
	}
	for runIndex, id := range ids {
		entries, _, err := s.store.ListEvalRunEntries(ctx, configstore.EvalRunEntryListParams{RunID: id, DatasetRowIDs: rowIDs})
		if err != nil {
			return nil, err
		}
		views, err := s.withResults(ctx, id, entries)
		if err != nil {
			return nil, err
		}
		for i := range views {
			row := &comparison.Rows[at[views[i].DatasetRowID]]
			row.Entries[runIndex] = &views[i]
			if row.Variables == nil {
				row.Variables = views[i].Variables
			}
		}
	}
	return comparison, nil
}

// ============================================================================
// Export
// ============================================================================

// Export writes a run's report as CSV: one line per entry, with the row's variables,
// the output, and each evaluator's score, verdict and reasoning. It reads the entries
// a page at a time and streams the lines out.
func (s *Service) Export(ctx context.Context, id string, out io.Writer) error {
	run, err := s.store.GetEvalRun(ctx, id)
	if err != nil {
		return err
	}
	definitions, err := definitionsOf(run)
	if err != nil {
		return err
	}
	var names []string
	if version, err := s.datasets.Version(ctx, run.DatasetID, run.DatasetVersion); err == nil {
		names = variableNames(datasets.VersionColumns(version))
	}
	names = slices.DeleteFunc(names, func(name string) bool { return name == VariableOutput })

	header := append([]string{"row", "status", "passed"}, names...)
	header = append(header, "output", "tool_calls", "error", "target_latency_ms")
	for _, definition := range definitions {
		header = append(header, definition.Name+": score", definition.Name+": pass", definition.Name+": reasoning")
	}
	writer := csv.NewWriter(out)
	if err := writer.Write(header); err != nil {
		return err
	}
	line := make([]string, 0, len(header))
	for offset := 0; ; offset += MaxEntryPageSize {
		entries, _, err := s.store.ListEvalRunEntries(ctx, configstore.EvalRunEntryListParams{RunID: id, Offset: offset, Limit: MaxEntryPageSize})
		if err != nil {
			return err
		}
		views, err := s.withResults(ctx, id, entries)
		if err != nil {
			return err
		}
		for i := range views {
			entry := &views[i]
			line = append(line[:0], strconv.Itoa(offset+i+1), entry.Status, verdict(entry.Passed))
			for _, name := range names {
				line = append(line, entry.Variables[name])
			}
			toolCalls := ""
			if entry.ToolCalls != nil {
				toolCalls = *entry.ToolCalls
			}
			line = append(line, entry.Output, toolCalls, entry.Error, strconv.FormatInt(entry.TargetLatencyMs, 10))
			for _, definition := range definitions {
				score, pass, reasoning := "", "", ""
				for j := range entry.Results {
					if result := &entry.Results[j]; result.EvaluatorID == definition.ID {
						score, pass, reasoning = resultText(result), verdict(result.Pass), result.Reasoning
						if result.Error != "" {
							reasoning = result.Error
						}
					}
				}
				line = append(line, score, pass, reasoning)
			}
			if err := writer.Write(line); err != nil {
				return err
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
		if len(entries) < MaxEntryPageSize {
			return nil
		}
	}
}

func verdict(pass *bool) string {
	if pass == nil {
		return ""
	}
	return strconv.FormatBool(*pass)
}

// resultText renders a result's score for a CSV cell: the score when there is one,
// and the status otherwise.
func resultText(result *tables.TableEvalRunResult) string {
	if result.Status != tables.EvalRunResultCompleted {
		return result.Status
	}
	var score evals.Score
	if err := json.Unmarshal(result.Score, &score); err != nil {
		return ""
	}
	return scoreText(score)
}
