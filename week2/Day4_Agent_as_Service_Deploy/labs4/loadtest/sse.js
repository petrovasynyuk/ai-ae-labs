import http from 'k6/http';
import { check } from 'k6';
import { Trend, Counter, Rate } from 'k6/metrics';

// Custom metrics for homework requirements:
// - TTFB to first SSE event (res.timings.waiting)
// - Full request duration (res.timings.duration)
// - Successful SSE requests counter
// - Error counter and rate
const sseTtfb = new Trend('sse_ttfb', true);
const sseDuration = new Trend('sse_full_duration', true);
const sseSuccess = new Counter('sse_success');
const sseErrors = new Counter('sse_errors');
const sseErrorRate = new Rate('sse_error_rate');

export const options = {
  scenarios: {
    sse_stress: {
      executor: 'constant-vus',
      vus: __ENV.VUS ? parseInt(__ENV.VUS, 10) : 10,
      duration: __ENV.DURATION || '10s',
    },
  },
  thresholds: {
    sse_error_rate: ['rate<0.01'],
    http_req_failed: ['rate<0.01'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const APP_NAME = 'first_graph_agent';
const USER_ID = 'learner';

export default function () {
  // Requirement: separate session for each parallel request/iteration
  const sessionId = `load-${__VU}-${__ITER}-${Date.now()}-${Math.floor(Math.random() * 100000)}`;

  // Step 1: Create distinct session
  const sessionUrl = `${BASE_URL}/api/apps/${APP_NAME}/users/${USER_ID}/sessions/${sessionId}`;
  const sessionRes = http.post(sessionUrl, '{}', {
    headers: { 'Content-Type': 'application/json' },
    tags: { name: 'POST /api/apps/:app/users/:user/sessions/:id' },
  });

  const sessionOk = check(sessionRes, {
    'session created (200)': (r) => r.status === 200,
  });

  if (!sessionOk) {
    sseErrors.add(1);
    sseErrorRate.add(1);
    return;
  }

  // Step 2: POST /api/run_sse (deterministic graph/stub path without LLM)
  const sseUrl = `${BASE_URL}/api/run_sse`;
  const payload = JSON.stringify({
    appName: APP_NAME,
    userId: USER_ID,
    sessionId: sessionId,
    newMessage: {
      role: 'user',
      parts: [
        {
          text: `Мерчант A-114 просить повернення по транзакції txn-2026-${__VU}-${__ITER}`,
        },
      ],
    },
  });

  const sseRes = http.post(sseUrl, payload, {
    headers: {
      'Content-Type': 'application/json',
      'Accept': 'text/event-stream',
    },
    tags: { name: 'POST /api/run_sse' },
    timeout: '30s',
  });

  // Record TTFB and total duration
  sseTtfb.add(sseRes.timings.waiting);
  sseDuration.add(sseRes.timings.duration);

  const sseOk = check(sseRes, {
    'sse status is 200': (r) => r.status === 200,
    'content-type is event-stream': (r) =>
      r.headers['Content-Type'] && r.headers['Content-Type'].includes('text/event-stream'),
    'has SSE data': (r) => r.body && r.body.includes('data:'),
    'case processed': (r) =>
      r.body && (r.body.includes('pending') || r.body.includes('already_open')),
  });

  if (sseOk) {
    sseSuccess.add(1);
    sseErrorRate.add(0);
  } else {
    sseErrors.add(1);
    sseErrorRate.add(1);
  }
}
