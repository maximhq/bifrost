'use client'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { apiService } from '@/lib/api'
import { ProviderIconType, renderProviderIcon } from '@/lib/constants/icons'
import { PROVIDER_LABELS } from '@/lib/constants/logs'
import { ProviderResponse } from '@/lib/types/config'
import { Key, Loader2, Settings, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { toast } from 'sonner'
import { CardDescription, CardHeader, CardTitle } from '../../../components/ui/card'
import ProviderForm from './provider-form'

interface ProvidersListProps {
  providers: ProviderResponse[]
  onRefresh: () => void
}

export default function ProvidersList({ providers, onRefresh }: ProvidersListProps) {
  const [showProviderForm, setShowProviderForm] = useState(false)
  const [selectedProvider, setSelectedProvider] = useState<ProviderResponse | null>(null)
  const [deletingProvider, setDeletingProvider] = useState<string | null>(null)

  const handleDelete = async (providerKey: string) => {
    setDeletingProvider(providerKey)
    const [, error] = await apiService.deleteProvider(providerKey)
    setDeletingProvider(null)

    if (error) {
      toast.error(error)
    } else {
      toast.success('Provider deleted successfully')
      onRefresh()
    }
  }

  const handleAddProvider = () => {
    // Open with first existing provider if any exist, otherwise null (which will default to first available)
    const firstExistingProvider = providers.find((p) => p.name === 'openai') || null
    setSelectedProvider(firstExistingProvider)
    setShowProviderForm(true)
  }

  const handleEditProvider = (provider: ProviderResponse) => {
    setSelectedProvider(provider)
    setShowProviderForm(true)
  }

  const handleProviderSaved = () => {
    setShowProviderForm(false)
    setSelectedProvider(null)
    onRefresh()
  }

  const handleProviderFormCancel = () => {
    setShowProviderForm(false)
    setSelectedProvider(null)
  }

  return (
    <>
      {showProviderForm && (
        <ProviderForm
          provider={selectedProvider}
          onSave={handleProviderSaved}
          onCancel={handleProviderFormCancel}
          existingProviders={providers.map((p) => p.name)}
          allProviders={providers}
        />
      )}
      <CardHeader className="mb-4 px-0">
        <CardTitle className="flex items-center justify-between">
          <div className="flex items-center gap-2">AI Providers</div>
          <Button onClick={handleAddProvider}>
            <Settings className="h-4 w-4" />
            Manage Providers
          </Button>
        </CardTitle>
        <CardDescription>Manage AI model providers, their API keys, and configuration settings.</CardDescription>
      </CardHeader>
      <div className="rounded-sm border">
        <Table>
          <TableHeader>
            <TableRow className="px-2">
              <TableHead>Provider</TableHead>
              <TableHead>Concurrency</TableHead>
              <TableHead>Buffer Size</TableHead>
              <TableHead>Max Retries</TableHead>
              <TableHead>API Keys</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {providers.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="py-6 text-center">
                  No providers found.
                </TableCell>
              </TableRow>
            )}
            {providers.map((provider) => (
              <TableRow
                key={provider.name}
                className="hover:bg-muted/50 cursor-pointer px-2 transition-colors"
                onClick={() => handleEditProvider(provider)}
              >
                <TableCell>
                  <div className="flex items-center space-x-2">
                    {renderProviderIcon(provider.name as ProviderIconType, { size: 16 })}
                    <p className="font-medium">{PROVIDER_LABELS[provider.name] || provider.name}</p>
                  </div>
                </TableCell>
                <TableCell>
                  <div className="flex items-center space-x-2">
                    <Badge variant="outline">{provider.concurrency_and_buffer_size?.concurrency || 1}</Badge>
                  </div>
                </TableCell>
                <TableCell>
                  <div className="flex items-center space-x-2">
                    <Badge variant="outline">{provider.concurrency_and_buffer_size?.buffer_size || 10}</Badge>
                  </div>
                </TableCell>
                <TableCell>
                  <div className="flex items-center space-x-2">
                    <Badge variant="outline">{provider.network_config?.max_retries || 0}</Badge>
                  </div>
                </TableCell>
                <TableCell>
                  <div className="flex items-center space-x-2">
                    {provider.name !== 'ollama' ? (
                      <>
                        <Key className="text-muted-foreground h-4 w-4" />
                        <span className="text-sm">{provider.keys?.length || 0} keys</span>
                      </>
                    ) : (
                      <span className="text-sm">N/A</span>
                    )}
                  </div>
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex items-center justify-end space-x-2">
                    <AlertDialog>
                      <AlertDialogTrigger asChild>
                        <Button
                          onClick={(e) => e.stopPropagation()}
                          variant="outline"
                          size="sm"
                          disabled={deletingProvider === provider.name}
                        >
                          {deletingProvider === provider.name ? (
                            <Loader2 className="h-4 w-4 animate-spin" />
                          ) : (
                            <Trash2 className="h-4 w-4" />
                          )}
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent onClick={(e) => e.stopPropagation()}>
                        <AlertDialogHeader>
                          <AlertDialogTitle>Delete Provider</AlertDialogTitle>
                          <AlertDialogDescription>
                            Are you sure you want to delete provider {provider.name}? This action cannot be undone.
                          </AlertDialogDescription>
                        </AlertDialogHeader>
                        <AlertDialogFooter>
                          <AlertDialogCancel>Cancel</AlertDialogCancel>
                          <AlertDialogAction onClick={() => handleDelete(provider.name)}>Delete</AlertDialogAction>
                        </AlertDialogFooter>
                      </AlertDialogContent>
                    </AlertDialog>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </>
  )
}
