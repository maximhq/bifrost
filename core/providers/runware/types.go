package runware

// Runware task types.
const (
	// taskTypeImageInference is used for all image operations
	// (text-to-image, image-to-image, inpainting, outpainting).
	taskTypeImageInference = "imageInference"
	// taskTypeVideoInference is used for text-to-video and image-to-video generation.
	taskTypeVideoInference = "videoInference"
	// taskTypeGetResponse polls an async task (e.g. video) by its taskUUID.
	taskTypeGetResponse = "getResponse"
)

// deliveryMethodAsync queues a task instead of holding the connection open; used for video.
const deliveryMethodAsync = "async"

// RunwareFrameImage anchors an input image to a video frame for image-to-video generation.
type RunwareFrameImage struct {
	InputImage string  `json:"inputImage"`      // image UUID, URL, or base64/data-URI string
	Frame      *string `json:"frame,omitempty"` // "first" | "last"
}

// RunwareInferenceRequest is a single Runware task. taskType selects the operation; each
// operation populates only the subset of fields it needs. Runware accepts an array of these
// objects per request; the provider wraps a single task in an array before sending.
type RunwareInferenceRequest struct {
	// Common
	TaskType       string  `json:"taskType"`
	TaskUUID       string  `json:"taskUUID"`
	Model          string  `json:"model"`
	PositivePrompt *string `json:"positivePrompt,omitempty"`
	NegativePrompt *string `json:"negativePrompt,omitempty"`
	Width          *int    `json:"width,omitempty"`
	Height         *int    `json:"height,omitempty"`
	Seed           *int    `json:"seed,omitempty"`
	NumberResults  *int    `json:"numberResults,omitempty"`
	OutputType     *string `json:"outputType,omitempty"`   // "URL" | "base64Data" | "dataURI"
	OutputFormat   *string `json:"outputFormat,omitempty"` // image: "PNG"/"JPG"/"WEBP"; video: "MP4"/"WEBM"

	// Image-only
	Steps     *int    `json:"steps,omitempty"`
	SeedImage *string `json:"seedImage,omitempty"` // image-to-image / inpainting / outpainting base image
	MaskImage *string `json:"maskImage,omitempty"` // inpainting mask

	// Video-only
	DeliveryMethod  *string             `json:"deliveryMethod,omitempty"`
	Duration        *float64            `json:"duration,omitempty"`
	FrameImages     []RunwareFrameImage `json:"frameImages,omitempty"` // image-to-video
	ReferenceImages []string            `json:"referenceImages,omitempty"`

	// ExtraParams carries provider-native fields with no Bifrost equivalent
	// (CFGScale, scheduler, strength, maskMargin, outpaint, fps, lora, ...). Merged into
	// the request body by the transport layer when passthrough is enabled.
	ExtraParams map[string]interface{} `json:"-"`
}

func (r *RunwareInferenceRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// RunwareGetResponseRequest polls an async task by its UUID.
type RunwareGetResponseRequest struct {
	TaskType string `json:"taskType"`
	TaskUUID string `json:"taskUUID"`
}

// RunwareResponse is the universal Runware envelope: successful task outputs land in Data,
// per-task failures land in Errors (which can be present on a 200 response).
type RunwareResponse struct {
	Data   []RunwareResult `json:"data,omitempty"`
	Errors []RunwareError  `json:"errors,omitempty"`
}

// RunwareResult is a single task result. Fields are populated per modality: image tasks fill
// the image* fields, video tasks fill the video* fields; the rest are shared.
type RunwareResult struct {
	// Common
	TaskType string  `json:"taskType"`
	TaskUUID string  `json:"taskUUID"`
	Status   string  `json:"status,omitempty"` // video: "processing" | "success" | "error"
	Seed     *int    `json:"seed,omitempty"`
	Cost     float64 `json:"cost,omitempty"`

	// Image
	ImageUUID       string `json:"imageUUID,omitempty"`
	ImageURL        string `json:"imageURL,omitempty"`
	ImageBase64Data string `json:"imageBase64Data,omitempty"`
	ImageDataURI    string `json:"imageDataURI,omitempty"`
	NSFWContent     *bool  `json:"NSFWContent,omitempty"`

	// Video
	VideoUUID string `json:"videoUUID,omitempty"`
	VideoURL  string `json:"videoURL,omitempty"`
}

// RunwareError describes a single task failure returned by the Runware API.
type RunwareError struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Parameter string `json:"parameter,omitempty"`
	TaskType  string `json:"taskType,omitempty"`
	TaskUUID  string `json:"taskUUID,omitempty"`
}
