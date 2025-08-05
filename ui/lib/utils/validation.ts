import { ReactNode } from 'react'

export interface ValidationRule {
  isValid: boolean
  message: string
}

export interface ValidationConfig {
  rules: ValidationRule[]
  showAlways?: boolean // If true, shows tooltip even when field is untouched
}

export interface FieldValidation {
  isValid: boolean
  message: string
  showTooltip: boolean
}

export const validateField = (value: any, config: ValidationConfig, touched: boolean): FieldValidation => {
  const invalidRule = config.rules.find((rule) => !rule.isValid)

  return {
    isValid: !invalidRule,
    message: invalidRule?.message || '',
    showTooltip: config.showAlways || (touched && !!invalidRule),
  }
}

export interface ValidationResult {
  isValid: boolean
  errors: string[]
}

export const validateForm = (rules: ValidationRule[]): ValidationResult => {
  const invalidRules = rules.filter((rule) => !rule.isValid)
  return {
    isValid: invalidRules.length === 0,
    errors: invalidRules.map((rule) => rule.message),
  }
}

export class Validator {
  private rules: ValidationRule[]

  constructor(rules: ValidationRule[]) {
    this.rules = rules.filter((rule) => rule !== undefined)
  }

  isValid(): boolean {
    return !this.rules.some((rule) => !rule.isValid)
  }

  getErrors(): string[] {
    return this.rules.filter((rule) => !rule.isValid).map((rule) => rule.message)
  }

  getFirstError(): string | undefined {
    const firstInvalidRule = this.rules.find((rule) => !rule.isValid)
    return firstInvalidRule?.message
  }

  // Built-in validators
  static required(value: any, message = 'This field is required'): ValidationRule {
    return {
      isValid: value !== undefined && value !== null && value !== '' && value !== 0,
      message,
    }
  }

  static minValue(value: number, min: number, message = `Must be at least ${min}`): ValidationRule {
    return {
      isValid: !isNaN(value) && value >= min,
      message,
    }
  }

  static maxValue(value: number, max: number, message = `Must be at most ${max}`): ValidationRule {
    return {
      isValid: !isNaN(value) && value <= max,
      message,
    }
  }

  static pattern(value: string, regex: RegExp, message: string): ValidationRule {
    return {
      isValid: regex.test(value || ''),
      message,
    }
  }

  static email(value: string, message = 'Must be a valid email'): ValidationRule {
    return this.pattern(value, /^[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}$/i, message)
  }

  static url(value: string, message = 'Must be a valid URL'): ValidationRule {
    return this.pattern(value, /^https?:\/\/.+/, message)
  }

  static minLength(value: string, min: number, message = `Must be at least ${min} characters`): ValidationRule {
    return {
      isValid: (value || '').length >= min,
      message,
    }
  }

  static maxLength(value: string, max: number, message = `Must be at most ${max} characters`): ValidationRule {
    return {
      isValid: (value || '').length <= max,
      message,
    }
  }

  static arrayMinLength<T>(array: T[], min: number, message = `Must have at least ${min} items`): ValidationRule {
    return {
      isValid: array?.length >= min,
      message,
    }
  }

  static arrayMaxLength<T>(array: T[], max: number, message = `Must have at most ${max} items`): ValidationRule {
    return {
      isValid: array?.length <= max,
      message,
    }
  }

  static arrayUnique<T>(array: T[], message = 'Must have unique items'): ValidationRule {
    return {
      isValid: array?.length === new Set(array).size,
      message,
    }
  }

  static arraysEqual<T>(array1: T[], array2: T[], message = 'Must be equal'): ValidationRule {
    return {
      isValid: array1?.length === array2?.length && array1?.every((value, index) => value === array2[index]),
      message,
    }
  }

  static custom(isValid: boolean, message: string): ValidationRule {
    return {
      isValid,
      message,
    }
  }

  // Combine multiple validation rules
  static all(rules: ValidationRule[]): ValidationRule {
    const invalidRule = rules.find((rule) => !rule.isValid)
    return invalidRule || { isValid: true, message: '' }
  }
}

// Utility functions for validation and redaction detection

/**
 * Checks if a value is redacted based on the backend redaction patterns
 * @param value - The value to check
 * @returns true if the value is redacted
 */
export function isRedacted(value: string): boolean {
  if (!value) {
    return false
  }

  // Check if it's an environment variable reference
  if (value.startsWith('env.')) {
    return true
  }

  // Check for exact redaction pattern: 4 chars + 24 asterisks + 4 chars (total 32)
  if (value.length === 32) {
    const middle = value.substring(4, 28)
    if (middle === '*'.repeat(24)) {
      return true
    }
  }

  // Check for short key redaction (all asterisks, length <= 8)
  if (value.length <= 8 && /^\*+$/.test(value)) {
    return true
  }

  return false
}

/**
 * Checks if a JSON string is valid
 * @param value - The JSON string to validate
 * @returns true if valid JSON
 */
export function isValidJSON(value: string): boolean {
  try {
    JSON.parse(value)
    return true
  } catch {
    return false
  }
}

/**
 * Validates Vertex auth credentials
 * @param value - The auth credentials value
 * @returns true if valid (redacted, env var, or valid service account JSON)
 */
export function isValidVertexAuthCredentials(value: string): boolean {
  if (!value || !value.trim()) {
    return false
  }

  // If redacted, consider it valid (backend has the real value)
  if (isRedacted(value)) {
    return true
  }

  // If environment variable, validate format
  if (value.startsWith('env.')) {
    return value.length > 4
  }

  // Try to parse as service account JSON
  try {
    const parsed = JSON.parse(value)
    return typeof parsed === 'object' && parsed !== null && parsed.type === 'service_account' && parsed.project_id && parsed.private_key
  } catch {
    return false
  }
}

/**
 * Validates deployments configuration
 * @param value - The deployments value (object or string)
 * @returns true if valid (redacted, or valid JSON object)
 */
export function isValidDeployments(value: Record<string, string> | string | undefined): boolean {
  if (!value) {
    return false
  }

  // If it's already an object, check if it has entries
  if (typeof value === 'object') {
    return Object.keys(value).length > 0
  }

  // If it's a string, check for redaction or valid JSON
  if (typeof value === 'string') {
    // If redacted, consider it valid (backend has the real value)
    if (isRedacted(value)) {
      return true
    }

    // Try to parse as JSON
    try {
      const parsed = JSON.parse(value)
      return typeof parsed === 'object' && parsed !== null && Object.keys(parsed).length > 0
    } catch {
      return false
    }
  }

  return false
}
