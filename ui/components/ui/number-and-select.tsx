import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import React from 'react'

const NumberAndSelect = ({
  id,
  label,
  value,
  selectValue,
  onChangeNumber,
  onChangeSelect,
  options,
}: {
  id: string
  label: string
  value: string
  selectValue: string
  onChangeNumber: (value: string) => void
  onChangeSelect: (value: string) => void
  options: { label: string; value: string }[]
}) => {
  return (
    <div className="flex w-full items-center justify-between gap-4">
      <div className="grow space-y-2">
        <Label htmlFor={id}>{label}</Label>
        <Input id={id} type="number" min="1" step="1" placeholder="100" value={value} onChange={(e) => onChangeNumber(e.target.value)} />
      </div>
      <div className="w-40 space-y-2">
        <Label htmlFor={`${id}-select`}>Reset Period</Label>
        <Select value={selectValue} onValueChange={(value) => onChangeSelect(value as string)}>
          <SelectTrigger className="m-0 w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent className="w-full">
            {options.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  )
}

export default NumberAndSelect
