package logging

import (
	"math"
	"reflect"

	"github.com/bytedance/sonic"
)

// stripUnserializablePayloads walks any value with reflection and zeroes out
// only the nested values that fail JSON serialization, preserving the rest.
// Subtrees that marshal cleanly are skipped whole; a value that still fails
// after sanitization (e.g. broken unexported state) is zeroed entirely by its
// parent. Pass a pointer (or map/slice) so repairs are visible to the caller;
// a plain struct value cannot be mutated through reflection.
func stripUnserializablePayloads(v any) {
	if v == nil || marshals(v) {
		return
	}
	sanitize(reflect.ValueOf(v), make(map[uintptr]bool), 0)
}

// maxSanitizeDepth bounds recursion on pathologically nested payloads.
const maxSanitizeDepth = 64

// sanitize recursively zeroes out values that fail JSON marshaling. Subtrees
// that marshal cleanly are skipped whole, so the walk cost is bounded by the
// broken paths rather than the payload size. Values that cannot be repaired
// in place are zeroed by the parent via the post-recursion marshal check.
func sanitize(v reflect.Value, visited map[uintptr]bool, depth int) {
	if depth > maxSanitizeDepth || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() || marshals(v.Interface()) {
			return
		}
		// Interface contents are not addressable; sanitize a copy and
		// write it back.
		inner := v.Elem()
		tmp := reflect.New(inner.Type()).Elem()
		tmp.Set(inner)
		sanitize(tmp, visited, depth+1)
		if !v.CanSet() {
			return
		}
		if marshals(tmp.Interface()) {
			v.Set(tmp)
		} else {
			v.Set(reflect.Zero(v.Type()))
		}
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		ptr := v.Pointer()
		if visited[ptr] {
			// Reference cycle: break it by nilling the back-edge.
			if v.CanSet() {
				v.Set(reflect.Zero(v.Type()))
			}
			return
		}
		visited[ptr] = true
		defer delete(visited, ptr)
		if v.CanInterface() && marshals(v.Interface()) {
			return
		}
		sanitize(v.Elem(), visited, depth+1)
		if v.CanSet() && v.CanInterface() && !marshals(v.Interface()) {
			v.Set(reflect.Zero(v.Type()))
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		for _, k := range v.MapKeys() {
			mv := v.MapIndex(k)
			if !mv.CanInterface() || marshals(mv.Interface()) {
				continue
			}
			// Map values are not addressable; sanitize a copy and
			// store it back.
			tmp := reflect.New(mv.Type()).Elem()
			tmp.Set(mv)
			sanitize(tmp, visited, depth+1)
			if marshals(tmp.Interface()) {
				v.SetMapIndex(k, tmp)
			} else {
				v.SetMapIndex(k, reflect.Zero(mv.Type()))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			ev := v.Index(i)
			if !ev.CanInterface() || marshals(ev.Interface()) {
				continue
			}
			sanitize(ev, visited, depth+1)
			if ev.CanSet() && ev.CanInterface() && !marshals(ev.Interface()) {
				ev.Set(reflect.Zero(ev.Type()))
			}
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if !t.Field(i).IsExported() {
				// JSON marshaling ignores unexported fields.
				continue
			}
			fv := v.Field(i)
			if !fv.CanInterface() || marshals(fv.Interface()) {
				continue
			}
			sanitize(fv, visited, depth+1)
			if fv.CanSet() && fv.CanInterface() && !marshals(fv.Interface()) {
				fv.Set(reflect.Zero(fv.Type()))
			}
		}
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if v.CanSet() && (math.IsNaN(f) || math.IsInf(f, 0)) {
			v.SetFloat(0)
		}
	case reflect.Chan, reflect.Func, reflect.UnsafePointer,
		reflect.Complex64, reflect.Complex128:
		// Never JSON-serializable; the parent zeroes these via its
		// post-recursion marshal check.
	}
}

// marshals reports whether v serializes cleanly to JSON; nil values trivially do.
func marshals(v any) bool {
	if v == nil {
		return true
	}
	_, err := sonic.Marshal(v)
	return err == nil
}
