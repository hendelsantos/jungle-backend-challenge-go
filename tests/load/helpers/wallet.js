// Wallet helpers: opening with the internal client and reads that cost
// latency are tagged so thresholds can exclude or include them explicitly.
import http from 'k6/http';
import { sleep } from 'k6';
import { config } from '../config.js';
import { internalToken, authHeader } from './auth.js';
import { noteTechnical } from './classify.js';

const headers = { 'Content-Type': 'application/json' };

export function openWallet(playerId, opening, scenarioTag, idempotencyKey) {
  const res = http.post(`${config.gatewayUrl}/wallets`, JSON.stringify({
    playerId, initialBalance: { amount: opening, currency: 'BRL' },
  }), {
    headers: { ...headers, ...authHeader(internalToken(scenarioTag)), 'Idempotency-Key': idempotencyKey },
    tags: { name: 'POST /wallets', scenario: scenarioTag },
    timeout: '10s',
  });
  noteTechnical(res);
  const body = res.json ? res.json() : {};
  if (body.id) console.info(`audit-wallet scenario=${scenarioTag} id=${body.id}`);
  return body.id || null;
}

export function balance(walletId, scenarioTag) {
  const res = http.get(`${config.gatewayUrl}/wallets/${walletId}`, {
    headers: authHeader(internalToken(scenarioTag)),
    tags: { name: 'GET /wallets/:id', scenario: scenarioTag },
    timeout: '10s',
  });
  noteTechnical(res);
  const body = res.json ? res.json() : {};
  return body.balance ? body.balance.amount : null;
}

export function reconciliation(walletId, scenarioTag) {
  return http.post(`${config.gatewayUrl}/wallets/${walletId}/reconciliation`, null, {
    headers: { ...headers, ...authHeader(internalToken(scenarioTag)) },
    tags: { name: 'POST /wallets/:id/reconciliation', scenario: scenarioTag },
    timeout: '10s',
  });
}

export function ledgerRows(walletId, scenarioTag) {
  const res = http.get(`${config.gatewayUrl}/wallets/${walletId}/ledger?limit=100`, {
    headers: authHeader(internalToken(scenarioTag)),
    tags: { name: 'GET /wallets/:id/ledger', scenario: scenarioTag },
    timeout: '10s',
  });
  const body = res.json ? res.json() : {};
  return body.entries ? body.entries.length : -1;
}

// Sequential helper: scenario setup builds wallets one by one (returned ids
// feed the per-VU pool) without extra concurrency beyond the gateway.
export function buildPool(size, opening, scenarioTag) {
  const ids = [];
  for (let i = 0; i < size; i += 1) {
    const id = openWallet(`${scenarioTag}-player-${i}`, opening, scenarioTag, `pool-${scenarioTag}-${i}`);
    if (!id) throw new Error(`wallet ${i} failed to open`);
    ids.push(id);
  }
  return ids;
}
