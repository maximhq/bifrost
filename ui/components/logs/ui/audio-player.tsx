import { Button } from '@/components/ui/button'
import { Pause, Play, Download } from 'lucide-react'
import { useState } from 'react'

const AudioPlayer = ({ src }: { src: string }) => {
  const [isPlaying, setIsPlaying] = useState(false)
  const [audio] = useState<HTMLAudioElement | null>(typeof window !== 'undefined' ? new Audio() : null)

  const handlePlayPause = () => {
    if (!audio || !src) return

    if (isPlaying) {
      audio.pause()
      setIsPlaying(false)
    } else {
      // Convert base64 to blob URL
      const audioBlob = new Blob([Uint8Array.from(atob(src), (c) => c.charCodeAt(0))], {
        type: 'audio/mpeg',
      })
      const audioUrl = URL.createObjectURL(audioBlob)
      audio.src = audioUrl
      audio.play()
      setIsPlaying(true)

      audio.onended = () => {
        setIsPlaying(false)
        URL.revokeObjectURL(audioUrl)
      }
    }
  }

  const handleDownload = () => {
    if (!src) return

    const audioBlob = new Blob([Uint8Array.from(atob(src), (c) => c.charCodeAt(0))], {
      type: 'audio/mpeg',
    })
    const audioUrl = URL.createObjectURL(audioBlob)

    const a = document.createElement('a')
    a.href = audioUrl
    a.download = 'speech-output.mp3'
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(audioUrl)
  }
  return (
    <div className="flex items-center gap-2">
      <Button onClick={handlePlayPause} variant="outline" size="sm" className="flex items-center gap-2">
        {isPlaying ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4" />}
        {isPlaying ? 'Pause' : 'Play'}
      </Button>

      <Button onClick={handleDownload} variant="outline" size="sm" className="flex items-center gap-2">
        <Download className="h-4 w-4" />
        Download
      </Button>
    </div>
  )
}

export default AudioPlayer
