package warp

// LogReaderStub embeds the LogReader interface without implementing it.
//
// This is the point: a fake embedding it satisfies the interface at compile
// time, but any method the fake does not override panics with a nil-pointer
// dereference when called. Warp's tools are supposed to touch a small, known
// subset of the read surface, so a test that suddenly panics is telling us an
// executor started reaching somewhere new - which is exactly the change that
// should require a human to look.
type LogReaderStub struct {
	LogReader
}
