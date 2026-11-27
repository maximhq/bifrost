package utils

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ErrPhysicalSendAuthorization marks a request that was stopped before its
// serialized HTTP bytes were written because the final-send guard was absent,
// failed, or could not bind the attempt.
var ErrPhysicalSendAuthorization = errors.New("bifrost: physical-send authorization failed")

// ErrPhysicalSendRequestTooLarge marks a guarded request that exceeds the bound
// supplied by its authorizer. The request is never truncated or written.
var ErrPhysicalSendRequestTooLarge = errors.New("bifrost: serialized request exceeds physical-send bound")

// PhysicalSendAuthorizationError carries a safe refusal code through provider
// error conversion. The cause is retained for errors.Is/errors.As and must not
// contain request body bytes or credentials.
type PhysicalSendAuthorizationError struct {
	StatusCode int
	ReasonCode string
	Cause      error
}

func (e *PhysicalSendAuthorizationError) Error() string {
	if e == nil || e.ReasonCode == "" {
		return "bifrost: physical-send authorization denied"
	}
	return "bifrost: physical-send authorization denied (" + e.ReasonCode + ")"
}

func (e *PhysicalSendAuthorizationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *PhysicalSendAuthorizationError) Is(target error) bool {
	return target == ErrPhysicalSendAuthorization
}

func NewPhysicalSendAuthorizationError(statusCode int, reasonCode string, cause error) *PhysicalSendAuthorizationError {
	if statusCode < 400 || statusCode > 599 {
		statusCode = 403
	}
	return &PhysicalSendAuthorizationError{StatusCode: statusCode, ReasonCode: reasonCode, Cause: cause}
}

func physicalSendGuardFromContext(ctx context.Context) (schemas.PhysicalSendGuard, bool) {
	if ctx == nil {
		return schemas.PhysicalSendGuard{}, false
	}
	if guarded, ok := ctx.(interface {
		PhysicalSendGuard() (schemas.PhysicalSendGuard, bool)
	}); ok {
		return guarded.PhysicalSendGuard()
	}
	bifrostCtx, ok := schemas.BifrostContextFromContext(ctx)
	if !ok {
		return schemas.PhysicalSendGuard{}, false
	}
	return bifrostCtx.PhysicalSendGuard()
}

func physicalSendGuardRequiredFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if guarded, ok := ctx.(interface{ PhysicalSendGuardRequired() bool }); ok {
		return guarded.PhysicalSendGuardRequired()
	}
	bifrostCtx, ok := schemas.BifrostContextFromContext(ctx)
	return ok && bifrostCtx.PhysicalSendGuardRequired()
}

func physicalSendAuthorizationExpired(guard schemas.PhysicalSendGuard) bool {
	return !guard.AuthorizationDeadline.IsZero() && !time.Now().Before(guard.AuthorizationDeadline)
}

func physicalSendBodyMatches(body []byte, expected string) bool {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]) == expected
}

type boundedRequestBuffer struct {
	bytes []byte
	limit int
}

func (b *boundedRequestBuffer) Write(value []byte) (int, error) {
	needed := len(b.bytes) + len(value)
	if needed > b.limit {
		return 0, ErrPhysicalSendRequestTooLarge
	}
	if needed > cap(b.bytes) {
		capacity := max(cap(b.bytes)*2, needed)
		if capacity > b.limit {
			capacity = b.limit
		}
		next := make([]byte, len(b.bytes), capacity)
		copy(next, b.bytes)
		zeroBytes(b.bytes)
		b.bytes = next
	}
	b.bytes = append(b.bytes, value...)
	return len(value), nil
}

func zeroBytes(value []byte) {
	clear(value)
}

func newPhysicalAttemptID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", NewPhysicalSendAuthorizationError(503, "ATTEMPT_ID_UNAVAILABLE", err)
	}
	return "bf-attempt-" + hex.EncodeToString(entropy[:]), nil
}

