package sagemaker

import (
	"io"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

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

// passthroughRoute is a parsed /endpoints/{endpoint}[/inference-components/{ic}]/{invocations|invocations-response-stream} path.
type passthroughRoute struct {
	id        modelID
	streaming bool
}

// eventStreamPayloadReader reads an InvokeEndpointWithResponseStream body and yields only the
// PayloadPart bytes, in order. A ModelStreamError or InternalStreamFailure ends the read with an error.
type eventStreamPayloadReader struct {
	src     io.Reader
	decoder *eventstream.Decoder
	pending []byte
}
