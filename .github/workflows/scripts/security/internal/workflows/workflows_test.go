package workflows

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestStepsKeepInlineFields proves secret references in step env and if survive re-marshaling,
// since every secret policy greps the marshaled job rather than the raw file.
func TestStepsKeepInlineFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "release-pipeline.yml")
	source := "on: push\njobs:\n  leak:\n    steps:\n      - name: step env\n        env:\n          KEY: ${{ secrets.R2_ACCESS_KEY_ID }}\n        run: echo\n      - if: ${{ secrets.NPM_TOKEN != '' }}\n        run: echo\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := yaml.Marshal(document.Jobs["leak"])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secrets.R2_ACCESS_KEY_ID", "secrets.NPM_TOKEN"} {
		if !strings.Contains(string(body), secret) {
			t.Errorf("marshaled job lost %s:\n%s", secret, body)
		}
	}
}
