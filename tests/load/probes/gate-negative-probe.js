// Negative gate probe: one financial check deliberately fails. The run MUST
// exit non-zero — before the threshold fix, k6 exited 0 with a failed check.
// Run: k6 run tests/load/probes/gate-negative-probe.js ; echo "exit=$?"
// Expected: exit code != 0 (the threshold `checks{gate:financial}` fails).
import { check } from 'k6';

export const options = {
  vus: 1,
  iterations: 1,
  thresholds: {
    checks: ['rate==1.0'],
  },
};

export default function () {
  check('99.00', { 'deliberately wrong expectation': (b) => b === '50.00' });
}
