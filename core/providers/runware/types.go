package runware

// taskTypeImageInference is the Runware task type used for all image operations
// (text-to-image, image-to-image, inpainting, outpainting).
const taskTypeImageInference = "imageInference"

// RunwareImageInferenceRequest is a single Runware image inference task. Runware accepts an
// array of these objects per request; the provider wraps a single task in an array before sending.
type RunwareImageInferenceRequest struct {
	TaskType       string  `json:"taskType"`
	TaskUUID       string  `json:"taskUUID"`
	Model          string  `json:"model"`
	PositivePrompt string  `json:"positivePrompt"`
	NegativePrompt *string `json:"negativePrompt,omitempty"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	Steps          *int    `json:"steps,omitempty"`
	Seed           *int    `json:"seed,omitempty"`
	NumberResults  *int    `json:"numberResults,omitempty"`
	OutputType     *string `json:"outputType,omitempty"` // "URL", "base64Data", "dataURI"
	OutputFormat   *string `json:"outputFormat,omitempty"` // "PNG", "JPG", "WEBP"
	SeedImage      *string `json:"seedImage,omitempty"`    // image-to-image / inpainting / outpainting base image
	MaskImage      *string `json:"maskImage,omitempty"`    // inpainting mask

	// ExtraParams carries provider-native fields with no Bifrost equivalent
	// (CFGScale, scheduler, strength, maskMargin, outpaint, lora, ...). Merged into
	// the request body by the transport layer when passthrough is enabled.
	ExtraParams map[string]interface{} `json:"-"`
}

func (r *RunwareImageInferenceRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// RunwareResponse is the envelope returned by the Runware API. Successful task outputs
// land in Data; per-task failures land in Errors (which can be present on a 200 response).
type RunwareResponse struct {
	Data   []RunwareImageResult `json:"data,omitempty"`
	Errors []RunwareError       `json:"errors,omitempty"`
}

// RunwareImageResult is a single generated image returned for an imageInference task.
type RunwareImageResult struct {
	TaskType         string  `json:"taskType"`
	TaskUUID         string  `json:"taskUUID"`
	ImageUUID        string  `json:"imageUUID"`
	ImageURL         string  `json:"imageURL,omitempty"`
	ImageBase64Data  string  `json:"imageBase64Data,omitempty"`
	ImageDataURI     string  `json:"imageDataURI,omitempty"`
	Seed             *int    `json:"seed,omitempty"`
	Cost             float64 `json:"cost,omitempty"`
	NSFWContent      *bool   `json:"NSFWContent,omitempty"`
}

// RunwareError describes a single task failure returned by the Runware API.
type RunwareError struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Parameter string `json:"parameter,omitempty"`
	TaskType  string `json:"taskType,omitempty"`
	TaskUUID  string `json:"taskUUID,omitempty"`
}
