// Cenário C — concorrência financeira: one wallet, N DISTINCT bets fired as
// one burst. Software invariant: exactly floor(opening/amount) processed, the
// rest rejected with INSUFFICIENT_FUNDS, exact final balance and a clean
// ledger — all computed from the configuration with exact decimal arithmetic.
// Teardown validates persistence; any failed check fails the run (threshold).
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { config } from '../config.js';
import { waitForBarrier } from '../helpers/barrier.js';
import { openWallet, balance, reconciliation, ledgerRows } from '../helpers/wallet.js';
import { sendWager } from '../helpers/wager.js';
import { uuid4 } from '../helpers/ids.js';
import { toCents, formatCents } from '../helpers/money.js';

export const options = {
  scenarios: {
    contention: {
      executor: 'per-vu-iterations',
      vus: Number(__ENV.CONTENTION_BETS || 50),
      iterations: 1,
      maxDuration: '60s',
    },
  },
  thresholds: {
    // Contention latency is dominated by N-way row serialization by design;
    // sustained-load latency targets live in the ramp and soak scenarios.
    'http_req_duration{endpoint:process-wager}': ['p(95)<900', 'p(99)<1500'],
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
  const walletId = openWallet(playerId, config.contentionOpening, 'contention', `cont-open-${runId}`);
  if (!walletId) exec.test.abort('contention wallet failed to open');
  return { runId, playerId, walletId, startAt: Date.now() + config.barrierMs };
}

export default function (data) {
  waitForBarrier(data.startAt);
  const vuIndex = exec.vu.idInTest;
  const result = sendWager({
    kind: 'BET', playerId: data.playerId, walletId: data.walletId,
    roundId: `cont-r-${data.runId}`, amount: config.contentionAmount,
    externalId: `cont-${data.runId}-${vuIndex}`, idempotencyKey: `cont-${data.runId}-${vuIndex}`,
    scenarioTag: 'contention',
  });
  check(result.res, {
    'terminal mechanism reached': () => result.kind !== 'technical',
    'rejected bets carry INSUFFICIENT_FUNDS': () =>
      result.kind !== 'rejected' || result.body.failureCode === 'INSUFFICIENT_FUNDS',
  });
}

export function teardown(data) {
  // Exact expectations from the configuration: with integer cents, the number
  // of winning bets is floor(opening/amount); each winner debits once.
  const opening = toCents(config.contentionOpening);
  const amount = toCents(config.contentionAmount);
  const affordable = opening / amount;
  const requested = BigInt(config.contentionBets);
  const winners = affordable < requested ? affordable : requested; // BigInt floor division: exact
  const expected = formatCents(opening - winners * amount);
  const expectedRows = (opening > 0n ? 1 : 0) + Number(winners);

  const finalBalance = balance(data.walletId, 'contention-teardown');
  check(finalBalance, { 'contention final balance matches the computed winners': (b) => b === expected })
    || console.error(`contention BALANCE MISMATCH: got ${finalBalance}, want ${expected}`);
  const rows = ledgerRows(data.walletId, 'contention-teardown');
  check(rows, { 'ledger rows: opening + computed winners': (n) => n === expectedRows })
    || console.error(`contention LEDGER MISMATCH: got ${rows}, want ${expectedRows}`);
  const recon = reconciliation(data.walletId, 'contention-teardown');
  check(recon, { 'contention reconciliation consistent': (r) => r.status === 200 && recon.json().consistent === true })
    || console.error(`contention RECON MISMATCH: ${recon.body}`);
  console.info(`contention: expected ${winners} winners from ${config.contentionOpening}/${config.contentionAmount}; ` +
    'per-class counters aggregate in the k6 summary (financial_ops_*, technical_errors)');
}
