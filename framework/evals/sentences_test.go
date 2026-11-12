package evals

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// sentenceReference is the output of pysbd 0.3.4 (English, clean=False), the splitter Maxim
// uses, on texts shaped like model output.
var sentenceReference = []struct {
	text      string
	sentences []string
}{
	{"The refund window is 30 days. Items must be unused. Contact support to start a return.", []string{"The refund window is 30 days. ", "Items must be unused. ", "Contact support to start a return."}},
	{"Dr. Smith arrived at 3 p.m. on Jan. 5. He met Mr. Jones and Mrs. Lee.", []string{"Dr. Smith arrived at 3 p.m. on Jan. 5. ", "He met Mr. Jones and Mrs. Lee."}},
	{"It costs $3.50 per unit. That is 2.5% cheaper than before!", []string{"It costs $3.50 per unit. ", "That is 2.5% cheaper than before!"}},
	{"Is it ready? Yes! Ship it now.", []string{"Is it ready? ", "Yes! ", "Ship it now."}},
	{"The U.S. economy grew. The U.K. economy did not.", []string{"The U.S. economy grew. ", "The U.K. economy did not."}},
	{"He said \"It works.\" Then he left.", []string{"He said \"It works.\" ", "Then he left."}},
	{"See section 3.2 for details. Version 1.10.4 fixes it.", []string{"See section 3.2 for details. ", "Version 1.10.4 fixes it."}},
	{"Wait... what happened? Nobody knows.", []string{"Wait... what happened? ", "Nobody knows."}},
	{"First line.\nSecond line without period\nThird line.", []string{"First line.\n", "Second line without period\n", "Third line."}},
	{"Paris is the capital of France", []string{"Paris is the capital of France"}},
	{"One. Two. Three.", []string{"One. ", "Two. ", "Three."}},
	{"The company (Acme Inc.) was founded in 1999. It grew fast.", []string{"The company (Acme Inc.) was founded in 1999. ", "It grew fast."}},
	{"e.g. apples, oranges, etc. are fruits. i.e. they grow on trees.", []string{"e.g. apples, oranges, etc. are fruits. ", "i.e. they grow on trees."}},
	{"She works at Google LLC. Her title is Sr. Engineer.", []string{"She works at Google LLC. ", "Her title is Sr. ", "Engineer."}},
	{"1. Install the package. 2. Run the tests. 3. Deploy.", []string{"1. Install the package. ", "2. Run the tests. ", "3. Deploy."}},
	{"- Refunds take 5 days\n- Exchanges take 7 days", []string{"- Refunds take 5 days\n", "- Exchanges take 7 days"}},
	{"The answer is 42.The next one is 43.", []string{"The answer is 42.The next one is 43."}},
	{"", []string{}},
	{"   ", []string{}},
	{"Prof. Xavier vs. Magneto, No. 5 in the series. It was great.", []string{"Prof. Xavier vs. Magneto, No. 5 in the series. ", "It was great."}},
	{"The temp. was 5 deg. C. It rose later.", []string{"The temp. ", "was 5 deg. ", "C. It rose later."}},
	{"\"Why?\" she asked. \"Because,\" he said.", []string{"\"Why?\" she asked. ", "\"Because,\" he said."}},
	{"I live in St. Louis. It is on the Mississippi River.", []string{"I live in St. Louis. ", "It is on the Mississippi River."}},
	{"The meeting is at 10 a.m. Bring the report.", []string{"The meeting is at 10 a.m. ", "Bring the report."}},
	{"Total: 1,234.56 units. Next: 7.", []string{"Total: 1,234.56 units. ", "Next: 7."}},
	{"This is a sentence! This is another one; it has a semicolon. Done.", []string{"This is a sentence! ", "This is another one; it has a semicolon. ", "Done."}},
}

func TestSplitSentencesMatchesPysbd(t *testing.T) {
	for _, row := range sentenceReference {
		got := SplitSentences(row.text)
		if len(row.sentences) == 0 {
			assert.Empty(t, got, "%q", row.text)
			continue
		}
		assert.Equal(t, row.sentences, got, "%q", row.text)
		assert.Equal(t, row.text, strings.Join(got, ""), "the pieces join back into the text")
	}
}

// pysbd splits a URL at its question mark. That is a defect, not a rule worth copying.
func TestSplitSentencesKeepsURLsWhole(t *testing.T) {
	got := SplitSentences("Visit https://example.com/a.b?c=d for more. Email me at a.b@example.com today.")
	assert.Equal(t, []string{"Visit https://example.com/a.b?c=d for more. ", "Email me at a.b@example.com today."}, got)
}

// pysbd keeps "Hello!!! Are you there??" as one sentence. A run of exclamation marks
// followed by a new sentence is split here.
func TestSplitSentencesSplitsAfterRepeatedExclamationMarks(t *testing.T) {
	assert.Equal(t, []string{"Hello!!! ", "Are you there?? ", "Yes."}, SplitSentences("Hello!!! Are you there?? Yes."))
}
