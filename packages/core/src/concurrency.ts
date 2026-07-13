const DEFAULT_POLICY_CONCURRENCY = 8;

/** Runs independent policy checks concurrently while reporting the earliest input-order failure. */
export async function runPolicyChecks(
  checks: readonly (() => Promise<void>)[],
  concurrency = DEFAULT_POLICY_CONCURRENCY,
): Promise<void> {
  const width = Math.max(1, Math.floor(concurrency));
  for (let offset = 0; offset < checks.length; offset += width) {
    const settled = await Promise.allSettled(
      checks.slice(offset, offset + width).map((check) => check()),
    );
    for (const result of settled) {
      if (result.status === 'rejected') {
        throw result.reason;
      }
    }
  }
}

