package runtime

import "bytes"

// vtStreamNormalizer preserves incomplete CSI sequences across PTY reads before
// applying the SGR compatibility rewrite that vt10x still needs.
type vtStreamNormalizer struct {
	pendingCSI []byte
}

func (n *vtStreamNormalizer) Normalize(data []byte) []byte {
	if len(n.pendingCSI) > 0 {
		combined := make([]byte, 0, len(n.pendingCSI)+len(data))
		combined = append(combined, n.pendingCSI...)
		combined = append(combined, data...)
		data = combined
		n.pendingCSI = nil
	}

	if len(data) == 0 {
		return nil
	}

	result := make([]byte, 0, len(data)+32)
	for i := 0; i < len(data); {
		if data[i] == 0x1b {
			if i+1 >= len(data) {
				n.pendingCSI = append(n.pendingCSI[:0], data[i:]...)
				break
			}
			if data[i+1] == '[' {
				start := i
				j := i + 2
				for j < len(data) && data[j] < 0x40 {
					j++
				}
				if j >= len(data) {
					n.pendingCSI = append(n.pendingCSI[:0], data[start:]...)
					break
				}
				// Drop CSI sequences that vt10x would misinterpret.
				// Sequences with intermediate bytes (>, <, =) are private-use
				// extensions (e.g. Kitty keyboard \x1b[>1u) that vt10x's CSI
				// parser misroutes. Also drop ?-prefixed sequences ending in
				// 'u' (\x1b[?u — Kitty keyboard query) which vt10x wrongly
				// dispatches as DECRC (cursor restore), corrupting cursor state.
				if shouldDropCSI(data[i+2:j], data[j]) {
					// silently drop — vt10x would misinterpret
				} else if data[j] == 'm' && bytes.ContainsRune(data[i+2:j], ':') {
					result = append(result, 0x1b, '[')
					result = append(result, rewriteSGRParams(data[i+2:j])...)
					result = append(result, 'm')
				} else {
					result = append(result, data[start:j+1]...)
				}
				i = j + 1
				continue
			}
		}

		result = append(result, data[i])
		i++
	}

	return result
}

// extractCursorShape scans data for the last DECSCUSR sequence (\x1b[N SP q)
// and returns the cursor shape value (0-6). Returns -1 if none found.
// DECSCUSR: 0=default, 1=blinking block, 2=steady block, 3=blinking underline,
// 4=steady underline, 5=blinking bar, 6=steady bar.
func extractCursorShape(data []byte) int32 {
	shape := int32(-1)
	for i := 0; i < len(data); i++ {
		if data[i] != 0x1b || i+1 >= len(data) || data[i+1] != '[' {
			continue
		}
		j := i + 2
		// Collect parameter + intermediate bytes (< 0x40)
		for j < len(data) && data[j] < 0x40 {
			j++
		}
		if j >= len(data) {
			break
		}
		params := data[i+2 : j]
		final := data[j]
		// DECSCUSR: CSI Ps SP q — params end with space (0x20), final is 'q'
		if final == 'q' && len(params) >= 2 && params[len(params)-1] == ' ' {
			// Parse the digit(s) before the space
			numPart := params[:len(params)-1]
			if len(numPart) == 1 && numPart[0] >= '0' && numPart[0] <= '6' {
				shape = int32(numPart[0] - '0')
			}
		}
		i = j
	}
	return shape
}

// extractCursorVisible scans data for the last cursor visibility toggle
// (\x1b[?25h or \x1b[?25l). Returns 1 for show, 0 for hide, -1 if none found.
func extractCursorVisible(data []byte) int32 {
	vis := int32(-1)
	for i := 0; i+5 < len(data); i++ {
		if data[i] != 0x1b || data[i+1] != '[' || data[i+2] != '?' ||
			data[i+3] != '2' || data[i+4] != '5' {
			continue
		}
		switch data[i+5] {
		case 'h':
			vis = 1
			i += 5
		case 'l':
			vis = 0
			i += 5
		}
	}
	return vis
}

// lastCursorShowIndex returns the byte index of the last \x1b[?25h in data,
// or -1 if not found. Used to split vt10x writes so we can capture cursor
// position at the exact moment the child shows the cursor.
func lastCursorShowIndex(data []byte) int {
	result := -1
	for i := 0; i+5 < len(data); i++ {
		if data[i] == 0x1b && data[i+1] == '[' && data[i+2] == '?' &&
			data[i+3] == '2' && data[i+4] == '5' && data[i+5] == 'h' {
			result = i
			i += 5
		}
	}
	return result
}

// shouldDropCSI decides whether a CSI sequence should be stripped before it
// reaches vt10x. Two categories are filtered:
//
//  1. Sequences with '>', '<', or '=' as the first parameter byte. These are
//     private-use extensions (Kitty keyboard protocol, DA2 responses, etc.)
//     that vt10x's CSI parser conflates with standard sequences.
//
//  2. '?'-prefixed sequences whose final byte is 'u'. The Kitty keyboard
//     query (\x1b[?u) would otherwise be dispatched as DECRC (cursor restore)
//     because vt10x's 'u' handler does not check the private flag.
//
// Regular '?'-prefixed sequences (\x1b[?1049h, \x1b[?25l, etc.) are NOT
// filtered — vt10x handles those correctly via its priv flag.
func shouldDropCSI(params []byte, finalByte byte) bool {
	if len(params) == 0 {
		return false
	}
	switch params[0] {
	case '>', '<', '=':
		return true
	case '?':
		return finalByte == 'u'
	}
	return false
}
