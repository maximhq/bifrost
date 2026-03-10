import { describe, it, expect } from 'vitest'
import { Message } from './message'
import {
  extractVariablesFromText,
  extractVariablesFromMessages,
  replaceVariablesInText,
  replaceVariablesInMessages,
  mergeVariables,
} from './variables'

// =============================================================================
// extractVariablesFromText
// =============================================================================

describe('extractVariablesFromText', () => {
  it('extracts a single variable', () => {
    expect(extractVariablesFromText('Hello {{ name }}')).toEqual(['name'])
  })

  it('extracts multiple distinct variables', () => {
    expect(extractVariablesFromText('{{ greeting }}, {{ name }}!')).toEqual(['greeting', 'name'])
  })

  it('deduplicates repeated variables', () => {
    expect(extractVariablesFromText('{{ x }} and {{ x }}')).toEqual(['x'])
  })

  it('returns empty array when no variables exist', () => {
    expect(extractVariablesFromText('Hello world')).toEqual([])
  })

  it('returns empty array for empty string', () => {
    expect(extractVariablesFromText('')).toEqual([])
  })

  it('handles variable with no spaces inside braces', () => {
    expect(extractVariablesFromText('{{name}}')).toEqual(['name'])
  })

  it('handles variable with extra whitespace inside braces', () => {
    expect(extractVariablesFromText('{{   name   }}')).toEqual(['name'])
  })

  it('handles underscored variable names', () => {
    expect(extractVariablesFromText('{{ user_name }}')).toEqual(['user_name'])
  })

  it('handles dot-notation variable names', () => {
    expect(extractVariablesFromText('{{ user.name }}')).toEqual(['user.name'])
  })

  it('handles variables with numbers in name', () => {
    expect(extractVariablesFromText('{{ item1 }} {{ item2 }}')).toEqual(['item1', 'item2'])
  })

  it('handles variable starting with underscore', () => {
    expect(extractVariablesFromText('{{ _private }}')).toEqual(['_private'])
  })

  it('does not extract variables starting with a number', () => {
    expect(extractVariablesFromText('{{ 1abc }}')).toEqual([])
  })

  it('does not extract variables with special characters', () => {
    expect(extractVariablesFromText('{{ na-me }}')).toEqual([])
  })

  it('handles multiline text with variables', () => {
    const text = `Line one {{ first }}
Line two {{ second }}
Line three`
    expect(extractVariablesFromText(text)).toEqual(['first', 'second'])
  })

  it('ignores jinja2 block tags', () => {
    expect(extractVariablesFromText('{% if condition %}yes{% endif %}')).toEqual([])
  })

  it('ignores jinja2 comments', () => {
    expect(extractVariablesFromText('{# this is a comment #}')).toEqual([])
  })

  it('extracts variables adjacent to jinja2 block tags', () => {
    const text = '{% if show %}{{ name }}{% endif %}'
    expect(extractVariablesFromText(text)).toEqual(['name'])
  })

  it('handles triple braces (not valid jinja2) gracefully', () => {
    // {{{ name }}} — the regex should still find "name" from the inner {{ }}
    const result = extractVariablesFromText('{{{ name }}}')
    expect(result).toEqual(['name'])
  })

  it('handles variables embedded in longer text', () => {
    const text = 'Dear {{ title }} {{ last_name }}, your order #{{ order_id }} is ready.'
    expect(extractVariablesFromText(text)).toEqual(['title', 'last_name', 'order_id'])
  })

  // Regression: calling extractVariablesFromText consecutively should work
  // (ensures regex lastIndex is properly reset)
  it('works correctly when called multiple times in succession', () => {
    expect(extractVariablesFromText('{{ a }}')).toEqual(['a'])
    expect(extractVariablesFromText('{{ b }}')).toEqual(['b'])
    expect(extractVariablesFromText('{{ a }} {{ c }}')).toEqual(['a', 'c'])
  })
})

// =============================================================================
// extractVariablesFromMessages
// =============================================================================

