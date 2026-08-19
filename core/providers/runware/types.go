package runware

// Runware task types.
const (
	// taskTypeImageInference is used for all image operations
	// (text-to-image, image-to-image, inpainting, outpainting).
	taskTypeImageInference = "imageInference"
	// taskTypeVideoInference is used for text-to-video and image-to-video generation.
	taskTypeVideoInference = "videoInference"
	// taskType3DInference is used for 3D model generation. It is not hardcoded into any request
	// builder; callers select it by passing "taskType" in extra_params on the video request.
	taskType3DInference = "3dInference"
	// taskTypeGetResponse polls an async task (e.g. video, 3D) by its taskUUID.
	taskTypeGetResponse = "getResponse"
	// taskTypeUpscale enlarges an existing image; the model selects the upscaler.
	taskTypeUpscale = "upscale"
	// taskTypeRemoveBackground cuts the subject out onto a transparent background.
	taskTypeRemoveBackground = "removeBackground"
	// taskTypeImageMasking segments an image, returning a mask plus the regions it detected.
	taskTypeImageMasking = "imageMasking"
	// taskTypeVectorize converts a raster image, or a prompt, into an SVG.
	taskTypeVectorize = "vectorize"
)

// deliveryMethodAsync queues a task instead of holding the connection open; used for video.
const deliveryMethodAsync = "async"

// RunwareFrameImage anchors an input image to a video frame for image-to-video generation.
type RunwareFrameImage struct {
	Image string  `json:"image"`           // image UUID, URL, or base64/data-URI string
	Frame *string `json:"frame,omitempty"` // "first" | "last"
}

// RunwareInputs holds a task's media inputs. Runware nests them under "inputs" while scalar
// parameters stay top-level.
type RunwareInputs struct {
	Image  *string  `json:"image,omitempty"`  // image UUID, URL, or base64/data-URI string
	Images []string `json:"images,omitempty"` // array form, used by some 3D models
	Video  *string  `json:"video,omitempty"`  // video UUID or URL

	// Frame and reference images are nested here rather than sent top-level: the newest video
	// models (klingai kling-video 3.x, alibaba wan 2.6/2.7, lightricks ltx 2.x, xai grok-imagine,
	// runway aleph) reject the flat form with unsupportedParameter, while every model accepts the
	// nested one. Note the item key is "image" here, where the flat form used "inputImage".
	FrameImages     []RunwareFrameImage `json:"frameImages,omitempty"`
	ReferenceImages []string            `json:"referenceImages,omitempty"`
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
	DeliveryMethod *string  `json:"deliveryMethod,omitempty"`
	Duration       *float64 `json:"duration,omitempty"`

	// Nested envelope, shared by the tool task types (upscale, removeBackground, 3D, ...).
	Inputs           *RunwareInputs         `json:"inputs,omitempty"`
	Settings         map[string]interface{} `json:"settings,omitempty"`         // model-specific tuning
	ProviderSettings map[string]interface{} `json:"providerSettings,omitempty"` // keyed by vendor
	OutputQuality    *int                   `json:"outputQuality,omitempty"`
	IncludeCost      *bool                  `json:"includeCost,omitempty"`

	// Upscale-only. UpscaleFactor and TargetMegapixels are mutually exclusive.
	UpscaleFactor    *int `json:"upscaleFactor,omitempty"`
	TargetMegapixels *int `json:"targetMegapixels,omitempty"`

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

	// Masking and ControlNet preprocessing name their artifact after what they produce rather
	// than reusing the image* fields, and echo the input they were derived from.
	MaskImageUUID        string             `json:"maskImageUUID,omitempty"`
	MaskImageURL         string             `json:"maskImageURL,omitempty"`
	MaskImageBase64Data  string             `json:"maskImageBase64Data,omitempty"`
	MaskImageDataURI     string             `json:"maskImageDataURI,omitempty"`
	GuideImageUUID       string             `json:"guideImageUUID,omitempty"`
	GuideImageURL        string             `json:"guideImageURL,omitempty"`
	GuideImageBase64Data string             `json:"guideImageBase64Data,omitempty"`
	GuideImageDataURI    string             `json:"guideImageDataURI,omitempty"`
	InputImageUUID       string             `json:"inputImageUUID,omitempty"`
	Detections           []RunwareDetection `json:"detections,omitempty"`

	// 3D / other modalities: newer Runware task types (e.g. 3dInference) return their
	// artifacts under a generic outputs.files[] array rather than modality-specific fields.
	Outputs *RunwareOutputs `json:"outputs,omitempty"`
}

// RunwareDetection is a region located by an imageMasking model, in absolute input-image pixels.
type RunwareDetection struct {
	XMin int `json:"x_min"`
	YMin int `json:"y_min"`
	XMax int `json:"x_max"`
	YMax int `json:"y_max"`
}

// RunwareOutputs holds the generic artifact list returned by task types such as 3dInference.
type RunwareOutputs struct {
	Files []RunwareOutputFile `json:"files,omitempty"`
}

// RunwareOutputFile is a single generated artifact (e.g. a .glb mesh) with its accessible URL.
type RunwareOutputFile struct {
	UUID string `json:"uuid,omitempty"`
	URL  string `json:"url,omitempty"`
}

// RunwareError describes a single task failure returned by the Runware API.
type RunwareError struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Parameter string `json:"parameter,omitempty"`
	TaskType  string `json:"taskType,omitempty"`
	TaskUUID  string `json:"taskUUID,omitempty"`
}
