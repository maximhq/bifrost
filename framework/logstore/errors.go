package logstore

import "fmt"

var (
	ErrNotFound    = fmt.Errorf("log not found")
	ErrJobInternal = fmt.Errorf("internal job store error")
)
