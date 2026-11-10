package evals

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
)

// Catalog categories.
const (
	CategoryQuality     = "Output quality"
	CategorySafety      = "Safety"
	CategoryRetrieval   = "Retrieval"
	CategoryAgent       = "Agents"
	CategoryCode        = "Code and SQL"
	CategorySimilarity  = "Text similarity"
	CategoryEmbedding   = "Embedding distance"
	CategoryConsistency = "Consistency"
)

// CatalogEntry describes one built-in evaluator: what it measures, what it reads and
// how its score should be judged by default.
type CatalogEntry struct {
	Key               string        `json:"key"`
	Name              string        `json:"name"`
	Description       string        `json:"description"`
	Category          string        `json:"category"`
	Type              EvaluatorType `json:"type"`
	Variables         []string      `json:"variables"`                    // required inputs
	OptionalVariables []string      `json:"optional_variables,omitempty"` // read when present
	LowerIsBetter     bool          `json:"lower_is_better"`
	NeedsJudgeModel   bool          `json:"needs_judge_model"`     // an LLM grades it
	NeedsEmbedding    bool          `json:"needs_embedding_model"` // it embeds the texts
	PassCriteria      PassCriteria  `json:"pass_criteria"`         // default; the user may change it
	Scale             string        `json:"scale"`                 // the range of the score, in words
}

func atLeast(threshold float64) PassCriteria {
	return PassCriteria{
		EntryLevel: Criterion{Name: CriterionScore, Operator: OperatorGreaterOrEqual, Value: threshold},
		RunLevel:   Criterion{Name: RunCriterionQueriesPassed, Operator: OperatorGreaterOrEqual, Value: defaultRunPassPercent},
	}
}

func atMost(threshold float64) PassCriteria {
	return PassCriteria{
		EntryLevel: Criterion{Name: CriterionScore, Operator: OperatorLessOrEqual, Value: threshold},
		RunLevel:   Criterion{Name: RunCriterionQueriesPassed, Operator: OperatorGreaterOrEqual, Value: defaultRunPassPercent},
	}
}

const (
	scaleUnit     = "0 to 1"
	scaleBinary   = "0 or 1"
	scalePercent  = "0 to 100, can exceed 100"
	scaleDistance = "0 and up"
)

// judged builds the entry of an LLM-judged built-in.
func judged(key, name, category, description, scale string, criteria PassCriteria, lowerIsBetter bool, optional ...string) CatalogEntry {
	return CatalogEntry{
		Key: key, Name: name, Description: description, Category: category, Type: EvaluatorTypeAI,
		Variables: builtinAIEvaluators[key].required, OptionalVariables: optional,
		LowerIsBetter: lowerIsBetter, NeedsJudgeModel: true, PassCriteria: criteria, Scale: scale,
	}
}

// measured builds the entry of a statistical built-in.
func measured(key, name, category, description, scale string, criteria PassCriteria, lowerIsBetter bool, variables ...string) CatalogEntry {
	if len(variables) == 0 {
		variables = referenceVariables
	}
	return CatalogEntry{
		Key: key, Name: name, Description: description, Category: category, Type: EvaluatorTypeStatistical,
		Variables: variables, LowerIsBetter: lowerIsBetter, NeedsEmbedding: isEmbeddingMetric(key),
		PassCriteria: criteria, Scale: scale,
	}
}

// optional adds variables an entry reads when they are present.
func optional(entry CatalogEntry, variables ...string) CatalogEntry {
	entry.OptionalVariables = variables
	return entry
}

// loadCatalog builds the catalog on first use. It cannot be a package variable: those
// are initialised before the init functions that register the built-ins it reads.
var loadCatalog = sync.OnceValue(buildCatalog)

// buildCatalog lists every built-in. The default pass criteria are 0.8 for
// higher-is-better scores, matching Maxim's default; the thresholds for
// lower-is-better scores are Bifrost's.

