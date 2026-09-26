package grant

// uniqueEntries accumulates tool patterns in the order they were first added, dropping repeats.
//
// The list and the record of what is already in it are one value, so no caller can hold a record
// that belongs to a different list. Keeping them apart is a silent bug rather than a loud one: a
// function accumulating two lists at once would go on collapsing entries against the wrong set
// and hand back a tool list that permits the wrong thing.
type uniqueEntries struct {
	entries []string
	seen    map[string]struct{}
}

// newUniqueEntries starts an empty accumulator sized for capacity entries.
func newUniqueEntries(capacity int) *uniqueEntries {
	return &uniqueEntries{
		entries: make([]string, 0, capacity),
		seen:    make(map[string]struct{}, capacity),
	}
}

// add appends entry unless it is already present.
func (u *uniqueEntries) add(entry string) {
	if _, dup := u.seen[entry]; dup {
		return
	}
	u.seen[entry] = struct{}{}
	u.entries = append(u.entries, entry)
}

// list returns what was accumulated, never nil: an empty tool list permits nothing, which a
// consumer has to be able to tell apart from nothing having been resolved at all.
func (u *uniqueEntries) list() []string {
	return u.entries
}
