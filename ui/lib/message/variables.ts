import { JINJA_VAR_REGEX } from './constant'
import type { Message } from './message'
import { MessageRole } from './types'

/** A map of variable name → user-supplied value */
export type VariableMap = Record<string, string>

/**
 * Extract all unique Jinja2 variable names from a single string.
 */
export function extractVariablesFromText(text: string): string[] {
  const vars = new Set<string>()
  let match: RegExpExecArray | null
  // Reset lastIndex to be safe
  JINJA_VAR_REGEX.lastIndex = 0
  while ((match = JINJA_VAR_REGEX.exec(text)) !== null) {
    vars.add(match[1])
  }
  return Array.from(vars)
}

/**
 * Extract all unique Jinja2 variable names from an array of Messages.
 * Scans content of every message (system, user, assistant, tool).
 */
export function extractVariablesFromMessages(messages: Message[]): string[] {
  const vars = new Set<string>()
  for (const msg of messages) {
    if(msg.role === MessageRole.ASSISTANT || msg.role === MessageRole.TOOL) continue;
    const content = msg.content
    if (!content) continue
    for (const v of extractVariablesFromText(content)) {
      vars.add(v)
    }
  }
  return Array.from(vars)
}

/**
 * Replace Jinja2 variables in a string with values from the provided map.
 * Variables without a value in the map are left untouched.
 */
export function replaceVariablesInText(text: string, variables: VariableMap): string {
  return text.replace(JINJA_VAR_REGEX, (fullMatch, varName: string) => {
    if (varName in variables && variables[varName] !== '') {
      return variables[varName]
    }
    return fullMatch
  })
}

/**
 * Create clones of messages with all Jinja2 variables replaced.
 * Original messages are NOT mutated.
 */
export function replaceVariablesInMessages(messages: Message[], variables: VariableMap): Message[] {
  // Fast path: nothing to replace
  const hasVars = Object.values(variables).some((v) => v !== '')
  if (!hasVars) return messages

  return messages.map((msg) => {
    const content = msg.content
    if (!content || !JINJA_VAR_REGEX.test(content)) {
      // Reset lastIndex after test
      JINJA_VAR_REGEX.lastIndex = 0
      return msg
    }
    JINJA_VAR_REGEX.lastIndex = 0
    const clone = msg.clone()
    clone.content = replaceVariablesInText(content, variables)
    return clone
  })
}

/**
 * Merge existing variable values with a new set of variable names.
 * - Keeps values for variables that still exist
 * - Adds empty values for new variables
 * - Drops variables that no longer exist in messages
 */
export function mergeVariables(currentVars: VariableMap, newVarNames: string[]): VariableMap {
  const merged: VariableMap = {}
  for (const name of newVarNames) {
    merged[name] = currentVars[name] ?? ''
  }
  return merged
}
