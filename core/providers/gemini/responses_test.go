package gemini

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/memtest"
)

// TestStripFunctionResponseMediaRefs_AllocationScaling pins the allocation shape of
// the $ref strip.
//
// The loop deletes one top-level key at a time, and every providerUtils.DeleteJSONField
// reserialises the whole function-response document, so N media refs cost N copies of
// it. Today N is small in practice, which is exactly why this went unnoticed: the
// complexity is wrong but the payloads have been forgiving. A tool returning many
// media parts is all it takes for that to stop being true.
//
// memtest compares allocation growth against input growth, so this fails on the
// complexity class rather than on a byte threshold that would encode this machine.
func TestStripFunctionResponseMediaRefs_AllocationScaling(t *testing.T) {
	memtest.AssertAllocScaling(t, func(refs int) []byte {
		var b bytes.Buffer
		b.WriteString(`{"output":"`)
		b.WriteString(strings.Repeat("o", 200))
		b.WriteString(`"`)
		for i := range refs {
			// Each media ref is a {"$ref": ...} placeholder, which is what the strip
			// targets. The long ref value is what makes the payload grow with N.
			b.WriteString(`,"media_`)
			b.WriteString(strings.Repeat("k", 3))
			b.WriteString(itoa(i))
			b.WriteString(`":{"$ref":"`)
			b.WriteString(strings.Repeat("r", 400))
			b.WriteString(`"}`)
		}
		b.WriteString(`}`)
		return b.Bytes()
	}, func(body []byte) {
		stripFunctionResponseMediaRefs(json.RawMessage(body))
	})
}

// itoa avoids pulling strconv in just for the payload builder above.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
