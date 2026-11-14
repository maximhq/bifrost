package vectorstore

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound     = errors.New("vectorstore: not found")
	ErrNotSupported = errors.New("vectorstore: operation not supported on this store")
	ErrQuerySyntax  = errors.New("vectorstore: query syntax error")
	// ErrDimensionMismatch is wrapped by CreateNamespace when the namespace already holds vectors
	// of a different size than the caller asked for. It is the caller's configuration that is
	// wrong, so callers can surface it as a client error rather than a server failure.
	ErrDimensionMismatch = errors.New("vectorstore: namespace dimension mismatch")
)

// dimensionMismatchError is the one message every store reports when CreateNamespace finds the
// namespace already holding vectors of another size. existing is 0 when the store cannot tell the
// size it holds, only that it differs. kind names what the operator would drop by hand.
func dimensionMismatchError(namespace string, existing, requested int, kind string) error {
	if existing <= 0 {
		return fmt.Errorf("%w: namespace %q already holds vectors of a different dimension than the %d the config requires — update vector_store_namespace to a new name or drop the existing %s manually", ErrDimensionMismatch, namespace, requested, kind)
	}
	return fmt.Errorf("%w: namespace %q already exists with dimension %d but config requires %d — update vector_store_namespace to a new name or drop the existing %s manually", ErrDimensionMismatch, namespace, existing, requested, kind)
}
