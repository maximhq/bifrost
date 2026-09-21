package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"
)

// errAdminUnavailable is every write tool's answer on a deployment whose
// transport supplied no AdminDispatcher.
var errAdminUnavailable = errors.New("this deployment cannot change configuration through tools: no admin dispatcher is configured")

// adminError is a refusal from an admin route: the status it answered with and
// the message in its error body. A tool returns it as is, so the caller reads
// the same sentence the dashboard would show.
type adminError struct {
	status  int
	message string
}

func (e *adminError) Error() string { return e.message }

// adminStatus reports the status behind err, or 0 when err did not come from
// an admin route.
func adminStatus(err error) int {
	var refused *adminError
	if errors.As(err, &refused) {
		return refused.status
	}
	return 0
}

// adminPath builds a route path from its template and the ids that fill it, in
// order. Each id is escaped as one path segment, so an id holding a separator
// or a traversal stays an id: the dispatcher and the router both match the
// escaped form.
func adminPath(template string, ids ...string) (string, error) {
	segments := strings.Split(template, "/")
	next := 0
	for i, segment := range segments {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		if next >= len(ids) {
			return "", fmt.Errorf("admin route %s needs %d ids, got %d", template, next+1, len(ids))
		}
		id := ids[next]
		// A dot segment survives escaping unchanged and means a traversal to
		// everything that reads a path.
		if id == "" || id == "." || id == ".." {
			return "", fmt.Errorf("%q is not a valid id", id)
		}
		segments[i] = url.PathEscape(id)
		next++
	}
	if next != len(ids) {
		return "", fmt.Errorf("admin route %s takes %d ids, got %d", template, next, len(ids))
	}
	return strings.Join(segments, "/"), nil
}

// adminBody is the request body a write tool sends. It is built field by field
// from the arguments the tool names, never copied from them wholesale: the
// admin handlers do not filter writes by tenant, so a field the tool did not
// decide on must not reach them.
type adminBody map[string]any

// adminBodyFrom starts a body from a row in the form the route returns it, for
// a route that takes the whole row: the tool then overlays what the caller
// changed. row must already be redacted - the body is the row's JSON.
func adminBodyFrom(row any) (adminBody, error) {
	encoded, err := json.Marshal(row)
	if err != nil {
		return nil, fmt.Errorf("could not encode the stored row: %w", err)
	}
	body := adminBody{}
	if err := json.Unmarshal(encoded, &body); err != nil {
		return nil, fmt.Errorf("could not encode the stored row: %w", err)
	}
	return body, nil
}

// set puts value under field.
func (b adminBody) set(field string, value any) adminBody {
	b[field] = value
	return b
}

// copyPresent carries each named argument across under the same name, when the
// caller sent it. Absent stays absent and null stays null, so "leave unchanged"
// and "clear" mean to the handler what they meant to the caller.
func (b adminBody) copyPresent(args map[string]any, names ...string) adminBody {
	for _, name := range names {
		if value, present := args[name]; present {
			b[name] = value
		}
	}
	return b
}

// copySet is copyPresent for a route that takes the whole row rather than a
// patch. There a null would decode to the zero value and clear the field, so a
// null argument is left out and the value already in the body stands.
func (b adminBody) copySet(args map[string]any, names ...string) adminBody {
	for _, name := range names {
		if value, present := args[name]; present && value != nil {
			b[name] = value
		}
	}
	return b
}

// rename carries one argument across under the name the route knows it by.
func (b adminBody) rename(args map[string]any, argument, field string) adminBody {
	if value, present := args[argument]; present {
		b[field] = value
	}
	return b
}

// callAdmin sends one admin request and returns the route's response body. A
// status of 400 or above comes back as an *adminError carrying the route's own
// message.
func callAdmin(ctx context.Context, deps *Deps, method, path string, body adminBody) ([]byte, error) {
	if deps == nil || deps.Admin == nil {
		return nil, errAdminUnavailable
	}
	var encoded []byte
	if body != nil {
		var err error
		// encoding/json, for its sorted map keys: the body is the same bytes
		// for the same arguments.
		if encoded, err = json.Marshal(map[string]any(body)); err != nil {
			return nil, fmt.Errorf("could not encode the request: %w", err)
		}
	}
	status, response, err := deps.Admin.Do(ctx, method, path, encoded)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, &adminError{status: status, message: adminErrorMessage(status, response)}
	}
	return response, nil
}

// undecodableBody is what the admin routes answer when the body does not
// decode into their request type. From a tool that can only mean an argument
// of the wrong type, which the route's own words do not say.
var undecodableBody = map[string]bool{
	"invalid json":            true,
	"invalid request payload": true,
	"invalid request format":  true,
	"invalid request body":    true,
}

// adminErrorMessage reads the message out of a route's error body, which is a
// BifrostError. A body in any other shape is reported by its status.
func adminErrorMessage(status int, response []byte) string {
	message := strings.TrimSpace(gjson.GetBytes(response, "error.message").String())
	if message == "" {
		return fmt.Sprintf("the admin API refused the change with status %d", status)
	}
	if undecodableBody[strings.ToLower(strings.TrimSuffix(message, "."))] {
		return "an argument has the wrong type; check each one against this tool's schema"
	}
	return message
}

// decodeAdmin reads a route's response into out.
func decodeAdmin(response []byte, out any) error {
	if err := sonic.Unmarshal(response, out); err != nil {
		return fmt.Errorf("the change was made, but its response could not be read: %w", err)
	}
	return nil
}

// decodeAdminField reads one field of a route's response into out, for the
// routes that wrap the row they changed: {"message": ..., "rule": {...}}.
func decodeAdminField(response []byte, field string, out any) error {
	value := gjson.GetBytes(response, field)
	if !value.Exists() {
		return fmt.Errorf("the change was made, but its response has no %q", field)
	}
	return decodeAdmin([]byte(value.Raw), out)
}
