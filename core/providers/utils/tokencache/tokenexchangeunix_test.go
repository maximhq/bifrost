//go:build unix

package tokencache

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenExchangeRefusesFIFOWithoutBlocking(t *testing.T) {
	es := newExchangeServer(t)
	fifo := filepath.Join(tokenDir(t), "token")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	mint := TokenExchangeMinter(TokenExchangeConfig{TokenURL: es.URL, SubjectTokenFile: fifo}, authClient(t))

	done := make(chan struct{})
	var message string
	go func() {
		defer close(done)
		_, bErr := mint(context.Background(), nil)
		if bErr != nil {
			message = bErr.Error.Message
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO subject token blocked")
	}
	assert.Contains(t, message, "not a regular file")
	hits, _, _ := es.snapshot()
	assert.Zero(t, hits)
}
