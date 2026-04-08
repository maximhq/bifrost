// ============================================================
// k6 Performance Test — Baseline: 1,000 RPS Steady State
// Suite: TC-015-001
// Usage: k6 run specs/data/performance/k6-baseline.js
// ============================================================

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

// Custom metrics
const inferenceErrors = new Counter('inference_errors');
const successRate = new Rate('inference_success_rate');
const latencyTrend = new Trend('gateway_latency_ms', true);

// Load config
const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const API_TOKEN = __ENV.API_USER_TOKEN || 'bfvk_performance_test_key_000010';

export const options = {
  scenarios: {
    constant_load: {
      executor: 'constant-arrival-rate',
      rate: 1000,              // 1,000 RPS
      timeUnit: '1s',
      duration: '5m',          // 5 minute run
      preAllocatedVUs: 300,
      maxVUs: 600,
    },
  },
  thresholds: {
    // Gateway overhead ≤ 50ms P99 (provider latency excluded via mock)
    'http_req_duration{type:inference}': ['p(99)<50'],
    // Error rate < 0.1%
    'http_req_failed': ['rate<0.001'],
    // Custom: success rate > 99.9%
    'inference_success_rate': ['rate>0.999'],
  },
};

const payload = JSON.stringify({
  model: 'gpt-4o-mini',
  messages: [{ role: 'user', content: 'What is 2+2? Answer in one word.' }],
  stream: false,
  max_tokens: 10,
});

const headers = {
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${API_TOKEN}`,
};

export default function () {
  const startTime = Date.now();

  const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, {
    headers,
    tags: { type: 'inference' },
  });

  const duration = Date.now() - startTime;
  latencyTrend.add(duration);

  const ok = check(res, {
    'status is 200': (r) => r.status === 200,
    'has choices': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.choices && body.choices.length > 0;
      } catch {
        return false;
      }
    },
  });

  successRate.add(ok);
  if (!ok) {
    inferenceErrors.add(1);
    console.log(`Error: status=${res.status} body=${res.body.substring(0, 200)}`);
  }
}

export function handleSummary(data) {
  return {
    'stdout': textSummary(data, { indent: ' ', enableColors: true }),
    'specs/data/performance/results/baseline-results.json': JSON.stringify(data, null, 2),
  };
}

function textSummary(data, opts) {
  return `
=== TC-015-001 Baseline Results ===
RPS Target: 1,000/s
Duration:   5 minutes

Latency:
  P50: ${data.metrics.http_req_duration?.values?.med?.toFixed(2)}ms
  P95: ${data.metrics.http_req_duration?.values['p(95)']?.toFixed(2)}ms
  P99: ${data.metrics.http_req_duration?.values['p(99)']?.toFixed(2)}ms

Requests: ${data.metrics.http_reqs?.values?.count}
Errors:   ${data.metrics.http_req_failed?.values?.passes}
  `;
}
