package grant

import "strings"

// Wildcard marks an unrestricted list: every value is allowed, or every value is blocked, depending
// on which kind of list carries it.
const Wildcard = "*"

// The lists a permit carries have the semantics of the configuration lists they are built from. A
// list holding only the wildcard is unrestricted, an empty list holds nothing, and membership
// ignores case. Key IDs are the one exception, matched exactly by whoever selects a key; nothing
// here is used for them.

// listIsUnrestricted reports whether list holds only the wildcard.
func listIsUnrestricted(list []string) bool {
	return len(list) == 1 && list[0] == Wildcard
}

// listContains reports whether list names value, ignoring case.
func listContains(list []string, value string) bool {
	for _, entry := range list {
		if strings.EqualFold(entry, value) {
			return true
		}
	}
	return false
}

// listAllows reads list as an allow list: everything when it is unrestricted, otherwise what it
// names. An empty allow list permits nothing.
func listAllows(list []string, value string) bool {
	return listIsUnrestricted(list) || listContains(list, value)
}

// listBlocks reads list as a block list: everything when it holds the wildcard, otherwise what it
// names. An empty block list refuses nothing.
func listBlocks(list []string, value string) bool {
	return listIsUnrestricted(list) || listContains(list, value)
}
