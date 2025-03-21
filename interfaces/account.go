package interfaces

type ConcurrencyAndBufferSize struct {
	Concurrency int `json:"concurrency"`
	BufferSize  int `json:"buffer_size"`
}

type Key struct {
	Value  string   `json:"value"`
	Models []string `json:"models"`
	Weight float64  `json:"weight"`
}

type Account interface {
	GetInitiallyConfiguredProviderKeys() ([]SupportedModelProvider, error)
	GetKeysForProvider(provider Provider) ([]Key, error)
	GetConcurrencyAndBufferSizeForProvider(provider Provider) (*ConcurrencyAndBufferSize, error)
}
