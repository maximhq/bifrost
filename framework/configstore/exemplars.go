package configstore

// Default semantic-routing exemplars are balanced along two axes at once.
//
// Use case: each tier contains 14 coding, 6 math/data, 6 writing, 5 knowledge,
// 5 conversational/creative, 5 extraction/classification, 3 pasted-material,
// 3 translation, and 3 agentic prompts. Topic ladders place related vocabulary
// in every tier so nearest-neighbor classification learns requested work rather
// than subject matter.
//
// Surface form: cosine similarity is at least as sensitive to how a request is
// written as to what it asks for, so form is balanced deliberately instead of
// being left to follow difficulty. Exemplars stay short and prototypical -- a
// long, hyper-specific phrase embeds mostly as its own incidental content and
// matches only near-identical requests, and a tier whose members are uniformly
// longer than the tier below it teaches the classifier that verbosity means
// difficulty. Tier length ranges therefore overlap on purpose: what separates
// them is the requested work, not the word count.
//
// The same applies to every other surface feature. Question and imperative
// phrasing, capitalized and all-lowercase text, terse and fully specified
// requests, and prompts that embed code all appear in similar proportion in all
// three tiers. Any of them concentrated in one tier becomes a shortcut the
// classifier will take: a tier that is mostly questions routes every question
// to itself. exemplars_test.go asserts the spread; keep it when editing.
//
// Every exemplar's tier must be derivable from its own text. Pointing at
// content that arrives with the request is fine ("summarize these notes"), but
// a phrase whose work lives in an earlier turn ("yes, go with option 2") has no
// defensible tier: option 2 could be a rename or a migration. Labelling one
// SIMPLE teaches the classifier to route every contentless follow-up to the
// cheapest model, whatever the conversation was actually about.

var defaultSimpleExemplars = []string{
	// Coding and software engineering (14).
	"how do i convert a string to an int in go?",
	"Give me the SQL to count paid orders.",
	"what's a mutex for?",
	"How do I read a request header in Go?",
	"Which status code means not found?",
	"disable this button while the form is submitting",
	"why does this panic? `var m map[string]int; m[\"a\"] = 1`",
	"Explain a cache hit versus a cache miss.",
	"Make this GitHub Actions job run only on pull requests.",
	"rename `n` to `count` in this function",
	"Write a function that returns the largest number in a list.",
	"Shell command to list every JSON file here.",
	"What does p95 latency actually measure?",
	"what's a context window?",

	// Math, logic, and data reasoning (6).
	"what's 15% off $80?",
	"chance of rolling an 8 with two dice?",
	"Take the median of these five numbers.",
	"if every admin is an employee, is this admin an employee?",
	"users went from 4,000 to 4,600, what's the percent increase?",
	"Average these three weeks and forecast the next one.",

	// Writing and content transformation (6).
	"Fix the grammar in this sentence.",
	"Summarize this paragraph in one sentence.",
	"Rewrite this in plain language.",
	"turn these notes into bullets",
	"Write a short release-note title for this change.",
	"make this sound less angry",

	// Knowledge and explanation (5).
	"What is antibiotic resistance?",
	"inflation, explain it in one line",
	"eli5 how the water cycle works",
	"Is gross margin the same as profit?",
	"what does end-to-end encryption actually protect?",

	// Conversational, creative, and roleplay (5).
	"hey! how's your day going?",
	"Write a two-line birthday message for a coworker.",
	"give me five name ideas for a grey kitten",
	"What's a good gift for a five-year-old?",
	"reply to this text casually",

	// Extraction, classification, and structured output (5).
	"Pull the invoice number out of this line.",
	"Is this review positive or negative?",
	"Convert this CSV row to JSON.",
	"does this message contain a phone number?",
	"Tag this ticket as bug or feature request.",

	// Work on pasted material (3).
	"Summarize these meeting notes.",
	"what are they asking me to do in this email thread?",
	"Put this clause in plain English.",

	// Translation and multilingual (3).
	"translate \"where is the train station?\" into spanish",
	"¿Cuál es la capital de Francia?",
	"what does \"schadenfreude\" mean?",

	// Agentic and tool use (3).
	"Run the tests and tell me if they pass.",
	"List the files you've changed so far.",
	"what tools can you use here?",
}

