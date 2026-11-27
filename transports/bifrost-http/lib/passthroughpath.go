package lib

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// validatePassthroughPath rejects passthrough remainders that could change how the upstream
// URL is interpreted once concatenated onto a provider base URL. The path must be rooted by
// exactly one "/", must not carry a userinfo separator in its first segment, and must not
// contain a scheme separator, backslashes, dot-dot segments, or control characters anywhere.
// The same rules are applied to the percent-decoded form so an encoded variant cannot slip
// through. Later segments may contain "@" because Vertex model versions use it
// ("models/claude-sonnet-4-5@20250929"); past the first segment it can no longer reach
// the authority, and the provider-side host check covers the remaining cases.
func ValidatePassthroughPath(p string) error {
	if err := checkPassthroughPathRules(p); err != nil {
		return err
	}
	decoded, err := url.PathUnescape(p)
	if err != nil {
		return fmt.Errorf("invalid passthrough path: %w", err)
	}
	if decoded != p {
		return checkPassthroughPathRules(decoded)
	}
	return nil
}

func checkPassthroughPathRules(p string) error {
	switch {
	case !strings.HasPrefix(p, "/"):
		return errors.New("invalid passthrough path: must start with /")
	case strings.HasPrefix(p, "//"):
		return errors.New("invalid passthrough path: must not start with //")
	case strings.Contains(p, "\\"):
		return errors.New("invalid passthrough path: must not contain backslashes")
	case strings.Contains(p, "://"):
		return errors.New("invalid passthrough path: must not contain a scheme")
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] == 0x7f {
			return errors.New("invalid passthrough path: must not contain control characters")
		}
	}
	first, _, _ := strings.Cut(p[1:], "/")
	if strings.Contains(first, "@") {
		return errors.New("invalid passthrough path: first segment must not contain @")
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." {
			return errors.New("invalid passthrough path: must not contain .. segments")
		}
	}
	return nil
}
