package websocket

import "errors"

var (
	ErrConnectionLimitReached = errors.New("websocket connection limit reached")
	ErrPoolClosed             = errors.New("websocket pool is closed")
)
