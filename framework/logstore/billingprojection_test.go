package logstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The list projection and the billing projection answer different questions and must
// not converge.
//
// listSelectColumns serves /api/logs. Its comment records a real incident: including
// unbounded TEXT payloads pushed the response past Cloud Run's 32MB body limit and
// returned 500s. So it must stay free of the output blobs.
//
// billingSelectColumns serves cost recomputation, which reads those blobs to recover
// audio duration, TTS character counts, image counts and OCR pages — but it must carry
// nothing else, because a recompute holds a whole batch in memory at once.

// newProjectionTestStore builds a real sqlite-backed store so the projection tests
// can actually issue the queries. A temp file rather than ":memory:" because sqlite
// gives each connection its own in-memory database, and the store's pool uses more
// than one — the same reason newTestHybrid takes this approach.
func newProjectionTestStore(t *testing.T) *RDBLogStore {
	t.Helper()
	store, err := newSqliteLogStore(
		context.Background(),
		&SQLiteConfig{Path: filepath.Join(t.TempDir(), "projection.db")},
		testLogger{},
	)
	require.NoError(t, err)
	return store
}

func TestListProjectionExcludesOutputPayloadBlobs(t *testing.T) {
	cols := newProjectionTestStore(t).listSelectColumns()
	for blob := range billingPayloadColumns {
		if containsColumn(cols, blob) {
			t.Fatalf("listSelectColumns must not select %q: it serves /api/logs, where unbounded "+
				"payload columns reintroduce the Cloud Run 32MB body-limit failure", blob)
		}
	}
}

// TestListProjectionIncludesBillingScalars pins the columns whose absence produced the
// production overbill. cached_read_tokens gates the degraded-usage rebuild in
// DeserializeFields, and has_object / content_hidden decide whether a row can be
// hydrated at all. They are cheap scalars, so the list path carries them too.
func TestListProjectionIncludesBillingScalars(t *testing.T) {
	cols := newProjectionTestStore(t).listSelectColumns()
	for _, scalar := range []string{
		"cached_read_tokens",
		"has_object",
		"content_hidden",
		"service_tier",
		"speed",
		"inference_geo",
	} {
		if !containsColumn(cols, scalar) {
			t.Fatalf("listSelectColumns is missing %q; cost recomputation cannot see it", scalar)
		}
	}
}

func TestBillingProjectionSelectsEveryPricingInput(t *testing.T) {
	cols := newProjectionTestStore(t).billingSelectColumns()
	for _, scalar := range billingScalarColumns {
		if !containsColumn(cols, scalar) {
			t.Fatalf("billingSelectColumns is missing %q; pricing reads it", scalar)
		}
	}
}

// TestBillingProjectionGatesPayloadsByObjectType is the memory guard.
//
// image_generation_output serializes ImageData.B64JSON — full base64 image bytes,
// megabytes per image — while pricing only ever reads len(Data). Selecting these
// unconditionally would pull that for every row in a batch, including plain chat rows.
func TestBillingProjectionGatesPayloadsByObjectType(t *testing.T) {
	cols := newProjectionTestStore(t).billingSelectColumns()

	for blob, objectTypes := range billingPayloadColumns {
		if !strings.Contains(cols, "AS "+blob) {
			t.Fatalf("billingSelectColumns is missing %q; the modality patch-ins in "+
				"calculateCostForLog cannot restore per-second / per-page / per-image billing without it", blob)
		}
		if containsColumn(cols, blob) {
			t.Fatalf("%q is selected unconditionally; it must be gated on object_type so a "+
				"chat row never pulls an unrelated payload into memory", blob)
		}
		for _, objectType := range objectTypes {
			if !strings.Contains(cols, "'"+objectType+"'") {
				t.Fatalf("%q is not gated for object_type %q, so those rows would lose their billing payload", blob, objectType)
			}
		}
	}
}

// TestBillingProjectionOmitsColumnsPricingNeverReads enforces "only what is absolutely
// required". A recompute batch is held entirely in memory, so anything pricing does not
// read is pure ballast — and these are the expensive ones.
func TestBillingProjectionOmitsColumnsPricingNeverReads(t *testing.T) {
	cols := newProjectionTestStore(t).billingSelectColumns()
	for _, unused := range []string{
		"input_history",
		"responses_input_history",
		"output_message",
		"content_summary",
		"metadata",
		"speech_input",
		"transcription_input",
		"image_generation_input",
		"video_generation_input",
	} {
		if strings.Contains(cols, unused) {
			t.Fatalf("billingSelectColumns pulls %q, which pricing never reads; a recompute "+
				"holds the whole batch at once so this is wasted memory", unused)
		}
	}
}

