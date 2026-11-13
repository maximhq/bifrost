package server

import (
	"context"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// complexityCaptureDeletes keeps the complexity router's failed captured-vector
// deletes in the log store's model_routing_vector_deletes table. It is an
// adapter rather than a log store method so the routing plugin does not import
// the log store.
type complexityCaptureDeletes struct {
	store logstore.ComplexitySuggestionStore
}

var _ complexity.CaptureDeleteRecorder = complexityCaptureDeletes{}

// AddCaptureDeletes records deletes to retry.
func (d complexityCaptureDeletes) AddCaptureDeletes(ctx context.Context, rows []complexity.CaptureDelete) error {
	if len(rows) == 0 {
		return nil
	}
	records := make([]logstore.ComplexityVectorDelete, len(rows))
	for i, row := range rows {
		records[i] = logstore.ComplexityVectorDelete{LogID: row.LogID, Namespace: row.Namespace}
	}
	return d.store.AddComplexityVectorDeletes(ctx, records)
}

// ListCaptureDeletes returns up to limit deletes to retry, oldest first.
func (d complexityCaptureDeletes) ListCaptureDeletes(ctx context.Context, limit int) ([]complexity.CaptureDelete, error) {
	records, err := d.store.ListComplexityVectorDeletes(ctx, limit)
	if err != nil || len(records) == 0 {
		return nil, err
	}
	rows := make([]complexity.CaptureDelete, len(records))
	for i, record := range records {
		rows[i] = complexity.CaptureDelete{LogID: record.LogID, Namespace: record.Namespace}
	}
	return rows, nil
}

// DeleteCaptureDeletes drops exactly the (log, namespace) deletes that have
// now gone through.
func (d complexityCaptureDeletes) DeleteCaptureDeletes(ctx context.Context, rows []complexity.CaptureDelete) error {
	if len(rows) == 0 {
		return nil
	}
	records := make([]logstore.ComplexityVectorDelete, len(rows))
	for i, row := range rows {
		records[i] = logstore.ComplexityVectorDelete{LogID: row.LogID, Namespace: row.Namespace}
	}
	return d.store.DeleteComplexityVectorDeletes(ctx, records)
}

// captureDeleteRecorderSetter is a plugin that keeps failed captured-vector
// deletes somewhere durable; the routing plugin's request capture is one.
type captureDeleteRecorderSetter interface {
	SetComplexityCaptureDeleteRecorder(recorder complexity.CaptureDeleteRecorder)
}

// applyRoutingCaptureDeleteRecorder gives a plugin the log store to keep its
// failed captured-vector deletes in, for its upkeep to retry, as Warp's are.
func (s *BifrostHTTPServer) applyRoutingCaptureDeleteRecorder(plugin any) {
	setter, ok := plugin.(captureDeleteRecorderSetter)
	if !ok || s.Config == nil || s.Config.LogsStore == nil {
		return
	}
	setter.SetComplexityCaptureDeleteRecorder(complexityCaptureDeletes{store: s.Config.LogsStore})
}
