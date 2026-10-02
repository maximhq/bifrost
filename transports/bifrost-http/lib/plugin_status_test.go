package lib

import (
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

type fileInfoPlugin struct {
	sha      string
	loadedAt time.Time
	err      error
}

func (p *fileInfoPlugin) GetName() string                      { return "custom" }
func (p *fileInfoPlugin) Cleanup() error                       { return nil }
func (p *fileInfoPlugin) FileInfo() (string, time.Time, error) { return p.sha, p.loadedAt, p.err }

type plainPlugin struct{}

func (p *plainPlugin) GetName() string { return "builtin" }
func (p *plainPlugin) Cleanup() error  { return nil }

func TestSetPluginFileInfo(t *testing.T) {
	SetLogger(&testLogger{})
	c := &Config{}
	loadedAt := time.Now()
	c.UpdatePluginOverallStatus("custom", "custom", schemas.PluginStatusActive, nil, nil)
	c.SetPluginFileInfo("custom", &fileInfoPlugin{sha: "abc", loadedAt: loadedAt})
	c.UpdatePluginOverallStatus("builtin", "builtin", schemas.PluginStatusActive, nil, nil)
	c.SetPluginFileInfo("builtin", &plainPlugin{})
	c.UpdatePluginOverallStatus("unhashed", "unhashed", schemas.PluginStatusActive, nil, nil)
	c.SetPluginFileInfo("unhashed", &fileInfoPlugin{loadedAt: loadedAt, err: errors.New("file removed")})

	if err := c.UpdatePluginDisplayName("custom", "renamed"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	c.pluginStatusMu.RLock()
	defer c.pluginStatusMu.RUnlock()
	got := c.pluginStatus["custom"]
	if got.Name != "renamed" || got.SHA256 != "abc" || got.LoadedAt == nil || !got.LoadedAt.Equal(loadedAt) {
		t.Fatalf("custom status not preserved: %+v", got)
	}
	if u := c.pluginStatus["unhashed"]; u.Status != schemas.PluginStatusActive || u.SHA256 != "" || u.LoadedAt != nil {
		t.Fatalf("hash failure should leave status active without file info: %+v", u)
	}
	if b := c.pluginStatus["builtin"]; b.SHA256 != "" || b.LoadedAt != nil {
		t.Fatalf("builtin status should have no file info: %+v", b)
	}
}
