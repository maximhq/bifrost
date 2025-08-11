'use client'

import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { useToast } from '@/hooks/use-toast'
import { apiService } from '@/lib/api'
import { CreateMCPClientRequest, MCPClient, MCPConnectionType, MCPStdioConfig, UpdateMCPClientRequest } from '@/lib/types/mcp'
import { isArrayEqual, parseArrayFromText } from '@/lib/utils/array'
import { Validator } from '@/lib/utils/validation'
import { Info } from 'lucide-react'
import React, { useEffect, useState } from 'react'

interface ClientFormProps {
  client?: MCPClient | null
  open: boolean
  onClose: () => void
  onSaved: () => void
}

const emptyStdioConfig: MCPStdioConfig = {
  command: '',
  args: [],
  envs: [],
}

const emptyForm: CreateMCPClientRequest = {
  name: '',
  connection_type: 'http',
  connection_string: '',
  stdio_config: emptyStdioConfig,
  tools_to_skip: [],
  tools_to_execute: [],
}

const ClientForm: React.FC<ClientFormProps> = ({ client, open, onClose, onSaved }) => {
  const [form, setForm] = useState<CreateMCPClientRequest>(emptyForm)
  const [isLoading, setIsLoading] = useState(false)
  const [argsText, setArgsText] = useState('')
  const [envsText, setEnvsText] = useState('')
  const [toolsToSkipText, setToolsToSkipText] = useState('')
  const [toolsToExecuteText, setToolsToExecuteText] = useState('')
  const { toast } = useToast()

  useEffect(() => {
    if (client) {
      setForm({
        name: client.name,
        connection_type: client.config.connection_type,
        connection_string: client.config.connection_string || '',
        stdio_config: {
          command: client.config.stdio_config?.command || '',
          args: client.config.stdio_config?.args || [],
          envs: client.config.stdio_config?.envs || [],
        },
        tools_to_skip: client.config.tools_to_skip || [],
        tools_to_execute: client.config.tools_to_execute || [],
      })
      setArgsText((client.config.stdio_config?.args || []).join(', '))
      setEnvsText((client.config.stdio_config?.envs || []).join(', '))
      setToolsToSkipText((client.config.tools_to_skip || []).join(', '))
      setToolsToExecuteText((client.config.tools_to_execute || []).join(', '))
    } else {
      setForm(emptyForm)
      setArgsText('')
      setEnvsText('')
      setToolsToSkipText('')
      setToolsToExecuteText('')
    }
  }, [client])

  const handleChange = (field: keyof CreateMCPClientRequest, value: string | string[] | MCPConnectionType | MCPStdioConfig | undefined) => {
    setForm((prev) => ({ ...prev, [field]: value }))
  }

  const handleStdioConfigChange = (field: keyof MCPStdioConfig, value: string | string[]) => {
    setForm((prev) => ({
      ...prev,
      stdio_config: {
        command: '',
        args: [],
        envs: [],
        ...(prev.stdio_config || {}),
        [field]: value,
      },
    }))
  }

  const hasOverlappingTools = (executeText: string, skipText: string): boolean => {
    const executeTools = new Set(parseArrayFromText(executeText))
    const skipTools = new Set(parseArrayFromText(skipText))
    return Array.from(executeTools).some((tool) => skipTools.has(tool))
  }

  const validator = new Validator([
    // Name validation
    Validator.required(form.name?.trim(), 'Client name is required'),
    Validator.pattern(form.name || '', /^[a-zA-Z0-9-_]+$/, 'Client name can only contain letters, numbers, hyphens and underscores'),
    Validator.minLength(form.name || '', 3, 'Client name must be at least 3 characters'),
    Validator.maxLength(form.name || '', 50, 'Client name cannot exceed 50 characters'),

    // Connection type specific validation
    ...((form.connection_type === 'http' || form.connection_type === 'sse') && !client
      ? [
          Validator.required(form.connection_string?.trim(), 'Connection URL is required'),
          Validator.pattern(
            form.connection_string || '',
            /^(http:\/\/|https:\/\/|env\.[A-Z_]+$)/,
            'Connection URL must start with http://, https://, or be an environment variable (env.VAR_NAME)',
          ),
        ]
      : []),

    // STDIO validation
    ...(form.connection_type === 'stdio' && !client
      ? [
          Validator.required(form.stdio_config?.command?.trim(), 'Command is required for STDIO connections'),
          ...(!client
            ? [
                // Only validate these for new clients
                Validator.pattern(form.stdio_config?.command || '', /^[^<>|&;]+$/, 'Command cannot contain special shell characters'),
              ]
            : []),
        ]
      : []),

    // Tools validation
    ...(toolsToExecuteText.trim()
      ? [
          Validator.pattern(
            toolsToExecuteText,
            /^[a-zA-Z0-9_,\s-]+$/,
            'Tools to execute can only contain letters, numbers, underscores, and commas',
          ),
        ]
      : []),

    ...(toolsToSkipText.trim()
      ? [
          Validator.pattern(
            toolsToSkipText,
            /^[a-zA-Z0-9_,\s-]+$/,
            'Tools to skip can only contain letters, numbers, underscores, and commas',
          ),
        ]
      : []),

    // Make sure changes are made to the form
    ...(client
      ? [
          Validator.custom(
            !isArrayEqual(form.tools_to_execute || [], parseArrayFromText(toolsToExecuteText)) ||
              !isArrayEqual(form.tools_to_skip || [], parseArrayFromText(toolsToSkipText)),
            'No changes to save',
          ),
        ]
      : []),

    // Prevent having same tools in both lists
    Validator.custom(!hasOverlappingTools(toolsToExecuteText, toolsToSkipText), 'Tools cannot appear in both execute and skip lists'),
  ])

  const handleSubmit = async () => {
    setIsLoading(true)
    let error: string | null = null

    // Prepare the payload
    const payload: CreateMCPClientRequest = {
      ...form,
      stdio_config:
        form.connection_type === 'stdio'
          ? {
              command: form.stdio_config?.command || '',
              args: parseArrayFromText(argsText),
              envs: parseArrayFromText(envsText),
            }
          : undefined,
      tools_to_skip: parseArrayFromText(toolsToSkipText),
      tools_to_execute: parseArrayFromText(toolsToExecuteText),
    }

    if (client) {
      const updatePayload: UpdateMCPClientRequest = {
        tools_to_execute: payload.tools_to_execute,
        tools_to_skip: payload.tools_to_skip,
      }
      ;[, error] = await apiService.updateMCPClient(client.name, updatePayload)
    } else {
      ;[, error] = await apiService.createMCPClient(payload)
    }

    setIsLoading(false)

    if (error) {
      toast({ title: 'Error', description: error, variant: 'destructive' })
    } else {
      toast({
        title: 'Success',
        description: client ? 'Client updated' : 'Client created',
      })
      onSaved()
      onClose()
    }
  }

  return (
    <Dialog open={open} onOpenChange={onClose}>
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{client ? 'Edit MCP Client Tools' : 'New MCP Client'}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label>Name</Label>
            <Input
              value={form.name}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleChange('name', e.target.value)}
              placeholder="Client name"
              maxLength={50}
              disabled={!!client} // Disable editing name for existing clients
            />
          </div>

          {!client && (
            <>
              <div className="w-full space-y-2">
                <Label>Connection Type</Label>
                <Select value={form.connection_type} onValueChange={(value: MCPConnectionType) => handleChange('connection_type', value)}>
                  <SelectTrigger className="w-full">
                    <SelectValue placeholder="Select connection type" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="http">HTTP (Streamable)</SelectItem>
                    <SelectItem value="sse">Server-Sent Events (SSE)</SelectItem>
                    <SelectItem value="stdio">STDIO</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              {(form.connection_type === 'http' || form.connection_type === 'sse') && (
                <div className="space-y-2">
                  <div className="flex w-fit items-center gap-1">
                    <Label>Connection URL</Label>
                    <TooltipProvider>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <span>
                            <Info className="text-muted-foreground ml-1 h-3 w-3" />
                          </span>
                        </TooltipTrigger>
                        <TooltipContent className="max-w-fit">
                          <p>
                            Use <code className="rounded bg-neutral-100 px-1 py-0.5 text-neutral-800">env.&lt;VAR&gt;</code> to read the
                            value from an environment variable.
                          </p>
                        </TooltipContent>
                      </Tooltip>
                    </TooltipProvider>
                  </div>

                  <Input
                    value={form.connection_string || ''}
                    onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleChange('connection_string', e.target.value)}
                    placeholder="http://your-mcp-server:3000 or env.MCP_SERVER_URL"
                  />
                </div>
              )}

              {form.connection_type === 'stdio' && (
                <>
                  <div className="space-y-2">
                    <Label>Command</Label>
                    <Input
                      value={form.stdio_config?.command || ''}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleStdioConfigChange('command', e.target.value)}
                      placeholder="node, python, /path/to/executable"
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>Arguments (comma-separated)</Label>
                    <Input
                      value={argsText}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => setArgsText(e.target.value)}
                      placeholder="--port, 3000, --config, config.json"
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>Environment Variables (comma-separated)</Label>
                    <Input
                      value={envsText}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEnvsText(e.target.value)}
                      placeholder="API_KEY, DATABASE_URL"
                    />
                  </div>
                </>
              )}
            </>
          )}

          <div className="space-y-2">
            <Label>Tools to Execute (comma-separated, leave empty for all)</Label>
            <Textarea
              value={toolsToExecuteText}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setToolsToExecuteText(e.target.value)}
              placeholder="tool1, tool2, tool3"
              rows={2}
            />
          </div>

          <div className="space-y-2">
            <Label>Tools to Skip (comma-separated)</Label>
            <Textarea
              value={toolsToSkipText}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setToolsToSkipText(e.target.value)}
              placeholder="skipTool1, skipTool2"
              rows={2}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={isLoading}>
            Cancel
          </Button>
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <span>
                  <Button onClick={handleSubmit} disabled={!validator.isValid() || isLoading} isLoading={isLoading}>
                    {client ? 'Save' : 'Create'}
                  </Button>
                </span>
              </TooltipTrigger>
              {!validator.isValid() && <TooltipContent>{validator.getFirstError() || 'Please fix validation errors'}</TooltipContent>}
            </Tooltip>
          </TooltipProvider>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export default ClientForm
