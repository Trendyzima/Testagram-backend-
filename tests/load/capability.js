import http from 'k6/http';
import { check, sleep } from 'k6';

const base = (__ENV.API_BASE_URL || '').replace(/\/+$/, '');
if (!base) throw new Error('Set API_BASE_URL to the deployed Testagram API origin');
const maxVUs = Math.max(1, Number(__ENV.MAX_VUS || 50));
const p95Ms = Math.max(1, Number(__ENV.P95_MS || 300));

export const options = {
  stages: [
    { duration: '30s', target: Math.max(1, Math.floor(maxVUs * 0.2)) },
    { duration: '60s', target: Math.max(1, Math.floor(maxVUs * 0.5)) },
    { duration: '120s', target: maxVUs },
    { duration: '30s', target: 0 },
  ],
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: [`p(95)<${p95Ms}`],
    checks: ['rate>0.99'],
  },
};

export default function () {
  const response = http.post(
    `${base}/api/capability`,
    JSON.stringify({ capability: 'testagram.health.read', input: {} }),
    {
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json',
        Origin: 'https://testagram.site',
      },
      tags: { endpoint: 'capability-health' },
      timeout: '10s',
    },
  );
  check(response, {
    'capability gateway returns 2xx': (r) => r.status >= 200 && r.status < 300,
    'capability gateway returns JSON': (r) => (r.headers['Content-Type'] || '').includes('application/json'),
  });
  sleep(0.2);
}
