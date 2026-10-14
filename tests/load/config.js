// Central configuration for the k6 suite. Every tunable comes from the
// environment so CI and local runs shape the experiment without editing code.
// Money travels as decimal strings end to end; k6 never converts amounts to
// float arithmetic — the independent verifier (cmd/verifywallet) audits cents.
export const config = {
  gatewayUrl: __ENV.GATEWAY_URL || 'http://localhost:9080',
  keycloakUrl: __ENV.KEYCLOAK_URL || 'http://localhost:59080',

  audience: __ENV.OIDC_AUDIENCE || 'jungle-api',
  internal: {
    clientId: __ENV.INTERNAL_CLIENT_ID || 'wallet-internal-client',
    clientSecret: __ENV.INTERNAL_CLIENT_SECRET || 'local-wallet-internal-client-secret',
  },
  providerA: {
    clientId: __ENV.PROVIDER_CLIENT_ID || 'provider-a-client',
    clientSecret: __ENV.PROVIDER_CLIENT_SECRET || 'local-provider-a-client-secret',
    providerId: __ENV.PROVIDER_ID || 'provider-a',
  },

  // Wallet pool handed out one per virtual user; budgets are large enough
  // that load scenarios only exercise latency, never rejection paths.
  poolSize: Number(__ENV.WALLET_POOL_SIZE || 100),
  poolOpening: __ENV.WALLET_POOL_OPENING || '100000.00',

  // Contention: one wallet, fifty distinct bets racing for its balance.
  contentionOpening: __ENV.CONTENTION_OPENING || '1000.00',
  contentionBets: Number(__ENV.CONTENTION_BETS || 50),
  contentionAmount: __ENV.CONTENTION_AMOUNT || '30.00',

  // Idempotency: one wallet, fifty identical requests with one identity.
  idempotencyOpening: __ENV.IDEMPOTENCY_OPENING || '100.00',
  idempotencyRequests: Number(__ENV.IDEMPOTENCY_REQUESTS || 50),
  idempotencyAmount: __ENV.IDEMPOTENCY_AMOUNT || '50.00',

  // Independent wallets: full BET/WIN/LOSS/REFUND/ROLLBACK cycle per wallet.
  multiwalletUsers: Number(__ENV.MULTIWALLET_USERS || 25),
  multiwalletCycleBudget: __ENV.MULTIWALLET_OPENING || '400.00',
  multiwalletAmount: __ENV.MULTIWALLET_AMOUNT || '40.00',
  multiwalletCycles: Number(__ENV.MULTIWALLET_CYCLES || 2),

  // Soak: moderate load for a bounded duration, safe to interrupt.
  soakVus: Number(__ENV.SOAK_VUS || 25),
  soakDuration: __ENV.SOAK_DURATION || '15m',
  soakAmount: __ENV.SOAK_AMOUNT || '1.00',

  // Barrier offset so racing scenarios fire after setup finished.
  barrierMs: Number(__ENV.BARRIER_MS || 1500),
};

// Payment of one wallet entry in the pool: VUs never share a wallet during
// load scenarios, so any 422 here would be a genuine finding, not a design.
export function poolWalletFor(indexOrVU) {
  return indexOrVU % config.poolSize;
}

export const BASE_TAGS = { scenarioTag: 'jungle-load' };
