package schemas

// OCRDocumentType specifies the type of document input for an OCR request.
type OCRDocumentType string

const (
	// OCRDocumentTypeDocumentURL represents a document URL input (e.g., PDF URL or base64 data URL).
	OCRDocumentTypeDocumentURL OCRDocumentType = "document_url"
	// OCRDocumentTypeImageURL represents an image URL input.
	OCRDocumentTypeImageURL OCRDocumentType = "image_url"
)

// OCRDocument represents the document input for an OCR request.
type OCRDocument struct {
	Type        OCRDocumentType `json:"type"`
	DocumentURL *string         `json:"document_url,omitempty"`
	ImageURL    *string         `json:"image_url,omitempty"`
}

// OCRParameters contains optional parameters for an OCR request.
type OCRParameters struct {
	IncludeImageBase64          *bool                  `json:"include_image_base64,omitempty"`
	Pages                       []int                  `json:"pages,omitempty"`
	ImageLimit                  *int                   `json:"image_limit,omitempty"`
	ImageMinSize                *int                   `json:"image_min_size,omitempty"`
	TableFormat                 *string                `json:"table_format,omitempty"`
	ExtractHeader               *bool                  `json:"extract_header,omitempty"`
	ExtractFooter               *bool                  `json:"extract_footer,omitempty"`
	BBoxAnnotationFormat        *string                `json:"bbox_annotation_format,omitempty"`
	DocumentAnnotationFormat    *string                `json:"document_annotation_format,omitempty"`
	DocumentAnnotationPrompt    *string                `json:"document_annotation_prompt,omitempty"`
	ExtraParams                 map[string]interface{} `json:"-"`
}

// BifrostOCRRequest represents a request to perform OCR on a document.
type BifrostOCRRequest struct {
	Provider       ModelProvider  `json:"provider"`
	Model          string         `json:"model"`
	ID             *string        `json:"id,omitempty"`
	Document       OCRDocument    `json:"document"`
	Params         *OCRParameters `json:"params,omitempty"`
	Fallbacks      []Fallback     `json:"fallbacks,omitempty"`
	RawRequestBody []byte         `json:"-"`
}

// GetRawRequestBody returns the raw request body for the OCR request.
func (r *BifrostOCRRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

// OCRPageImage represents an extracted image from an OCR page.
type OCRPageImage struct {
	ID            string  `json:"id"`
	TopLeftX      float64 `json:"top_left_x"`
	TopLeftY      float64 `json:"top_left_y"`
	BottomRightX  float64 `json:"bottom_right_x"`
	BottomRightY  float64 `json:"bottom_right_y"`
	ImageBase64   *string `json:"image_base64,omitempty"`
}

// OCRPageDimensions represents the dimensions of an OCR page.
type OCRPageDimensions struct {
	DPI    int `json:"dpi"`
	Height int `json:"height"`
	Width  int `json:"width"`
}

// OCRPage represents a single processed page from an OCR response.
type OCRPage struct {
	Index      int                `json:"index"`
	Markdown   string             `json:"markdown"`
	Images     []OCRPageImage     `json:"images,omitempty"`
	Dimensions *OCRPageDimensions `json:"dimensions,omitempty"`
}

// OCRUsageInfo represents usage information from an OCR response.
type OCRUsageInfo struct {
	PagesProcessed int `json:"pages_processed"`
	DocSizeBytes   int `json:"doc_size_bytes"`
}

// BifrostOCRResponse represents the response from an OCR request.
type BifrostOCRResponse struct {
	Model               string                     `json:"model"`
	Pages               []OCRPage                  `json:"pages"`
	UsageInfo           *OCRUsageInfo              `json:"usage_info,omitempty"`
	DocumentAnnotation  *string                    `json:"document_annotation,omitempty"`
	ExtraFields         BifrostResponseExtraFields `json:"extra_fields"`
}
