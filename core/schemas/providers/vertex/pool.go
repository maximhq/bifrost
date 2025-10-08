package vertex

import (
	"sync"
)

// Pool capacity limits to prevent memory leaks from overly large slices
const (
	maxSliceCapacity = 64   // Max capacity for slices before discarding
	maxEmbeddingSize = 4096 // Max embedding vector size before discarding
)

// ==================== SLICE POOLS ====================

// Pool for VertexEmbeddingInstance slices
var vertexInstancesPool = sync.Pool{
	New: func() interface{} {
		s := make([]VertexEmbeddingInstance, 0, 8) // Most requests have 1-8 instances
		return &s
	},
}

// Pool for VertexEmbeddingPrediction slices
var vertexPredictionsPool = sync.Pool{
	New: func() interface{} {
		s := make([]VertexEmbeddingPrediction, 0, 8) // Most responses have 1-8 predictions
		return &s
	},
}

// Pool for float64 slices (embedding values) - WORTH IT: Large vectors ~12KB each
var vertexFloat64Pool = sync.Pool{
	New: func() interface{} {
		s := make([]float64, 0, 1536) // Common embedding size: 1536 * 8 bytes = ~12KB
		return &s
	},
}

// Pool for float32 slices (converted embeddings) - WORTH IT: Large vectors ~6KB each
var vertexFloat32Pool = sync.Pool{
	New: func() interface{} {
		s := make([]float32, 0, 1536) // Common embedding size: 1536 * 4 bytes = ~6KB
		return &s
	},
}

// ==================== STRUCT POOLS ====================

// vertexEmbeddingRequestPool provides a pool for Vertex embedding request objects.
var vertexEmbeddingRequestPool = sync.Pool{
	New: func() interface{} {
		return &VertexEmbeddingRequest{}
	},
}

// vertexEmbeddingResponsePool provides a pool for Vertex embedding response objects.
var vertexEmbeddingResponsePool = sync.Pool{
	New: func() interface{} {
		return &VertexEmbeddingResponse{}
	},
}

// Pool for VertexEmbeddingParameters objects
var vertexParametersPool = sync.Pool{
	New: func() interface{} {
		return &VertexEmbeddingParameters{}
	},
}

// Pool for VertexEmbeddingStatistics objects
var vertexStatisticsPool = sync.Pool{
	New: func() interface{} {
		return &VertexEmbeddingStatistics{}
	},
}

// Pool for VertexEmbeddingValues objects
var vertexValuesPool = sync.Pool{
	New: func() interface{} {
		return &VertexEmbeddingValues{}
	},
}

// ==================== SLICE HELPERS ====================

// acquireVertexInstances gets a VertexEmbeddingInstance slice from the pool.
func acquireVertexInstances() []VertexEmbeddingInstance {
	instances := *vertexInstancesPool.Get().(*[]VertexEmbeddingInstance)
	return instances[:0] // Reset length, keep capacity
}

// releaseVertexInstances returns a VertexEmbeddingInstance slice to the pool.
func releaseVertexInstances(instances []VertexEmbeddingInstance) {
	if cap(instances) <= maxSliceCapacity {
		// Clear instances to prevent memory leaks
		for i := 0; i < len(instances); i++ {
			instances[i] = VertexEmbeddingInstance{} // Reset to zero value
		}
		vertexInstancesPool.Put(&instances)
	}
}

// acquireVertexPredictions gets a VertexEmbeddingPrediction slice from the pool.
func acquireVertexPredictions() []VertexEmbeddingPrediction {
	predictions := *vertexPredictionsPool.Get().(*[]VertexEmbeddingPrediction)
	return predictions[:0] // Reset length, keep capacity
}

// releaseVertexPredictions returns a VertexEmbeddingPrediction slice to the pool.
func releaseVertexPredictions(predictions []VertexEmbeddingPrediction) {
	if cap(predictions) <= maxSliceCapacity {
		// Clear nested objects
		for i := 0; i < len(predictions); i++ {
			if predictions[i].Embeddings != nil {
				releaseVertexValues(predictions[i].Embeddings)
			}
			predictions[i] = VertexEmbeddingPrediction{} // Reset to zero value
		}
		vertexPredictionsPool.Put(&predictions)
	}
}

// acquireVertexFloat64Slice gets a float64 slice from the pool.
func acquireVertexFloat64Slice() []float64 {
	values := *vertexFloat64Pool.Get().(*[]float64)
	return values[:0] // Reset length, keep capacity
}

