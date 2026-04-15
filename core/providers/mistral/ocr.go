package mistral

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToMistralOCRRequest converts a Bifrost OCR request to a Mistral OCR request.
func ToMistralOCRRequest(req *schemas.BifrostOCRRequest) *MistralOCRRequest {
	if req == nil {
		return nil
	}

	mistralReq := &MistralOCRRequest{
		Model: req.Model,
		Document: MistralOCRDocument{
			Type: string(req.Document.Type),
		},
	}

	if req.ID != nil {
		mistralReq.ID = *req.ID
	}

	switch req.Document.Type {
	case schemas.OCRDocumentTypeDocumentURL:
		if req.Document.DocumentURL != nil {
			mistralReq.Document.DocumentURL = *req.Document.DocumentURL
		}
	case schemas.OCRDocumentTypeImageURL:
		if req.Document.ImageURL != nil {
			mistralReq.Document.ImageURL = *req.Document.ImageURL
		}
	}

	if req.Params != nil {
		mistralReq.IncludeImageBase64 = req.Params.IncludeImageBase64
		mistralReq.Pages = req.Params.Pages
		mistralReq.ImageLimit = req.Params.ImageLimit
		mistralReq.ImageMinSize = req.Params.ImageMinSize
		mistralReq.TableFormat = req.Params.TableFormat
		mistralReq.ExtractHeader = req.Params.ExtractHeader
		mistralReq.ExtractFooter = req.Params.ExtractFooter
		mistralReq.BBoxAnnotationFormat = req.Params.BBoxAnnotationFormat
		mistralReq.DocumentAnnotationFormat = req.Params.DocumentAnnotationFormat
		mistralReq.DocumentAnnotationPrompt = req.Params.DocumentAnnotationPrompt
		mistralReq.ExtraParams = req.Params.ExtraParams
	}

	return mistralReq
}

// ToBifrostOCRResponse converts a Mistral OCR response to a Bifrost OCR response.
func (r *MistralOCRResponse) ToBifrostOCRResponse() *schemas.BifrostOCRResponse {
	if r == nil {
		return nil
	}

	resp := &schemas.BifrostOCRResponse{
		Model:              r.Model,
		DocumentAnnotation: r.DocumentAnnotation,
	}

	// Convert pages
	if len(r.Pages) > 0 {
		resp.Pages = make([]schemas.OCRPage, len(r.Pages))
		for i, p := range r.Pages {
			page := schemas.OCRPage{
				Index:    p.Index,
				Markdown: p.Markdown,
			}
			if len(p.Images) > 0 {
				page.Images = make([]schemas.OCRPageImage, len(p.Images))
				for j, img := range p.Images {
					page.Images[j] = schemas.OCRPageImage{
						ID:           img.ID,
						TopLeftX:     img.TopLeftX,
						TopLeftY:     img.TopLeftY,
						BottomRightX: img.BottomRightX,
						BottomRightY: img.BottomRightY,
						ImageBase64:  img.ImageBase64,
					}
				}
			}
			if p.Dimensions != nil {
				page.Dimensions = &schemas.OCRPageDimensions{
					DPI:    p.Dimensions.DPI,
					Height: p.Dimensions.Height,
					Width:  p.Dimensions.Width,
				}
			}
			resp.Pages[i] = page
		}
	}

	// Convert usage info
	if r.UsageInfo != nil {
		resp.UsageInfo = &schemas.OCRUsageInfo{
			PagesProcessed: r.UsageInfo.PagesProcessed,
			DocSizeBytes:   r.UsageInfo.DocSizeBytes,
		}
	}

	return resp
}
