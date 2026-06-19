package utils

// ExtraParamsMixin is embedded into provider request types to provide the
// ExtraParams field plus GetExtraParams (satisfies RequestBodyWithExtraParams)
// and SetExtraParams (satisfies the integration router's
// RequestWithSettableExtraParams). The field keeps `json:"-"`, so providers must
// merge it into the outbound request body via GetExtraParams() rather than relying
// on direct serialization.
type ExtraParamsMixin struct {
	ExtraParams map[string]interface{} `json:"-"`
}

func (m *ExtraParamsMixin) GetExtraParams() map[string]interface{} {
	return m.ExtraParams
}

func (m *ExtraParamsMixin) SetExtraParams(params map[string]interface{}) {
	m.ExtraParams = params
}
