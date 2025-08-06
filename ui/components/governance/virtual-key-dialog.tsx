'use client'

import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { TagInput } from '@/components/ui/tag-input'
import { apiService } from '@/lib/api'
import { VirtualKey, Team, Customer, CreateVirtualKeyRequest, UpdateVirtualKeyRequest } from '@/lib/types/governance'
import { Users, User } from 'lucide-react'
import { useState, useEffect, useMemo } from 'react'
import { toast } from 'sonner'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { Info } from 'lucide-react'
import { Validator } from '@/lib/utils/validation'
import isEqual from 'lodash.isequal'
import { DottedSeparator } from '@/components/ui/separator'
import Toggle from '@/components/ui/toggle'
import NumberAndSelect from '@/components/ui/number-and-select'
import { resetDurationOptions } from '@/lib/constants/governance'
import FormFooter from '../form-footer'

interface VirtualKeyDialogProps {
  virtualKey?: VirtualKey | null
  teams: Team[]
  customers: Customer[]
  onSave: () => void
  onCancel: () => void
}

interface VirtualKeyFormData {
  name: string
  description: string
  allowedModels: string[]
  allowedProviders: string[]
  entityType: 'team' | 'customer' | 'none'
  teamId: string
  customerId: string
  isActive: boolean
  // Budget
  budgetMaxLimit: number | undefined
  budgetResetDuration: string
  // Token limits
  tokenMaxLimit: number | undefined
  tokenResetDuration: string
  // Request limits
  requestMaxLimit: number | undefined
  requestResetDuration: string
  isDirty: boolean
}

// Helper function to create initial state
const createInitialState = (virtualKey?: VirtualKey | null): Omit<VirtualKeyFormData, 'isDirty'> => {
  return {
    name: virtualKey?.name || '',
    description: virtualKey?.description || '',
    allowedModels: virtualKey?.allowed_models || [],
    allowedProviders: virtualKey?.allowed_providers || [],
    entityType: virtualKey?.team_id ? 'team' : virtualKey?.customer_id ? 'customer' : 'none',
    teamId: virtualKey?.team_id || '',
    customerId: virtualKey?.customer_id || '',
    isActive: virtualKey?.is_active ?? true,
    // Budget
    budgetMaxLimit: virtualKey?.budget ? virtualKey.budget.max_limit : undefined, // Already in dollars
    budgetResetDuration: virtualKey?.budget?.reset_duration || '1M',
    // Token limits
    tokenMaxLimit: virtualKey?.rate_limit?.token_max_limit || undefined,
    tokenResetDuration: virtualKey?.rate_limit?.token_reset_duration || '1h',
    // Request limits
    requestMaxLimit: virtualKey?.rate_limit?.request_max_limit || undefined,
    requestResetDuration: virtualKey?.rate_limit?.request_reset_duration || '1h',
  }
}

