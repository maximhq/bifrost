// ============================================================
// k6 Performance Test — 1,000 Concurrent Streaming Connections
// Suite: TC-015-003
// Usage: k6 run specs/data/performance/k6-streaming.js
// ============================================================

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend, Counter } from 'k6/metrics';

const streamSuccessRate = new Rate('stream_success_rate');
const timeToFirstChunk = new Trend('ttfc_ms', true);  // Time to first chunk
const totalStreamTime = new Trend('total_stream_ms', true);
const streamErrors = new Counter('stream_errors');

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const API_TOKEN = __ENV.API_USER_TOKEN || 'bfvk_performance_test_key_000010';

export const options = {
  scenarios: {
    concurrent_streams: {
      executor: 'constant-vus',
      vus: 1000,           // 1,000 concurrent streaming connections
      duration: '3m',
    },
  },
  thresholds: {
    'ttfc_ms': ['p(95)<50'],             // Time-to-first-chunk P95 ≤ 50ms
    'stream_success_rate': ['rate>0.99'],
    'http_req_failed': ['rate<0.01'],
  },
};

const payload = JSON.stringify({
  model: 'gpt-4o-mini',
  messages: [
    { role: 'user', content: 'Count from 1 to 10, one number per line.' }
  ],
  stream: true,
  max_tokens: 50,
});

const headers = {
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${API_TOKEN}`,
  'Accept': 'text/event-stream',
};

export default function () {
  const start = Date.now();
  let firstChunkReceived = false;
  let ttfc = 0;

  // k6 streaming via responseType
  const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, {
    headers,
    responseType: 'text',
    timeout: '30s',
  });

  const end = Date.now();

  // If streamed properly, first chunk marker should appear quickly
  if (res.body && res.body.includes('data:')) {
    firstChunkReceived = true;
    // Approximate TTFC — in real streaming we'd parse chunk timestamps
    ttfc = end - start;  // Full response time (conservative TTFC estimate)
  }

  if (firstChunkReceived) {
    timeToFirstChunk.add(ttfc);
  }

  totalStreamTime.add(end - start);

  const ok = check(res, {
    'status 200': (r) => r.status === 200,
    'has SSE data': (r) => r.body && r.body.includes('data:'),
    'stream complete': (r) => r.body && r.body.includes('[DONE]'),
  });

  streamSuccessRate.add(ok);
  if (!ok) {
    streamErrors.add(1);
    if (__ENV.DEBUG === '1') {
      console.log(`Stream error: status=${res.status}`);
    }
  }

  // Small pause between iterations per VU
  sleep(0.1);
}
