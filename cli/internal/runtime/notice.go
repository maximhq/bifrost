package runtime

import "github.com/maximhq/vt10x"

// TabNoticeLevel controls how transient tab bar notices are styled.
type TabNoticeLevel int

const (
	TabNoticeInfo TabNoticeLevel = iota
	TabNoticeError
)

const hostTrackedVTModeMask = vt10x.ModeMouseMask | vt10x.ModeMouseSgr | vt10x.ModeFocus