var defaultMediumExemplars = []string{
	// Coding and software engineering (14).
	"Write a worker pool that caps concurrency and drains cleanly on cancel.",
	"fix this `useEffect` so a stale response can't overwrite fresher state",
	"how do i add a nullable column to a big table and backfill it without downtime?",
	"Add API-key auth: hash the keys, reject revoked ones, and never log them.",
	"this counter fails under `-race`; fix it so the final count is reliable",
	"add a short-lived cache around this call and coalesce concurrent misses",
	"Split these tests into parallel CI jobs and cache the dependencies.",
	"our endpoint got slower after a release. How do i find out why?",
	"how do i get O(1) get and put out of an LRU cache?",
	"what's the cleanest way to build a cli that validates config files and exits nonzero?",
	"This handler mixes validation, logic, and persistence, so split it without changing behavior.",
	"Define one error response shape and map validation and auth failures onto it.",
	"Reject requests over the model's context window and report the remaining budget.",
	"What should an upload endpoint validate before it stores a file?",

	// Math, logic, and data reasoning (6).
	"Work out the total repayment for a loan with a fee plus simple interest.",
	"Given base rates and test accuracy, how likely is a flagged item defective?",
	"is one outlier driving this, or did the whole distribution move?",
	"Schedule four tasks across four days given ordering and availability constraints.",
	"Compare activation and retention across two months and explain what changed.",
	"Fit a trend to these monthly numbers, forecast, and check the residuals.",

	// Writing and content transformation (6).
	"Rewrite this support reply to acknowledge the frustration and give a next action.",
	"Summarize this into three bullets and add a headline that doesn't imply causation.",
	"Rewrite this deprecation notice for admins, with impact and next steps.",
	"turn these notes into a decision memo comparing two options",
	"Write release notes grouped by added, improved, and safety.",
	"Draft a customer update from this incident timeline.",

	// Knowledge and explanation (5).
	"Explain the water cycle, then predict what less vegetation does to runoff.",
	"Why do higher interest rates take time to slow inflation?",
	"How does antibiotic overuse affect people who never took the drug?",
	"When is contribution margin more useful than gross margin?",
	"How does key exchange keep the service from reading messages?",

	// Conversational, creative, and roleplay (5).
	"Be my interview practice partner and critique each answer before moving on.",
	"Write a short story about a lighthouse keeper, ending unresolved.",
	"Plan a three-day trip for two with no early mornings and a fixed budget.",
	"Act as a copy editor on these headlines and justify each change.",
	"brainstorm ten subject lines, then pick the two you'd test first",

	// Extraction, classification, and structured output (5).
	"Extract the order details from this email into JSON, using null for missing fields.",
	"Classify these tickets into six categories with a one-line reason each.",
	"Map this CSV to our schema, coerce the types, and list rows that failed.",
	"redact names, emails, and phone numbers, then list what you removed",
	"Score these leads and return a ranked table with the reason for each.",

	// Work on pasted material (3).
	"summarize these notes, then list decisions, owners, and anything contradictory",
	"find the failing call in this log and suggest a fix",
	"Explain what this clause obliges us to do and where the risk is.",

	// Translation and multilingual (3).
	"Translate this macro into French and German, keeping tone and placeholders intact.",
	"Necesito una consulta SQL con los pedidos pagados por mes y el total facturado.",
	"Rewrite this announcement for a Japanese audience and flag what won't translate.",

	// Agentic and tool use (3).
	"which functions call `ParseConfig`? list them with file and line",
	"Permission denied writing the cache directory. Work around it and tell me what changed.",
	"run the failing package tests, fix the assertion that broke, and show me the diff",
}

