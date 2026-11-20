//go:build unix && !tinygo && !wasm

package starlark

import (
	"net/http"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestListToolFilesTokenFileFIFOFallsBack points token_file at a named pipe
// with no writer: the query must fall back to lexical ranking instead of
// blocking on the open.
func TestListToolFilesTokenFileFIFOFallsBack(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "token")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a named pipe: %v", err)
	}
	rs := newRankerServer(t, func(w http.ResponseWriter, _ *http.Request, _ discoveryRankRequest) {
		t.Error("ranker must not be called without its token")
	})
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
	useRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{URL: rs.URL, TokenFile: fifo})

	done := make(chan string, 1)
	go func() { done <- listToolFiles(t, mode, `{"query":"create an issue"}`) }()
	select {
	case out := <-done:
		assertLexicalFallback(t, out)
	case <-time.After(2 * time.Second):
		t.Fatal("listToolFiles blocked opening a named pipe as the token file")
	}
}
