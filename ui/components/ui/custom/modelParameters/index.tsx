'use client'

import { useGetModelDatasheetQuery } from '@/lib/store/apis/providersApi'
import { Parameter, ParameterType } from './modelParameterFields/types'
import ParameterFieldView from './modelParameterFields/paramFieldView'
import { Skeleton } from '@/components/ui/skeleton'
import { useCallback, useMemo } from 'react'

const SUPPORTED_TYPES = new Set<string>(Object.values(ParameterType))

interface ModelParametersProps {
  model: string
  config: Record<string, any>
  onChange: (config: Record<string, any>) => void
  disabled?: boolean
  /** Parameter IDs to exclude from rendering */
  hideFields?: string[]
}

function ModelParametersSkeleton() {
  return (
    <div className="flex flex-col gap-6">
      {Array.from({ length: 4 }).map((_, i) => (
        <div key={i} className="flex flex-col gap-2">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-8 w-full" />
        </div>
      ))}
    </div>
  )
}

export default function ModelParameters({
  model,
  config,
  onChange,
  disabled,
  hideFields,
}: ModelParametersProps) {
  const { data, isLoading, isError } = useGetModelDatasheetQuery(model, {
    skip: !model,
  })

  const parameters = useMemo(() => {
    if (!data?.model_parameters) return []
    return data.model_parameters.filter((p) => SUPPORTED_TYPES.has(p.type))
  }, [data])

  const handleFieldChange = useCallback(
    (fieldId: string, value: any) => {
      if (value === undefined) {
        const next = { ...config }
        delete next[fieldId]
        onChange(next)
      } else {
        onChange({ ...config, [fieldId]: value })
      }
    },
    [config, onChange]
  )

  if (isLoading) return <ModelParametersSkeleton />

  if (isError || parameters.length === 0) return null

  return (
    <div className="flex flex-col gap-6">
      {parameters.map((param) => (
        <ParameterFieldView
          key={param.id}
          field={param as Parameter}
          config={config}
          onChange={(value) => handleFieldChange(param.id, value)}
          disabled={disabled}
          forceHideFields={hideFields}
        />
      ))}
    </div>
  )
}
