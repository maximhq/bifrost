// ============================================================
// k6 Performance Test — Ramp to 5,000 RPS
// Suite: TC-015-002
// Usage: k6 run specs/data/performance/k6-ramp.js
// ============================================================

import http from 'k6/http';
import { check } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const successRate = new Rate('inference_success_rate');
const gatewayLatency = new Trend('gateway_latency_ms', true);

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const API_TOKEN = __ENV.API_USER_TOKEN || 'bfvk_performance_test_key_000010';

export const options = {
  scenarios: {
    ramp_load: {
      executor: 'ramping-arrival-rate',
      startRate: 0,
      timeUnit: '1s',
      preAllocatedVUs: 500,
      maxVUs: 2000,
      stages: [
        { duration: '2m', target: 1000 },   // Ramp up to 1K
        { duration: '3m', target: 5000 },   // Ramp up to 5K
        { duration: '5m', target: 5000 },   // Sustain 5K
        { duration: '2m', target: 0 },      // Ramp down
      ],
    },
  },
  thresholds: {
    'http_req_duration': ['p(99)<50'],       // P99 ≤ 50ms gateway overhead
    'http_req_failed': ['rate<0.01'],        // < 1% error rate
    'inference_success_rate': ['rate>0.99'],
  },
};

const payload = JSON.stringify({
  model: 'gpt-4o-mini',
  messages: [{ role: 'user', content: 'Reply with: ok' }],
  max_tokens: 5,
});

const headers = {
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${API_TOKEN}`,
};

export default function () {
  const start = Date.now();
  const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, { headers });
  gatewayLatency.add(Date.now() - start);

  const ok = check(res, {
    'status 200': (r) => r.status === 200,
  });
  successRate.add(ok);
}