func physicalSendDestination(req *fasthttp.Request) (string, error) {
	parsed, err := url.Parse(string(req.URI().FullURI()))
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", NewPhysicalSendAuthorizationError(403, "INVALID_DESTINATION", err)
	}
	if parsed.RawQuery != "" {
		return "", NewPhysicalSendAuthorizationError(501, "UNSUPPORTED_DESTINATION_QUERY", nil)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", NewPhysicalSendAuthorizationError(501, "UNSUPPORTED_DESTINATION_SCHEME", nil)
	}
	if host := string(req.Header.Host()); host != "" && !strings.EqualFold(host, parsed.Host) {
		return "", NewPhysicalSendAuthorizationError(403, "DESTINATION_HOST_MISMATCH", nil)
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host) + parsed.EscapedPath(), nil
}

func stagePhysicalSendRequest(req *fasthttp.Request, limit int) (wire, body []byte, err error) {
	if req == nil || limit <= 0 || req.IsBodyStream() {
		return nil, nil, NewPhysicalSendAuthorizationError(501, "UNSUPPORTED_REQUEST_BODY", nil)
	}
	out := fasthttp.AcquireRequest()
	defer func() {
		zeroBytes(out.Body())
		out.Header.VisitAll(func(_, value []byte) { zeroBytes(value) })
		fasthttp.ReleaseRequest(out)
	}()
	req.CopyTo(out)
	initialCapacity := min(limit, len(out.Body())+(4*1024))
	buffer := boundedRequestBuffer{bytes: make([]byte, 0, initialCapacity), limit: limit}
	writer := bufio.NewWriterSize(&buffer, min(limit, 4*1024))
	serializeErr := out.Write(writer)
	if serializeErr == nil {
		serializeErr = writer.Flush()
	}
	writer.Reset(io.Discard)
	clear(writer.AvailableBuffer())
	if serializeErr != nil {
		zeroBytes(buffer.bytes)
		if errors.Is(serializeErr, ErrPhysicalSendRequestTooLarge) {
			return nil, nil, NewPhysicalSendAuthorizationError(413, "REQUEST_TOO_LARGE", serializeErr)
		}
		return nil, nil, NewPhysicalSendAuthorizationError(403, "REQUEST_SERIALIZATION_FAILED", serializeErr)
	}
	separator := bytes.Index(buffer.bytes, []byte("\r\n\r\n"))
	if separator < 0 {
		zeroBytes(buffer.bytes)
		return nil, nil, NewPhysicalSendAuthorizationError(403, "REQUEST_SERIALIZATION_FAILED", nil)
	}
	body = buffer.bytes[separator+4:]
	contentLength := out.Header.ContentLength()
	if out.Header.Peek("Transfer-Encoding") != nil || contentLength < 0 || int64(len(body)) != int64(contentLength) || !bytes.Equal(body, out.Body()) {
		zeroBytes(buffer.bytes)
		return nil, nil, NewPhysicalSendAuthorizationError(501, "UNSUPPORTED_BODY_FRAMING", nil)
	}
	return buffer.bytes, body, nil
}

func writeStagedPhysicalSendRequest(conn net.Conn, wire []byte) error {
	for len(wire) > 0 {
		written, err := conn.Write(wire)
		wire = wire[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func physicalSendAttempt(ctx context.Context, req *fasthttp.Request, body []byte) (schemas.PhysicalSendAttempt, error) {
	bifrostCtx, ok := schemas.BifrostContextFromContext(ctx)
	if !ok {
		return schemas.PhysicalSendAttempt{}, NewPhysicalSendAuthorizationError(403, "REQUEST_CONTEXT_UNAVAILABLE", nil)
	}
	route, ok := bifrostCtx.PhysicalSendRoute()
	if !ok || route.Provider == "" || route.Model == "" {
		return schemas.PhysicalSendAttempt{}, NewPhysicalSendAuthorizationError(403, "ROUTE_IDENTITY_UNAVAILABLE", nil)
	}
	destination, err := physicalSendDestination(req)
	if err != nil {
		return schemas.PhysicalSendAttempt{}, err
	}
	attemptID, err := newPhysicalAttemptID()
	if err != nil {
		return schemas.PhysicalSendAttempt{}, err
	}
	bodyDigest := sha256.Sum256(body)
	return schemas.PhysicalSendAttempt{
		AttemptID:   attemptID,
		Provider:    route.Provider,
		Model:       route.Model,
		Method:      string(req.Header.Method()),
		Destination: destination,
		Body:        body,
		BodySHA256:  hex.EncodeToString(bodyDigest[:]),
	}, nil
}
