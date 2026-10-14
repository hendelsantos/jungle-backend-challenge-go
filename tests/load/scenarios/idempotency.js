// Cenário D — idempotência sob carga: one wallet, N simultaneous identical
// requests (same Idempotency-Key, same payload). Expected: one processed
// answer, N-1 idempotent replays reporting the stored result, final balance
// opening-amount, exactly one debit in the ledger. Every expectation is
// computed from the configuration with exact decimal arithmetic.
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { config } from '../config.js';
import { waitForBarrier } from '../helpers/barrier.js';
import { openWallet, balance, reconciliation, ledgerRows } from '../helpers/wallet.js';
import { sendWager } from '../helpers/wager.js';
import { uuid4 } from '../helpers/ids.js';
import { subAmounts } from '../helpers/money.js';

export const options = {
  scenarios: {
    idempotency: {
      executor: 'per-vu-iterations',
      vus: Number(__ENV.IDEMPOTENCY_REQUESTS || 50),
      iterations: 1,
      maxDuration: '60s',
    },
  },
  thresholds: {
    // Same-identity races serialize on the unique indexes by design: losers
    // query the winner's stored result through bounded retry, so latency here
    // reflects contention, not sustained capacity.
    'http_req_duration{endpoint:process-wager}': ['p(95)<2000'],
    'financial_technical_error_rate': ['rate<0.05'],
    // The financial gate: ANY failed check fails the whole run (exit != 0).
    checks: ['rate==1.0'],
  },
};

export function setup() {
  const gate = http.get(`${config.gatewayUrl}/health/ready`, { timeout: '5s' });
  if (gate.status !== 200) exec.test.abort(`environment not ready (${gate.status})`);
  const runId = `${Date.now()}`;
  const playerId = uuid4();
  const walletId = openWallet(playerId, config.idempotencyOpening, 'idempotency', `idem-open-${runId}`);
  if (!walletId) exec.test.abort('idempotency wallet failed to open');
  return { runId, playerId, walletId, startAt: Date.now() + config.barrierMs, key: `idem-bet-${runId}` };
}

export default function (data) {
  waitForBarrier(data.startAt);
  const result = sendWager({
    kind: 'BET', playerId: data.playerId, walletId: data.walletId,
    roundId: `idem-r-${data.runId}`, amount: config.idempotencyAmount,
    externalId: data.key, idempotencyKey: data.key, scenarioTag: 'idempotency',
  });
  check(result.res, {
    'no technical errors under identity race': () => result.kind !== 'technical',
    'every answer reports PROCESSED': () => result.res.status === 200 && result.body.status === 'PROCESSED',
    'transaction identifier present': () => !!result.body.transactionId,
  });
}

export function teardown(data) {
  // Exactly one debit of the configured amount must be visible, regardless of
  // how many identical requests raced.
  const expectedBalance = subAmounts(config.idempotencyOpening, config.idempotencyAmount);
  const finalBalance = balance(data.walletId, 'idempotency-teardown');
  check(finalBalance, { 'final balance is opening minus one debit': (b) => b === expectedBalance })
    || console.error(`idempotency BALANCE MISMATCH: got ${finalBalance}, want ${expectedBalance}`);
  const rows = ledgerRows(data.walletId, 'idempotency-teardown');
  check(rows, { 'ledger: opening credit + one debit = 2 rows': (n) => n === 2 })
    || console.error(`idempotency LEDGER MISMATCH: got ${rows}`);
  // The "exactly one transaction identity" proof belongs to the independent
  // Go verifier (cmd/verifywallet): N answers must persist exactly one
  // wager_transactions row beyond the opening.
  const recon = reconciliation(data.walletId, 'idempotency-teardown');
  check(recon, { 'reconciliation consistent': (r) => r.status === 200 && recon.json().consistent === true })
    || console.error(`idempotency RECON MISMATCH: ${recon.body}`);
  console.info('idempotency done; per-class counters aggregate in the k6 summary (financial_ops_*, technical_errors)');
}
