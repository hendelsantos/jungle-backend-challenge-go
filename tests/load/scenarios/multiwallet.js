// Cenário E — carteiras independentes: every wallet walks the complete
// domain cycle BET → WIN → LOSS → REFUND → ROLLBACK. Each cycle nets zero and
// produces an exact, auditable shape: 4 ledger rows and 5 transactions per
// cycle, no PENDING_REFERENCE, reconciliation consistent. The teardown audits
// EVERY wallet of the run; any failed check fails the whole run.
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
      maxDuration: '120s',
    },
  },
  thresholds: {
    'http_req_duration{endpoint:process-wager}': ['p(95)<500'],
    'financial_technical_error_rate': ['rate<0.01'],
    // The financial gate: ANY failed check fails the whole run (exit != 0).
    checks: ['rate==1.0'],
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
  check(results, {
    'all five operations processed': (items) => items.every((item) => item.res.status === 200 && item.body.status === 'PROCESSED'),
    'no technical errors in the cycle': (items) => items.every((item) => item.kind !== 'technical'),
  }) || results.forEach((item) => console.error(`multiwallet ${item.externalId}: ${item.res.status} ${item.res.body}`));
}

export function teardown(data) {
  // Audit EVERY wallet of the run: balance back to the opening amount and the
  // exact ledger shape (1 opening + 4 rows per cycle), plus reconciliation.
  const cycles = Number(__ENV.MULTIWALLET_CYCLES || 2);
  const expectedRows = 1 + 4 * cycles;
  data.wallets.forEach((wallet, i) => {
    const current = balance(wallet.id, 'multiwallet-teardown');
    check(current, { [`wallet ${i} balance back to opening`]: (b) => b === config.multiwalletCycleBudget })
      || console.error(`multiwallet BALANCE MISMATCH wallet ${i}: got ${current}, want ${config.multiwalletCycleBudget}`);
    const rows = ledgerRows(wallet.id, 'multiwallet-teardown');
    check(rows, { [`wallet ${i} ledger rows match the exact cycle shape`]: (n) => n === expectedRows })
      || console.error(`multiwallet LEDGER MISMATCH wallet ${i}: got ${rows}, want ${expectedRows}`);
    const recon = reconciliation(wallet.id, 'multiwallet-teardown');
    check(recon, { [`wallet ${i} reconciliation consistent`]: (r) => r.status === 200 && r.json().consistent === true })
      || console.error(`multiwallet RECON MISMATCH wallet ${i}: ${recon.body}`);
  });
  console.info('multiwallet: all wallets audited; run cmd/verifywallet for the SQL-level independent audit');
}
