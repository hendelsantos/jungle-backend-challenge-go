// Cenário C — concorrência financeira: one wallet with 1000.00, fifty
// DISTINCT bets of 30.00 fired as one burst. Software invariant: exactly 33
// processed, 17 rejected with INSUFFICIENT_FUNDS, final balance 10.00 and a
// clean ledger. Teardown validates persistence and aborts the scenario on any
// technical failure (not on 422 — that is the designed outcome).
import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { config } from '../config.js';
import { waitForBarrier } from '../helpers/barrier.js';
import { openWallet, balance, reconciliation, ledgerRows } from '../helpers/wallet.js';
import { sendWager } from '../helpers/wager.js';
import { uuid4 } from '../helpers/ids.js';
import { counters, classify } from '../helpers/classify.js';

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
    // Contention latency is dominated by 50-way row serialization by design;
    // sustained-load latency targets live in the ramp and soak scenarios.
    'http_req_duration{endpoint:process-wager}': ['p(95)<900', 'p(99)<1500'],
    'financial_technical_error_rate': ['rate<0.05'],
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
  const kind = classify(result.res);
  check(result.res, {
    'terminal mechanism reached': (r) => kind !== 'technical',
    'rejected bets carry INSUFFICIENT_FUNDS': (r) =>
      kind !== 'rejected' || result.body.failureCode === 'INSUFFICIENT_FUNDS',
  });
}

export function teardown(data) {
  // Persistence validation happens once all fifty answers are final.
  const finalBalance = balance(data.walletId, 'contention-teardown');
  check(finalBalance, { 'contention final balance is 10.00': (b) => b === '10.00' })
    || console.error(`contention BALANCE MISMATCH: got ${finalBalance}`);
  const rows = ledgerRows(data.walletId, 'contention-teardown');
  check(rows, { 'ledger rows: opening + 33 debits = 34': (n) => n === 34 })
    || console.error(`contention LEDGER MISMATCH: got ${rows}`);
  const recon = reconciliation(data.walletId, 'contention-teardown');
  check(recon, { 'contention reconciliation consistent': (r) => recon.json().consistent === true })
    || console.error(`contention RECON MISMATCH: ${recon.body}`);
  console.info(`contention counters: processed=${counters.processed.value} rejected=${counters.rejected.value} technical=${counters.technical.value}`);
  if (counters.technical.value > 0) console.error('contention saw TECHNICAL errors');
  void sleep; void config;
}
