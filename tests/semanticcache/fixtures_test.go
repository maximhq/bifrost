package semanticcache

import "testing"

// paraphrasePair holds two prompts that are SEMANTICALLY equivalent (near
// rephrasings, expected cosine ≥ ~0.92 with text-embedding-3-small) plus an
// UNRELATED prompt from a completely different domain (expected cosine
// ≤ ~0.4). The gap from the default 0.8 threshold is intentionally large
// on both sides so Phase 2 hit/miss assertions never sit on a flaky
// boundary.
//
// Pair design rules (when adding new ones):
//   - Canonical vs Paraphrase: only swap 1-2 words/phrases (e.g. "What is"
//     ↔ "Tell me"), keep ALL content nouns and proper nouns identical, keep
//     overall sentence shape. This pushes cosine into 0.92-0.97.
//   - Unrelated: pick a topic from a completely different domain (cooking
//     vs astronomy, history vs electronics, etc.). Single-domain switches
//     ("dogs" ↔ "cats") creep up to 0.6+ and would be flaky.
//   - Sentences should be long enough (>= ~8 content words) that small
//     wording changes don't dominate the embedding.
type paraphrasePair struct {
	Name       string
	Canonical  string
	Paraphrase string
	Unrelated  string
}

// paraphrasePairs is the chat/text-paraphrase corpus used by Phase 2 semantic
// cases. Each pair is hand-curated to land WELL above (canonical→paraphrase)
// or WELL below (canonical→unrelated) the default 0.8 threshold.
var paraphrasePairs = []paraphrasePair{
	{
		Name:       "capital_france",
		Canonical:  "What is the capital city of France in modern times?",
		Paraphrase: "Tell me the capital city of France in modern times.",
		Unrelated:  "Explain how a transistor works at the silicon level.",
	},
	{
		Name:       "boiling_water",
		Canonical:  "At what temperature does pure water boil at sea level?",
		Paraphrase: "What is the boiling point of pure water at sea level?",
		Unrelated:  "Recommend a well-known jazz album recorded in the 1960s.",
	},
	{
		Name:       "vinaigrette",
		Canonical:  "How do I make a basic vinaigrette salad dressing at home?",
		Paraphrase: "What are the steps to make a basic vinaigrette salad dressing at home?",
		Unrelated:  "Describe quantum entanglement in a single paragraph for a beginner.",
	},
	{
		Name:       "opera_composer",
		Canonical:  "Name a famous Italian opera composer from the nineteenth century.",
		Paraphrase: "Tell me one famous Italian opera composer from the nineteenth century.",
		Unrelated:  "What is the average distance from Earth to the planet Mars?",
	},
	{
		Name:       "photosynthesis",
		Canonical:  "Briefly explain how photosynthesis works in green plants.",
		Paraphrase: "In a few sentences, describe how photosynthesis works in green plants.",
		Unrelated:  "How do you knit a basic scarf using stockinette stitch?",
	},
}

// imagePromptPairs is the image-generation paraphrase corpus used by Phase 2
// case 2.25 (image_gen_semantic_paraphrase). Image prompts tend to be shorter
// than chat prompts so we leave the content nouns identical and only vary
// modifiers slightly.
var imagePromptPairs = []paraphrasePair{
	{
		Name:       "red_apple",
		Canonical:  "A bright red apple sitting on a wooden kitchen table in daylight.",
		Paraphrase: "A vivid red apple resting on a wooden kitchen table in daylight.",
		Unrelated:  "A futuristic silver spaceship orbiting Saturn against a starry void.",
	},
}

// pairByName looks up a paraphrase pair by name. Fatal if not defined — the
// suite should fail loudly if a case references a pair that was removed.
func pairByName(t *testing.T, name string) paraphrasePair {
	t.Helper()
	for _, p := range paraphrasePairs {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("paraphrase pair %q not defined in paraphrasePairs", name)
	return paraphrasePair{}
}

// imagePairByName looks up an image prompt pair by name. Fatal if not defined.
func imagePairByName(t *testing.T, name string) paraphrasePair {
	t.Helper()
	for _, p := range imagePromptPairs {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("image prompt pair %q not defined in imagePromptPairs", name)
	return paraphrasePair{}
}
