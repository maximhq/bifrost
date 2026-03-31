package runtime

import (
	"bytes"
	"strconv"
)

// rewriteSGRParams converts colon-separated SGR sub-parameters to semicolon-
// separated equivalents. Each semicolon-delimited group is processed:
//
//   - 4:x         -> 4          (underline style -> basic underline)
//   - 38:2:cs:r:g:b -> 38;2;r;g;b (fg true-color, drop colorspace)
//   - 48:2:cs:r:g:b -> 48;2;r;g;b (bg true-color, drop colorspace)
//   - 38:5:n      -> 38;5;n     (fg 256-color)
//   - 48:5:n      -> 48;5;n     (bg 256-color)
//   - 58:...      -> (dropped - underline color, unsupported by vt10x)
//   - other:x     -> other      (keep first sub-param only)
func rewriteSGRParams(params []byte) []byte {
	parts := bytes.Split(params, []byte{';'})
	var out [][]byte
	for _, part := range parts {
		if !bytes.ContainsRune(part, ':') {
			out = append(out, part)
			continue
		}
		subs := bytes.Split(part, []byte{':'})
		if len(subs) == 0 {
			continue
		}
		code, err := strconv.Atoi(string(subs[0]))
		if err != nil {
			out = append(out, subs[0])
			continue
		}
		switch code {
		case 4: // underline style -> basic underline
			out = append(out, []byte("4"))
		case 38, 48: // fg/bg color
			if len(subs) >= 2 {
				switch string(subs[1]) {
				case "2": // true-color: code:2[:cs]:r:g:b
					// Find r,g,b - skip optional colorspace id.
					if len(subs) >= 6 {
						// code:2:cs:r:g:b
						out = append(out, subs[0], []byte("2"), subs[3], subs[4], subs[5])
					} else if len(subs) >= 5 {
						// code:2:r:g:b (no colorspace)
						out = append(out, subs[0], []byte("2"), subs[2], subs[3], subs[4])
					} else {
						out = append(out, subs[0])
					}
				case "5": // 256-color: code:5:n
					if len(subs) >= 3 {
						out = append(out, subs[0], []byte("5"), subs[2])
					} else {
						out = append(out, subs[0])
					}
				default:
					out = append(out, subs[0])
				}
			} else {
				out = append(out, subs[0])
			}
		case 58: // underline color - not supported by vt10x, drop entirely
			continue
		default:
			out = append(out, subs[0])
		}
	}
	return bytes.Join(out, []byte{';'})
}
