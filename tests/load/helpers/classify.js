import { Rate, Counter } from 'k6/metrics';

// Technical-only error rate: business 422/409 and contract 4xx never count
// here. Every classified response feeds the rate exactly once (add(true) for
// technical failures, add(false) otherwise), so the denominator is the total
// number of classified operations — never the number of failures.
export const technicalErrorRate = new Rate('financial_technical_error_rate');
export const auxiliaryErrorRate = new Rate('auxiliary_technical_error_rate');
const auxiliaryErrors = new Counter('auxiliary_technical_errors');
const auxiliaryTimeouts = new Counter('auxiliary_timeouts');

// Class counters are k6 metrics: they aggregate across VUs and into the
// end-of-test summary (plain JS objects are per-VU and cannot do this).
export const counters = {
  processed: new Counter('financial_ops_processed'),
  rejected: new Counter('financial_ops_rejected'),
  replays: new Counter('financial_ops_replays'),
  conflicts: new Counter('financial_ops_conflicts'),
  contract: new Counter('financial_ops_contract_rejections'),
  technical: new Counter('technical_errors'),
  timeouts: new Counter('request_timeouts'),
};

// kindToCounter maps each returned class to its counter key (kept explicit so
// a mismatch can never throw inside a VU).
const kindToCounter = {
  processed: counters.processed,
  replay: counters.replays,
  rejected: counters.rejected,
  conflict: counters.conflicts,
  contract: counters.contract,
  technical: counters.technical,
};

// classify inspects status and body and returns one of:
//   processed | replay | rejected | conflict | contract | technical
// It records the response exactly once: one rate sample and one counter bump.
// Callers must classify each response a single time (the helpers below
// classify inside the helper and return the class; scenarios must not
// classify again). k6 stamps error_code on any non-2xx (0/1400/1500), so the
// classifier looks at the HTTP status itself, not at k6's error hints.
export function classify(res) {
  const message = res.error ? String(res.error) : '';
  if (res.status === 0 || res.status >= 500) {
    if (message.includes('timeout')) counters.timeouts.add(1);
    counters.technical.add(1);
    technicalErrorRate.add(1);
    return 'technical';
  }
  let kind;
  if (res.status === 422) kind = 'rejected';
  else if (res.status === 409) kind = 'conflict';
  else if (res.status >= 200 && res.status < 300) {
    const body = res.json ? res.json() : {};
    kind = body && body.idempotentReplay === true ? 'replay' : 'processed';
  } else {
    // Remaining 4xx: the request never satisfied the contract (400/401/403/
    // 404). It is not an infrastructure failure, but it is not a legitimate
    // financial outcome either — it signals a malformed or unauthorized call.
    kind = 'contract';
  }
  kindToCounter[kind].add(1);
  technicalErrorRate.add(0);
  return kind;
}

// noteTechnical feeds a separate rate and counters for auxiliary calls — token fetches, wallet openings and reads —
// without polluting the per-class financial operation counters.
export function noteTechnical(res) {
  const message = res.error ? String(res.error) : '';
  if (res.status === 0 || res.status >= 500) {
    if (message.includes('timeout')) auxiliaryTimeouts.add(1);
    auxiliaryErrors.add(1);
    auxiliaryErrorRate.add(1);
    return 'technical';
  }
  auxiliaryErrorRate.add(0);
  return 'ok';
}
