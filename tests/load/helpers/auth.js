// Auth helpers: tokens come straight from the real Keycloak by
// client_credentials. Each VU keeps its own cache with expiry, so long soak
// runs renew quietly instead of failing mid-load. Wallet creation uses the
// wallet-internal client, wagers the provider client — exactly the two
// identities the OIDC layer authorizes.
import http from 'k6/http';
import { config } from '../config.js';
import { noteTechnical } from './classify.js';

const tokenCache = {};

function requestToken(client, cacheKey, scenarioTag) {
  const body = `grant_type=client_credentials&client_id=${client.clientId}&client_secret=${client.clientSecret}`;
  const res = http.post(`${config.keycloakUrl}/realms/jungle/protocol/openid-connect/token`, body, {
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    tags: { name: 'auth-token', scenario: scenarioTag },
    timeout: '10s',
  });
  noteTechnical(res);
  if (res.status !== 200) {
    throw new Error(`token request failed: ${res.status} ${res.body}`);
  }
  const token = res.json('access_token');
  const expiresIn = res.json('expires_in') || 300;
  tokenCache[cacheKey] = { token, expiresAt: Date.now() + (expiresIn - 30) * 1000 };
  return token;
}

export function internalToken(scenarioTag = 'setup') {
  const key = 'internal';
  const cached = tokenCache[key];
  if (cached && Date.now() < cached.expiresAt) return cached.token;
  return requestToken(config.internal, key, scenarioTag);
}

export function providerToken(scenarioTag = 'setup') {
  const key = 'providerA';
  const cached = tokenCache[key];
  if (cached && Date.now() < cached.expiresAt) return cached.token;
  return requestToken(config.providerA, key, scenarioTag);
}

export function authHeader(token) {
  return { Authorization: `Bearer ${token}` };
}
