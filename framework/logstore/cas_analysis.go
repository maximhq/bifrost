package logstore

import "fmt"

// CASAnalysisObject describes one encoded object produced by the production
// CAS codec. Offline materialization tools persist these fields into isolated
// benchmark databases; aggregate reports must not include Hash or Data.
type CASAnalysisObject struct {
	Hash            string
	Codec           string
	OrigLen         int64
	Data            []byte
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

// CompressFieldDataForAnalysis applies the same zstd codec and level used by
// CAS and returns an owned encoded buffer for isolated benchmark databases.
func CompressFieldDataForAnalysis(raw []byte) []byte {
	return casEncoder.EncodeAll(raw, nil)
}

// DecompressFieldDataForAnalysis decodes a standalone field-zstd benchmark
// value and verifies its original length.
func DecompressFieldDataForAnalysis(data []byte, origLen int64) ([]byte, error) {
	raw, err := casDecoder.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("logstore/cas analysis: zstd decode: %w", err)
	}
	if int64(len(raw)) != origLen {
		return nil, fmt.Errorf("logstore/cas analysis: decoded length %d != recorded %d", len(raw), origLen)
	}
	return raw, nil
}

// CompressFieldForAnalysis applies the same zstd codec and level used by CAS.
func CompressFieldForAnalysis(raw []byte) int64 {
	return int64(len(CompressFieldDataForAnalysis(raw)))
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
			Codec:           blob.Codec,
			OrigLen:         blob.OrigLen,
			Data:            append([]byte(nil), blob.Data...),
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
		Codec:           manifestBlob.Codec,
		OrigLen:         manifestBlob.OrigLen,
		Data:            append([]byte(nil), manifestBlob.Data...),
		CompressedBytes: int64(len(manifestBlob.Data)),
		Manifest:        true,
	})
	return analysis, nil
}

// ManifestHash returns the content-addressed pointer stored in cas_payloads.
func (a *CASFieldAnalysis) ManifestHash() string {
	if a == nil {
		return ""
	}
	return a.manifestHash
}

// CASObjectStoreForAnalysis holds verified-materialization input in the same
// internal shape used by the production decoder. Construct it once and reuse
// it across field reconstructions to avoid rebuilding a large hash map per
// payload pointer.
type CASObjectStoreForAnalysis struct {
	blobs map[string]casBlob
}

// NewCASObjectStoreForAnalysis prepares a reusable object lookup.
func NewCASObjectStoreForAnalysis(objects map[string]CASAnalysisObject) *CASObjectStoreForAnalysis {
	store := &CASObjectStoreForAnalysis{blobs: make(map[string]casBlob, len(objects))}
	for hash, object := range objects {
		store.blobs[hash] = casBlob{
			Hash:    object.Hash,
			Codec:   object.Codec,
			OrigLen: object.OrigLen,
			Data:    object.Data,
		}
	}
	return store
}

// Reconstruct verifies and reconstructs one field from the reusable store.
func (s *CASObjectStoreForAnalysis) Reconstruct(manifestHash string) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("logstore/cas analysis: nil object store")
	}
	manifestBlob, ok := s.blobs[manifestHash]
	if !ok {
		return nil, fmt.Errorf("logstore/cas analysis: manifest missing")
	}
	manifest, err := casDecodeBlob(manifestBlob, casManifestDomain)
	if err != nil {
		return nil, err
	}
	return casReconstruct(manifest, func(hash string) ([]byte, error) {
		blob, ok := s.blobs[hash]
		if !ok {
			return nil, fmt.Errorf("logstore/cas analysis: segment missing")
		}
		return casDecodeBlob(blob, casDataDomain)
	})
}

// ReconstructCASObjectMapForAnalysis verifies objects loaded from a materialized
// CAS database and reconstructs the field selected by manifestHash.
func ReconstructCASObjectMapForAnalysis(manifestHash string, objects map[string]CASAnalysisObject) ([]byte, error) {
	return NewCASObjectStoreForAnalysis(objects).Reconstruct(manifestHash)
}

// ReconstructCASObjectsForAnalysis verifies materialized production CAS objects
// and reconstructs the field selected by manifestHash.
func ReconstructCASObjectsForAnalysis(manifestHash string, objects []CASAnalysisObject) ([]byte, error) {
	byHash := make(map[string]CASAnalysisObject, len(objects))
	for _, object := range objects {
		byHash[object.Hash] = object
	}
	return ReconstructCASObjectMapForAnalysis(manifestHash, byHash)
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
