package vectorstore

import "errors"

var (
	ErrNotFound     = errors.New("vectorstore: not found")
	ErrNotSupported = errors.New("vectorstore: operation not supported on this store")
)
