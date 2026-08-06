package munsit

import (
	"github.com/maximhq/bifrost/core/schemas"
)

func ToMunsitSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest, streaming bool) *MunsitSpeechRequest {
    if bifrostReq == nil || bifrostReq.Input == nil {
        return nil
    }

    munsitReq := &MunsitSpeechRequest{
        Text: bifrostReq.Input.Input,
    }

	munsitReq.Streaming = streaming

    if bifrostReq.Params != nil {

        if bifrostReq.Params.VoiceConfig != nil &&
           bifrostReq.Params.VoiceConfig.Voice != nil {
            munsitReq.VoiceID = *bifrostReq.Params.VoiceConfig.Voice
        }

        if bifrostReq.Params.Speed != nil {
            munsitReq.Speed = *bifrostReq.Params.Speed
        }

        // munsitReq.Streaming = true

        if bifrostReq.Params.ExtraParams != nil {

            munsitReq.ExtraParams = bifrostReq.Params.ExtraParams

            if stability, ok := schemas.SafeExtractFloat64Pointer(
                bifrostReq.Params.ExtraParams["stability"],
            ); ok {
                munsitReq.Stability = *stability
                delete(munsitReq.ExtraParams, "stability")
            }
        }
    }

    return munsitReq
}