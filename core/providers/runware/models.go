package runware

import (
	"slices"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// runware3DImageInputIsArray records, per 3D model, whether the input image goes into
// inputs.images[] (true) or inputs.image (false). The split is per-model and comes from Runware's
// published request schemas (https://schemas.runware.ai/resolve/<model>); refresh this table when
// Runware adds a 3D model. Models absent from it fall back to the array form, which the majority
// of current models use.
//
// Runware rejects an input key a model does not declare with unsupportedParameter, so sending the
// wrong form fails the request outright. Callers can override the whole object through the
// "inputs" extra param.
var runware3DImageInputIsArray = map[string]bool{
	"hyper3d:rodin@gen-2":          true,
	"meshy:meshy@6":                true,
	"meta:sam@3d":                  false,
	"microsoft:trellis-2@4b":       false,
	"tencent:hunyuan-3d@3.1-pro":   true,
	"tencent:hunyuan-3d@3.1-rapid": false,
	"tripo:v3.1@0":                 true,
}

// uses3DImageArrayInput reports whether a 3D model takes its input image as inputs.images[].
// The datasheet's wire name decides when it carries one; the table answers otherwise.
func uses3DImageArrayInput(caps schemas.ModelCaps) bool {
	switch caps.FieldName(schemas.LogicalFieldInputImage, "") {
	case "images":
		return true
	case "image":
		return false
	}
	if isArray, ok := runware3DImageInputIsArray[caps.Model()]; ok {
		return isArray
	}
	return true
}

// runwareVideoModelsRequiringDimensions lists the video models whose request schema marks width and
// height as required. Every other model derives them from the reference image on image-to-video,
// and many reject them outright once a frame image is present, so the width/height default is only
// kept for these. Sourced from https://schemas.runware.ai/resolve/<model>; refresh it when Runware
// adds a model that mandates dimensions.
var runwareVideoModelsRequiringDimensions = map[string]bool{
	"lightricks:ltx@2.3":      true,
	"lightricks:ltx@2.3-fast": true,
	"runway:1@2":              true,
	"vidu:4@2":                true,
}

// runwareVideoRequiresDimensions reports whether a video model rejects a request that omits
// width and height.
func runwareVideoRequiresDimensions(model string) bool {
	return runwareVideoModelsRequiringDimensions[model]
}

// runwareImageInputForm selects where an imageInference model takes its input image. Runware
// rejects an input key a model does not declare with unsupportedParameter, so sending the wrong
// form fails the request outright.
type runwareImageInputForm int

const (
	// runwareImageInputSeedImage sends a flat seedImage/maskImage pair. This is the default: it is
	// what the architecture-based models take (sdxl, flux-dev, pony, z-image, ...), and those are
	// open-ended — callers can name any custom or civitai checkpoint by AIR, so an unlisted model
	// is far more likely to be one of those than a newly published first-party model.
	runwareImageInputSeedImage runwareImageInputForm = iota
	// runwareImageInputReferenceImages nests the inputs under inputs.referenceImages[]. Used by the
	// current flagship editing models, none of which accept seedImage at all.
	runwareImageInputReferenceImages
	// runwareImageInputImageMask nests a single image under inputs.image, with inputs.mask for the
	// erase and object-removal models that take one.
	runwareImageInputImageMask
)

// runwareImageModel records how one model deviates from Bifrost's defaults. A model absent from the
// table takes the flat seedImage form and accepts dimensions and a prompt, which is what the
// architecture-based checkpoints (sdxl, flux-dev, pony, z-image, ...) expect. Those are open-ended -
// callers can name any custom or civitai checkpoint by AIR - so the default has to suit them.
type runwareImageModel struct {
	inputForm    runwareImageInputForm
	noDimensions bool // model declares no width/height and rejects them
	noPrompt     bool // model declares no positivePrompt and rejects it
}

// runwareImageModels is sourced from https://schemas.runware.ai/resolve/<AIR>; refresh it when
// Runware publishes new models.
var runwareImageModels = map[string]runwareImageModel{
	"alibaba:qwen-image@2.0":         {inputForm: runwareImageInputReferenceImages},
	"alibaba:qwen-image@2.0-pro":     {inputForm: runwareImageInputReferenceImages},
	"alibaba:qwen-image@3.0":         {inputForm: runwareImageInputReferenceImages},
	"alibaba:qwen-image@3.0-pro":     {inputForm: runwareImageInputReferenceImages},
	"alibaba:qwen-image@layered":     {inputForm: runwareImageInputReferenceImages, noDimensions: true},
	"alibaba:wan@2.6-image":          {inputForm: runwareImageInputReferenceImages},
	"alibaba:wan@2.7-image":          {inputForm: runwareImageInputReferenceImages},
	"alibaba:wan@2.7-image-pro":      {inputForm: runwareImageInputReferenceImages},
	"bfl:1@2":                        {noDimensions: true},
	"bfl:1@3":                        {noDimensions: true},
	"bfl:3@1":                        {inputForm: runwareImageInputReferenceImages},
	"bfl:4@1":                        {inputForm: runwareImageInputReferenceImages},
	"bfl:5@1":                        {inputForm: runwareImageInputReferenceImages},
	"bfl:6@1":                        {inputForm: runwareImageInputReferenceImages},
	"bfl:7@1":                        {inputForm: runwareImageInputReferenceImages},
	"bfl:flux@erase":                 {inputForm: runwareImageInputImageMask, noDimensions: true, noPrompt: true},
	"bfl:flux@outpainting":           {inputForm: runwareImageInputImageMask, noDimensions: true, noPrompt: true},
	"bfl:flux@vto":                   {inputForm: runwareImageInputReferenceImages, noDimensions: true},
	"bria:11@1":                      {inputForm: runwareImageInputReferenceImages},
	"bria:20@1":                      {inputForm: runwareImageInputImageMask},
	"bria:20@3":                      {inputForm: runwareImageInputImageMask},
	"bria:21@1":                      {inputForm: runwareImageInputImageMask, noDimensions: true},
	"bria:21@2":                      {inputForm: runwareImageInputImageMask, noDimensions: true},
	"bytedance:5@0":                  {inputForm: runwareImageInputReferenceImages},
	"bytedance:seedream@4.5":         {inputForm: runwareImageInputReferenceImages},
	"bytedance:seedream@5.0-lite":    {inputForm: runwareImageInputReferenceImages},
	"bytedance:seedream@5.0-pro":     {inputForm: runwareImageInputReferenceImages},
	"exactly:photo@bright-pulse":     {inputForm: runwareImageInputReferenceImages, noPrompt: true},
	"exactly:photo@distant-reality":  {inputForm: runwareImageInputReferenceImages, noPrompt: true},
	"exactly:photo@extreme-contrast": {inputForm: runwareImageInputReferenceImages, noPrompt: true},
	"exactly:photo@grain-film-look":  {inputForm: runwareImageInputReferenceImages, noPrompt: true},
	"exactly:photo@journey":          {inputForm: runwareImageInputReferenceImages, noPrompt: true},
	"exactly:photo@warm-light":       {inputForm: runwareImageInputReferenceImages, noPrompt: true},
	"google:4@1":                     {inputForm: runwareImageInputReferenceImages},
	"google:4@2":                     {inputForm: runwareImageInputReferenceImages},
	"google:4@3":                     {inputForm: runwareImageInputReferenceImages},
	"google:nano-banana@2-lite":      {inputForm: runwareImageInputReferenceImages},
	"ideogram:2@2":                   {inputForm: runwareImageInputReferenceImages},
	"ideogram:3@2":                   {inputForm: runwareImageInputReferenceImages},
	"ideogram:4@2":                   {inputForm: runwareImageInputReferenceImages},
	"ideogram:4@3":                   {noDimensions: true},
	"ideogram:4@4":                   {noPrompt: true},
	"ideogram:4@5":                   {noDimensions: true},
	"ideogram:layerize-text@0":       {inputForm: runwareImageInputImageMask, noDimensions: true},
	"ideogram:object-remover@0":      {inputForm: runwareImageInputImageMask, noDimensions: true, noPrompt: true},
	"imagineart:2.0@0":               {inputForm: runwareImageInputReferenceImages},
	"klingai:kling-image@3":          {inputForm: runwareImageInputReferenceImages},
	"klingai:kling-image@o1":         {inputForm: runwareImageInputReferenceImages},
	"klingai:kling-image@o3":         {inputForm: runwareImageInputReferenceImages},
	"krea:krea@2-large":              {inputForm: runwareImageInputReferenceImages},
	"krea:krea@2-medium":             {inputForm: runwareImageInputReferenceImages},
	"krea:krea@2-medium-turbo":       {inputForm: runwareImageInputReferenceImages},
	"luma:uni@1":                     {inputForm: runwareImageInputReferenceImages},
	"luma:uni@1-max":                 {inputForm: runwareImageInputReferenceImages},
	"openai:1@1":                     {inputForm: runwareImageInputReferenceImages},
	"openai:1@2":                     {inputForm: runwareImageInputReferenceImages},
	"openai:4@1":                     {inputForm: runwareImageInputReferenceImages},
	"openai:gpt-image@2":             {inputForm: runwareImageInputReferenceImages},
	"prunaai:2@1":                    {inputForm: runwareImageInputReferenceImages},
	"prunaai:p-image@try-on":         {inputForm: runwareImageInputReferenceImages, noDimensions: true},
	"reve:2@1":                       {inputForm: runwareImageInputReferenceImages},
	"runware:106@1":                  {inputForm: runwareImageInputReferenceImages},
	"runware:108@20":                 {inputForm: runwareImageInputReferenceImages},
	"runware:108@22":                 {inputForm: runwareImageInputReferenceImages},
	"runware:300@1":                  {inputForm: runwareImageInputImageMask, noDimensions: true},
	"runware:400@1":                  {inputForm: runwareImageInputReferenceImages},
	"runware:400@2":                  {inputForm: runwareImageInputReferenceImages},
	"runware:400@3":                  {inputForm: runwareImageInputReferenceImages},
	"runware:400@4":                  {inputForm: runwareImageInputReferenceImages},
	"runware:400@5":                  {inputForm: runwareImageInputReferenceImages},
	"runware:400@6":                  {inputForm: runwareImageInputReferenceImages},
	"runway:4@1":                     {inputForm: runwareImageInputReferenceImages},
	"runway:4@2":                     {inputForm: runwareImageInputReferenceImages},
	"sourceful:riverflow-2.0@fast":   {inputForm: runwareImageInputReferenceImages},
	"sourceful:riverflow-2.0@pro":    {inputForm: runwareImageInputReferenceImages},
	"sourceful:riverflow-2.5@fast":   {inputForm: runwareImageInputReferenceImages},
	"sourceful:riverflow-2.5@pro":    {inputForm: runwareImageInputReferenceImages},
	"vidu:q1@image":                  {inputForm: runwareImageInputReferenceImages},
	"xai:grok-imagine@image":         {inputForm: runwareImageInputReferenceImages},
	"xai:grok-imagine@image-2.0":     {inputForm: runwareImageInputReferenceImages},
	"xai:grok-imagine@image-quality": {inputForm: runwareImageInputReferenceImages},
}

// runwareImageModelFor reports the traits of a model. The datasheet wins where it carries a record,
// and the table below answers whenever it says nothing, so the mapping can move to the feed
// incrementally without a flag day.
func runwareImageModelFor(caps schemas.ModelCaps) runwareImageModel {
	traits := runwareImageModels[caps.Model()]

	switch caps.FieldName(schemas.LogicalFieldInputImage, "") {
	case "referenceImages":
		traits.inputForm = runwareImageInputReferenceImages
	case "image":
		traits.inputForm = runwareImageInputImageMask
	case "seedImage":
		traits.inputForm = runwareImageInputSeedImage
	}

	traits.noPrompt = caps.FieldUnsupported("positivePrompt", traits.noPrompt)
	traits.noDimensions = caps.FieldUnsupported("width", traits.noDimensions)

	return traits
}

// runwareVideoInputForm selects where a videoInference model takes its reference image. Most models
// anchor it to a frame under inputs.frameImages, but some newer ones declare no frameImages at all
// and reject it outright.
type runwareVideoInputForm int

const (
	// runwareVideoInputFrameImages nests the reference under inputs.frameImages[]. The default.
	runwareVideoInputFrameImages runwareVideoInputForm = iota
	// runwareVideoInputReferenceImages nests it under inputs.referenceImages[].
	runwareVideoInputReferenceImages
	// runwareVideoInputImage nests it under inputs.image.
	runwareVideoInputImage
)

// runwareVideoInputForms records the models that reject inputs.frameImages. Several also require an
// audio input that Bifrost's video schema does not carry, so routing the image correctly is
// necessary but not sufficient for those. Sourced from https://schemas.runware.ai/resolve/<AIR>.
var runwareVideoInputForms = map[string]runwareVideoInputForm{
	"bytedance:5@1":                    runwareVideoInputReferenceImages,
	"bytedance:5@2":                    runwareVideoInputImage,
	"creatify:aurora@0":                runwareVideoInputImage,
	"creatify:aurora@fast":             runwareVideoInputImage,
	"heygen:avatar@4":                  runwareVideoInputImage,
	"heygen:video-agent@0":             runwareVideoInputImage,
	"klingai:avatar@2.0-pro":           runwareVideoInputImage,
	"klingai:avatar@2.0-standard":      runwareVideoInputImage,
	"klingai:kling-video@2.6-standard": runwareVideoInputReferenceImages,
	"pixverse:modify@0":                runwareVideoInputReferenceImages,
	"prunaai:p-video@animate":          runwareVideoInputReferenceImages,
	"prunaai:p-video@replace":          runwareVideoInputReferenceImages,
	"runway:2@1":                       runwareVideoInputReferenceImages,
	"veed:fabric@1.0-hosted":           runwareVideoInputImage,
}

// runwareVideoInputFormFor reports how a video model takes its reference image. The datasheet wins
// where it carries a record; the table answers otherwise.
func runwareVideoInputFormFor(caps schemas.ModelCaps) runwareVideoInputForm {
	switch caps.FieldName(schemas.LogicalFieldInputImage, "") {
	case "referenceImages":
		return runwareVideoInputReferenceImages
	case "image":
		return runwareVideoInputImage
	case "frameImages":
		return runwareVideoInputFrameImages
	}
	return runwareVideoInputForms[caps.Model()]
}

// runwareModelSearchPageSize is above the documented maximum of 100, which Runware nonetheless
// honours. It is deliberate: a modelSearch page costs ~10-20s regardless of size, so paging the
// curated catalog at 100 would mean five sequential round trips and blow the request deadline,
// while 500 fetches the whole set in one.
const runwareModelSearchPageSize = 500

// runwareCuratedSource limits a catalog sweep to Runware's first-party models. The unfiltered
// catalog is dominated by community LoRA uploads (~273k of ~320k) that no Bifrost route targets.
const runwareCuratedSource = "curated"

// ToBifrostListModelsResponse converts Runware catalog pages to a Bifrost list response. The AIR is
// the model identifier every inference task keys off, so it becomes the Bifrost model ID.
func ToBifrostListModelsResponse(
	models []RunwareModel,
	providerKey schemas.ModelProvider,
	allowedModels schemas.WhiteList,
	blacklistedModels schemas.BlackList,
	aliases schemas.KeyAliases,
	unfiltered bool,
) *schemas.BifrostListModelsResponse {
	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(models)),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     allowedModels,
		BlacklistedModels: blacklistedModels,
		Aliases:           aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       providerKey,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	for _, model := range models {
		if model.AIR == "" {
			continue
		}
		for _, result := range pipeline.FilterModel(model.AIR) {
			bifrostModel := schemas.Model{
				ID: string(providerKey) + "/" + result.ResolvedID,
			}
			if model.Name != "" {
				bifrostModel.Name = &model.Name
			}
			if model.Comment != "" {
				bifrostModel.Description = &model.Comment
			}
			if model.Creator != nil {
				if owner := model.Creator.Name; owner != "" {
					bifrostModel.OwnedBy = &owner
				} else if owner := model.Creator.ID; owner != "" {
					bifrostModel.OwnedBy = &owner
				}
			}
			if model.AddedUnixTimestamp > 0 {
				bifrostModel.Created = &model.AddedUnixTimestamp
			}
			// Runware describes a model's shape with capability tags rather than modality lists, so
			// the input/output modalities are derived from its "io:<from>-to-<to>" entries.
			if architecture := runwareModelArchitecture(model); architecture != nil {
				bifrostModel.Architecture = architecture
			}
			if result.AliasValue != "" {
				bifrostModel.Alias = &result.AliasValue
			}
			bifrostResponse.Data = append(bifrostResponse.Data, bifrostModel)
		}
	}

	return bifrostResponse
}

// runwareModelArchitecture derives modality lists from a model's io: capability tags. Returns nil
// when the entry declares none, so the field stays absent rather than empty.
func runwareModelArchitecture(model RunwareModel) *schemas.Architecture {
	inputs := map[string]bool{}
	outputs := map[string]bool{}
	for _, capability := range model.Capabilities {
		pair, ok := strings.CutPrefix(capability, "io:")
		if !ok {
			continue
		}
		from, to, found := strings.Cut(pair, "-to-")
		if !found {
			continue
		}
		inputs[from] = true
		outputs[to] = true
	}
	if len(inputs) == 0 && len(outputs) == 0 && model.Architecture == "" {
		return nil
	}
	architecture := &schemas.Architecture{
		InputModalities:  sortedKeys(inputs),
		OutputModalities: sortedKeys(outputs),
	}
	if model.Architecture != "" {
		architecture.Tokenizer = &model.Architecture
	}
	return architecture
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
