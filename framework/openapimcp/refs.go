package openapimcp

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// errExternalRef marks a $ref that points outside the document. The operation
// using it is reported as unsupported rather than failing the whole spec.
var errExternalRef = errors.New("external $ref is not supported")

// resolver expands local JSON-pointer $refs against the decoded document.
// Nodes are resolved at their point of use (path items, parameters, bodies,
// security schemes), never the whole document, so untouched components cost
// nothing and shared schemas are copied only where an operation needs them.
type resolver struct {
	root     map[string]any
	warnings []string
}

func (r *resolver) warn(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

// resolve returns a deep copy of node with every local $ref expanded. A cycle
// is cut at the point of re-entry with a placeholder object (and a warning).
func (r *resolver) resolve(node any) (any, error) {
	return r.resolveAt(node, nil, 0)
}

func (r *resolver) resolveAt(node any, stack []string, depth int) (any, error) {
	switch v := node.(type) {
	case map[string]any:
		if refRaw, ok := v["$ref"]; ok {
			ref, _ := refRaw.(string)
			if !strings.HasPrefix(ref, "#") {
				return nil, fmt.Errorf("%w: %q", errExternalRef, ref)
			}
			for _, seen := range stack {
				if seen == ref {
					r.warn("recursive $ref %s cut at its point of re-entry", ref)
					return map[string]any{"type": "object", "description": "recursive reference to " + ref}, nil
				}
			}
			if depth >= maxRefDepth {
				return nil, fmt.Errorf("$ref chain deeper than %d at %s", maxRefDepth, ref)
			}
			target, err := r.lookup(ref)
			if err != nil {
				return nil, err
			}
			resolved, err := r.resolveAt(target, append(stack, ref), depth+1)
			if err != nil {
				return nil, err
			}
			// 3.1 allows summary/description next to a $ref; they override the target's.
			if m, ok := resolved.(map[string]any); ok && len(v) > 1 {
				out := make(map[string]any, len(m)+2)
				for k, val := range m {
					out[k] = val
				}
				for k, val := range v {
					if k == "description" || k == "summary" {
						out[k] = val
					}
				}
				return out, nil
			}
			return resolved, nil
		}
		out := make(map[string]any, len(v))
		for k, val := range v {
			rv, err := r.resolveAt(val, stack, depth)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			rv, err := r.resolveAt(val, stack, depth)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		return v, nil
	}
}

// resolveShallow follows only the node's own $ref (chasing ref-to-ref chains)
// and leaves nested references in place for later, per-use resolution.
func (r *resolver) resolveShallow(node any) (any, error) {
	for depth := 0; ; depth++ {
		m, ok := node.(map[string]any)
		if !ok {
			return node, nil
		}
		refRaw, has := m["$ref"]
		if !has {
			return node, nil
		}
		ref, _ := refRaw.(string)
		if !strings.HasPrefix(ref, "#") {
			return nil, fmt.Errorf("%w: %q", errExternalRef, ref)
		}
		if depth >= maxRefDepth {
			return nil, fmt.Errorf("$ref chain deeper than %d at %s", maxRefDepth, ref)
		}
		target, err := r.lookup(ref)
		if err != nil {
			return nil, err
		}
		node = target
	}
}

// lookup follows a "#/a/b/c" JSON pointer through the document.
func (r *resolver) lookup(ref string) (any, error) {
	pointer := strings.TrimPrefix(ref, "#")
	if pointer == "" {
		return r.root, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("unsupported $ref %q (expected a JSON pointer)", ref)
	}
	var cur any = r.root
	for _, seg := range strings.Split(pointer[1:], "/") {
		if unescaped, err := url.PathUnescape(seg); err == nil {
			seg = unescaped
		}
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		switch c := cur.(type) {
		case map[string]any:
			next, ok := c[seg]
			if !ok {
				return nil, fmt.Errorf("$ref %q does not resolve: missing %q", ref, seg)
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(c) {
				return nil, fmt.Errorf("$ref %q does not resolve: bad index %q", ref, seg)
			}
			cur = c[idx]
		default:
			return nil, fmt.Errorf("$ref %q does not resolve: %q is not a container", ref, seg)
		}
	}
	return cur, nil
}