// TestBillingProjectionDeliversModalityOutputsEndToEnd is the query-level counterpart
// to the string assertions above.
//
// calculateCostForLog patches audio duration, TTS character counts, image counts, and
// OCR pages back onto the reconstructed response by reading these Parsed fields. Since
// listSelectColumns never selected the underlying columns, those patch-ins were dead
// code on the recompute path and every one of those request types was priced off
// aggregate token counts alone.
func TestBillingProjectionDeliversModalityOutputsEndToEnd(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	annotation := "annotated"
	seconds := "8"
	// object_type drives the CASE gate, so each payload needs a row of its own type.
	rows := []*Log{
		{
			ID: "speech-1", Object: "speech", Provider: "openai", Model: "gpt-4o-mini-tts",
			SpeechOutputParsed: &schemas.BifrostSpeechResponse{
				Usage: &schemas.SpeechUsage{InputTokens: 40, InputChars: 512, TotalTokens: 40},
			},
		},
		{
			ID: "transcription-1", Object: "transcription", Provider: "openai", Model: "whisper-1",
			TranscriptionOutputParsed: &schemas.BifrostTranscriptionResponse{
				Usage: &schemas.TranscriptionUsage{Seconds: new(float64(31))},
			},
		},
		{
			ID: "video-1", Object: "video_generation", Provider: "openai", Model: "sora",
			VideoGenerationOutputParsed: &schemas.BifrostVideoGenerationResponse{Seconds: &seconds},
		},
		{
			ID: "ocr-1", Object: "ocr", Provider: "mistral", Model: "mistral-ocr",
			OCROutputParsed: &schemas.BifrostOCRResponse{
				Pages:              []schemas.OCRPage{{}, {}, {}},
				UsageInfo:          &schemas.OCRUsageInfo{PagesProcessed: 3},
				DocumentAnnotation: &annotation,
			},
		},
	}
	for i, r := range rows {
		r.Timestamp = time.Now().UTC().Add(time.Duration(i) * time.Second)
		r.Status = "success"
		require.NoError(t, r.SerializeFields())
		require.NoError(t, store.Create(ctx, r))
	}

	listResult, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, listResult.Logs, len(rows))
	for _, got := range listResult.Logs {
		assert.Nil(t, got.OCROutputParsed,
			"the list projection must keep omitting the output blobs; /api/logs has a body-size limit to respect")
		assert.Nil(t, got.SpeechOutputParsed)
	}

	billingResult, err := store.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, billingResult.Logs, len(rows))

	byID := make(map[string]*Log, len(billingResult.Logs))
	for i := range billingResult.Logs {
		byID[billingResult.Logs[i].ID] = &billingResult.Logs[i]
	}

	speech := byID["speech-1"]
	require.NotNil(t, speech.SpeechOutputParsed, "TTS per-character billing needs speech_output")
	assert.Equal(t, 512, speech.SpeechOutputParsed.Usage.InputChars)
	assert.Nil(t, speech.OCROutputParsed, "a speech row must not pull the OCR payload")

	transcription := byID["transcription-1"]
	require.NotNil(t, transcription.TranscriptionOutputParsed, "per-second audio billing needs transcription_output")
	require.NotNil(t, transcription.TranscriptionOutputParsed.Usage.Seconds)
	assert.Equal(t, float64(31), *transcription.TranscriptionOutputParsed.Usage.Seconds)

	video := byID["video-1"]
	require.NotNil(t, video.VideoGenerationOutputParsed, "per-second video billing needs video_generation_output")
	assert.Equal(t, "8", *video.VideoGenerationOutputParsed.Seconds)

	ocr := byID["ocr-1"]
	require.NotNil(t, ocr.OCROutputParsed, "per-page OCR billing needs ocr_output")
	assert.Equal(t, 3, ocr.OCROutputParsed.UsageInfo.PagesProcessed)
	require.NotNil(t, ocr.OCROutputParsed.DocumentAnnotation, "the annotation surcharge is keyed off this field")
}

// TestBillingProjectionStripsGeneratedImageBytes pins the OOM guard. Pricing needs the
// image count, never the images: extractCostInput passes len(Data) to
// populateOutputImageCount and nothing reads B64JSON, which can be megabytes per image.
func TestBillingProjectionStripsGeneratedImageBytes(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	size := "1024x1024"
	entry := &Log{
		ID:        "image-1",
		Timestamp: time.Now().UTC(),
		Object:    "image_generation",
		Provider:  "openai",
		Model:     "gpt-image-1",
		Status:    "success",
		ImageGenerationOutputParsed: &schemas.BifrostImageGenerationResponse{
			Data: []schemas.ImageData{
				{B64JSON: strings.Repeat("A", 4096), RevisedPrompt: "a cat"},
				{B64JSON: strings.Repeat("B", 4096)},
			},
			ImageGenerationResponseParameters: &schemas.ImageGenerationResponseParameters{Size: size},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	result, err := store.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	got := result.Logs[0]

	require.NotNil(t, got.ImageGenerationOutputParsed)
	assert.Len(t, got.ImageGenerationOutputParsed.Data, 2,
		"the image count drives per-image pricing and must survive")
	require.NotNil(t, got.ImageGenerationOutputParsed.ImageGenerationResponseParameters)
	assert.Equal(t, size, got.ImageGenerationOutputParsed.Size, "size drives per-pixel pricing")

	for i, img := range got.ImageGenerationOutputParsed.Data {
		assert.Empty(t, img.B64JSON, "generated image bytes must be released at index %d; pricing never reads them", i)
	}
	assert.Empty(t, got.ImageGenerationOutput,
		"the serialized column still holds the base64 and must be released once parsed")
}

// containsColumn matches a bare column name in a SELECT clause. It deliberately does
// not match a name that only appears inside a CASE expression, so the gating tests can
// tell "selected unconditionally" from "selected for its own object_type".
func containsColumn(clause, column string) bool {
	for _, part := range strings.Split(clause, ",") {
		if strings.TrimSpace(part) == column {
			return true
		}
	}
	return false
}
