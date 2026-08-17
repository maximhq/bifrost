package runware

// runware3DImageInputIsArray records, per 3D model, whether the input image goes into
// inputs.images[] (true) or inputs.image (false). The split is per-model and comes from Runware's
// published request schemas (https://schemas.runware.ai/resolve/<model>); refresh this table when
// Runware adds a 3D model. Models absent from it fall back to the array form, which the majority
// of current models use.
//
// Runware silently drops inputs it does not recognise, so sending the wrong form yields a
// prompt-only result at full price instead of an error. Callers can override the whole object
// through the "inputs" extra param.
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
func uses3DImageArrayInput(model string) bool {
	if isArray, ok := runware3DImageInputIsArray[model]; ok {
		return isArray
	}
	return true
}
