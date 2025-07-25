'use client'

import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { apiService } from '@/lib/api'
import { Team, Customer, CreateTeamRequest, UpdateTeamRequest } from '@/lib/types/governance'
import { User } from 'lucide-react'
import { useState, useEffect, useMemo } from 'react'
import { toast } from 'sonner'
import NumberAndSelect from '@/components/ui/number-and-select'
import { resetDurationOptions } from '@/lib/constants/governance'
import { Badge } from '@/components/ui/badge'
import { Validator } from '@/lib/utils/validation'
import isEqual from 'lodash.isequal'
import FormFooter from '../form-footer'
import { formatDistanceToNow } from 'date-fns'
import { parseResetPeriod } from '@/lib/utils/governance'
import { formatCurrency } from '@/lib/utils/governance'

interface TeamDialogProps {
  team?: Team | null
  customers: Customer[]
  onSave: () => void
  onCancel: () => void
}

interface TeamFormData {
  name: string
  customerId: string
  // Budget
  budgetMaxLimit: number | undefined
  budgetResetDuration: string
  isDirty: boolean
}

// Helper function to create initial state
const createInitialState = (team?: Team | null): Omit<TeamFormData, 'isDirty'> => {
  return {
    name: team?.name || '',
    customerId: team?.customer_id || '',
    // Budget
    budgetMaxLimit: team?.budget ? team.budget.max_limit : undefined, // Already in dollars
    budgetResetDuration: team?.budget?.reset_duration || '1M',
  }
}

export default function TeamDialog({ team, customers, onSave, onCancel }: TeamDialogProps) {
  const isEditing = !!team
  const [loading, setLoading] = useState(false)
  const [initialState] = useState<Omit<TeamFormData, 'isDirty'>>(createInitialState(team))
  const [formData, setFormData] = useState<TeamFormData>({
    ...initialState,
    isDirty: false,
  })

  // Track isDirty state
  useEffect(() => {
    const currentData = {
      name: formData.name,
      customerId: formData.customerId,
      budgetMaxLimit: formData.budgetMaxLimit,
      budgetResetDuration: formData.budgetResetDuration,
    }
    setFormData((prev) => ({
      ...prev,
      isDirty: !isEqual(initialState, currentData),
    }))
  }, [formData.name, formData.customerId, formData.budgetMaxLimit, formData.budgetResetDuration, initialState])

  // Validation
  const validator = useMemo(
    () =>
      new Validator([
        // Basic validation
        Validator.required(formData.name.trim(), 'Team name is required'),

        // Check if anything is dirty
        Validator.custom(formData.isDirty, 'No changes to save'),

        // Budget validation
        ...(formData.budgetMaxLimit
          ? [
              Validator.minValue(formData.budgetMaxLimit || 0, 0.01, 'Budget max limit must be greater than $0.01'),
              Validator.required(formData.budgetResetDuration, 'Budget reset duration is required'),
            ]
          : []),
      ]),
    [formData],
  )

  const updateField = <K extends keyof TeamFormData>(field: K, value: TeamFormData[K]) => {
    setFormData((prev) => ({ ...prev, [field]: value }))
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()

    if (!validator.isValid()) {
      toast.error(validator.getFirstError())
      return
    }

    setLoading(true)

    try {
      if (isEditing && team) {
        // Update existing team
        const updateData: UpdateTeamRequest = {
          name: formData.name,
          customer_id: formData.customerId,
        }

        // Add budget if enabled
        if (formData.budgetMaxLimit) {
          updateData.budget = {
            max_limit: formData.budgetMaxLimit, // Already in dollars
            reset_duration: formData.budgetResetDuration,
          }
        }

        const [, error] = await apiService.updateTeam(team.id, updateData)
        if (error) {
          toast.error(error)
          return
        }
        toast.success('Team updated successfully')
      } else {
        // Create new team
        const createData: CreateTeamRequest = {
          name: formData.name,
          customer_id: formData.customerId || undefined,
        }

        // Add budget if enabled
        if (formData.budgetMaxLimit) {
          createData.budget = {
            max_limit: formData.budgetMaxLimit, // Already in dollars
            reset_duration: formData.budgetResetDuration,
          }
        }

        const [, error] = await apiService.createTeam(createData)
        if (error) {
          toast.error(error)
          return
        }
        toast.success('Team created successfully')
      }

      onSave()
    } catch (error) {
      toast.error('Failed to save team')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Dialog open onOpenChange={onCancel}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">{isEditing ? 'Edit Team' : 'Create Team'}</DialogTitle>
          <DialogDescription>
            {isEditing ? 'Update the team information and settings.' : 'Create a new team to organize users and manage shared resources.'}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-6">
          <div className="space-y-6">
            {/* Basic Information */}
            <div className="space-y-6">
              <div className="space-y-2">
                <Label htmlFor="name">Team Name *</Label>
                <Input
                  id="name"
                  placeholder="e.g., Engineering Team"
                  value={formData.name}
                  onChange={(e) => updateField('name', e.target.value)}
                />
              </div>

              {/* Customer Assignment */}
              {customers.length > 0 && (
                <div className="w-full space-y-2">
                  <Label>Customer Assignment (optional)</Label>
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
                  <p className="text-muted-foreground text-sm">Assign this team to a customer or leave independent.</p>
                </div>
              )}
            </div>

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

            {isEditing && team?.budget && (
              <div className="space-y-2">
                <div className="flex items-center gap-2">
                  <span className="text-sm">Current Usage:</span>
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm">
                      {formatCurrency(team.budget.current_usage)} / {formatCurrency(team.budget.max_limit)}
                    </span>
                    <Badge variant={team.budget.current_usage >= team.budget.max_limit ? 'destructive' : 'default'} className="text-xs">
                      {Math.round((team.budget.current_usage / team.budget.max_limit) * 100)}%
                    </Badge>
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <span className="text-sm">Last Reset:</span>
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm">{formatDistanceToNow(new Date(team.budget.last_reset), { addSuffix: true })}</span>
                  </div>
                </div>
              </div>
            )}
          </div>

          <FormFooter validator={validator} label="Team" onCancel={onCancel} isLoading={loading} isEditing={isEditing} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
