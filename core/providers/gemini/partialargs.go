package gemini

import (
	"slices"
	"strconv"
	"strings"
)

// Streamed function call arguments (toolConfig.functionCallingConfig.streamFunctionCallArguments).
//
// With the flag set, Vertex streamGenerateContent on Gemini 3+ sends a function call as a run
// of parts instead of one part carrying the complete args:
//
//	{"functionCall":{"name":"record","id":"7EKB","partialArgs":[{"jsonPath":"$.count","numberValue":42.5}],"willContinue":true},"thoughtSignature":"..."}
//	{"functionCall":{"partialArgs":[{"jsonPath":"$.title","stringValue":"first words","willContinue":true}],"willContinue":true}}
//	{"functionCall":{"partialArgs":[{"jsonPath":"$.title","stringValue":""}],"willContinue":true}}
//	{"functionCall":{"partialArgs":[{"jsonPath":"$.tags[0]","stringValue":"a"}],"willContinue":true}}
//	{"functionCall":{"partialArgs":[{"jsonPath":"$.empty[]"},{"jsonPath":"$.meta.extra{}"}],"willContinue":true}}
//	{"functionCall":{}}
//
// The first part names the call (name, id, thoughtSignature); later parts carry only
// partialArgs; an empty functionCall ends it. Calls never interleave: a second call starts
// only after the first ended. Each partialArg is one JSON leaf addressed by a JSONPath, in
// the order the model generated the document. A string leaf may span many parts
// (willContinue on the partialArg) and ends with an empty stringValue. A path ending in
// "[]" or "{}" names a container: it is the only entry an empty container gets, and Vertex
// also sends it after the last child of a non-empty one. No part ever repeats the complete
// args, so the arguments exist only as the sum of the leaves.
//
// geminiPartialArgsWriter turns that leaf stream into the OpenAI-style argument text
// stream: every fragment it returns is appended to the previous ones, and the
// concatenation is the JSON object the leaves describe. It never revisits emitted text,
// so a fragment can be forwarded the moment its part arrives. It holds only the stack of
// open containers (bounded by the arguments' nesting depth); the argument text itself is
// not retained, which keeps the per-chunk cost to appending the fragment.

// geminiPathSegment is one step of a partialArg JSONPath: an object key or an array index.
type geminiPathSegment struct {
	key     string
	index   int
	isIndex bool
}

// geminiPartialArgsFrame is one open container in the argument document. frames[0] is the
// root object; frames[i] for i >= 1 was opened for path segment i-1.
type geminiPartialArgsFrame struct {
	segment  geminiPathSegment
	isArray  bool
	children int
	last     geminiPathSegment // the most recent member written into this container
	keys     []string          // object keys written into this container, to refuse duplicates
}

// hasMember reports whether segment was already written into this container. Array
// indices only grow in document order, so an index at or below the last one is a repeat.
func (f *geminiPartialArgsFrame) hasMember(segment geminiPathSegment) bool {
	if f.children == 0 {
		return false
	}
	if f.isArray {
		return segment.isIndex && segment.index <= f.last.index
	}
	return slices.Contains(f.keys, segment.key)
}

// geminiPartialArgsWriter incrementally serializes one streamed function call's arguments.
// The zero value is ready to use.
type geminiPartialArgsWriter struct {
	frames     []geminiPartialArgsFrame
	segments   []geminiPathSegment // scratch for parsing, reused across leaves
	stringOpen bool                // the last member of the innermost frame is a string still receiving text
}

// reset clears the writer for the next call, keeping its slices' capacity.
func (w *geminiPartialArgsWriter) reset() {
	w.frames = w.frames[:0]
	w.segments = w.segments[:0]
	w.stringOpen = false
}

// appendPartialArgs appends the argument text for args to dst and returns it. Entries
// whose path cannot be parsed, or that carry neither a value nor a container suffix, are
// skipped rather than guessed at.
func (w *geminiPartialArgsWriter) appendPartialArgs(dst []byte, args []*PartialArg) []byte {
	for _, arg := range args {
		if arg != nil {
			dst = w.appendPartialArg(dst, arg)
		}
	}
	return dst
}

