// Exact decimal money for k6 expectations: amounts travel as strings and all
// arithmetic runs on integer cents via BigInt — never through float64/Number
// arithmetic, mirroring the backend's Money invariant.
export function toCents(amount) {
  const text = String(amount);
  const dot = text.indexOf('.');
  const intPart = dot < 0 ? text : text.slice(0, dot);
  const fracPart = dot < 0 ? '' : text.slice(dot + 1);
  if (!/^\d+$/.test(intPart) || !/^\d{0,2}$/.test(fracPart)) {
    throw new Error(`invalid decimal amount: ${text}`);
  }
  return BigInt(intPart) * 100n + BigInt((fracPart + '00').slice(0, 2) || '0');
}

export function formatCents(cents) {
  const sign = cents < 0n ? '-' : '';
  const abs = cents < 0n ? -cents : cents;
  const intPart = abs / 100n;
  const fracPart = String(abs % 100n).padStart(2, '0');
  return `${sign}${intPart}.${fracPart}`;
}

export function subAmounts(a, b) {
  return formatCents(toCents(a) - toCents(b));
}

export function mulAmount(amount, n) {
  return formatCents(toCents(amount) * BigInt(n));
}