export default function VirtualKeyDialog({ virtualKey, teams, customers, onSave, onCancel }: VirtualKeyDialogProps) {
  const isEditing = !!virtualKey
  const [isLoading, setIsLoading] = useState(false)
  const [initialState] = useState<Omit<VirtualKeyFormData, 'isDirty'>>(createInitialState(virtualKey))
  const [formData, setFormData] = useState<VirtualKeyFormData>({
    ...initialState,
    isDirty: false,
  })

  // Track isDirty state
  useEffect(() => {
    const currentData = {
      name: formData.name,
      description: formData.description,
      allowedModels: formData.allowedModels,
      allowedProviders: formData.allowedProviders,
      entityType: formData.entityType,
      teamId: formData.teamId,
      customerId: formData.customerId,
      isActive: formData.isActive,
      budgetMaxLimit: formData.budgetMaxLimit,
      budgetResetDuration: formData.budgetResetDuration,
      tokenMaxLimit: formData.tokenMaxLimit,
      tokenResetDuration: formData.tokenResetDuration,
      requestMaxLimit: formData.requestMaxLimit,
      requestResetDuration: formData.requestResetDuration,
    }
    setFormData((prev) => ({
      ...prev,
      isDirty: !isEqual(initialState, currentData),
    }))
  }, [
    formData.name,
    formData.description,
    formData.allowedModels,
    formData.allowedProviders,
    formData.entityType,
    formData.teamId,
    formData.customerId,
    formData.isActive,
    formData.budgetMaxLimit,
    formData.budgetResetDuration,
    formData.tokenMaxLimit,
    formData.tokenResetDuration,
    formData.requestMaxLimit,
    formData.requestResetDuration,
    initialState,
  ])

  // Validation
  const validator = useMemo(
    () =>
      new Validator([
        // Basic validation
        Validator.required(formData.name.trim(), 'Virtual key name is required'),

        // Check if anything is dirty
        Validator.custom(formData.isDirty, 'No changes to save'),

        // Entity validation
        Validator.custom(
          formData.entityType === 'none' ||
            (formData.entityType === 'team' && !!formData.teamId) ||
            (formData.entityType === 'customer' && !!formData.customerId),
          'Please select a valid team or customer assignment',
        ),

        // Budget validation
        ...(formData.budgetMaxLimit
          ? [
              Validator.minValue(formData.budgetMaxLimit, 0.01, 'Budget max limit must be greater than $0.01'),
              Validator.required(formData.budgetResetDuration, 'Budget reset duration is required'),
            ]
          : []),

        // Rate limit validation - at least one limit must be set if rate limiting is enabled
        ...(formData.tokenMaxLimit || formData.requestMaxLimit
          ? [
              // Token limit validation
              ...(formData.tokenMaxLimit
                ? [
                    Validator.required(formData.tokenMaxLimit, 'Token max limit is required when token limiting is enabled'),
                    Validator.minValue(formData.tokenMaxLimit || 0, 1, 'Token max limit must be at least 1'),
                    Validator.required(formData.tokenResetDuration, 'Token reset duration is required'),
                  ]
                : []),
              // Request limit validation
              ...(formData.requestMaxLimit
                ? [
                    Validator.required(formData.requestMaxLimit, 'Request max limit is required when request limiting is enabled'),
                    Validator.minValue(formData.requestMaxLimit || 0, 1, 'Request max limit must be at least 1'),
                    Validator.required(formData.requestResetDuration, 'Request reset duration is required'),
                  ]
                : []),
            ]
          : []),
      ]),
    [formData],
  )

  const updateField = <K extends keyof VirtualKeyFormData>(field: K, value: VirtualKeyFormData[K]) => {
    setFormData((prev) => ({ ...prev, [field]: value }))
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()

    if (!validator.isValid()) {
      toast.error(validator.getFirstError())
      return
    }

    setIsLoading(true)

    try {
      if (isEditing && virtualKey) {
        // Update existing virtual key
        const updateData: UpdateVirtualKeyRequest = {
          description: formData.description || undefined,
          allowed_models: formData.allowedModels,
          allowed_providers: formData.allowedProviders,
          team_id: formData.entityType === 'team' ? formData.teamId : undefined,
          customer_id: formData.entityType === 'customer' ? formData.customerId : undefined,
          is_active: formData.isActive,
        }

        // Add budget if enabled
        if (formData.budgetMaxLimit) {
          updateData.budget = {
            max_limit: formData.budgetMaxLimit, // Already in dollars
            reset_duration: formData.budgetResetDuration,
          }
        }

        // Add rate limit if enabled
        if (formData.tokenMaxLimit || formData.requestMaxLimit) {
          updateData.rate_limit = {
            token_max_limit: formData.tokenMaxLimit,
            token_reset_duration: formData.tokenResetDuration,
            request_max_limit: formData.requestMaxLimit,
            request_reset_duration: formData.requestResetDuration,
          }
        }

        const [, error] = await apiService.updateVirtualKey(virtualKey.id, updateData)
        if (error) {
          toast.error(error)
          return
        }
        toast.success('Virtual key updated successfully')
      } else {
        // Create new virtual key
        const createData: CreateVirtualKeyRequest = {
          name: formData.name,
          description: formData.description || undefined,
          allowed_models: formData.allowedModels.length > 0 ? formData.allowedModels : undefined,
          allowed_providers: formData.allowedProviders.length > 0 ? formData.allowedProviders : undefined,
          team_id: formData.entityType === 'team' ? formData.teamId : undefined,
          customer_id: formData.entityType === 'customer' ? formData.customerId : undefined,
          is_active: formData.isActive,
        }

        // Add budget if enabled
        if (formData.budgetMaxLimit) {
          createData.budget = {
            max_limit: formData.budgetMaxLimit, // Already in dollars
            reset_duration: formData.budgetResetDuration,
          }
        }

        // Add rate limit if enabled
        if (formData.tokenMaxLimit || formData.requestMaxLimit) {
          createData.rate_limit = {
            token_max_limit: formData.tokenMaxLimit,
            token_reset_duration: formData.tokenResetDuration,
            request_max_limit: formData.requestMaxLimit,
            request_reset_duration: formData.requestResetDuration,
          }
        }

        const [, error] = await apiService.createVirtualKey(createData)
        if (error) {
          toast.error(error)
          return
        }
        toast.success('Virtual key created successfully')
      }

      onSave()
    } catch (error) {
      toast.error('Failed to save virtual key')
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <Dialog open onOpenChange={onCancel}>
      <DialogContent className="custom-scrollbar max-h-[90vh] max-w-4xl overflow-y-auto p-0">
        <DialogHeader className="z-10 border-b px-6 pt-6">
          <DialogTitle className="flex items-center gap-2">{isEditing ? virtualKey?.name : 'Create Virtual Key'}</DialogTitle>
          <DialogDescription>
            {isEditing
              ? 'Update the virtual key configuration and permissions.'
              : 'Create a new virtual key with specific permissions, budgets, and rate limits.'}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="px-6">
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="name">Name *</Label>
              <Input
                id="name"
                placeholder="e.g., Production API Key"
                value={formData.name}
                onChange={(e) => updateField('name', e.target.value)}
                maxLength={50}
                disabled={isEditing} // Can't change name when editing
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="description">Description</Label>
              <Textarea
                id="description"
                placeholder="This key is used for..."
                value={formData.description}
                onChange={(e) => updateField('description', e.target.value)}
                rows={3}
              />
            </div>

            <Toggle label="Is this key active?" val={formData.isActive} setVal={(val: boolean) => updateField('isActive', val)} />

            <DottedSeparator className="mb-5 mt-6" />

            <div className="space-y-2">
              <div className="flex items-center gap-2">
                <label className="text-sm font-medium">Allowed Models</label>
                <TooltipProvider>
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <span>
                        <Info className="text-muted-foreground h-3 w-3" />
                      </span>
                    </TooltipTrigger>
                    <TooltipContent>
                      <p>Comma-separated list of models this key applies to. Leave blank for all models.</p>
                    </TooltipContent>
                  </Tooltip>
                </TooltipProvider>
              </div>
              <TagInput
                placeholder="e.g. gpt-4, gpt-3.5-turbo"
                value={formData.allowedModels}
                onValueChange={(value) => updateField('allowedModels', value)}
              />
            </div>

            <div className="space-y-2">
              <div className="flex items-center gap-2">
                <label className="text-sm font-medium">Allowed Providers</label>
                <TooltipProvider>
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <span>
                        <Info className="text-muted-foreground h-3 w-3" />
                      </span>
                    </TooltipTrigger>
                    <TooltipContent>
                      <p>Comma-separated list of providers this key applies to. Leave blank for all providers.</p>
                    </TooltipContent>
                  </Tooltip>
                </TooltipProvider>
              </div>
              <TagInput
                placeholder="e.g. openai, anthropic"
                value={formData.allowedProviders}
                onValueChange={(value) => updateField('allowedProviders', value)}
              />
            </div>

            <DottedSeparator className="mb-5 mt-6" />

            {/* Budget Configuration */}
            <NumberAndSelect
              id="budgetMaxLimit"
              label="Maximum Spend (USD)"
              value={formData.budgetMaxLimit?.toString() || ''}
              selectValue={formData.budgetResetDuration}
              onChangeNumber={(value) => updateField('budgetMaxLimit', parseFloat(value) || 0)}
              onChangeSelect={(value) => updateField('budgetResetDuration', value)}
              options={resetDurationOptions}
            />

            {/* Rate Limiting Configuration */}
            <div className="space-y-4">
              <NumberAndSelect
                id="tokenMaxLimit"
                label="Maximum Tokens"
                value={formData.tokenMaxLimit?.toString() || ''}
                selectValue={formData.tokenResetDuration}
                onChangeNumber={(value) => updateField('tokenMaxLimit', parseInt(value) || 0)}
                onChangeSelect={(value) => updateField('tokenResetDuration', value)}
                options={resetDurationOptions}
              />

              <NumberAndSelect
                id="requestMaxLimit"
                label="Maximum Requests"
                value={formData.requestMaxLimit?.toString() || ''}
                selectValue={formData.requestResetDuration}
                onChangeNumber={(value) => updateField('requestMaxLimit', parseInt(value) || 0)}
                onChangeSelect={(value) => updateField('requestResetDuration', value)}
                options={resetDurationOptions}
              />
            </div>

            <DottedSeparator className="my-6" />

            {/* Entity Assignment */}
            <div className="space-y-4">
              <div className="space-y-2">
                <Label>Assignment Type</Label>
                <Select
                  value={formData.entityType}
                  onValueChange={(value: 'team' | 'customer' | 'none') => updateField('entityType', value)}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent className="w-full">
                    <SelectItem value="none">No Assignment</SelectItem>
                    {teams.length > 0 && <SelectItem value="team">Assign to Team</SelectItem>}
                    {customers.length > 0 && <SelectItem value="customer">Assign to Customer</SelectItem>}
                  </SelectContent>
                </Select>
              </div>

              {formData.entityType === 'team' && teams.length > 0 && (
                <div className="space-y-2">
                  <Label>Select Team</Label>
                  <Select value={formData.teamId} onValueChange={(value) => updateField('teamId', value)}>
                    <SelectTrigger className="w-full">
                      <SelectValue placeholder="Select a team" />
                    </SelectTrigger>
                    <SelectContent className="w-full">
                      {teams.map((team) => (
                        <SelectItem key={team.id} value={team.id}>
                          <div className="flex items-center gap-2">
                            <Users className="h-4 w-4" />
                            {team.name}
                            {team.customer && <span className="text-muted-foreground">({team.customer.name})</span>}
                          </div>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              )}

              {formData.entityType === 'customer' && customers.length > 0 && (
                <div className="space-y-2">
                  <Label>Select Customer</Label>
                  <Select value={formData.customerId} onValueChange={(value) => updateField('customerId', value)}>
                    <SelectTrigger className="w-full">
                      <SelectValue placeholder="Select a customer" />
                    </SelectTrigger>
                    <SelectContent className="w-full">
                      {customers.map((customer) => (
                        <SelectItem key={customer.id} value={customer.id}>
                          <div className="flex items-center gap-2">
                            <User className="h-4 w-4" />
                            {customer.name}
                          </div>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              )}
            </div>
          </div>
          <div className="bg-background sticky bottom-0 py-3">
            <FormFooter validator={validator} label="Virtual Key" onCancel={onCancel} isLoading={isLoading} isEditing={isEditing} />
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
