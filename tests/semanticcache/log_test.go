package semanticcache

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	logMu        sync.Mutex
	runReportDir string
	runLogFile   *os.File
	trailSID     string
)

func initLog() error {
	base := filepath.Join("reports", time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(base, "run.log"))
	if err != nil {
		return err
	}
	runReportDir = base
	runLogFile = f
	trailSID = strings.TrimSpace(os.Getenv("TRAIL_SESSION_ID"))
	return nil
}

func closeLog() {
	logMu.Lock()
	defer logMu.Unlock()
	if runLogFile != nil {
		_ = runLogFile.Close()
		runLogFile = nil
	}
}

type logCtx struct {
	phase string
	name  string
	step  int
}

func newLogCtx(phase, name string) logCtx { return logCtx{phase: phase, name: name} }

func (lc logCtx) at(step int) logCtx { lc.step = step; return lc }

func logf(t *testing.T, lc logCtx, lvl, event string, fields map[string]any) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "[SC-E2E] ts=%s lvl=%-5s phase=%s case=%s step=%d event=%s",
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		lvl, lc.phase, lc.name, lc.step, event)
	if trailSID != "" {
		fmt.Fprintf(&b, " trail_sid=%s", trailSID)
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, fields[k])
	}
	line := b.String()
	t.Log(line)
	logMu.Lock()
	if runLogFile != nil {
		fmt.Fprintln(runLogFile, line)
	}
	logMu.Unlock()
}

func reportPath(parts ...string) string {
	if runReportDir == "" {
		return filepath.Join(parts...)
	}
	return filepath.Join(append([]string{runReportDir}, parts...)...)
}

func dumpJSON(t *testing.T, name string, body []byte) string {
	t.Helper()
	p := reportPath(name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Logf("warning: dump %s failed: %v", p, err)
	}
	return p
}