func (w *geminiPartialArgsWriter) appendPartialArg(dst []byte, arg *PartialArg) []byte {
	path := arg.JSONPath
	containerArray, containerObject := strings.HasSuffix(path, "[]"), strings.HasSuffix(path, "{}")
	isContainer := containerArray || containerObject
	if isContainer {
		path = path[:len(path)-2]
	}
	hasValue := arg.StringValue != nil || len(arg.NumberValue) > 0 || arg.BoolValue != nil || len(arg.NullValue) > 0
	if !isContainer && !hasValue {
		return dst
	}
	segments, ok := parseGeminiJSONPath(path, w.segments[:0])
	w.segments = segments
	if !ok || len(segments) == 0 {
		return dst
	}
	parent, member := segments[:len(segments)-1], segments[len(segments)-1]

	// The common prefix between the open containers and the parent of this member. Leaves
	// arrive in document order, so everything below that prefix is finished.
	common := 0
	for common < len(parent) && common+1 < len(w.frames) && w.frames[common+1].segment == parent[common] {
		common++
	}
	inParent := common == len(parent) && len(w.frames) == len(parent)+1

	if w.stringOpen {
		if inParent && !isContainer && w.frames[len(w.frames)-1].last == member {
			if arg.StringValue != nil {
				dst = appendGeminiJSONStringContent(dst, *arg.StringValue)
				if arg.WillContinue == nil || !*arg.WillContinue {
					dst = append(dst, '"')
					w.stringOpen = false
				}
				return dst
			}
			// A non-string value for the open string's path: end the string, drop the value.
			dst = append(dst, '"')
			w.stringOpen = false
			return dst
		}
		dst = append(dst, '"')
		w.stringOpen = false
	}

	// A container that is already open (its children came first) needs nothing: its
	// closing bracket is written when the document moves past it.
	if isContainer && len(w.frames) > len(segments) {
		openHere := true
		for i, segment := range segments {
			if w.frames[i+1].segment != segment {
				openHere = false
				break
			}
		}
		if openHere {
			return dst
		}
	}

	// The first segment this entry would add to the deepest container still shared with it.
	// If that container already holds it, the entry repeats a finished member (a string
	// terminator, a late close marker) or re-enters a container the document has moved
	// past. Writing it again would duplicate a key, so it is dropped.
	if len(w.frames) > 0 {
		first := member
		if common < len(parent) {
			first = parent[common]
		}
		if w.frames[common].hasMember(first) {
			return dst
		}
	}

	if len(w.frames) == 0 {
		w.pushFrame(geminiPathSegment{}, false)
		dst = append(dst, '{')
	}
	for len(w.frames) > common+1 {
		dst = w.closeFrame(dst)
	}
	for i := common; i < len(parent); i++ {
		next := member
		if i+1 < len(parent) {
			next = parent[i+1]
		}
		dst = w.openMember(dst, parent[i])
		w.pushFrame(parent[i], next.isIndex)
		if next.isIndex {
			dst = append(dst, '[')
		} else {
			dst = append(dst, '{')
		}
	}

	dst = w.openMember(dst, member)
	switch {
	case isContainer:
		w.pushFrame(member, containerArray)
		if containerArray {
			dst = append(dst, '[')
		} else {
			dst = append(dst, '{')
		}
	case arg.StringValue != nil:
		dst = append(dst, '"')
		dst = appendGeminiJSONStringContent(dst, *arg.StringValue)
		if arg.WillContinue != nil && *arg.WillContinue {
			w.stringOpen = true
		} else {
			dst = append(dst, '"')
		}
	case len(arg.NumberValue) > 0:
		dst = append(dst, arg.NumberValue...)
	case arg.BoolValue != nil:
		dst = strconv.AppendBool(dst, *arg.BoolValue)
	default:
		dst = append(dst, "null"...)
	}
	return dst
}

// openMember writes the separator and, inside an object, the key for a new member of the
// innermost open container, and records it as that container's latest member.
func (w *geminiPartialArgsWriter) openMember(dst []byte, segment geminiPathSegment) []byte {
	top := &w.frames[len(w.frames)-1]
	if top.children > 0 {
		dst = append(dst, ',')
	}
	top.children++
	top.last = segment
	if !top.isArray {
		top.keys = append(top.keys, segment.key)
		dst = append(dst, '"')
		if segment.isIndex {
			dst = strconv.AppendInt(dst, int64(segment.index), 10)
		} else {
			dst = appendGeminiJSONStringContent(dst, segment.key)
		}
		dst = append(dst, '"', ':')
	}
	return dst
}

// pushFrame opens a container, reusing the key slice a previous frame at this depth left
// behind so a long stream does not reallocate it per container.
func (w *geminiPartialArgsWriter) pushFrame(segment geminiPathSegment, isArray bool) {
	var keys []string
	if n := len(w.frames); n < cap(w.frames) {
		keys = w.frames[:n+1][n].keys[:0]
	}
	w.frames = append(w.frames, geminiPartialArgsFrame{segment: segment, isArray: isArray, keys: keys})
}

func (w *geminiPartialArgsWriter) closeFrame(dst []byte) []byte {
	if w.frames[len(w.frames)-1].isArray {
		dst = append(dst, ']')
	} else {
		dst = append(dst, '}')
	}
	w.frames = w.frames[:len(w.frames)-1]
	return dst
}