var defaultComplexExemplars = []string{
	// Coding and software engineering (14).
	"How do we shard a live database, keep rollback possible, and preserve read-your-writes?",
	"how do we rotate signing keys across regions without invalidating live tokens?",
	"we need a multi-region failover plan for the api and its database",
	"Two providers race to stream the first chunk. Make sure exactly one wins.",
	"Reconstruct what happened across these services and give me the root cause, not the symptom.",
	"`Store` has four overlapping implementations. How do we consolidate while feature work continues?",
	"Design a rate limiter that stays fair across tenants and degrades safely when coordination fails.",
	"Threat-model uploads we scan, preview, and serve back, and identify where tenant boundaries break.",
	"What indexes and partitioning does a multi-tenant table with three access patterns need?",
	"how do we evolve the error contract without breaking old clients or their retries?",
	"order schema and service rollouts across repos that deploy independently, with rollback",
	"`authz` decisions are cached in three regions; how do we invalidate when one is offline?",
	"Trace partial failures across services without leaking tenant data or exploding cardinality.",
	"truncate context safely when tools, history, and fallback models all have different limits",

	// Math, logic, and data reasoning (6).
	"buy or lease, when we don't know how long the project runs? show the sensitivity",
	"Pick a fraud threshold given review cost, missed-fraud cost, and reviewer capacity.",
	"the segments reverse the overall result. Do we launch anyway?",
	"Several access rules conflict. Define precedence and derive the final decision logic.",
	"minimize cost across shifts with coverage, adjacency, and per-person limits",
	"Separate treatment effect from selection, spillover, and seasonality in this rollout.",

	// Writing and content transformation (6).
	"Reconcile legal, engineering, and support positions into one notice that stays consistent.",
	"Write a decision brief separating facts from hypotheses, and define the evidence gates.",
	"Draft messages for three audiences from the same facts, keeping commitments consistent.",
	"Two drafts of this policy contradict each other, so produce one and mark every disagreement.",
	"Turn this RFC into an executive summary and an engineering appendix that stay traceable.",
	"Draft customer and internal incident reports where the timeline itself is uncertain.",

	// Knowledge and explanation (5).
	"Untangle warming, land use, and pumping here, and say what evidence would separate them.",
	"Compare monetary and fiscal responses to stagflation, including who bears the cost.",
	"Balance testing, prescribing rules, and staffing against rising resistant infections.",
	"Raise the device price, the subscription, or both? Weigh churn, margin, and payback.",
	"Add multi-device access and revocation without weakening end-to-end encryption.",

	// Conversational, creative, and roleplay (5).
	"Roleplay a supplier with a hidden cost floor across several turns, then debrief me.",
	"Write the next chapter in the established voice, planting a detail that pays off later.",
	"Which offer wins when cash, equity, and visa risk all point different ways?",
	"Design a persona that stays in character, refuses out-of-scope asks, and escalates cleanly.",
	"enterprise buyers want control, developers want speed, and evidence is thin. Where do we position?",

	// Extraction, classification, and structured output (5).
	"Merge records from three systems that disagree, and flag what can't be reconciled.",
	"Design a taxonomy for this backlog, including multi-label rules and genuinely ambiguous cases.",
	"two ledger exports disagree, so explain the differences before proposing any correction",
	"Define one redaction policy across mixed documents where some are under legal hold.",
	"Build the scoring rubric, apply it, and show where it disagrees with sales.",

	// Work on pasted material (3).
	"these three postmortems blame different things. Which one actually fits the evidence?",
	"Diff these two policy versions and say which changes need customer notification.",
	"Do these three bug reports share a root cause, and what evidence is missing?",

	// Translation and multilingual (3).
	"Localize this copy across five locales, handling plurals, length limits, and legal wording.",
	"Migrar de claves estáticas a tokens de corta duración sin cortar el servicio ni perder la auditoría.",
	"Keep three vendors' translations consistent with a source that changes every release.",

	// Agentic and tool use (3).
	"Three attempts at this flaky test failed the same way. Which assumption is wrong?",
	"reproduce, isolate, fix, and prove the fix holds, then say where you're uncertain",
	"Unfamiliar repo, flaky tests: decide what to run, when to stop, and what to hand back.",
}

// DefaultComplexityExemplars returns independent copies of the curated
// semantic-routing exemplars. The configstore migration and governance runtime
// share this source so existing and new installations receive the same phrases.
func DefaultComplexityExemplars() ComplexityEditableKeywordConfig {
	return ComplexityEditableKeywordConfig{
		SimpleKeywords:  cloneComplexityExemplars(defaultSimpleExemplars),
		MediumKeywords:  cloneComplexityExemplars(defaultMediumExemplars),
		ComplexKeywords: cloneComplexityExemplars(defaultComplexExemplars),
	}
}

func cloneComplexityExemplars(values []string) []string {
	return append([]string(nil), values...)
}
