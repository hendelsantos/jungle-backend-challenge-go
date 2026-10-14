// Wager helpers: the provider identity sends the requested operation. Every
// response is classified exactly once here (rate + counters) so scenarios
// never double-count; the class is returned for scenario checks.
import http from 'k6/http';
import { config } from '../config.js';
import { providerToken, authHeader } from './auth.js';
import { classify } from './classify.js';

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
  let bodyJson = {};
  try { bodyJson = res.json(); } catch (e) { /* technical path: no JSON body */ }
  const cls = classify(res);
  return { res, body: bodyJson, kind: cls, externalId };
}
