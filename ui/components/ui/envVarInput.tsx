"use client"

import { EnvVar } from "@/lib/types/schemas"
import { cn } from "@/lib/utils"
import * as React from "react"
import { useEffect, useRef } from "react"
import { Badge } from "./badge"

type BaseEnvVarInputProps = {
  value?: EnvVar
  onChange?: (value: EnvVar) => void
  inputClassName?: string
  variant?: 'input' | 'textarea'
  rows?: number
}

type InputVariantProps = BaseEnvVarInputProps & {
  variant?: 'input'
} & Omit<React.InputHTMLAttributes<HTMLInputElement>, "value" | "onChange">

type TextareaVariantProps = BaseEnvVarInputProps & {
  variant: 'textarea'
} & Omit<React.TextareaHTMLAttributes<HTMLTextAreaElement>, "value" | "onChange">

export type EnvVarInputProps = InputVariantProps | TextareaVariantProps

export const EnvVarInput = React.forwardRef<HTMLInputElement | HTMLTextAreaElement, EnvVarInputProps>(
  ({ className, value, onChange, inputClassName, variant = 'input', rows, ...props }, ref) => {
    // Extract display value from EnvVar object
    const displayValue = value?.value ?? ""
    const hasChanged = useRef(false)
    const isUserChange = useRef(false)

    // Reset hasChanged when value prop changes externally (save/switch items)
    useEffect(() => {
      if (!isUserChange.current) {
        // External change (save/switch) - reset hasChanged
        hasChanged.current = false
      }
      // Reset the flag for the next change
      isUserChange.current = false
    }, [value])

    // Show badge when value is from env (server-synced or user-typed)
    const showBadge = value?.from_env && value?.env_var

    const handleChange = (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
      const newValue = e.target.value
      hasChanged.current = true
      isUserChange.current = true
      // Auto-detect env var prefix
      if (newValue.startsWith("env.")) {
        onChange?.({ value: newValue, env_var: newValue, from_env: true })
      } else {
        onChange?.({ value: newValue, env_var: "", from_env: false })
      }
    }

    // Show hint when user is typing an env var (from_env is true but no resolved value yet)
    const showEnvHint = value?.from_env && value?.env_var && hasChanged.current

    const isTextarea = variant === 'textarea'

    const sharedClassName = cn(
      "placeholder:text-muted-foreground/70 selection:bg-primary selection:text-primary-foreground w-full min-w-0 bg-transparent px-3 py-1 text-base shadow-none outline-none disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 md:text-sm",
      inputClassName,
    )

    const containerClassName = cn(
      "dark:bg-input/30 border-input focus-within:border-primary flex w-full items-center rounded-sm border bg-transparent transition-[color,box-shadow]",
      "aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive",
      isTextarea ? "min-h-[80px] items-end" : "h-9",
      className,
    )

    return (
      <div className="w-full">
        <div className={containerClassName}>
          {isTextarea ? (
            <textarea
              ref={ref as React.Ref<HTMLTextAreaElement>}
              data-slot="textarea"
              className={cn(sharedClassName, "h-full resize-none py-2")}
              value={displayValue}
              onChange={handleChange}
              rows={rows ?? 4}
              {...(props as Omit<React.TextareaHTMLAttributes<HTMLTextAreaElement>, "value" | "onChange">)}
            />
          ) : (
            <input
              type={(props as React.InputHTMLAttributes<HTMLInputElement>).type}
              ref={ref as React.Ref<HTMLInputElement>}
              data-slot="input"
              className={cn(
                sharedClassName,
                "file:text-foreground flex h-full file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium",
              )}
              value={displayValue}
              onChange={handleChange}
              {...(props as Omit<React.InputHTMLAttributes<HTMLInputElement>, "value" | "onChange">)}
            />
          )}
          {showBadge && (
            <Badge variant="success" className={cn("mr-2 whitespace-nowrap", isTextarea && "mb-2")}>
              {value?.env_var}
            </Badge>
          )}
        </div>
        {showEnvHint && <p className="mt-1.5 text-xs text-orange-400">The resolved value will appear after saving</p>}
      </div>
    )
  },
)

EnvVarInput.displayName = "EnvVarInput"
