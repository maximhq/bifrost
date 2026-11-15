package livestore

// Collections holds the typed collection of each config store table the live store loads. New
// registers every one of them, so a reader takes them from Store.Collections.
type Collections struct{}

// New returns a store with a collection registered for each config store table. It loads
// nothing: LoadAll fills the collections from the database, or LoadRecords from config.json when
// there is none. A build that adds collections registers them on the returned store before either
// runs.
func New() (*Store, error) {
	s := newStore()
	cols, err := registerCollections(s)
	if err != nil {
		return nil, err
	}
	s.Collections = cols
	return s, nil
}

// registerCollections registers the collection of each config store table on s.
func registerCollections(s *Store) (*Collections, error) {
	return &Collections{}, nil
}
