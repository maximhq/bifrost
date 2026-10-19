package schemas

// BifrostContextKeyHTTPExchangeObserver enables observation at the provider HTTP
// boundary, before parsing or conversion. Its value is HTTPExchangeObserverFactory.
const BifrostContextKeyHTTPExchangeObserver BifrostContextKey = "bifrost-http-exchange-observer"

// HTTPExchangeObserverFactory creates an observer for one HTTP attempt. It may
// return nil. Callbacks run synchronously and must not block or retain byte slices
// without copying them. Implementations own storage, bounds, and redaction.
type HTTPExchangeObserverFactory func() HTTPExchangeObserver

// HTTPExchangeObserver receives body bytes, never credentials in headers or URLs.
// Bodies retain Content-Encoding; HTTP transfer framing has been removed. A nil
// request body means an unobserved streaming upload; an empty slice means empty.
// Response may be called repeatedly as a stream is read, including once with nil
// data when its headers arrive. Done is called once; its error describes transport read/close failures.
type HTTPExchangeObserver interface {
	Request(contentType, contentEncoding string, body []byte)
	Response(status int, contentType, contentEncoding string, body []byte)
	Done(error)
}