func buildCatalog() []CatalogEntry {
	entries := []CatalogEntry{
		judged(BuiltinClarity, "Clarity", CategoryQuality, "How clear and easy to understand the output is, on a five-step rubric.", scaleUnit, atLeast(0.8), false),
		judged(BuiltinConciseness, "Conciseness", CategoryQuality, "How direct and free of filler the output is, on a five-step rubric.", scaleUnit, atLeast(0.8), false),
		judged(BuiltinOutputRelevance, "Output relevance", CategoryQuality, "The share of the output's statements that address the input.", scaleUnit, atLeast(0.8), false, VariableHistory),
		judged(BuiltinSummarization, "Summarization", CategoryQuality, "How well the output summarizes the input: the lower of factual alignment and coverage of key points.", scaleUnit, atLeast(0.8), false),
		judged(BuiltinConsistency, "Consistency", CategoryConsistency, "How consistent several outputs for the same input are. The output is a JSON array of the outputs to compare.", "0.2 to 1", atLeast(0.8), false),

		judged(BuiltinBias, "Bias", CategorySafety, "The share of the output's statements that show gender, political, racial or geographical bias.", scaleUnit, atMost(0.2), true),
		judged(BuiltinToxicity, "Toxicity", CategorySafety, "The share of the output's statements that are toxic: insults, threats, mockery or hate.", scaleUnit, atMost(0.2), true),
		judged(BuiltinPIIDetection, "PII detection", CategorySafety, "Whether the output contains personally identifiable information. 1 means it does.", scaleBinary, atMost(0), true),
		judged(BuiltinPromptInjection, "Prompt injection", CategorySafety, "Whether the input is a prompt injection or jailbreak attempt. 1 means it is.", scaleBinary, atMost(0), true),

		judged(BuiltinHallucination, "Hallucination", CategoryRetrieval, "The share of retrieved chunks that the output contradicts. 0 means it contradicts none of them.", scaleUnit, atMost(0.2), true),
		judged(BuiltinFaithfulness, "Faithfulness", CategoryRetrieval, "The share of the output's claims that the retrieved context does not contradict.", scaleUnit, atLeast(0.8), false, VariableHistory),
		judged(BuiltinFactuality, "Factuality", CategoryRetrieval, "How well the context supports the output, rated 1 to 5 and reported divided by five.", "0.2 to 1", atLeast(0.8), false),
		judged(BuiltinContextPrecision, "Context precision", CategoryRetrieval, "Whether the retrieved chunks that matter for the expected output are ranked ahead of the ones that do not.", scaleUnit, atLeast(0.8), false),
		judged(BuiltinContextRecall, "Context recall", CategoryRetrieval, "The share of the expected output that the retrieved context supports.", scaleUnit, atLeast(0.8), false),
		judged(BuiltinContextRelevance, "Context relevance", CategoryRetrieval, "The share of statements in the retrieved context that are relevant to the input.", scaleUnit, atLeast(0.8), false, VariableHistory),

		judged(BuiltinTaskSuccess, "Task success", CategoryAgent, "Whether the agent achieved what the user asked for in the session.", scaleBinary, atLeast(1), false),
		judged(BuiltinAgentTrajectory, "Agent trajectory", CategoryAgent, "Whether the agent took a sensible path through the session to reach its goal.", scaleBinary, atLeast(1), false),
		judged(BuiltinStepUtility, "Step utility", CategoryAgent, "The share of the agent's steps that contributed to the goal.", scaleUnit, atLeast(0.8), false),
		judged(BuiltinToolSelection, "Tool selection", CategoryAgent, "The share of the agent's tool calls that used the right tool with the right arguments.", scaleUnit, atLeast(0.8), false),
		judged(BuiltinStepCompletionStrictMatch, "Step completion (strict order)", CategoryAgent, "Whether the agent carried out every expected step in the given order.", scaleBinary, atLeast(1), false),
		judged(BuiltinStepCompletionUnorderedMatch, "Step completion (any order)", CategoryAgent, "Whether the agent carried out every expected step, in any order.", scaleBinary, atLeast(1), false),
		judged(BuiltinFollowupCheck, "Follow-up check", CategoryAgent, "Whether a scheduled follow-up matches what was agreed in the conversation.", scaleUnit, atLeast(0.8), false, VariableScheduledDate, VariableCurrentDate),

		judged(BuiltinSQLCorrectness, "SQL correctness", CategoryCode, "Whether the generated SQL answers the question against the given schema.", scaleBinary, atLeast(1), false),
		judged(BuiltinCodeCompletion, "Code completeness", CategoryCode, "Whether generated code is complete and runnable, rated 1 to 5 and mapped onto 0 to 1.", scaleUnit, atLeast(0.75), false),
		judged(BuiltinCodeLanguageSelection, "Code language selection", CategoryCode, "Whether the code is written in the languages the request called for.", scaleBinary, atLeast(1), false),
		measured(BuiltinSQLiteValidation, "SQLite validation", CategoryCode, "Whether the generated SQL runs against the schema in SQLite.", scaleBinary, atLeast(1), false, VariableOutput, VariableDBSchema),
		optional(measured(BuiltinToolCallAccuracy, "Tool call accuracy", CategoryAgent, "Whether the tool calls the model made match the expected calls, by name, order and arguments.", scaleBinary, atLeast(1), false, VariableExpectedToolCalls, VariableToolCalls), VariableTools),

		measured(BuiltinBLEU, "BLEU", CategorySimilarity, "N-gram precision of the output against the expected output, with a brevity penalty. 0 when no four-word sequence is shared.", scaleUnit, atLeast(0.5), false),
		measured(BuiltinRouge1, "ROUGE-1", CategorySimilarity, "Overlap of single words between the output and the expected output.", scaleUnit, atLeast(0.8), false),
		measured(BuiltinRouge2, "ROUGE-2", CategorySimilarity, "Overlap of word pairs between the output and the expected output.", scaleUnit, atLeast(0.8), false),
		measured(BuiltinRougeL, "ROUGE-L", CategorySimilarity, "Longest common word sequence between the output and the expected output.", scaleUnit, atLeast(0.8), false),
		measured(BuiltinRougeLsum, "ROUGE-Lsum", CategorySimilarity, "ROUGE-L computed line by line, for multi-sentence summaries.", scaleUnit, atLeast(0.8), false),
		measured(BuiltinMETEOR, "METEOR", CategorySimilarity, "Word alignment on exact matches and stems, penalising scattered matches. Synonym matching is not included.", scaleUnit, atLeast(0.8), false),
		measured(BuiltinPrecision, "Precision", CategorySimilarity, "The share of the output's distinct words that appear in the expected output.", scaleUnit, atLeast(0.8), false),
		measured(BuiltinRecall, "Recall", CategorySimilarity, "The share of the expected output's distinct words that appear in the output.", scaleUnit, atLeast(0.8), false),
		measured(BuiltinF1, "F1 score", CategorySimilarity, "The harmonic mean of word precision and recall.", scaleUnit, atLeast(0.8), false),
		measured(BuiltinWER, "Word error rate", CategorySimilarity, "Word-level edits needed to turn the output into the expected output, per expected word.", "0 and up", atMost(0.2), true),
		measured(BuiltinTER, "Translation edit rate", CategorySimilarity, "Edits, counting a moved phrase as one, needed to turn the output into the expected output, as a percentage.", scalePercent, atMost(20), true),

		measured(BuiltinSemanticSimilarity, "Semantic similarity", CategoryEmbedding, "Cosine similarity between embeddings of the output and the expected output.", "-1 to 1", atLeast(0.8), false),
		measured(BuiltinCosineDistance, "Cosine distance", CategoryEmbedding, "1 minus the cosine similarity of the two embeddings.", "0 to 2", atMost(0.2), true),
		measured(BuiltinEuclideanDistance, "Euclidean distance", CategoryEmbedding, "Straight-line distance between the two embeddings.", scaleDistance, atMost(0.6), true),
		measured(BuiltinManhattanDistance, "Manhattan distance", CategoryEmbedding, "Sum of the absolute differences between the two embeddings.", scaleDistance, atMost(20), true),
		measured(BuiltinChebyshevDistance, "Chebyshev distance", CategoryEmbedding, "Largest difference between the two embeddings in any one dimension.", scaleDistance, atMost(0.05), true),
		measured(BuiltinHammingDistance, "Hamming distance", CategoryEmbedding, "The share of dimensions in which the two embeddings differ.", scaleUnit, atMost(0.5), true),
	}
	slices.SortStableFunc(entries, func(a, b CatalogEntry) int {
		if c := strings.Compare(a.Category, b.Category); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return entries
}

// Catalog returns every built-in evaluator, grouped by category and sorted by name.
func Catalog() []CatalogEntry {
	return slices.Clone(loadCatalog())
}

// CatalogEntryFor returns the built-in with the given key.
func CatalogEntryFor(key string) (CatalogEntry, bool) {
	for _, entry := range loadCatalog() {
		if entry.Key == key {
			return entry, true
		}
	}
	return CatalogEntry{}, false
}

// InstallOptions are the choices a user makes when adding a built-in.
type InstallOptions struct {
	Provider     string         // judge or embedding provider, when the built-in needs one
	Model        string         // judge or embedding model
	ModelParams  map[string]any // judge model parameters
	PassCriteria *PassCriteria  // overrides the built-in's default
}

// InstallConfig builds the stored config of an evaluator created from this built-in.
func (e CatalogEntry) InstallConfig(opts InstallOptions) ([]byte, error) {
	criteria := e.PassCriteria
	if opts.PassCriteria != nil {
		criteria = *opts.PassCriteria
	}
	common := CommonConfig{PassCriteria: criteria, Reversed: e.LowerIsBetter}
	needsModel := e.NeedsJudgeModel || e.NeedsEmbedding
	if needsModel && (strings.TrimSpace(opts.Provider) == "" || strings.TrimSpace(opts.Model) == "") {
		if e.NeedsEmbedding {
			return nil, errors.New("this evaluator needs an embedding provider and model")
		}
		return nil, errors.New("this evaluator needs a judge provider and model")
	}

	var cfg Config
	switch e.Type {
	case EvaluatorTypeAI:
		cfg = &AIConfig{
			CommonConfig: common,
			// The prompt is fixed by the built-in; the instructions field only says so.
			Instructions: e.Description,
			GradingStyle: GradingStyleNumber,
			Provider:     opts.Provider,
			Model:        opts.Model,
			ModelParams:  opts.ModelParams,
		}
	case EvaluatorTypeStatistical:
		statistical := &StatisticalConfig{CommonConfig: common}
		if e.NeedsEmbedding {
			statistical.EmbeddingProvider, statistical.EmbeddingModel = opts.Provider, opts.Model
		}
		cfg = statistical
	default:
		return nil, fmt.Errorf("built-in %q has an unsupported type %q", e.Key, e.Type)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return providerUtils.MarshalSorted(cfg)
}
