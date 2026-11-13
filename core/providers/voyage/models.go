package voyage

import (
	"slices"
	"strings"
)

// voyageEndpoint is the Voyage API surface a model is served by.
type voyageEndpoint int

const (
	endpointEmbeddings voyageEndpoint = iota
	endpointContextualized
	endpointMultimodal
	endpointRerank
)

func (e voyageEndpoint) String() string {
	switch e {
	case endpointEmbeddings:
		return "text embedding"
	case endpointContextualized:
		return "contextualized embedding"
	case endpointMultimodal:
		return "multimodal embedding"
	case endpointRerank:
		return "rerank"
	}
	return "unknown"
}

// matryoshkaDimensions are the output_dimension values Voyage's flexible-dimension models accept.
var matryoshkaDimensions = []int{256, 512, 1024, 2048}

// fixedDimensions is the only output dimension of the fixed-size models.
var fixedDimensions = []int{1024}

// voyageModel is one named Voyage model and the request features it accepts.
type voyageModel struct {
	endpoint   voyageEndpoint
	dimensions []int // accepted output_dimension values; empty for rerankers
	quantized  bool  // accepts output_dtype other than float
	video      bool  // multimodal models only: accepts video parts
}

// voyageModels lists every model this provider serves, keyed by the exact name sent to Voyage.
// A model outside this table is rejected before any request is made: there is no default model
// and no fallback to a nearby one.
var voyageModels = map[string]voyageModel{
	"voyage-4-large":   {endpoint: endpointEmbeddings, dimensions: matryoshkaDimensions, quantized: true},
	"voyage-4":         {endpoint: endpointEmbeddings, dimensions: matryoshkaDimensions, quantized: true},
	"voyage-4-lite":    {endpoint: endpointEmbeddings, dimensions: matryoshkaDimensions, quantized: true},
	"voyage-code-4":    {endpoint: endpointEmbeddings, dimensions: matryoshkaDimensions, quantized: true},
	"voyage-3-large":   {endpoint: endpointEmbeddings, dimensions: matryoshkaDimensions, quantized: true},
	"voyage-3.5":       {endpoint: endpointEmbeddings, dimensions: matryoshkaDimensions, quantized: true},
	"voyage-3.5-lite":  {endpoint: endpointEmbeddings, dimensions: matryoshkaDimensions, quantized: true},
	"voyage-code-3":    {endpoint: endpointEmbeddings, dimensions: matryoshkaDimensions, quantized: true},
	"voyage-finance-2": {endpoint: endpointEmbeddings, dimensions: fixedDimensions},
	"voyage-law-2":     {endpoint: endpointEmbeddings, dimensions: fixedDimensions},

	"voyage-context-4": {endpoint: endpointContextualized, dimensions: matryoshkaDimensions},
	"voyage-context-3": {endpoint: endpointContextualized, dimensions: matryoshkaDimensions},

	"voyage-multimodal-3.5": {endpoint: endpointMultimodal, dimensions: matryoshkaDimensions, video: true},
	"voyage-multimodal-3":   {endpoint: endpointMultimodal, dimensions: fixedDimensions},

	"rerank-3":        {endpoint: endpointRerank},
	"rerank-3-lite":   {endpoint: endpointRerank},
	"rerank-2.5":      {endpoint: endpointRerank},
	"rerank-2.5-lite": {endpoint: endpointRerank},
}

// lookupModel resolves a model name against the table. Names are matched exactly (after trimming
// surrounding spaces): Voyage model names are case-sensitive.
func lookupModel(name string) (string, voyageModel, bool) {
	name = strings.TrimSpace(name)
	m, ok := voyageModels[name]
	return name, m, ok
}

// acceptsDimension reports whether output_dimension d is valid for the model.
func (m voyageModel) acceptsDimension(d int) bool {
	return slices.Contains(m.dimensions, d)
}
