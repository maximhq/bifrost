//go:build !tinygo && !wasm

package starlark

import (
	"fmt"
	"math/big"

	"github.com/canonical/starlark/starlark"
	"github.com/canonical/starlark/starlarkstruct"
)

// Compare before formatting or copying an integer. Int.BigInt copies its
// backing words, so even asking BigInt().BitLen() would allocate too early.
var (
	maxResultInteger = starlark.MakeBigInt(new(big.Int).Lsh(big.NewInt(1), 4096))
	minResultInteger = starlark.MakeBigInt(new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 4096)))
)

// Each conversion bounds the expanded value, not just its unique objects:
// shared subtrees can otherwise expand exponentially when serialized as JSON.
// Conservative size charges include JSON escaping and pretty-print indentation.
type valueConversion struct {
	thread    *starlark.Thread
	remaining int
	maxDepth  int
}

func newValueConversion(thread *starlark.Thread) *valueConversion {
	limits := sandboxLimits(thread)
	return &valueConversion{thread: thread, remaining: limits.MaxValueBytes, maxDepth: limits.MaxNestingDepth}
}

func (c *valueConversion) reserve(n int) error {
	if n < 0 || n > c.remaining {
		return fmt.Errorf("code mode value exceeds size limit")
	}
	c.remaining -= n
	if err := c.thread.AddSteps(starlark.SafeInt(1)); err != nil {
		return err
	}
	return c.thread.AddAllocs(starlark.SafeInt(n))
}

// rawStringSize charges an inbound string at its own length. The JSON-escaping
// factor in stringSize applies when a value leaves Starlark, where toGo runs a
// fresh conversion that charges it again.
func (c *valueConversion) rawStringSize(s string) error {
	return c.reserve(len(s) + 2)
}

func (c *valueConversion) stringSize(s string) error {
	if len(s) > c.remaining/6 {
		return fmt.Errorf("code mode value exceeds size limit")
	}
	return c.reserve(6*len(s) + 2)
}

func (c *valueConversion) node(depth int) error {
	if depth > c.maxDepth {
		return fmt.Errorf("code mode value exceeds nesting limit or contains a cycle")
	}
	if err := c.reserve(32 + 4*depth); err != nil {
		return err
	}
	return c.thread.AddAllocs(starlark.SafeInt(128))
}

func (c *valueConversion) container(n int) error {
	if n > c.remaining/2 {
		return fmt.Errorf("code mode value exceeds size limit")
	}
	if err := c.reserve(n * 2); err != nil {
		return err
	}
	// Container backing storage counts against memory, independently of the
	// encoded-value budget. Charge before allocating slices or hash tables.
	return c.thread.AddAllocs(starlark.SafeMul(n, 128))
}

func (c *valueConversion) toGo(v starlark.Value, depth int) (interface{}, error) {
	if err := c.node(depth); err != nil {
		return nil, err
	}
	switch val := v.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(val), nil
	case starlark.Int:
		if i, ok := val.Int64(); ok {
			return i, nil
		}
		if i, ok := val.Uint64(); ok {
			return i, nil
		}
		upper, _ := val.Cmp(maxResultInteger, 1)
		lower, _ := val.Cmp(minResultInteger, 1)
		if upper >= 0 || lower <= 0 {
			return nil, fmt.Errorf("code mode integer exceeds size limit")
		}
		buf := starlark.NewSafeStringBuilder(c.thread)
		if err := val.SafeString(c.thread, buf); err != nil {
			return nil, err
		}
		if err := buf.Err(); err != nil {
			return nil, err
		}
		return buf.String(), c.stringSize(buf.String())
	case starlark.Float:
		return float64(val), nil
	case starlark.String:
		return string(val), c.stringSize(string(val))
	case starlark.Bytes:
		return string(val), c.stringSize(string(val))
	case *starlark.List:
		return c.sequence(val.Len(), val.Index, depth)
	case starlark.Tuple:
		return c.sequence(len(val), val.Index, depth)
	case *starlark.Dict:
		if err := c.container(val.Len()); err != nil {
			return nil, err
		}
		result := make(map[string]interface{}, val.Len())
		iter := val.Iterate()
		defer iter.Done()
		var key starlark.Value
		for iter.Next(&key) {
			name, err := c.key(key)
			if err != nil {
				return nil, err
			}
			item, _, err := val.SafeGet(c.thread, key)
			if err != nil {
				return nil, err
			}
			converted, err := c.toGo(item, depth+1)
			if err != nil {
				return nil, err
			}
			result[name] = converted
		}
		return result, iter.Err()
	case *starlarkstruct.Struct:
		// Only gateway-created server structs are exposed; their fields are
		// bounded by the binding setup budget. Values still require conversion.
		names := val.AttrNames()
		if err := c.container(len(names)); err != nil {
			return nil, err
		}
		result := make(map[string]interface{}, len(names))
		for _, name := range names {
			if err := c.stringSize(name); err != nil {
				return nil, err
			}
			item, err := val.SafeAttr(c.thread, name)
			if err != nil {
				return nil, err
			}
			converted, err := c.toGo(item, depth+1)
			if err != nil {
				return nil, err
			}
			result[name] = converted
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported code mode value type %s; return JSON-compatible values", v.Type())
	}
}

func (c *valueConversion) key(v starlark.Value) (string, error) {
	if s, ok := v.(starlark.String); ok {
		return string(s), c.stringSize(string(s))
	}
	// Never call String on arbitrary object graphs or host-defined values.
	switch v.(type) {
	case starlark.Int, starlark.Float, starlark.Bool, starlark.NoneType:
		if _, err := c.toGo(v, 0); err != nil {
			return "", err
		}
		name := v.String()
		return name, c.stringSize(name)
	default:
		return "", fmt.Errorf("code mode dictionary keys must be scalars")
	}
}

func (c *valueConversion) sequence(n int, index func(int) starlark.Value, depth int) (interface{}, error) {
	if err := c.container(n); err != nil {
		return nil, err
	}
	result := make([]interface{}, n)
	for i := range result {
		value, err := c.toGo(index(i), depth+1)
		if err != nil {
			return nil, err
		}
		result[i] = value
	}
	return result, nil
}

func (c *valueConversion) fromGo(v interface{}, depth int) (starlark.Value, error) {
	if err := c.node(depth); err != nil {
		return nil, err
	}
	switch val := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(val), nil
	case int:
		return starlark.MakeInt(val), nil
	case int64:
		return starlark.MakeInt64(val), nil
	case uint64:
		return starlark.MakeUint64(val), nil
	case float64:
		return starlark.Float(val), nil
	case string:
		return starlark.String(val), c.rawStringSize(val)
	case []interface{}:
		if err := c.container(len(val)); err != nil {
			return nil, err
		}
		items := make([]starlark.Value, len(val))
		for i, item := range val {
			converted, err := c.fromGo(item, depth+1)
			if err != nil {
				return nil, err
			}
			items[i] = converted
		}
		return starlark.NewList(items), nil
	case map[string]interface{}:
		if err := c.container(len(val)); err != nil {
			return nil, err
		}
		dict, err := starlark.SafeNewDict(c.thread, len(val))
		if err != nil {
			return nil, err
		}
		for k, v := range val {
			if err := c.rawStringSize(k); err != nil {
				return nil, err
			}
			converted, err := c.fromGo(v, depth+1)
			if err != nil {
				return nil, err
			}
			if err := dict.SafeSetKey(c.thread, starlark.String(k), converted); err != nil {
				return nil, err
			}
		}
		return dict, nil
	default:
		return nil, fmt.Errorf("unsupported code mode tool result type %T", v)
	}
}
