import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '20s', target: 15 }, // ramp-up to 15 concurrent users
    { duration: '1m',  target: 30 }, // hold at 30 users
    { duration: '20s', target: 0 },  // cool-down
  ],
};

const BASE_URL = __ENV.API_BASE_URL || 'http://localhost:8080';

export default function () {
  // 1. Health check
  let resHealth = http.get(`${BASE_URL}/health`);
  check(resHealth, { 'health is 200': (r) => r.status === 200 });

  // 2. Fetch contests list
  let resList = http.get(`${BASE_URL}/contests/list?page=0`);
  check(resList, { 'contest list is 200': (r) => r.status === 200 });

  // 3. Problem list fetch (adjust contest id as needed)
  let resProblems = http.get(`${BASE_URL}/contests/c1/problems`);
  check(resProblems, { 'problems is 200 or 401/403/404': (r) => [200, 401, 403, 404].includes(r.status) });

  sleep(1);
}
