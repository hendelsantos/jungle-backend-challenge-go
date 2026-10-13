// Barrier: VUs sleep until the absolute target agreed by setup, so the
// contention scenarios fire as one burst.
import { sleep } from 'k6';

export function waitForBarrier(startAtMs) {
  const remaining = startAtMs - Date.now();
  if (remaining > 0) sleep(remaining / 1000);
}
