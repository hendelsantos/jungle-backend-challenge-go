// Wager helpers: the provider identity sends the requested operation, and
// every response is classified so legitimate rejections never pollute error
// rates or thresholds.
import http from 'k6/http';
import { config } from '../config.js';
import { providerToken, authHeader } from './auth.js';
import { noteTechnical, counters } from './classify.js';

const headers = { 'Content-Type': 'application/json', Authorized: 'provider' };

export function sendWager(params) {
  const { kind, playerId, walletId, roundId, amount, externalId, idempotencyKey, scenarioTag, referenceId } = params;
  const body = JSON.stringify({
    providerId: config.providerA.providerId,
    externalTransactionId: externalId,
    playerId, walletId, roundId,
    gameId: params.gameId || 'fortune-chimp',
    kind, money: { amount, currency: 'BRL' },
    ...(referenceId ? { referenceExternalTransactionId: referenceId } : {}),
  });
  const res = http.post(`${config.gatewayUrl}/wagering/transactions`, body, {
    headers: { 'Content-Type': 'application/json', ...authHeader(providerToken(scenarioTag)), 'Idempotency-Key': idempotencyKey },
    tags: { name: 'POST /wagering/transactions', kind, scenario: scenarioTag, endpoint: 'process-wager' },
    timeout: '10s',
  });
  let body2 = {};
  try { body2 = res.json(); } catch (e) { /* technical path */ }
  const statusOfType = body2 && body2.status ? body2.status : '';
  let finalType = '';
  if (res.status === 422) finalType = 'rejected';
  else if (res.status >= 500 || res.status === 0 || res.error_code) {
    counters.technical.value += 1;
    if (res.error && String(res.error).includes('timeout')) counters.timeouts.value += 1;
  }
  return { res, body: body2, statusOfType, finalType, externalId };
}
