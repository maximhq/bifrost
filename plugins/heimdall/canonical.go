package heimdall

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// CanonicalArgs returns tool arguments in a form that compares equal when they mean the same thing:
// object keys sorted, number text kept as written, and any key named in ignore removed at every depth.
// Text that is not a single JSON value is returned trimmed.
func CanonicalArgs(raw string, ignore []string) string {
	trimmed := strings.TrimSpace(raw)
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return trimmed
	}
	if _, err := dec.Token(); err != io.EOF {
		return trimmed
	}
	if len(ignore) > 0 {
		set := make(map[string]struct{}, len(ignore))
		for _, k := range ignore {
			set[k] = struct{}{}
		}
		v = dropKeys(v, set)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return trimmed
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func dropKeys(v any, drop map[string]struct{}) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if _, ok := drop[k]; ok {
				delete(t, k)
				continue
			}
			t[k] = dropKeys(child, drop)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = dropKeys(child, drop)
		}
		return t
	default:
		return v
	}
}

// messageText returns the text of a message body. nonText is true when it holds anything but text,
// such as an image, audio or a file.
func messageText(c *schemas.ChatMessageContent) (text string, nonText bool) {
	if c == nil {
		return "", false
	}
	if c.ContentStr != nil {
		return *c.ContentStr, false
	}
	var b strings.Builder
	for _, block := range c.ContentBlocks {
		switch block.Type {
		case schemas.ChatContentBlockTypeText, schemas.ChatContentBlockTypeRefusal:
			if block.Text != nil {
				b.WriteString(*block.Text)
			} else if block.Refusal != nil {
				b.WriteString(*block.Refusal)
			}
		default:
			nonText = true
		}
	}
	return b.String(), nonText
}

type normalizedCall struct {
	ID   string `json:"i,omitempty"`
	Name string `json:"n"`
	Args string `json:"a"`
}

// normalizedMessage is what two logged copies of a message must share. Reasoning fields, indexes and
// provider extras are left out because clients drop or rewrite them.
type normalizedMessage struct {
	Role       string           `json:"r"`
	Name       string           `json:"n,omitempty"`
	Text       string           `json:"t,omitempty"`
	ToolCallID string           `json:"i,omitempty"`
	Calls      []normalizedCall `json:"c,omitempty"`
}

func normalizeMessage(m schemas.ChatMessage) normalizedMessage {
	n := normalizedMessage{Role: string(m.Role)}
	if m.Name != nil {
		n.Name = *m.Name
	}
	n.Text, _ = messageText(m.Content)
	if m.ChatToolMessage != nil && m.ToolCallID != nil {
		n.ToolCallID = *m.ToolCallID
	}
	if m.ChatAssistantMessage != nil {
		for _, tc := range m.ToolCalls {
			c := normalizedCall{Args: CanonicalArgs(tc.Function.Arguments, nil)}
			if tc.ID != nil {
				c.ID = *tc.ID
			}
			if tc.Function.Name != nil {
				c.Name = *tc.Function.Name
			}
			n.Calls = append(n.Calls, c)
		}
	}
	return n
}

// messageHash is the digest of a message's normalised form.
func messageHash(m schemas.ChatMessage) [32]byte {
	b, _ := json.Marshal(normalizeMessage(m))
	return sha256.Sum256(b)
}
