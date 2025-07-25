'use client'

import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { apiService } from '@/lib/api'
import { Customer, CreateCustomerRequest, UpdateCustomerRequest } from '@/lib/types/governance'
import { User, DollarSign } from 'lucide-react'
import { useState, useEffect, useMemo } from 'react'
import { toast } from 'sonner'
import NumberAndSelect from '@/components/ui/number-and-select'
import { resetDurationOptions } from '@/lib/constants/governance'
import { Badge } from '@/components/ui/badge'
import { Validator } from '@/lib/utils/validation'
import isEqual from 'lodash.isequal'
import FormFooter from '../form-footer'
import { formatDistanceToNow } from 'date-fns'
import { formatCurrency } from '@/lib/utils/governance'

interface CustomerDialogProps {
  customer?: Customer | null
  onSave: () => void
  onCancel: () => void
}

interface CustomerFormData {
  name: string
  // Budget
  budgetMaxLimit: number | undefined
  budgetResetDuration: string
  isDirty: boolean
}

// Helper function to create initial state
const createInitialState = (customer?: Customer | null): Omit<CustomerFormData, 'isDirty'> => {
  return {
    name: customer?.name || '',
    // Budget
    budgetMaxLimit: customer?.budget ? customer.budget.max_limit : undefined, // Already in dollars
    budgetResetDuration: customer?.budget?.reset_duration || '1M',
  }
}

export default function CustomerDialog({ customer, onSave, onCancel }: CustomerDialogProps) {
  const isEditing = !!customer
  const [loading, setLoading] = useState(false)
  const [initialState] = useState<Omit<CustomerFormData, 'isDirty'>>(createInitialState(customer))
  const [formData, setFormData] = useState<CustomerFormData>({
    ...initialState,
    isDirty: false,
  })

  // Track isDirty state
  useEffect(() => {
    const currentData = {
      name: formData.name,
      budgetMaxLimit: formData.budgetMaxLimit,
      budgetResetDuration: formData.budgetResetDuration,
    }
    setFormData((prev) => ({
      ...prev,
      isDirty: !isEqual(initialState, currentData),
    }))
  }, [formData.name, formData.budgetMaxLimit, formData.budgetResetDuration, initialState])

  // Validation
  const validator = useMemo(
    () =>
      new Validator([
        // Basic validation
        Validator.required(formData.name.trim(), 'Customer name is required'),

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

  const updateField = <K extends keyof CustomerFormData>(field: K, value: CustomerFormData[K]) => {
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
      if (isEditing && customer) {
        // Update existing customer
        const updateData: UpdateCustomerRequest = {
          name: formData.name,
        }

        // Add budget if enabled
        if (formData.budgetMaxLimit) {
          updateData.budget = {
            max_limit: formData.budgetMaxLimit, // Already in dollars
            reset_duration: formData.budgetResetDuration,
          }
        }

        const [, error] = await apiService.updateCustomer(customer.id, updateData)
        if (error) {
          toast.error(error)
          return
        }
        toast.success('Customer updated successfully')
      } else {
        // Create new customer
        const createData: CreateCustomerRequest = {
          name: formData.name,
        }

        // Add budget if enabled
        if (formData.budgetMaxLimit) {
          createData.budget = {
            max_limit: formData.budgetMaxLimit, // Already in dollars
            reset_duration: formData.budgetResetDuration,
          }
        }

        const [, error] = await apiService.createCustomer(createData)
        if (error) {
          toast.error(error)
          return
        }
        toast.success('Customer created successfully')
      }

      onSave()
    } catch (error) {
      toast.error('Failed to save customer')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Dialog open onOpenChange={onCancel}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">{isEditing ? 'Edit Customer' : 'Create Customer'}</DialogTitle>
          <DialogDescription>
            {isEditing
              ? 'Update the customer information and settings.'
              : 'Create a new customer account to organize teams and manage resources.'}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-6">
          <div className="space-y-6">
            {/* Basic Information */}
            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="name">Customer Name *</Label>
                <Input
                  id="name"
                  placeholder="e.g., Acme Corporation"
                  value={formData.name}
                  onChange={(e) => updateField('name', e.target.value)}
                />
                <p className="text-muted-foreground text-sm">This name will be used to identify the customer account.</p>
              </div>
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

            {isEditing && customer?.budget && (
              <Card>
                <CardHeader>
                  <CardTitle className="flex items-center gap-2">
                    <DollarSign className="h-5 w-5" />
                    Current Budget
                  </CardTitle>
                </CardHeader>
                <CardContent>
                  <div className="space-y-2">
                    <div className="flex justify-between">
                      <span>Current Usage:</span>
                      <span>${customer.budget.current_usage.toFixed(2)}</span>
                    </div>
                    <div className="flex justify-between">
                      <span>Budget Limit:</span>
                      <span>${customer.budget.max_limit.toFixed(2)}</span>
                    </div>
                    <div className="flex justify-between">
                      <span>Reset Period:</span>
                      <span>{customer.budget.reset_duration}</span>
                    </div>
                    <div className="h-2 w-full rounded-full bg-gray-200">
                      <div
                        className="h-2 rounded-full bg-blue-600"
                        style={{
                          width: `${Math.min((customer.budget.current_usage / customer.budget.max_limit) * 100, 100)}%`,
                        }}
                      ></div>
                    </div>
                  </div>
                  <p className="text-muted-foreground mt-2 text-sm">
                    Budget management for existing customers should be done through the budget edit dialog.
                  </p>
                </CardContent>
              </Card>
            )}

            {isEditing && customer?.budget && (
              <div className="space-y-2">
                <div className="flex items-center gap-2">
                  <span className="text-sm">Current Usage:</span>
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm">
                      {formatCurrency(customer.budget.current_usage)} / {formatCurrency(customer.budget.max_limit)}
                    </span>
                    <Badge
                      variant={customer.budget.current_usage >= customer.budget.max_limit ? 'destructive' : 'default'}
                      className="text-xs"
                    >
                      {Math.round((customer.budget.current_usage / customer.budget.max_limit) * 100)}%
                    </Badge>
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <span className="text-sm">Last Reset:</span>
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm">
                      {formatDistanceToNow(new Date(customer.budget.last_reset), { addSuffix: true })}
                    </span>
                  </div>
                </div>
              </div>
            )}
          </div>

          <FormFooter validator={validator} label="Customer" onCancel={onCancel} isLoading={loading} isEditing={isEditing} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
