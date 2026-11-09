export interface EvaluatorData {
  name: string
  description: string
}

/** A unique evaluator name, so parallel runs and reruns never collide. */
export function createEvaluatorData(overrides: Partial<EvaluatorData> = {}): EvaluatorData {
  return {
    name: `e2e-evaluator-${Date.now()}-${Math.floor(Math.random() * 1000)}`,
    description: 'Created by the evaluators E2E suite',
    ...overrides,
  }
}
