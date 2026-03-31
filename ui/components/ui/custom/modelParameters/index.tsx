'use client'

import { useGetModelParametersQuery } from '@/lib/store/apis/providersApi'
import { Parameter, ParameterType } from './types'
import ParameterFieldView from './paramFieldView'
import { Skeleton } from '@/components/ui/skeleton'
import { useCallback, useEffect, useMemo, useRef } from 'react'

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
  const { data, isLoading, isError } = useGetModelParametersQuery(model, {
    skip: !model,
  })

  // Ensure parameters belong to the current model (RTK Query may briefly return stale cached data)
  const datasheetModel = data?.base_model
  const parameters = useMemo(() => {
    if (!data?.model_parameters || isLoading) return []
    return data.model_parameters.filter((p) => SUPPORTED_TYPES.has(p.type))
  }, [data, isLoading])

  // Clear config when switching models — values stay undefined until the user explicitly sets them
  const prevModelRef = useRef(model)
  useEffect(() => {
    if (prevModelRef.current === model) return
    prevModelRef.current = model
    onChange({})
  }, [model, datasheetModel, parameters, onChange])

  const handleFieldChange = useCallback(
    (fieldId: string, value: any, overrides?: Record<string, any>) => {
      const next = { ...config, ...overrides }
      if (value === undefined) {
        delete next[fieldId]
      } else {
        next[fieldId] = value
      }
      onChange(next)
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
          onChange={(value, overrides) => handleFieldChange(param.id, value, overrides)}
          disabled={disabled}
          forceHideFields={hideFields}
        />
      ))}
    </div>
  )
}
