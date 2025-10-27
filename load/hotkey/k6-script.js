import http from 'k6/http';
import { sleep } from 'k6';

export const options = {
  vus: __ENV.VUS ? parseInt(__ENV.VUS) : 50,
  duration: __ENV.DURATION || '1m',
  thresholds: {
    http_req_failed: ['rate<0.05'], // <5% failures
    http_req_duration: ['p(95)<50', 'p(99)<100'], // ms
  },
};

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const HOT = __ENV.HOT_KEY || 'hot';
const COLD_KEYS = __ENV.COLD_KEYS ? parseInt(__ENV.COLD_KEYS) : 50;

function coldKey(i) { return `cold:${i}`; }

export default function () {
  // 75% traffic to a hot key, rest spread over COLD_KEYS keys
  const r = Math.random();
  let key;
  if (r < 0.75) {
    key = HOT;
  } else {
    key = coldKey(Math.floor(Math.random() * COLD_KEYS));
  }
  const res = http.get(`${BASE}/check?api_key=${key}`);
  // optional: occasionally refund to exercise /release
  if (Math.random() < 0.02) {
    http.post(`${BASE}/release?api_key=${key}`, null);
  }
  sleep(0.001); // tiny think time to avoid tight loop
}