describe('extractVariablesFromMessages', () => {
  it('extracts variables from a system message', () => {
    const messages = [Message.system('You are {{ role }}')]
    expect(extractVariablesFromMessages(messages)).toEqual(['role'])
  })

  it('extracts variables from a user message', () => {
    const messages = [Message.request('Tell me about {{ topic }}')]
    expect(extractVariablesFromMessages(messages)).toEqual(['topic'])
  })

  it('extracts variables from an assistant message', () => {
    const messages = [Message.response('Hello {{ name }}')]
    expect(extractVariablesFromMessages(messages)).toEqual(['name'])
  })

  it('extracts variables across multiple messages', () => {
    const messages = [
      Message.system('You are {{ role }}'),
      Message.request('Tell me about {{ topic }}'),
      Message.response('The {{ topic }} is interesting'),
    ]
    expect(extractVariablesFromMessages(messages)).toEqual(['role', 'topic'])
  })

  it('deduplicates across messages', () => {
    const messages = [
      Message.system('{{ name }}'),
      Message.request('{{ name }}'),
    ]
    expect(extractVariablesFromMessages(messages)).toEqual(['name'])
  })

  it('returns empty array when no messages have variables', () => {
    const messages = [
      Message.system('You are a helpful assistant'),
      Message.request('Hello'),
    ]
    expect(extractVariablesFromMessages(messages)).toEqual([])
  })

  it('returns empty array for empty messages array', () => {
    expect(extractVariablesFromMessages([])).toEqual([])
  })

  it('handles messages with empty content', () => {
    const messages = [Message.system('')]
    expect(extractVariablesFromMessages(messages)).toEqual([])
  })

  it('handles error messages gracefully', () => {
    const messages = [Message.error('Something went wrong')]
    expect(extractVariablesFromMessages(messages)).toEqual([])
  })
})

// =============================================================================
// replaceVariablesInText
// =============================================================================

describe('replaceVariablesInText', () => {
  it('replaces a single variable', () => {
    expect(replaceVariablesInText('Hello {{ name }}', { name: 'World' })).toBe('Hello World')
  })

  it('replaces multiple variables', () => {
    const result = replaceVariablesInText('{{ greeting }}, {{ name }}!', {
      greeting: 'Hi',
      name: 'Alice',
    })
    expect(result).toBe('Hi, Alice!')
  })

  it('replaces all occurrences of the same variable', () => {
    expect(replaceVariablesInText('{{ x }} and {{ x }}', { x: 'yes' })).toBe('yes and yes')
  })

  it('leaves variable untouched when not in map', () => {
    expect(replaceVariablesInText('{{ unknown }}', {})).toBe('{{ unknown }}')
  })

  it('leaves variable untouched when value is empty string', () => {
    expect(replaceVariablesInText('{{ name }}', { name: '' })).toBe('{{ name }}')
  })

  it('returns original text when no variables present', () => {
    expect(replaceVariablesInText('Hello world', { name: 'test' })).toBe('Hello world')
  })

  it('returns empty string for empty input', () => {
    expect(replaceVariablesInText('', { name: 'test' })).toBe('')
  })

  it('handles replacement value containing special regex characters', () => {
    expect(replaceVariablesInText('{{ val }}', { val: '$100.00' })).toBe('$100.00')
  })

  it('handles replacement value containing curly braces', () => {
    expect(replaceVariablesInText('{{ val }}', { val: '{{ nested }}' })).toBe('{{ nested }}')
  })

  it('handles variable with no spaces in braces', () => {
    expect(replaceVariablesInText('{{name}}', { name: 'Bob' })).toBe('Bob')
  })

  it('handles variable with extra whitespace in braces', () => {
    expect(replaceVariablesInText('{{   name   }}', { name: 'Bob' })).toBe('Bob')
  })

  it('replaces only known variables and leaves others', () => {
    const result = replaceVariablesInText('{{ known }} and {{ unknown }}', { known: 'yes' })
    expect(result).toBe('yes and {{ unknown }}')
  })

  it('handles multiline text replacement', () => {
    const text = `Hello {{ name }},
Your order {{ order_id }} is ready.`
    const result = replaceVariablesInText(text, { name: 'Alice', order_id: '12345' })
    expect(result).toBe(`Hello Alice,
Your order 12345 is ready.`)
  })

  it('handles dot-notation variables', () => {
    expect(replaceVariablesInText('{{ user.name }}', { 'user.name': 'Alice' })).toBe('Alice')
  })

  // Regression: consecutive calls should work (lastIndex reset)
  it('works correctly when called multiple times in succession', () => {
    expect(replaceVariablesInText('{{ a }}', { a: '1' })).toBe('1')
    expect(replaceVariablesInText('{{ b }}', { b: '2' })).toBe('2')
  })
})

