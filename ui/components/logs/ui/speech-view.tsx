import React from 'react'
import { BifrostSpeech, SpeechInput } from '@/lib/types/logs'
import { Play, Volume2 } from 'lucide-react'
import AudioPlayer from './audio-player'

interface SpeechViewProps {
  speechInput?: SpeechInput
  speechOutput?: BifrostSpeech
  isStreaming?: boolean
}

export default function SpeechView({ speechInput, speechOutput, isStreaming }: SpeechViewProps) {
  return (
    <div className="space-y-4">
      {/* Speech Input */}
      {speechInput && (
        <div className="w-full rounded-sm border">
          <div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
            <Volume2 className="h-4 w-4" />
            Speech Input
          </div>
          <div className="space-y-4 p-6">
            <div>
              <div className="text-muted-foreground mb-2 text-xs font-medium">TEXT TO SYNTHESIZE</div>
              <div className="font-mono text-xs">{speechInput.input}</div>
            </div>

            {speechInput.instructions && (
              <div>
                <div className="text-muted-foreground mb-2 text-xs font-medium">INSTRUCTIONS</div>
                <div className="font-mono text-xs">{speechInput.instructions}</div>
              </div>
            )}

            <div className="grid grid-cols-2 gap-4">
              <div>
                <div className="text-muted-foreground mb-2 text-xs font-medium">VOICE</div>
                <div className="font-mono text-xs">
                  {typeof speechInput.voice === 'string' ? speechInput.voice : JSON.stringify(speechInput.voice)}
                </div>
              </div>

              {speechInput.response_format && (
                <div>
                  <div className="text-muted-foreground mb-2 text-xs font-medium">FORMAT</div>
                  <div className="font-mono text-xs">{speechInput.response_format}</div>
                </div>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Speech Output */}
      {(speechOutput || isStreaming) && (
        <div className="w-full rounded-sm border">
          <div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
            <Play className="h-4 w-4" />
            Speech Output
          </div>
          <div className="space-y-4 p-6">
            {isStreaming ? <div className="font-mono text-xs">Output was streamed.</div> : <AudioPlayer src={speechOutput?.audio || ''} />}
          </div>
        </div>
      )}
    </div>
  )
}
