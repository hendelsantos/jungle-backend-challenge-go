// Cenário B — ramp-up: 10 → 25 → 50 → 100 VUs. One wallet per VU (never
// shared if WALLET_POOL_SIZE >= max targets), every iteration a single BET
// whose amount keeps the per-wallet budget positive — any 422 here is a
// genuine finding, not a designed rejection.
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { config, poolWalletFor } from '../config.js';
import { openWallet } from '../helpers/wallet.js';
import { sendWager } from '../helpers/wager.js';
import { uuid4 } from '../helpers/ids.js';

export const options = {
  discardResponseBodies: false,
  scenarios: {
    'ramp-up': {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: __ENV.RAMP_LEVEL
        ? [
            { duration: __ENV.RAMP_HOLD || '40s', target: Number(__ENV.RAMP_LEVEL) },
            { duration: '10s', target: 0 },
          ]
        : [
            { duration: __ENV.STAGE1 || '20s', target: Number(__ENV.STAGE_VUS1 || 10) },
            { duration: __ENV.STAGE2 || '20s', target: Number(__ENV.STAGE_VUS2 || 25) },
            { duration: __ENV.STAGE3 || '20s', target: Number(__ENV.STAGE_VUS3 || 50) },
            { duration: __ENV.STAGE4 || '30s', target: Number(__ENV.STAGE_VUS4 || 100) },
            { duration: __ENV.STAGE5 || '20s', target: 0 },
          ],
      gracefulRampDown: '10s',
    },
  },
  thresholds: {
    'http_req_duration{endpoint:process-wager}': ['p(95)<500', 'p(99)<900'],
    'financial_technical_error_rate': ['rate<0.01'],
    // The financial gate: ANY failed check fails the whole run (exit != 0).
    checks: ['rate==1.0'],
  },
};

export function setup() {
  const gate = http.get(`${config.gatewayUrl}/health/ready`, { timeout: '5s' });
  if (gate.status !== 200) {
    exec.test.abort(`environment not ready (${gate.status}); no load will begin`);
  }
  const runId = `${Date.now()}`;
  const wallets = [];
  for (let i = 0; i < config.poolSize; i += 1) {
    const playerId = uuid4();
    const id = openWallet(playerId, config.poolOpening, 'ramp', `ramp-open-${runId}-${i}`);
    if (!id) exec.test.abort(`wallet pool failed at index ${i}`);
    wallets.push({ id, playerId });
  }
  return { runId, wallets };
}

export default function (data) {
  const vuIndex = exec.vu.idInTest;
  const wallet = data.wallets[poolWalletFor(vuIndex)];
  const externalId = `ramp-${data.runId}-v${vuIndex}-i${__ITER}`;

  const result = sendWager({
    kind: 'BET', playerId: wallet.playerId, walletId: wallet.id,
    roundId: `ramp-r-${vuId(vuIndex)}-${__ITER}`, amount: '25.00',
    externalId, idempotencyKey: externalId, scenarioTag: 'ramp-up',
  });

  check(result.res, {
    'BET reached a terminal outcome': () => result.kind !== 'technical',
  }) || console.error(`ramp unexpected status ${result.res.status}: ${result.res.body}`);
}

function vuId(n) { return n % 100000; }
