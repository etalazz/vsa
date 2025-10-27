import http from 'k6/http';
import { sleep } from 'k6';

export const options = {
  vus: __ENV.VUS ? parseInt(__ENV.VUS) : 50,
  duration: __ENV.DURATION || '1m',
  thresholds: {
    http_req_failed: ['rate<0.02'],
    http_req_duration: ['p(95)<40', 'p(99)<80'],
  },
};

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const KEYS = __ENV.KEYS ? parseInt(__ENV.KEYS) : 1000;

function key(i) { return `u:${i}`; }

export default function () {
  const idx = Math.floor(Math.random() * KEYS);
  const k = key(idx);
  http.get(`${BASE}/check?api_key=${k}`);
  if (Math.random() < 0.01) {
    http.post(`${BASE}/release?api_key=${k}`, null);
  }
  sleep(0.001);
}
