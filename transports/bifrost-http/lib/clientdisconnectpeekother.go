//go:build !unix

package lib

import "syscall"

// clientDisconnectPeekSupported is false here: the watcher is a no-op and only
// the reactive write-failure cancel observes a disconnect.
const clientDisconnectPeekSupported = false

// peekClientSocket has no non-consuming peek on this platform; the watcher
// stops on its first tick and the reactive write-failure cancel stays the only
// disconnect signal.
func peekClientSocket(_ syscall.RawConn) peekResult { return peekUndetermined }
