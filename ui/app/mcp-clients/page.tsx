'use client'

import MCPClientsList from '@/app/config/views/mcp-clients-lists'
import FullPageLoader from '@/components/full-page-loader'
import { useToast } from '@/hooks/use-toast'
import { apiService } from '@/lib/api'
import { MCPClient } from '@/lib/types/mcp'
import { useEffect, useState } from 'react'
export default function MCPServersPage() {
  const [mcpClients, setMcpClients] = useState<MCPClient[]>([])
  const [isLoadingMcpClients, setIsLoadingMcpClients] = useState(true)
  const { toast } = useToast()

  useEffect(() => {
    loadMcpClients()
  }, [])

  const loadMcpClients = async () => {
    const [data, error] = await apiService.getMCPClients()
    setIsLoadingMcpClients(false)

    if (error) {
      toast({
        title: 'Error',
        description: error,
        variant: 'destructive',
      })
      return
    }

    setMcpClients(data || [])
  }

  return <div>{isLoadingMcpClients ? <FullPageLoader /> : <MCPClientsList mcpClients={mcpClients} />}</div>
}