// releaseVertexFloat64Slice returns a float64 slice to the pool.
func releaseVertexFloat64Slice(values []float64) {
	if cap(values) <= maxEmbeddingSize {
		// Clear values
		for i := 0; i < len(values); i++ {
			values[i] = 0.0
		}
		vertexFloat64Pool.Put(&values)
	}
}

// acquireVertexFloat32Slice gets a float32 slice from the pool.
func acquireVertexFloat32Slice() []float32 {
	values := *vertexFloat32Pool.Get().(*[]float32)
	return values[:0] // Reset length, keep capacity
}

// releaseVertexFloat32Slice returns a float32 slice to the pool.
func releaseVertexFloat32Slice(values []float32) {
	if cap(values) <= maxEmbeddingSize {
		// Clear values
		for i := 0; i < len(values); i++ {
			values[i] = 0.0
		}
		vertexFloat32Pool.Put(&values)
	}
}

// ==================== PUBLIC ACQUIRE/RELEASE ====================

// AcquireEmbeddingRequest gets an embedding request from the pool and resets it.
func AcquireEmbeddingRequest() *VertexEmbeddingRequest {
	req := vertexEmbeddingRequestPool.Get().(*VertexEmbeddingRequest)

	// Reset all fields
	req.Instances = nil
	req.Parameters = nil

	return req
}

// ReleaseEmbeddingRequest returns an embedding request to the pool.
func ReleaseEmbeddingRequest(req *VertexEmbeddingRequest) {
	if req == nil {
		return
	}

	// Release nested objects first
	if req.Instances != nil {
		releaseVertexInstances(req.Instances)
	}

	if req.Parameters != nil {
		releaseVertexParameters(req.Parameters)
	}

	vertexEmbeddingRequestPool.Put(req)
}

// AcquireEmbeddingResponse gets an embedding response from the pool and resets it.
func AcquireEmbeddingResponse() *VertexEmbeddingResponse {
	resp := vertexEmbeddingResponsePool.Get().(*VertexEmbeddingResponse)

	// Reset all fields
	resp.Predictions = nil

	return resp
}

// ReleaseEmbeddingResponse returns an embedding response to the pool.
func ReleaseEmbeddingResponse(resp *VertexEmbeddingResponse) {
	if resp == nil {
		return
	}

	// Release nested objects first
	if resp.Predictions != nil {
		releaseVertexPredictions(resp.Predictions)
	}

	vertexEmbeddingResponsePool.Put(resp)
}

// ==================== NESTED OBJECT POOLS ====================

// acquireVertexParameters gets a VertexEmbeddingParameters from the pool and resets it.
func acquireVertexParameters() *VertexEmbeddingParameters {
	params := vertexParametersPool.Get().(*VertexEmbeddingParameters)

	// Reset fields
	params.AutoTruncate = nil
	params.OutputDimensionality = nil

	return params
}

// releaseVertexParameters returns a VertexEmbeddingParameters to the pool.
func releaseVertexParameters(params *VertexEmbeddingParameters) {
	if params != nil {
		vertexParametersPool.Put(params)
	}
}

// acquireVertexStatistics gets a VertexEmbeddingStatistics from the pool and resets it.
func acquireVertexStatistics() *VertexEmbeddingStatistics {
	stats := vertexStatisticsPool.Get().(*VertexEmbeddingStatistics)

	// Reset fields
	stats.Truncated = false
	stats.TokenCount = 0

	return stats
}

// releaseVertexStatistics returns a VertexEmbeddingStatistics to the pool.
func releaseVertexStatistics(stats *VertexEmbeddingStatistics) {
	if stats != nil {
		vertexStatisticsPool.Put(stats)
	}
}

// acquireVertexValues gets a VertexEmbeddingValues from the pool and resets it.
func acquireVertexValues() *VertexEmbeddingValues {
	values := vertexValuesPool.Get().(*VertexEmbeddingValues)

	// Reset fields
	values.Values = nil
	values.Statistics = nil

	return values
}

// releaseVertexValues returns a VertexEmbeddingValues to the pool.
func releaseVertexValues(values *VertexEmbeddingValues) {
	if values == nil {
		return
	}

	// Release nested objects
	if values.Values != nil {
		releaseVertexFloat64Slice(values.Values)
	}

	if values.Statistics != nil {
		releaseVertexStatistics(values.Statistics)
	}

	vertexValuesPool.Put(values)
}
