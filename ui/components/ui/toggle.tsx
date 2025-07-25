import React from 'react'
import { Switch } from './switch'

interface Props {
  label?: string
  val: boolean
  setVal: React.Dispatch<React.SetStateAction<boolean>> | ((val: boolean) => void)
  required?: boolean
  disabled?: boolean
  caption?: string
}

const Toggle = ({ label, val, setVal, required = false, disabled = false, caption }: Props) => {
  return (
    <div className="w-full">
      <label
        className={`dark:bg-input/30 flex w-full select-none items-center justify-between gap-2 rounded-lg border px-2 py-2 text-sm ${disabled ? 'cursor-default' : 'cursor-pointer'}`}
      >
        {label && (
          <div className="">
            {label} {required && '*'}
          </div>
        )}
        <Switch checked={val} onCheckedChange={setVal} />
      </label>
      {caption && <div className="mt-1 text-xs text-gray-400">{caption}</div>}
    </div>
  )
}

export default Toggle
