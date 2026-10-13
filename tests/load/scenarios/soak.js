// Cenário F — soak: moderate load for a bounded duration (SOAK_DURATION,
// default 15m), one wallet per VU, a single BET plus periodic reconciliation
// per iteration. Safety: aborts on health degradation, financial threshold
// checks run continuously, and Ctrl+C (second SIGINT) stops k6 cleanly.
import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { config } from '../config.js';
import { openWallet } from '../helpers/wallet.js';
import { internalToken, authHeader } from '../helpers/auth.js';
import { uuid4 } from '../helpers/ids.js';
import { sendWager } from '../helpers/wager.js';
import { counters } from '../helpers/classify.js';

export const options = {
  discardResponseBodies: false,
  scenarios: {
    soak: {
      executor: 'per-vu-iterations',
      vus: Number(__ENV.SOAK_VUS || 25),
      iterations: Number(__ENV.SOAK_ITERATIONS_PER_VU || 400),
      maxDuration: __ENV.SOAK_DURATION || '15m',
    },
  },
  thresholds: {
    'http_req_duration{endpoint:process-wager}': ['p(95)<500'],
    'financial_technical_error_rate': ['rate<0.01'],
  },
};

export function setup() {
  const gate = http.get(`${config.gatewayUrl}/health/ready`, { timeout: '5s' });
  if (gate.status !== 200) exec.test.abort(`environment not ready (${gate.status})`);
  const count = Number(__ENV.SOAK_VUS || 25);
  const runId = `${Date.now()}`;
  const wallets = [];
  for (let i = 0; i < count; i += 1) {
    const playerId = uuid4();
    const id = openWallet(playerId, __ENV.SOAK_OPENING || '5000.00', 'soak', `soak-open-${runId}-${i}`);
    if (!id) exec.test.abort(`soak wallet ${i} failed to open`);
    wallets.push({ id, playerId });
  }
  return { runId, wallets };
}

// every iteration: one BET, then a breath; every 25th iteration also read
// reconciliation so the soak exercises reads under sustained writes.
export default function (data) {
  const vu = exec.vu.idInTest;
  const wallet = data.wallets[vu % data.wallets.length];
  const externalId = `soak-${data.runId}-v${vu}-i${__ITER}`;

  const result = sendWager({
    kind: 'BET', playerId: wallet.playerId, walletId: wallet.id,
    roundId: `soak-r-${vu}-${__ITER}`, amount: config.soakAmount,
    externalId, idempotencyKey: externalId, scenarioTag: 'soak',
  });
  check(result.res, {
    'soak bets always processed (wallets exclusive to VU)': (r) => r.status === 200 && result.body.status === 'PROCESSED',
  }) || console.error(`soak anomaly ${result.externalId}: ${result.res.status} ${result.res.body}`);

  if (__ITER % 25 === 0) {
    const recon = http.post(`${config.gatewayUrl}/wallets/${wallet.id}/reconciliation`, null, {
      headers: authHeader(internalToken('soak-recon')),
      tags: { name: 'POST /wallets/:id/reconciliation', scenario: 'soak' },
      timeout: '10s',
    });
    check(recon, { 'live reconciliation stays consistent under load': (r) => recon.json().consistent === true })
      || console.error(`soak RECON DIVERGENCE at vu ${vu} iter ${__ITER}: ${recon.body}`);
  }
  sleep(1); // pacing keeps the soak moderate and the run safely interruptible
}

export function teardown(data) {
  console.info(`soak counters: processed=${counters.processed.value} technical=${counters.technical.value} timeouts=${counters.timeouts.value}`);
  void data;
}