// appendClose appends the text that completes the arguments: a pending closing quote and
// one bracket per open container, or "{}" for a call that streamed no leaf at all. The
// writer is reset afterwards.
func (w *geminiPartialArgsWriter) appendClose(dst []byte) []byte {
	if len(w.frames) == 0 {
		dst = append(dst, '{', '}')
	}
	if w.stringOpen {
		dst = append(dst, '"')
	}
	for len(w.frames) > 0 {
		dst = w.closeFrame(dst)
	}
	w.reset()
	return dst
}

// parseGeminiJSONPath splits a partialArg JSONPath ("$.a.b[0]['c.d']") into segments,
// appending to dst. Keys are substrings of path unless they contain escapes.
func parseGeminiJSONPath(path string, dst []geminiPathSegment) ([]geminiPathSegment, bool) {
	if !strings.HasPrefix(path, "$") {
		return dst, false
	}
	i := 1
	for i < len(path) {
		switch path[i] {
		case '.':
			j := i + 1
			for j < len(path) && path[j] != '.' && path[j] != '[' {
				j++
			}
			if j == i+1 {
				return dst, false
			}
			dst = append(dst, geminiPathSegment{key: path[i+1 : j]})
			i = j
		case '[':
			if i+1 >= len(path) {
				return dst, false
			}
			if quote := path[i+1]; quote == '\'' || quote == '"' {
				key, end, ok := parseGeminiQuotedPathKey(path, i+2, quote)
				if !ok || end+1 >= len(path) || path[end+1] != ']' {
					return dst, false
				}
				dst = append(dst, geminiPathSegment{key: key})
				i = end + 2
				continue
			}
			j := i + 1
			for j < len(path) && path[j] >= '0' && path[j] <= '9' {
				j++
			}
			if j == i+1 || j >= len(path) || path[j] != ']' {
				return dst, false
			}
			index, err := strconv.Atoi(path[i+1 : j])
			if err != nil {
				return dst, false
			}
			dst = append(dst, geminiPathSegment{index: index, isIndex: true})
			i = j + 1
		default:
			return dst, false
		}
	}
	return dst, true
}

// parseGeminiQuotedPathKey reads a bracket-quoted key starting at start, returning the key,
// the index of its closing quote, and whether it was well formed.
func parseGeminiQuotedPathKey(path string, start int, quote byte) (string, int, bool) {
	escaped := false
	for j := start; j < len(path); j++ {
		switch path[j] {
		case '\\':
			escaped = true
			j++
		case quote:
			if !escaped {
				return path[start:j], j, true
			}
			var b strings.Builder
			for k := start; k < j; k++ {
				if path[k] == '\\' && k+1 < j {
					k++
				}
				b.WriteByte(path[k])
			}
			return b.String(), j, true
		}
	}
	return "", 0, false
}

// appendGeminiJSONStringContent appends s JSON-escaped, without surrounding quotes. Unlike
// encoding/json it leaves <, > and & alone; the result is still valid JSON.
func appendGeminiJSONStringContent(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		dst = append(dst, s[start:i]...)
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
		}
		start = i + 1
	}
	return append(dst, s[start:]...)
}

// isStreamStart reports whether fc opens a streamed call: it names the function and
// either already carries partialArgs or announces more parts.
func (fc *FunctionCall) isStreamStart() bool {
	return fc.Name != "" && (fc.PartialArgs != nil || (fc.WillContinue != nil && *fc.WillContinue))
}

// isStreamContinuation reports whether fc carries more of the open streamed call. A bare
// empty functionCall is the end-of-call marker and counts as a continuation that ends it.
func (fc *FunctionCall) isStreamContinuation() bool {
	return fc.Name == "" && len(fc.Args) == 0
}

// streamEnds reports whether, after fc, the open streamed call is complete: the part does
// not announce more, and no string leaf in it is still open.
func (fc *FunctionCall) streamEnds() bool {
	if fc.WillContinue != nil && *fc.WillContinue {
		return false
	}
	for _, arg := range fc.PartialArgs {
		if arg != nil && arg.WillContinue != nil && *arg.WillContinue {
			return false
		}
	}
	return true
}

// endsStreamedCall reports whether part is content of its own (text, thought, media,
// code, a server tool part), which means a function call still being streamed is
// finished: the model writes its output in order, so it does not return to the call. A
// part carrying only a thoughtSignature or empty text is not content.
func (part *Part) endsStreamedCall() bool {
	return part.FunctionCall == nil && (part.Text != "" || part.InlineData != nil || part.FileData != nil ||
		part.ExecutableCode != nil || part.CodeExecutionResult != nil || part.FunctionResponse != nil ||
		part.ToolCall != nil || part.ToolResponse != nil)
}
