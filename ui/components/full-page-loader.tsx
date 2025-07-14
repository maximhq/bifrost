import { Loader2 } from 'lucide-react'

function FullPageLoader() {
  return (
    <div className="h-base flex items-center justify-center pb-24">
      <Loader2 className="h-4 w-4 animate-spin" />
    </div>
  )
}

export default FullPageLoader