// =============================================================================
// replaceVariablesInMessages
// =============================================================================

describe('replaceVariablesInMessages', () => {
  it('replaces variables in message content', () => {
    const messages = [Message.system('You are {{ role }}')]
    const result = replaceVariablesInMessages(messages, { role: 'a pirate' })
    expect(result[0].content).toBe('You are a pirate')
  })

  it('does not mutate original messages', () => {
    const messages = [Message.system('You are {{ role }}')]
    replaceVariablesInMessages(messages, { role: 'a pirate' })
    expect(messages[0].content).toBe('You are {{ role }}')
  })

  it('returns original messages array when all variable values are empty', () => {
    const messages = [Message.system('You are {{ role }}')]
    const result = replaceVariablesInMessages(messages, { role: '' })
    expect(result).toBe(messages) // same reference — fast path
  })

  it('returns original messages array when variables map is empty', () => {
    const messages = [Message.system('You are {{ role }}')]
    const result = replaceVariablesInMessages(messages, {})
    expect(result).toBe(messages)
  })

  it('replaces variables across multiple messages', () => {
    const messages = [
      Message.system('You are {{ role }}'),
      Message.request('Tell me about {{ topic }}'),
    ]
    const result = replaceVariablesInMessages(messages, { role: 'a teacher', topic: 'math' })
    expect(result[0].content).toBe('You are a teacher')
    expect(result[1].content).toBe('Tell me about math')
  })

  it('preserves messages without variables unchanged', () => {
    const messages = [
      Message.system('Hello'),
      Message.request('{{ name }}'),
    ]
    const result = replaceVariablesInMessages(messages, { name: 'Alice' })
    expect(result[0].content).toBe('Hello')
    expect(result[1].content).toBe('Alice')
  })

  it('preserves message count', () => {
    const messages = [
      Message.system('{{ a }}'),
      Message.request('{{ b }}'),
      Message.response('{{ c }}'),
    ]
    const result = replaceVariablesInMessages(messages, { a: '1', b: '2', c: '3' })
    expect(result).toHaveLength(3)
  })

  it('handles empty messages array', () => {
    const result = replaceVariablesInMessages([], { name: 'Alice' })
    expect(result).toEqual([])
  })

  it('handles messages with empty content', () => {
    const messages = [Message.system('')]
    const result = replaceVariablesInMessages(messages, { name: 'Alice' })
    expect(result[0].content).toBe('')
  })
})

// =============================================================================
// mergeVariables
// =============================================================================

describe('mergeVariables', () => {
  it('creates entries for new variable names with empty values', () => {
    expect(mergeVariables({}, ['name', 'topic'])).toEqual({
      name: '',
      topic: '',
    })
  })

  it('preserves existing values for variables that still exist', () => {
    const current = { name: 'Alice', topic: 'math' }
    const result = mergeVariables(current, ['name', 'topic'])
    expect(result).toEqual({ name: 'Alice', topic: 'math' })
  })

  it('drops variables no longer in the new names list', () => {
    const current = { name: 'Alice', old_var: 'value' }
    const result = mergeVariables(current, ['name'])
    expect(result).toEqual({ name: 'Alice' })
    expect(result).not.toHaveProperty('old_var')
  })

  it('adds new variables while preserving existing ones', () => {
    const current = { name: 'Alice' }
    const result = mergeVariables(current, ['name', 'topic'])
    expect(result).toEqual({ name: 'Alice', topic: '' })
  })

  it('handles empty current variables', () => {
    expect(mergeVariables({}, ['a', 'b'])).toEqual({ a: '', b: '' })
  })

  it('handles empty new names (returns empty map)', () => {
    const current = { name: 'Alice', topic: 'math' }
    expect(mergeVariables(current, [])).toEqual({})
  })

  it('handles both empty', () => {
    expect(mergeVariables({}, [])).toEqual({})
  })

  it('does not mutate the original variables map', () => {
    const current = { name: 'Alice' }
    mergeVariables(current, ['name', 'topic'])
    expect(current).toEqual({ name: 'Alice' })
  })

  it('preserves empty string values for existing variables', () => {
    const current = { name: '' }
    const result = mergeVariables(current, ['name'])
    expect(result).toEqual({ name: '' })
  })
})
