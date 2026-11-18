package integrations

import (
	"bytes"
	"errors"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/tidwall/gjson"
)

// filterCodexModelList keeps the entries of a ChatGPT Codex model list ({"models":[{"slug":...}]}) whose slug keep accepts.
func filterCodexModelList(body []byte, keep func(model string) bool) ([]byte, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("codex model list is not valid JSON")
	}
	models := providerUtils.GetJSONField(body, "models")
	if !models.IsArray() {
		return body, nil
	}
	var kept bytes.Buffer
	kept.WriteByte('[')
	n := 0
	for _, m := range models.Array() {
		if !keep(m.Get("slug").String()) {
			continue
		}
		if n > 0 {
			kept.WriteByte(',')
		}
		kept.WriteString(m.Raw)
		n++
	}
	kept.WriteByte(']')
	return providerUtils.SetRawJSONField(body, "models", kept.Bytes())
}
