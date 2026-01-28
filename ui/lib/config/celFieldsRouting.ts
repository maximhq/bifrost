/**
 * CEL Fields Configuration for Routing Rules
 * Defines available fields for building routing rule expressions
 */

import { getProviderLabel } from "@/lib/constants/logs";

export interface CELFieldDefinition {
  name: string;
  label: string;
  placeholder?: string;
  inputType?: 'text' | 'select' | 'keyValue' | 'number';
  valueEditorType?: 'text' | 'select' | 'keyValue' | 'number' | 'textarea' | 'budgetNumber' | ((operator: string) => 'text' | 'select' | 'keyValue' | 'number' | 'textarea' | 'budgetNumber');
  operators?: string[];
  defaultOperator?: string;
  defaultValue?: any;
  values?: Array<{ name: string; label: string; disabled?: boolean }>;
  metricOptions?: Array<{ name: string; label: string }>; // For budgetNumber type
  description?: string; // Helpful note for the user
}

export const baseRoutingFields: CELFieldDefinition[] = [
  {
    name: 'model',
    label: 'Model',
    placeholder: 'e.g., gpt-4, claude-3-sonnet',
    inputType: 'text',
    valueEditorType: (operator: string) =>
      operator === '=' || operator === '!=' ? 'select' :
      operator === 'in' || operator === 'notIn' ? 'select' :
      'text',
    operators: ['=', '!=', 'in', 'notIn', 'contains', 'beginsWith', 'endsWith', 'matches'],
    defaultOperator: '=',
  },
  {
    name: 'provider',
    label: 'Provider',
    placeholder: 'Select provider',
    inputType: 'select',
    valueEditorType: (operator: string) =>
      operator === 'matches' ? 'text' :
      operator === 'in' || operator === 'notIn' ? 'select' :
      'select',
    operators: ['=', '!=', 'in', 'notIn', 'matches'],
    defaultOperator: '=',
  },
  {
    name: 'headers',
    label: 'Header',
    placeholder: 'e.g., authorization, x-custom-header (use lowercase)',
    inputType: 'keyValue',
    valueEditorType: 'keyValue',
    operators: ['=', '!=', 'contains', 'beginsWith', 'endsWith', 'matches', 'null', 'notNull'],
    defaultOperator: '=',
  },
  {
    name: 'tokens_used',
    label: 'Tokens Used (%)',
    placeholder: 'e.g., 80',
    inputType: 'text',
    valueEditorType: 'number',
    operators: ['=', '!=', '>', '<', '>=', '<='],
    defaultOperator: '>=',
    description: 'Check token usage as percentage. Checked against max of model and provider configs.',
  },
  {
    name: 'request',
    label: 'Request (%)',
    placeholder: 'e.g., 80',
    inputType: 'text',
    valueEditorType: 'number',
    operators: ['=', '!=', '>', '<', '>=', '<='],
    defaultOperator: '>=',
    description: 'Check request usage as percentage. Checked against max of model and provider configs.',
  },
  {
    name: 'budget_used',
    label: 'Budget Used (%)',
    placeholder: 'e.g., 50',
    inputType: 'text',
    valueEditorType: 'number',
    operators: ['=', '!=', '>', '<', '>=', '<='],
    defaultOperator: '>=',
    description: 'Check budget usage as percentage. Checked against max of model and provider configs.',
  },
  {
    name: 'params',
    label: 'Query Parameter',
    placeholder: 'e.g., api_key, user_id',
    inputType: 'keyValue',
    valueEditorType: 'keyValue',
    operators: ['=', '!=', 'contains', 'beginsWith', 'endsWith', 'matches', 'null', 'notNull'],
    defaultOperator: '=',
  },
];

/**
 * Get routing fields with dynamic providers and models
 * Provider field values are populated dynamically from available providers
 * Metric options for rate limits and budget are populated from available providers and models
 */
export function getRoutingFields(providers: string[] = [], models: string[] = []): CELFieldDefinition[] {
  // Create provider field values
  const providerValues =
    providers.length > 0
      ? providers.map((provider) => ({
          name: provider,
          label: getProviderLabel(provider),
        }))
      : [{ name: '_no_providers', label: 'No providers configured', disabled: true }];

  // Create model field values
  const modelValues =
    models.length > 0
      ? models.map((model) => ({
          name: model,
          label: model,
        }))
      : [];

  // Create metric options for scope input: providers + models
  const scopeOptions = [
    { name: '', label: '(provider-level)' }, // Empty scope for provider-level
    ...providers.map((provider) => ({
      name: provider,
      label: `${provider} (provider)`,
    })),
    ...models.map((model) => ({
      name: model,
      label: `${model} (model)`,
    })),
  ];

  // Update provider field with dynamic values and rate limit/budget fields with scope options
  const fieldsWithDynamicValues = baseRoutingFields.map((field) => {
    if (field.name === 'provider') {
      return {
        ...field,
        values: providerValues,
      };
    }
    if (field.name === 'model') {
      return {
        ...field,
        values: modelValues,
      };
    }
    if (field.name === 'tokens_used' || field.name === 'request' || field.name === 'budget_used') {
      return {
        ...field,
        metricOptions: scopeOptions,
      };
    }
    return field;
  });

  return fieldsWithDynamicValues;
}

export const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  azure: 'Azure OpenAI',
  gemini: 'Google Gemini',
  vertex: 'Vertex AI',
  cohere: 'Cohere',
};
