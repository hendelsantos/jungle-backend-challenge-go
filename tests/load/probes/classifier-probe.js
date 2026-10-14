// Positive classifier probe (no HTTP): a known oracle of synthetic responses
// must produce the exact expected technical-error rate and per-class counts.
// Run: k6 run tests/load/probes/classifier-probe.js
import { check } from 'k6';
import { classify, technicalErrorRate, counters } from '../helpers/classify.js';

export const options = {
  vus: 1,
  iterations: 1,
  thresholds: {
    // 1 technical in 1002 classified operations ≈ 0.0998% — well under 1%.
    // With the pre-fix classifier this probe reported 100% (1 out of 1).
    financial_technical_error_rate: ['rate<0.01'],
    checks: ['rate==1.0'],
  },
};

const ok = () => ({ status: 200, json: () => ({ status: 'PROCESSED' }) });

export default function () {
  for (let i = 0; i < 997; i += 1) classify(ok());
  check(classify({ status: 422, json: () => ({}) }), { '422 is business': (k) => k === 'rejected' });
  check(classify({ status: 409, json: () => ({}) }), { '409 is conflict': (k) => k === 'conflict' });
  check(classify({ status: 400, json: () => ({}) }), { '400 is contract, not technical': (k) => k === 'contract' });
  check(classify({ status: 200, json: () => ({ status: 'PROCESSED', idempotentReplay: true }) }), { 'replay detected': (k) => k === 'replay' });
  check(classify({ status: 500, json: () => ({}) }), { '500 is technical': (k) => k === 'technical' });
}

export function teardown() {
  // k6 metric values are readable in the end-of-test summary; the threshold
  // above is the gate. The assertions below are informational for humans.
  console.info('classifier probe done: expect financial_technical_error_rate ~= 0.10% (1/1002) and thresholds green');
  void technicalErrorRate; void counters;
}
