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
	root          map[string]any
	warnings      []string
	siblingsApply bool // 3.1+: keywords beside a $ref apply alongside the target
	nodes         int  // nodes produced by the current resolve call
}

func (r *resolver) warn(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

// resolve returns a deep copy of node with every local $ref expanded. A cycle
// is cut at the point of re-entry with a placeholder object (and a warning).
// Each call may produce at most maxResolvedNodes nodes: a target is copied at
// every use, so references shared down a chain multiply, and the budget turns
// that into an error the caller reports against the one operation.
func (r *resolver) resolve(node any) (any, error) {
	r.nodes = 0
	return r.resolveAt(node, nil, 0)
}

func (r *resolver) resolveAt(node any, stack []string, depth int) (any, error) {
	switch node.(type) {
	case map[string]any, []any:
		r.nodes++
		if r.nodes > maxResolvedNodes {
			return nil, fmt.Errorf("schema expands to more than %d nodes through shared $refs", maxResolvedNodes)
		}
	}
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
			if m, ok := resolved.(map[string]any); ok && len(v) > 1 {
				return r.mergeRefSiblings(m, v, stack, depth)
			}
			return resolved, nil
		}
		out := make(map[string]any, len(v))
		for k, val := range v {
			switch {
			case isInstanceDataKey(k):
				// Example and default data, enums and extensions are values, not
				// schema: a $ref key inside them is data and is left as it is.
				out[k] = val
				continue
			case k == "properties" || k == "patternProperties":
				// Keys here are property names (one may be "$ref"); each value is
				// a schema, so resolve the values, never the map itself.
				if props, ok := val.(map[string]any); ok {
					resolvedProps := make(map[string]any, len(props))
					for name, prop := range props {
						rp, err := r.resolveAt(prop, stack, depth)
						if err != nil {
							return nil, err
						}
						resolvedProps[name] = rp
					}
					out[k] = resolvedProps
					continue
				}
			}
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

// mergeRefSiblings applies the keywords written beside a $ref to its resolved
// target. From 3.1 on they all apply: properties and required are unioned with
// the target's and any other keyword overrides it. Before 3.1 siblings are
// ignored except summary and description, which override the target's.
func (r *resolver) mergeRefSiblings(target, ref map[string]any, stack []string, depth int) (any, error) {
	out := make(map[string]any, len(target)+len(ref))
	for k, val := range target {
		out[k] = val
	}
	for _, k := range sortedKeys(ref) {
		if k == "$ref" || (!r.siblingsApply && k != "description" && k != "summary") {
			continue
		}
		val, err := r.resolveAt(ref[k], stack, depth)
		if err != nil {
			return nil, err
		}
		switch k {
		case "properties":
			merged := map[string]any{}
			for name, prop := range asMap(out["properties"]) {
				merged[name] = prop
			}
			for name, prop := range asMap(val) {
				merged[name] = prop
			}
			out[k] = merged
		case "required":
			out[k] = unionStrings(asStringSlice(out["required"]), asStringSlice(val))
		default:
			out[k] = val
		}
	}
	return out, nil
}

// unionStrings returns the sorted, de-duplicated union of a and b.
func unionStrings(a, b []string) []any {
	set := map[string]any{}
	for _, s := range append(append([]string{}, a...), b...) {
		set[s] = true
	}
	return toAnySlice(sortedKeys(set))
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

// isInstanceDataKey reports whether a key holds instance data or extensions
// rather than a schema or other document structure.
func isInstanceDataKey(k string) bool {
	switch k {
	case "example", "examples", "default", "enum", "const":
		return true
	}
	return strings.HasPrefix(k, "x-")
}
