'use client'

import ProvidersList from '@/app/config/views/providers-list'
import FullPageLoader from '@/components/full-page-loader'
import { useToast } from '@/hooks/use-toast'
import { apiService } from '@/lib/api'
import { ProviderResponse } from '@/lib/types/config'
import { useEffect, useState } from 'react'

export default function Providers() {
  const [isLoadingProviders, setIsLoadingProviders] = useState(true)
  const [providers, setProviders] = useState<ProviderResponse[]>([])
  const { toast } = useToast()

  useEffect(() => {
    loadProviders()
  }, [])

  const loadProviders = async () => {
    const [data, error] = await apiService.getProviders()
    setIsLoadingProviders(false)

    if (error) {
      toast({
        title: 'Error',
        description: error,
        variant: 'destructive',
      })
      return
    }
    setProviders(data?.providers || [])
  }
  return <div>{isLoadingProviders ? <FullPageLoader /> : <ProvidersList providers={providers} onRefresh={loadProviders} />}</div>
}
