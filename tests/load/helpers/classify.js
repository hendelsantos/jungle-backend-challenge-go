import { Rate } from 'k6/metrics';

// Technical-only error rate: business 422/409 never count here.
export const technicalErrorRate = new Rate('financial_technical_error_rate');

// Response classifier: separates legitimate financial outcomes from
// technical failures. A 422 with INSUFFICIENT_FUNDS (or any other persisted
// failure code) is business behavior, never infrastructure error.
const counters = {
  processed: { label: 'financial_ops_processed', value: 0 },
  rejected: { label: 'financial_ops_rejected', value: 0 },
  replays: { label: 'financial_ops_replays', value: 0 },
  conflicts: { label: 'financial_ops_conflicts', value: 0 },
  technical: { label: 'technical_errors', value: 0 },
  timeouts: { label: 'request_timeouts', value: 0 },
};

function bump(name) {
  if (counters[name]) counters[name].value += 1;
}

// classify inspects status and body and returns one of:
//   processed | replay | rejected | conflict | technical
// k6 stamps error_code on any non-2xx (0/1400/1500), so the classifier must
// look at the HTTP status itself, not at k6's error hints.
function classify(res) {
  const message = res.error ? String(res.error) : '';
  if (res.status === 0 || res.status >= 500) {
    if (message.includes('timeout')) bump('timeouts');
    bump('technical');
    technicalErrorRate.add(1);
    return 'technical';
  }
  if (res.status === 422) {
    bump('rejected');
    return 'rejected';
  }
  if (res.status === 409) {
    bump('conflicts');
    return 'conflict';
  }
  if (res.status >= 200 && res.status < 300) {
    const body = res.json ? res.json() : {};
    if (body && body.idempotentReplay === true) {
      bump('replays');
      return 'replay';
    }
    bump('processed');
    return 'processed';
  }
  bump('technical');
  return 'technical';
}

export { counters, classify };

// noteTechnical records a request as a technical failure when status,
// error code or network error suggest something outside business rules.
export function noteTechnical(res) {
  const kind = classify(res);
  return kind;
}

// financialOutcome collapses the four terminal-ish flows into thresholds use.
export function financialOutcome(res) {
  const k = classify(res);
  return k === 'processed' || k === 'replay' || k === 'rejected' || k === 'conflict' ? k : 'technical';
}
