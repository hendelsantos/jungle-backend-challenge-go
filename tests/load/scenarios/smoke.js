// Cenário A — smoke: the environment must answer everything before any load
// exists. Aborts the test immediately when readiness fails.
import http from 'k6/http';
import { check, fail } from 'k6';
import exec from 'k6/execution';
import { config } from '../config.js';
import { internalToken, providerToken, authHeader } from '../helpers/auth.js';
import { openWallet, balance, reconciliation, ledgerRows } from '../helpers/wallet.js';
import { sendWager } from '../helpers/wager.js';
import { uuid4 } from '../helpers/ids.js';
import { subAmounts } from '../helpers/money.js';

const OPENING = '150.00';
const BET = '25.00';

export const options = {
  vus: 1,
  iterations: 1,
  thresholds: {
    http_req_duration: ['p(95)<500'],
    // The financial gate: ANY failed check fails the whole run (exit != 0).
    checks: ['rate==1.0'],
  },
};

export function setup() {
  const res = http.get(`${config.gatewayUrl}/health/ready`, { timeout: '5s' });
  if (res.status !== 200) {
    exec.test.abort(`environment not ready (health/ready ${res.status}); aborting before any load`);
  }
  const runId = `${Date.now()}`;
  const playerId = uuid4();
  const walletId = openWallet(playerId, OPENING, 'smoke', `smoke-open-${runId}`);
  if (!walletId) fail('wallet opening failed in smoke');
  return { walletId, playerId, runId };
}

export default function (data) {
  const externalId = `smoke-bet-${data.runId}`;

  const first = sendWager({ kind: 'BET', playerId: data.playerId, walletId: data.walletId, roundId: 'smoke-r1', amount: BET, externalId, idempotencyKey: externalId, scenarioTag: 'smoke' });
  check(first.res, {
    'BET accepted (200/202)': (r) => r.status === 200 || r.status === 202,
    'BET has transactionId': () => !!first.body.transactionId,
  }) || console.error(`smoke BET responded ${first.res.status}: ${first.res.body}`);

  const replay = sendWager({ kind: 'BET', playerId: data.playerId, walletId: data.walletId, roundId: 'smoke-r1', amount: BET, externalId, idempotencyKey: externalId, scenarioTag: 'smoke' });
  check(replay.res, {
    'replay distinguishes idempotency': (r) => r.status === 200 && replay.body.idempotentReplay === true,
  });

  check(balance(data.walletId, 'smoke'), {
    'balance is opening minus the single debit': (b) => b === subAmounts(OPENING, BET),
  });

  check(ledgerRows(data.walletId, 'smoke'), {
    'ledger has opening + debit': (rows) => rows === 2,
  });

  const recon = reconciliation(data.walletId, 'smoke');
  check(recon, {
    'reconciliation consistent': (r) => r.status === 200 && recon.json().consistent === true,
  });

  void providerToken; // referenced to keep helper import meaningful for extension
  void authHeader;   // scenarios computing their own headers use this export
}
