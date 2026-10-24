package sagemaker

// modelID is a parsed sagemaker model id: [region/]endpoint[/inference-component].
type modelID struct {
	region   string
	endpoint string
	ic       string
}

type sageMakerListEndpointsRequest struct {
	MaxResults   int    `json:"MaxResults"`
	NextToken    string `json:"NextToken,omitempty"`
	StatusEquals string `json:"StatusEquals"`
}

type sageMakerListEndpointsResponse struct {
	Endpoints []struct {
		EndpointName string `json:"EndpointName"`
	} `json:"Endpoints"`
	NextToken string `json:"NextToken"`
}
