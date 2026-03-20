package tray

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestGenerateIcons generates all icon variants and writes them to a temporary
// directory for visual inspection. Run with:
//
//	go test ./tray/ -run TestGenerateIcons -v
//
// Then open the generated PNGs to verify they look correct.
func TestGenerateIcons(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "bifrost-icons")
	os.MkdirAll(dir, 0755)

	icons := map[string][]byte{
		"template_connected.png":    TemplateIconConnected,
		"template_disconnected.png": TemplateIconDisconnected,
		"color_connected.png":       IconConnected,
		"color_disconnected.png":    IconDisconnected,
		"color_error.png":           IconError,
		"color_warning.png":         IconWarning,
	}

	for name, data := range icons {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Errorf("failed to write %s: %v", name, err)
			continue
		}

		// Verify it's a valid PNG
		_, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Errorf("%s is not a valid PNG: %v", name, err)
			continue
		}

		t.Logf("wrote %s (%d bytes)", path, len(data))
	}

	t.Logf("\nIcons written to: %s", dir)
	t.Logf("Open them to verify the logo-style tray mark looks correct.")
}

func TestIconSizes(t *testing.T) {
	icons := map[string][]byte{
		"TemplateIconConnected":    TemplateIconConnected,
		"TemplateIconDisconnected": TemplateIconDisconnected,
		"IconConnected":            IconConnected,
		"IconDisconnected":         IconDisconnected,
		"IconError":                IconError,
		"IconWarning":              IconWarning,
	}

	for name, data := range icons {
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Errorf("%s: invalid PNG: %v", name, err)
			continue
		}
		bounds := img.Bounds()
		w, h := bounds.Dx(), bounds.Dy()
		if w != iconSize2x || h != iconSize2x {
			t.Errorf("%s: size %dx%d, want %dx%d", name, w, h, iconSize2x, iconSize2x)
		}
	}
}
