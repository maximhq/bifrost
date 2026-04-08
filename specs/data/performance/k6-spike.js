// ============================================================
// k6 Performance Test — Spike Load (10x burst)
// Suite: TC-015-009
// Usage: k6 run specs/data/performance/k6-spike.js
// ============================================================

import http from 'k6/http';
import { check } from 'k6';
import { Rate, Counter, Trend } from 'k6/metrics';

const successRate = new Rate('success_rate');
const spikeErrors = new Counter('spike_errors');
const spikeLatency = new Trend('spike_latency_ms', true);

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const API_TOKEN = __ENV.API_USER_TOKEN || 'bfvk_performance_test_key_000010';

export const options = {
  scenarios: {
    spike_test: {
      executor: 'ramping-arrival-rate',
      startRate: 500,
      timeUnit: '1s',
      preAllocatedVUs: 1000,
      maxVUs: 5000,
      stages: [
        { duration: '2m',  target: 500  },   // Warm-up: steady 500 RPS
        { duration: '5s',  target: 5000 },   // Spike: instant 10x jump
        { duration: '30s', target: 5000 },   // Hold spike
        { duration: '5s',  target: 500  },   // Return to normal
        { duration: '2m',  target: 500  },   // Recovery verification
      ],
    },
  },
  thresholds: {
    // During spike: allow up to 5% errors (queuing expected)
    'http_req_failed': ['rate<0.05'],
    // After spike recovery (checked via custom metric)
    'success_rate': ['rate>0.95'],
  },
};

const payload = JSON.stringify({
  model: 'gpt-4o-mini',
  messages: [{ role: 'user', content: 'ok' }],
  max_tokens: 3,
});

const headers = {
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${API_TOKEN}`,
};

export default function () {
  const start = Date.now();
  const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, { headers });
  spikeLatency.add(Date.now() - start);

  const ok = check(res, {
    'not 500': (r) => r.status !== 500,
    'not 503': (r) => r.status !== 503,
  });

  successRate.add(ok);
  if (!ok) spikeErrors.add(1);
}
