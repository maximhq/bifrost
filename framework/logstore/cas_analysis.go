package logstore

import "fmt"

// CASAnalysisObject describes one encoded object produced by the production
// CAS codec. It exposes only identity and aggregate size; encoded bytes remain
// private to the analysis value used for immediate reconstruction.
type CASAnalysisObject struct {
	Hash            string
	CompressedBytes int64
	Manifest        bool
}

// CASAnalysisReference identifies one production manifest-to-segment edge.
// Offline tooling uses it only as an in-memory deduplication key.
type CASAnalysisReference struct {
	OwnerHash  string
	TargetHash string
}

// CASFieldAnalysis is the production CAS encoding of one serialized payload
// field. Reconstruct verifies codec, length, digest and byte-exact recovery.
type CASFieldAnalysis struct {
	Objects    []CASAnalysisObject
	References []CASAnalysisReference
	Fallback   bool
	RawBytes   int64

	manifestHash string
	blobs        map[string]casBlob
}

// PayloadColumnsForAnalysis returns every serialized payload column. Callers
// use this list for the baseline and row-resident byte totals.
func PayloadColumnsForAnalysis() []string {
	return append([]string(nil), payloadFields...)
}

// CASPayloadColumnsForAnalysis returns the payload columns eligible for CAS
// under the production defaults. Pricing metadata remains row-resident.
func CASPayloadColumnsForAnalysis() []string {
	out := make([]string, 0, len(payloadFields)-2)
	for _, field := range payloadFields {
		if field == "token_usage" || field == "cache_debug" {
			continue
		}
		out = append(out, field)
	}
	return out
}

// CompressFieldForAnalysis applies the same zstd codec and level used by CAS.
func CompressFieldForAnalysis(raw []byte) int64 {
	return int64(len(casEncoder.EncodeAll(raw, nil)))
}

// AnalyzeCASField encodes raw using the production manifest, chunking, hash and
// compression implementation. It has no database side effects.
func AnalyzeCASField(raw []byte, minChunk int) (*CASFieldAnalysis, error) {
	if minChunk <= 0 {
		return nil, fmt.Errorf("logstore/cas analysis: min chunk must be positive")
	}
	manifest, segments, manifestBytes, fallback, err := buildManifest(raw, minChunk)
	if err != nil {
		return nil, err
	}
	analysis := &CASFieldAnalysis{
		Fallback: fallback,
		RawBytes: int64(len(raw)),
		blobs:    make(map[string]casBlob, len(segments)+1),
	}
	for _, part := range manifest.Parts {
		if part.Hash != "" {
			analysis.References = append(analysis.References, CASAnalysisReference{
				TargetHash: part.Hash,
			})
		}
	}
	for _, blob := range segments {
		analysis.blobs[blob.Hash] = blob
		analysis.Objects = append(analysis.Objects, CASAnalysisObject{
			Hash:            blob.Hash,
			CompressedBytes: int64(len(blob.Data)),
		})
	}
	analysis.manifestHash = casHash(casManifestDomain, manifestBytes)
	manifestBlob := casBlob{
		Hash:    analysis.manifestHash,
		Codec:   casCodecZstd,
		OrigLen: int64(len(manifestBytes)),
		Data:    casEncoder.EncodeAll(manifestBytes, nil),
	}
	analysis.blobs[manifestBlob.Hash] = manifestBlob
	for i := range analysis.References {
		analysis.References[i].OwnerHash = analysis.manifestHash
	}
	analysis.Objects = append(analysis.Objects, CASAnalysisObject{
		Hash:            manifestBlob.Hash,
		CompressedBytes: int64(len(manifestBlob.Data)),
		Manifest:        true,
	})
	return analysis, nil
}

// Reconstruct verifies the encoded objects and returns the original bytes.
func (a *CASFieldAnalysis) Reconstruct() ([]byte, error) {
	if a == nil {
		return nil, fmt.Errorf("logstore/cas analysis: nil field analysis")
	}
	manifestBlob, ok := a.blobs[a.manifestHash]
	if !ok {
		return nil, fmt.Errorf("logstore/cas analysis: manifest missing")
	}
	manifest, err := casDecodeBlob(manifestBlob, casManifestDomain)
	if err != nil {
		return nil, err
	}
	return casReconstruct(manifest, func(hash string) ([]byte, error) {
		blob, ok := a.blobs[hash]
		if !ok {
			return nil, fmt.Errorf("logstore/cas analysis: segment missing")
		}
		return casDecodeBlob(blob, casDataDomain)
	})
}
