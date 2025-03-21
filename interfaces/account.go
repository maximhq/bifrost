package interfaces

type Key struct {
	Value  string   `json:"value"`
	Models []string `json:"models"`
	Weight float64  `json:"weight"`
}

// TODO one get config method
type Account interface {
	GetInitiallyConfiguredProviders() ([]SupportedModelProvider, error)
	GetKeysForProvider(providerKey SupportedModelProvider) ([]Key, error)
	GetConfigForProvider(providerKey SupportedModelProvider) (*ProviderConfig, error)
}
