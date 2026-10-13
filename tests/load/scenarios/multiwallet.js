// Cenário E — carteiras independentes: every wallet walks the complete
// domain cycle BET → WIN → LOSS → REFUND → ROLLBACK. Two cycles leave the
// opening balance untouched (each cycle nets zero) and produce an exact,
// auditable shape: 1 opening + 4 ledger rows per cycle, 6 transactions per
// cycle, no PENDING_REFERENCE, reconciliation consistent.
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { config } from '../config.js';
import { openWallet, balance, reconciliation, ledgerRows } from '../helpers/wallet.js';
import { sendWager } from '../helpers/wager.js';
import { uuid4 } from '../helpers/ids.js';

export const options = {
  scenarios: {
    multiwallet: {
      executor: 'per-vu-iterations',
      vus: Number(__ENV.MULTIWALLET_USERS || 25),
      iterations: Number(__ENV.MULTIWALLET_CYCLES || 2),
      maxDuration: '60s',
    },
  },
  thresholds: {
    'http_req_duration{endpoint:process-wager}': ['p(95)<500'],
  },
};

export function setup() {
  const gate = http.get(`${config.gatewayUrl}/health/ready`, { timeout: '5s' });
  if (gate.status !== 200) exec.test.abort(`environment not ready (${gate.status})`);
  const count = Number(__ENV.MULTIWALLET_USERS || 25);
  const runId = `${Date.now()}`;
  const wallets = [];
  for (let i = 0; i < count; i += 1) {
    const playerId = uuid4();
    const openId = openWallet(playerId, config.multiwalletCycleBudget, 'multiwallet', `multi-open-${runId}-${i}`);
    if (!openId) exec.test.abort(`multiwallet wallet ${i} failed to open`);
    wallets.push({ id: openId, playerId });
  }
  return { runId, wallets, startAt: Date.now() + config.barrierMs };
}

export default function (data) {
  const vu = exec.vu.idInTest;
  const wallet = data.wallets[vu % data.wallets.length];
  const cycle = __ITER; // 0..cyclesCount-1
  const roundId = `multi-${data.runId}-${vu}-${cycle}`;
  const amount = config.multiwalletAmount;

  const send = (kind, amountOut, reference) => sendWager({
    kind, playerId: wallet.playerId, walletId: wallet.id, roundId,
    amount: amountOut, externalId: `multi-${data.runId}-${vu}-${cycle}-${kind.toLowerCase()}`,
    idempotencyKey: `multi-${data.runId}-${vu}-${cycle}-${kind.toLowerCase()}`,
    scenarioTag: 'multiwallet', referenceId: reference || '',
  });

  const bet = send('BET', amount);
  const win = send('WIN', amount);
  const loss = send('LOSS', '0.00');
  const refund = send('REFUND', amount, bet.externalId);
  const rollback = send('ROLLBACK', amount, refund.externalId);

  const results = [bet, win, loss, refund, rollback];
  check(results.reduce((acc, item) => acc + (item.res.status === 200 ? 1 : 0), 0), {
    'all five operations succeeded': (okCount) => okCount === 5,
  }) || results.forEach((item) => console.error(`multiwallet ${item.externalId}: ${item.res.status} ${item.res.body}`));
}

export function teardown(data) {
  const before = balance(data.wallets[0].id, 'multiwallet-teardown');
  const expectedRows = 1 + 4 * Number(__ENV.MULTIWALLET_CYCLES || 2);
  const rows = ledgerRows(data.wallets[0].id, 'multiwallet-teardown');
  check(rows, {
    'wallet 0 ledger rows match the exact cycle shape': (n) => n === expectedRows,
  }) || console.error(`multiwallet LEDGER MISMATCH: got ${rows}, want ${expectedRows}`);
  check(before, {
    'wallet 0 balance back to opening': (b) => b === config.multiwalletCycleBudget,
  }) || console.error(`multiwallet BALANCE MISMATCH: ${before}`);
  const recon = reconciliation(data.wallets[0].id, 'multiwallet-teardown');
  check(recon, { 'reconciliation consistent': (r) => recon.json().consistent === true })
    || console.error(`multiwallet RECON MISMATCH: ${recon.body}`);
  console.info('multiwallet: per-wallet financial independence validated for sampled wallet; run cmd/verifywallet for the full audit');
}
